#!/usr/bin/env python3
"""常驻单容器管理进程：HTTP/SSE 代理、二进制更新、子进程监督（仅标准库）。"""
import argparse
import copy
import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
import tarfile
import tempfile
import threading
import time
from urllib.parse import urlsplit
from urllib.request import urlopen
import uuid

import update
from versioning import artifact_version, validate_commit

MAX_EXPANDED = 64 * 1024 * 1024
MAX_REQUEST = 1024 * 1024
MAX_CONFIG_REQUEST = 4 * 1024 * 1024
MAX_UPDATE_REQUEST = 4096
HOP_HEADERS = {"connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
               "te", "trailer", "transfer-encoding", "upgrade"}
BUSY_STATES = {"checking", "updating"}


class BusyError(update.UpdateError):
    pass


def binary_identity(path):
    try:
        result = subprocess.run([str(path), "-version"], check=True, capture_output=True,
                                text=True, encoding="utf-8", timeout=10)
    except (OSError, subprocess.SubprocessError) as exc:
        raise update.UpdateError("执行程序版本检查失败：" + str(exc)) from exc
    found = re.fullmatch(r"slaveSim (\S+) 提交=(\S+) 构建时间=(\S+)\s*", result.stdout)
    if not found:
        raise update.UpdateError("程序版本输出格式不正确")
    return dict(version=found[1], commit=found[2])


def verify_candidate(path, manifest):
    identity = binary_identity(path)
    if any(identity[key] != manifest[key] for key in ("version", "commit")):
        raise update.UpdateError("候选程序版本或完整提交号与清单不一致")
    try:
        validate_commit(manifest["channel"], identity["version"], identity["commit"])
    except ValueError as exc:
        raise update.UpdateError("候选程序版本身份不正确：" + str(exc)) from exc


def extract_binaries(path, destination):
    """手工写入白名单文件；绝不调用 tar.extract 或采用归档文件权限。"""
    destination = Path(destination)
    destination.mkdir(parents=True, exist_ok=True)
    seen, binaries, expanded = set(), set(), 0
    try:
        with tarfile.open(path, "r:gz") as archive:
            for item in archive:
                parts = item.name.split("/")
                if (not item.name or "\\" in item.name or ":" in item.name
                        or any(part in ("", ".", "..") for part in parts)
                        or item.name in seen or not item.isreg() or item.sparse is not None):
                    raise update.UpdateError("二进制归档包含不安全路径、链接、非普通文件或重复条目")
                if item.size < 0 or (item.name in ("slavesim", "probe") and item.size == 0):
                    raise update.UpdateError("二进制归档包含空程序或无效长度")
                expanded += item.size
                if expanded > MAX_EXPANDED:
                    raise update.UpdateError("二进制归档解压大小超过 64 MiB")
                seen.add(item.name)
                if len(seen) > 4096:
                    raise update.UpdateError("二进制归档条目过多")
                # 发布包还包含配置示例、文档和脚本，校验但从不采用这些文件。
                if item.name not in ("slavesim", "probe"):
                    continue
                binaries.add(item.name)
                with archive.extractfile(item) as source, (destination / item.name).open("xb") as target:
                    remaining = item.size
                    while remaining:
                        chunk = source.read(min(1024 * 1024, remaining))
                        if not chunk:
                            raise update.UpdateError("二进制归档文件内容不完整")
                        target.write(chunk)
                        remaining -= len(chunk)
                    target.flush()
                    os.fsync(target.fileno())
                (destination / item.name).chmod(0o755)
        if binaries != {"slavesim", "probe"}:
            raise update.UpdateError("二进制归档缺少 slavesim 或 probe")
    except (OSError, ValueError, tarfile.TarError) as exc:
        raise update.UpdateError("二进制归档读取失败：" + str(exc)) from exc


def durable_json(path, value):
    update.atomic_json(path, value)
    # Linux 上刷目录，保证掉电后 rename 的提交仍可恢复。
    if os.name == "posix":
        descriptor = os.open(str(Path(path).parent), os.O_RDONLY)
        try:
            try:
                os.fsync(descriptor)
            except OSError as exc:
                # rename 已提交，不能把目录刷盘失败解释成未提交并回滚内存状态。
                update.log("[slaveSim 管理] 状态已提交，目录刷盘失败：" + str(exc))
        finally:
            os.close(descriptor)


class Manager:
    def __init__(self, runtime_dir, updater_config, config="/etc/slavesim/config.json",
                 logfile="", seed=Path("/slavesim"), probe=Path("/probe"), child_port=16001):
        self.runtime_dir = Path(runtime_dir)
        self.runtime_dir.mkdir(parents=True, exist_ok=True)
        (self.runtime_dir / "releases").mkdir(exist_ok=True)
        self.seed, self.seed_probe = Path(seed), Path(probe)
        self.config, self.logfile, self.child_port = str(config), logfile, child_port
        self.guard = threading.RLock()
        self.lifecycle = threading.RLock()
        self.stopping = threading.Event()
        self.child = self.worker = self.monitor = None
        self.busy = False
        self.source_url, config_error = "", ""
        settings_path = Path(updater_config)
        if settings_path.exists():
            try:
                settings = update.read_json(settings_path)
                source = settings.get("manifest_url", "") if isinstance(settings, dict) else ""
                parsed = update.validate_url(source)
                if parsed.path.split("/")[-2:] not in (["ci", "latest.json"], ["stable", "latest.json"]):
                    raise update.UpdateError("清单地址必须以 ci/latest.json 或 stable/latest.json 结尾")
                self.source_url = source
            except (update.UpdateError, TypeError, ValueError) as exc:
                config_error = "更新配置无效：" + str(exc)
        state_path = self.runtime_dir / "state.json"
        if state_path.exists():
            self.current = update.read_json(state_path)
            self.validate_installed(self.current)
        else:
            identity = binary_identity(self.seed)
            channel = "ci" if "-ci." in identity["version"] else "stable"
            if identity["version"].endswith("-dev"):
                channel = ""
            self.current = dict(identity, channel=channel, release="seed")
        self.result = dict(latest_version="", state="idle", message=config_error or "等待手动检查更新",
                           update_available=False, job_id="")
        task_path = self.runtime_dir / "task.json"
        if task_path.exists():
            saved = update.read_json(task_path)
            if isinstance(saved, dict):
                self.result.update({key: saved[key] for key in self.result if key in saved})
                if self.result["state"] in BUSY_STATES:
                    self.result.update(state="error", message="上次更新任务中断，已恢复最后提交的版本", update_available=False)
                    self.save_task()

    def validate_installed(self, state):
        if not isinstance(state, dict) or not all(isinstance(state.get(key), str) for key in ("version", "commit", "channel", "release")):
            raise update.UpdateError("持久化安装状态不正确")
        release = state["release"]
        if release == "seed":
            identity = binary_identity(self.seed)
            if any(identity[key] != state[key] for key in ("version", "commit")):
                raise update.UpdateError("镜像种子与最后提交状态不一致；请恢复旧基础镜像")
            return
        if release != artifact_version(state["version"]) or not re.fullmatch(r"[A-Za-z0-9_.-]+", release):
            raise update.UpdateError("持久化版本路径不正确")
        try:
            validate_commit(state["channel"], state["version"], state["commit"])
        except ValueError as exc:
            raise update.UpdateError("持久化版本身份不正确：" + str(exc)) from exc
        folder = self.runtime_dir / "releases" / release
        if folder.is_symlink() or any(not (folder / name).is_file() or (folder / name).is_symlink() for name in ("slavesim", "probe")):
            raise update.UpdateError("已安装版本的二进制文件缺失或为链接")
        verify_candidate(folder / "slavesim", state)

    def binary_path(self, state=None):
        state = state or self.current
        return self.seed if state["release"] == "seed" else self.runtime_dir / "releases" / state["release"] / "slavesim"

    def status(self):
        with self.guard:
            return dict(enabled=bool(self.source_url), current_version=self.current["version"],
                        source_url=self.source_url, channel=self.current.get("channel", "") or
                        (urlsplit(self.source_url).path.split("/")[-2] if self.source_url else ""), **self.result)

    def save_task(self):
        durable_json(self.runtime_dir / "task.json", self.result)

    def set_result(self, **fields):
        with self.guard:
            self.result.update(fields)
            self.save_task()

    def begin(self, operation):
        with self.guard:
            if self.busy or self.stopping.is_set():
                raise BusyError("已有更新任务运行或管理进程正在退出")
            if not self.source_url:
                raise update.UpdateError("未配置有效的 manifest_url，网页更新未启用")
            self.busy = True
            try:
                self.set_result(state="checking" if operation == "check" else "updating",
                                message="正在检查发布清单", job_id=uuid.uuid4().hex)
                self.worker = threading.Thread(target=self.run_job, args=(operation,), daemon=True, name="update-worker")
                self.worker.start()
            except Exception:
                self.busy = False
                raise

    def ensure_running(self):
        if self.stopping.is_set():
            raise update.UpdateError("管理进程正在退出，更新任务已取消")

    def available(self, manifest):
        # 本地开发版本没有发布身份，第一次显式安装允许切换到配置的通道。
        if not self.current.get("channel"):
            return True
        installed = copy.deepcopy(self.current)
        # Docker 基础镜像可重新构建；程序更新身份只由版本、提交和二进制摘要决定。
        installed.pop("platform", None)
        installed.pop("image_id", None)
        available = update.needs_update(manifest, installed)
        if not available and self.current.get("binary_sha256"):
            artifact = manifest["platforms"][self.current["platform"]]
            if artifact.get("binary_sha256") != self.current["binary_sha256"]:
                raise update.UpdateError("同版本二进制摘要发生变化，拒绝覆盖")
        return available

    def prepare(self, manifest, artifact):
        fields = ("binary_url", "binary_sha256", "binary_size")
        if any(field not in artifact for field in fields):
            raise update.UpdateError("当前发布清单缺少二进制更新包")
        if artifact["binary_size"] > MAX_EXPANDED:
            raise update.UpdateError("二进制压缩包超过 64 MiB")
        release = artifact_version(manifest["version"])
        if not re.fullmatch(r"[A-Za-z0-9_.-]+", release):
            raise update.UpdateError("发布版本路径不正确")
        destination = self.runtime_dir / "releases" / release
        with tempfile.TemporaryDirectory(prefix=".download-", dir=self.runtime_dir) as temporary:
            directory = Path(temporary)
            download = dict(url=artifact["binary_url"], sha256=artifact["binary_sha256"], size=artifact["binary_size"])
            path = update.download_artifact(download, directory)
            self.ensure_running()
            candidate = directory / "candidate"
            extract_binaries(path, candidate)
            verify_candidate(candidate / "slavesim", manifest)
            self.ensure_running()
            if destination.exists():
                # 尚未提交的旧候选可重新下载；不得覆写当前/回滚版本。
                protected = {self.current["release"], self.current.get("previous", {}).get("release")}
                if release in protected or destination.is_symlink():
                    raise update.UpdateError("候选路径与受保护版本冲突")
                shutil.rmtree(destination)
            os.replace(candidate, destination)
            if os.name == "posix":
                descriptor = os.open(str(destination.parent), os.O_RDONLY)
                try:
                    os.fsync(descriptor)
                finally:
                    os.close(descriptor)
        return destination

    def run_job(self, operation):
        try:
            with update.file_lock(self.runtime_dir):
                self.ensure_running()
                manifest = update.fetch_manifest(self.source_url)
                key = update.platform_key()
                artifact = update.validate_manifest(manifest, self.source_url, key)
                self.ensure_running()
                available = self.available(manifest)
                self.set_result(latest_version=manifest["version"], update_available=available)
                if not available:
                    self.set_result(state="current", message="当前已是最新版本")
                    return
                if operation == "check":
                    if not all(field in artifact for field in ("binary_url", "binary_sha256", "binary_size")):
                        raise update.UpdateError("当前发布清单缺少二进制更新包")
                    self.set_result(state="available", message="发现新版本，可手动更新")
                    return
                self.set_result(state="updating", message="正在下载与校验，当前模拟器继续服务")
                candidate = self.prepare(manifest, artifact)
                self.ensure_running()
                old = copy.deepcopy(self.current)
                next_state = {field: manifest[field] for field in ("version", "commit", "channel")}
                next_state.update(platform=key, image_id=artifact["image_id"],
                                  binary_sha256=artifact["binary_sha256"], release=candidate.name,
                                  previous={field: value for field, value in old.items() if field != "previous"})
                with self.lifecycle:
                    self.ensure_running()
                    self.set_result(message="校验通过，正在启动新版本")
                    self.stop_child()
                    try:
                        self.launch(next_state)
                        if not self.healthy():
                            raise update.UpdateError("新版本 HTTP/PTY 健康检查失败")
                        self.ensure_running()
                        durable_json(self.runtime_dir / "state.json", next_state)
                    except Exception as exc:
                        self.stop_child()
                        if self.stopping.is_set():
                            raise update.UpdateError("更新任务已取消，将在下次启动恢复最后提交的版本") from exc
                        try:
                            self.launch(old)
                            if not self.healthy():
                                raise update.UpdateError("旧版本 HTTP/PTY 健康检查失败")
                        except Exception as rollback:
                            raise update.UpdateError("更新失败且回滚失败，需人工处理：" + str(rollback)) from exc
                        raise update.UpdateError("更新失败，已回滚：" + str(exc)) from exc
                    with self.guard:
                        self.current = next_state
                    self.set_result(state="succeeded", message="更新成功：" + manifest["version"], update_available=False)
                self.prune()
        except Exception as exc:
            update.log("[slaveSim 管理] " + str(exc))
            try:
                self.set_result(state="error", message=str(exc))
            except OSError as write_error:
                update.log("[slaveSim 管理] 保存更新结果失败：" + str(write_error))
        finally:
            with self.guard:
                self.busy = False

    def prune(self):
        keep = {self.current["release"], self.current.get("previous", {}).get("release")}
        for folder in (self.runtime_dir / "releases").iterdir():
            if folder.name not in keep and folder.is_dir() and not folder.is_symlink():
                shutil.rmtree(folder)

    def launch(self, state=None):
        self.ensure_running()
        binary = self.binary_path(state)
        update.log("[slaveSim 管理] 启动模拟器：" + str(binary))
        args = [str(binary), "-config", self.config, "-http", "127.0.0.1:" + str(self.child_port)]
        if self.logfile:
            args += ["-logfile", self.logfile]
        self.child = subprocess.Popen(args, stdin=subprocess.DEVNULL)

    def stop_child(self):
        child, self.child = self.child, None
        if child is None:
            return
        if child.poll() is None:
            child.terminate()
            try:
                child.wait(timeout=5)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait(timeout=5)
        else:
            child.wait()

    def healthy(self, timeout=30):
        config = update.read_json(self.config)
        expected = config.get("buses", []) if isinstance(config, dict) else []
        names = {bus.get("name") for bus in expected if isinstance(bus, dict)}
        if not expected or len(names) != len(expected):
            raise update.UpdateError("现场配置缺少总线或总线名称重复")
        deadline = time.monotonic() + timeout
        while not self.stopping.is_set() and time.monotonic() < deadline:
            if self.child is None or self.child.poll() is not None:
                return False
            try:
                with urlopen("http://127.0.0.1:" + str(self.child_port) + "/api/state", timeout=2) as response:
                    raw = response.read(update.MAX_MANIFEST + 1)
                if len(raw) <= update.MAX_MANIFEST:
                    state = json.loads(raw)
                    buses = state.get("buses", []) if isinstance(state, dict) else []
                    if (isinstance(buses, list) and len(buses) == len(expected)
                            and all(isinstance(bus, dict) and bus.get("slave_path") for bus in buses)
                            and {bus.get("name") for bus in buses} == names):
                        return True
            except (OSError, ValueError, http.client.HTTPException):
                pass
            self.stopping.wait(0.3)
        return False

    def start(self):
        with self.lifecycle:
            with update.file_lock(self.runtime_dir):
                # 上次进程被强制中止时可能留下下载目录；只清理管理进程自身的目录。
                for folder in self.runtime_dir.glob(".download-*"):
                    if folder.is_dir() and not folder.is_symlink():
                        shutil.rmtree(folder)
                self.prune()
            self.launch()
            if not self.healthy():
                self.stop_child()
                raise update.UpdateError("模拟器启动失败，HTTP/PTY 未就绪")
            if not (self.runtime_dir / "state.json").exists():
                durable_json(self.runtime_dir / "state.json", self.current)
        self.monitor = threading.Thread(target=self.supervise, daemon=True, name="child-supervisor")
        self.monitor.start()

    def supervise(self):
        failures = []
        while not self.stopping.wait(1):
            with self.lifecycle:
                if self.stopping.is_set() or self.child is None or self.child.poll() is None:
                    continue
                code = self.child.returncode
                self.stop_child()
                now = time.monotonic()
                failures = [stamp for stamp in failures if now - stamp < 60]
                if len(failures) >= 3:
                    self.set_result(state="error", message="模拟器一分钟内多次退出，已停止自动重启")
                    update.log("[slaveSim 管理] 模拟器异常退出，停止自动重启")
                    continue
                failures.append(now)
                update.log("[slaveSim 管理] 模拟器异常退出（" + str(code) + "），尝试恢复最后提交版本")
                try:
                    self.launch()
                    if not self.healthy(timeout=10):
                        raise update.UpdateError("恢复 HTTP/PTY 健康检查失败")
                except Exception as exc:
                    self.set_result(state="error", message="模拟器恢复失败：" + str(exc))

    def close(self):
        self.stopping.set()
        with self.lifecycle:
            self.stop_child()
        if self.worker and self.worker is not threading.current_thread():
            self.worker.join(timeout=3)
        if self.monitor and self.monitor is not threading.current_thread():
            self.monitor.join(timeout=2)


class RuntimeServer(ThreadingHTTPServer):
    daemon_threads = True
    allow_reuse_address = True

    def __init__(self, address, manager):
        self.manager = manager
        super().__init__(address, RuntimeHandler)


class RuntimeHandler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def setup(self):
        super().setup()
        self.connection.settimeout(15)

    def log_message(self, format_string, *args):
        update.log("[slaveSim HTTP] " + format_string % args)

    def json_response(self, code, data):
        raw = json.dumps(data, ensure_ascii=False).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Cache-Control", "no-store")
        self.send_header("Content-Length", str(len(raw)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.close_connection = True
        if self.command != "HEAD":
            self.wfile.write(raw)

    def fail(self, code, message):
        self.json_response(code, {"error": message})

    def body(self, limit):
        sizes = self.headers.get_all("Content-Length", [])
        if self.headers.get("Transfer-Encoding") or len(sizes) > 1:
            raise ValueError("不支持请求传输编码或重复长度")
        length = sizes[0] if sizes else "0"
        if not re.fullmatch(r"[0-9]+", length):
            raise ValueError("请求长度不正确")
        length = int(length)
        if length > limit:
            raise OverflowError("请求内容超过大小限制")
        raw = self.rfile.read(length)
        if len(raw) != length:
            raise ValueError("请求内容不完整")
        return raw

    def action_allowed(self):
        if self.headers.get("X-SlaveSim-Action") != "update":
            return False
        origins = self.headers.get_all("Origin", [])
        hosts = self.headers.get_all("Host", [])
        if len(hosts) != 1 or len(origins) > 1:
            return False
        if not origins:
            return True
        try:
            origin = urlsplit(origins[0])
            host = urlsplit("//" + hosts[0])
            return (origin.scheme in ("http", "https") and not origin.username and not origin.password
                    and not origin.path and not origin.query and not origin.fragment
                    and origin.hostname == host.hostname
                    and (origin.port or (443 if origin.scheme == "https" else 80))
                    == (host.port or (443 if origin.scheme == "https" else 80)))
        except ValueError:
            return False

    def handle_request(self):
        parsed = urlsplit(self.path)
        if parsed.path.startswith("/api/update/"):
            if parsed.query or parsed.fragment:
                self.fail(400, "更新接口不接受查询参数")
                return
            if parsed.path == "/api/update/status":
                if self.command != "GET":
                    self.fail(405, "状态接口只接受 GET")
                else:
                    self.json_response(200, self.server.manager.status())
                return
            if parsed.path not in ("/api/update/check", "/api/update/apply"):
                self.fail(404, "更新接口不存在")
                return
            if self.command != "POST":
                self.fail(405, "更新操作只接受 POST")
                return
            if not self.action_allowed():
                self.fail(403, "缺少更新操作标记或 Origin 与 Host 不一致")
                return
            try:
                raw = self.body(MAX_UPDATE_REQUEST)
                if raw and json.loads(raw) != {}:
                    raise ValueError("更新操作不接受运行参数")
                self.server.manager.begin(parsed.path.rsplit("/", 1)[1])
                self.json_response(202, self.server.manager.status())
            except BusyError as exc:
                self.fail(409, str(exc))
            except update.UpdateError as exc:
                self.fail(503, str(exc))
            except OverflowError as exc:
                self.fail(413, str(exc))
            except (ValueError, OSError) as exc:
                self.fail(400, str(exc))
            return
        self.proxy()

    def proxy(self):
        connection = http.client.HTTPConnection("127.0.0.1", self.server.manager.child_port, timeout=3)
        headers_sent = False
        try:
            # 配置保存上限与 Go 接口一致，其他代理请求沿用原有大小限制。
            limit = MAX_CONFIG_REQUEST if self.command == "PUT" and urlsplit(self.path).path == "/api/config" else MAX_REQUEST
            raw = self.body(limit)
            blocked = HOP_HEADERS | {part.strip().lower() for part in self.headers.get("Connection", "").split(",")}
            headers = {name: value for name, value in self.headers.items() if name.lower() not in blocked}
            headers["Host"] = "127.0.0.1:" + str(self.server.manager.child_port)
            headers["Connection"] = "close"
            connection.request(self.command, self.path, raw if raw else None, headers)
            socket = connection.sock
            response = connection.getresponse()
            if socket:
                # SSE 没有固定心跳；允许空闲连接，子进程退出会关闭旧流。
                socket.settimeout(None if response.getheader("Content-Type", "").startswith("text/event-stream") else 30)
            blocked = HOP_HEADERS | {part.strip().lower() for part in response.getheader("Connection", "").split(",")}
            self.send_response(response.status)
            for name, value in response.getheaders():
                if name.lower() not in blocked and name.lower() not in ("server", "date"):
                    self.send_header(name, value)
            self.send_header("Connection", "close")
            self.end_headers()
            self.close_connection = True
            headers_sent = True
            if self.command != "HEAD":
                while not self.server.manager.stopping.is_set():
                    chunk = response.read1(64 * 1024)
                    if not chunk:
                        break
                    self.wfile.write(chunk)
                    self.wfile.flush()
        except OverflowError as exc:
            self.fail(413, str(exc))
        except ValueError as exc:
            if not headers_sent:
                self.fail(400, str(exc))
        except (OSError, http.client.HTTPException) as exc:
            if not headers_sent:
                self.fail(502, "模拟器暂时不可用：" + str(exc))
        finally:
            connection.close()

    do_GET = do_POST = do_HEAD = do_PUT = do_DELETE = do_PATCH = do_OPTIONS = do_TRACE = handle_request


def listen_address(value):
    host, separator, port = value.rpartition(":")
    if not separator or not port.isdigit() or not 1 <= int(port) <= 65535:
        raise argparse.ArgumentTypeError("HTTP 地址必须为 host:port 或 :port")
    return host or "0.0.0.0", int(port)


def main(argv=None):
    parser = argparse.ArgumentParser(description="slaveSim 单容器管理进程")
    parser.add_argument("-config", default="/etc/slavesim/config.json")
    parser.add_argument("-http", default=":16000", type=listen_address)
    parser.add_argument("-logfile", default="")
    parser.add_argument("-version", action="store_true")
    parser.add_argument("--runtime-dir", type=Path, default=Path("/var/lib/slavesim"))
    parser.add_argument("--updater-config", type=Path, default=Path("/etc/slavesim/updater.json"))
    parser.add_argument("--seed-binary", type=Path, default=Path("/slavesim"), help=argparse.SUPPRESS)
    parser.add_argument("--probe-binary", type=Path, default=Path("/probe"), help=argparse.SUPPRESS)
    parser.add_argument("--child-port", type=int, default=16001, help=argparse.SUPPRESS)
    args = parser.parse_args(argv)
    if args.version:
        try:
            binary = args.seed_binary
            if (args.runtime_dir / "state.json").exists():
                installed = Manager(args.runtime_dir, args.updater_config, args.config, args.logfile,
                                    args.seed_binary, args.probe_binary, args.child_port)
                binary = installed.binary_path()
            return subprocess.call([str(binary), "-version"])
        except (update.UpdateError, OSError, ValueError) as exc:
            update.log("[slaveSim 管理] 版本检查失败：" + str(exc))
            return 1
    manager = server = None
    stopped = threading.Event()
    def stop(signum, frame):
        stopped.set()
        if manager:
            manager.stopping.set()
    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    try:
        manager = Manager(args.runtime_dir, args.updater_config, args.config, args.logfile,
                          args.seed_binary, args.probe_binary, args.child_port)
        manager.start()
        server = RuntimeServer(args.http, manager)
        server.timeout = 0.5
        update.log("[slaveSim 管理] 网页代理已启动：" + str(args.http))
        while not stopped.is_set():
            server.handle_request()
        return 0
    except (update.UpdateError, OSError, ValueError) as exc:
        update.log("[slaveSim 管理] 启动失败：" + str(exc))
        return 1
    finally:
        if manager:
            manager.close()
        if server:
            server.server_close()


if __name__ == "__main__":
    sys.exit(main())
