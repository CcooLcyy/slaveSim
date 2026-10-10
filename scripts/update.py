#!/usr/bin/env python3
"""slaveSim 公开发布清单检查、Docker 镜像更新与失败回滚（仅标准库）。"""
import argparse
from contextlib import contextmanager
from datetime import datetime
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import sys
import tempfile
import tarfile
import time
from urllib.parse import unquote, urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener

try:
    import fcntl
except ImportError:
    fcntl = None  # Windows 可执行检查及单元测试，设备更新必须在 Linux 上运行。

DEFAULT_MANIFEST = "https://pub-19f3d71852b04011b120b1b814141c12.r2.dev/slavesim/ci/latest.json"
NETWORK_TIMEOUT = 30
MAX_MANIFEST = 1024 * 1024
MAX_ARCHIVE = 2 * 1024 * 1024 * 1024
ENV_KEYS = ("SLAVESIM_IMAGE", "CONFIG_PATH", "SERIAL_SIM_DIR", "LOG_DIR", "HTTP_PORT")


class UpdateError(RuntimeError):
    pass


def log(message):
    print(message, flush=True)


def validate_url(url, reference=None):
    parsed = urlsplit(url)
    if parsed.scheme != "https" or not parsed.hostname or parsed.username or parsed.password:
        raise UpdateError("下载地址必须为无认证信息的 HTTPS 绝对地址")
    if parsed.query or parsed.fragment or "\\" in parsed.path:
        raise UpdateError("发布地址不能包含查询参数、片段或反斜杠")
    decoded = parsed.path
    for _ in range(4):
        next_path = unquote(decoded)
        if next_path == decoded:
            break
        decoded = next_path
    if decoded != parsed.path or any(part in (".", "..") for part in decoded.split("/")):
        raise UpdateError("发布地址不能包含编码路径或路径穿越")
    if reference:
        base = validate_url(reference)
        prefix = base.path.rsplit("/", 1)[0] + "/"
        if parsed.netloc.lower() != base.netloc.lower() or not parsed.path.startswith(prefix):
            raise UpdateError("下载地址必须位于清单相同来源与路径前缀内")
    return parsed


class SafeRedirect(HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, msg, headers, newurl):
        validate_url(newurl, request.full_url)
        return super().redirect_request(request, fp, code, msg, headers, newurl)


def open_https(url):
    validate_url(url)
    return build_opener(SafeRedirect()).open(
        Request(url, headers={"User-Agent": "slavesim-updater/1", "Cache-Control": "no-cache"}),
        timeout=NETWORK_TIMEOUT)


def fetch_manifest(url):
    try:
        with open_https(url) as response:
            raw = response.read(MAX_MANIFEST + 1)
        if len(raw) > MAX_MANIFEST:
            raise UpdateError("版本清单超过大小限制")
        return json.loads(raw)
    except (ValueError, OSError) as exc:
        raise UpdateError("读取版本清单失败：" + str(exc)) from exc


def platform_key():
    arch = {"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(platform.machine().lower())
    if not arch:
        raise UpdateError("不支持的设备架构：" + platform.machine())
    return "linux-" + arch


def version_tuple(channel, version):
    expression = r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"
    if channel == "ci":
        match = re.fullmatch(r"0\.0\.0-ci\.([1-9][0-9]*)\.([1-9][0-9]*)", version)
    elif channel == "stable":
        match = re.fullmatch(expression, version)
    else:
        raise UpdateError("发布通道必须为 ci 或 stable")
    if not match:
        raise UpdateError("版本号与发布通道不匹配：" + str(version))
    return tuple(int(part) for part in match.groups())


def validate_manifest(data, manifest_url, selected_platform):
    base = validate_url(manifest_url)
    if not isinstance(data, dict) or type(data.get("schema_version")) is not int or data["schema_version"] != 1:
        raise UpdateError("版本清单 schema_version 必须为 1")
    if data.get("project") != "slavesim" or data.get("channel") not in ("ci", "stable"):
        raise UpdateError("版本清单项目或通道不正确")
    if base.path.split("/")[-2:] != [data["channel"], "latest.json"]:
        raise UpdateError("版本清单地址与发布通道不一致")
    version_tuple(data["channel"], str(data.get("version", "")))
    if not re.fullmatch(r"[0-9a-f]{40}", str(data.get("commit", ""))):
        raise UpdateError("版本清单提交号不正确")
    try:
        published = datetime.fromisoformat(str(data.get("published_at", "")).replace("Z", "+00:00"))
        if published.tzinfo is None:
            raise ValueError("缺少时区")
    except ValueError as exc:
        raise UpdateError("版本清单发布时间不正确") from exc
    entries = data.get("platforms")
    if not isinstance(entries, dict) or selected_platform not in entries:
        raise UpdateError("版本清单没有当前架构的镜像")
    for key, artifact in entries.items():
        if key not in ("linux-amd64", "linux-arm64") or not isinstance(artifact, dict):
            raise UpdateError("版本清单架构条目不正确")
        validate_url(str(artifact.get("url", "")), manifest_url)
        if type(artifact.get("size")) is not int or not 0 < artifact["size"] <= MAX_ARCHIVE:
            raise UpdateError("镜像包大小不正确或超过限制")
        if not re.fullmatch(r"[0-9a-f]{64}", str(artifact.get("sha256", ""))):
            raise UpdateError("镜像包 SHA-256 不正确")
        if not re.fullmatch(r"sha256:[0-9a-f]{64}", str(artifact.get("image_id", ""))):
            raise UpdateError("镜像内容 ID 不正确")
        if artifact.get("image") != "slavesim:" + data["version"] + "-" + key.split("-")[1]:
            raise UpdateError("镜像标签与版本和架构不匹配")
        binary_fields = ("binary_url", "binary_sha256", "binary_size")
        if any(field in artifact for field in binary_fields):
            validate_url(str(artifact.get("binary_url", "")), manifest_url)
            if not re.fullmatch(r"[0-9a-f]{64}", str(artifact.get("binary_sha256", ""))):
                raise UpdateError("二进制 SHA-256 不正确")
            if type(artifact.get("binary_size")) is not int or not 0 < artifact["binary_size"] <= MAX_ARCHIVE:
                raise UpdateError("二进制大小不正确")
    return entries[selected_platform]


def needs_update(manifest, state):
    if not state:
        return True
    old_channel = state.get("channel", manifest["channel"])
    if old_channel != manifest["channel"]:
        raise UpdateError("不允许自动切换发布通道，请人工处理")
    new = version_tuple(manifest["channel"], manifest["version"])
    old = version_tuple(old_channel, str(state.get("version", "")))
    if new == old:
        if manifest["commit"] != state.get("commit"):
            raise UpdateError("同版本对应不同提交，拒绝覆盖")
        selected = state.get("platform")
        if selected and manifest["platforms"].get(selected, {}).get("image_id") != state.get("image_id"):
            raise UpdateError("相同版本的镜像身份发生变化，拒绝覆盖")
        return False
    if new < old:
        raise UpdateError("远端版本低于已安装版本，拒绝回退")
    return True


def atomic_bytes(path, content):
    path = Path(path)
    descriptor, temporary = tempfile.mkstemp(prefix="." + path.name + ".", dir=path.parent)
    try:
        with os.fdopen(descriptor, "wb") as target:
            target.write(content)
            target.flush()
            os.fsync(target.fileno())
        os.replace(temporary, path)
    finally:
        Path(temporary).unlink(missing_ok=True)


def atomic_json(path, data):
    atomic_bytes(path, (json.dumps(data, ensure_ascii=False, indent=2) + "\n").encode("utf-8"))


def read_json(path):
    try:
        return json.loads(Path(path).read_text(encoding="utf-8"))
    except (OSError, ValueError) as exc:
        raise UpdateError("读取部署配置失败：" + str(path)) from exc


@contextmanager
def file_lock(folder):
    if fcntl is None:
        raise UpdateError("设备更新必须在支持 fcntl 的 Linux 上运行")
    with (folder / ".update.lock").open("a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as exc:
            raise UpdateError("已有更新任务运行，本次退出") from exc
        try:
            yield
        finally:
            fcntl.flock(lock, fcntl.LOCK_UN)


def download_artifact(artifact, folder):
    descriptor, filename = tempfile.mkstemp(prefix="image-", suffix=".tar.gz", dir=folder)
    path = Path(filename)
    count, digest = 0, hashlib.sha256()
    try:
        with os.fdopen(descriptor, "wb") as target, open_https(artifact["url"]) as response:
            while True:
                chunk = response.read(min(1024 * 1024, artifact["size"] - count + 1))
                if not chunk:
                    break
                count += len(chunk)
                if count > artifact["size"]:
                    raise UpdateError("镜像包实际大小超过清单声明")
                digest.update(chunk)
                target.write(chunk)
        if count != artifact["size"] or digest.hexdigest() != artifact["sha256"]:
            raise UpdateError("镜像包大小或 SHA-256 校验失败")
        return path
    except Exception as exc:
        path.unlink(missing_ok=True)
        if isinstance(exc, UpdateError):
            raise
        raise UpdateError("下载镜像包失败：" + str(exc)) from exc


def image_archive_digest(path, image):
    # 旧式存储返回配置摘要，新式存储可能返回 OCI 索引摘要；归档配置摘要跨环境一致。
    try:
        with tarfile.open(path, "r:*") as archive:
            manifest_member = archive.getmember("manifest.json")
            if not manifest_member.isfile() or manifest_member.size > MAX_MANIFEST:
                raise UpdateError("镜像归档清单不合法")
            entries = json.load(archive.extractfile(manifest_member))
            if not isinstance(entries, list) or not entries:
                raise UpdateError("镜像归档清单为空")
            if any(not isinstance(item, dict) or not isinstance(item.get("RepoTags") or [], list) for item in entries):
                raise UpdateError("镜像归档标签列表不合法")
            selected = [item for item in entries if image in (item.get("RepoTags") or [])]
            if len(selected) != 1 or any(tag != image for item in entries for tag in (item.get("RepoTags") or [])):
                raise UpdateError("镜像归档标签与发布清单不一致")
            config_member = archive.getmember(selected[0]["Config"])
            if not config_member.isfile() or config_member.size > MAX_MANIFEST:
                raise UpdateError("镜像配置内容不合法")
            content = archive.extractfile(config_member).read()
            return "sha256:" + hashlib.sha256(content).hexdigest()
    except (OSError, ValueError, KeyError, TypeError, tarfile.TarError) as exc:
        raise UpdateError("读取镜像归档失败：" + str(exc)) from exc


def run(args):
    try:
        result = subprocess.run(args, check=True, text=True, capture_output=True, timeout=180)
        return result.stdout
    except (subprocess.CalledProcessError, subprocess.TimeoutExpired, OSError) as exc:
        detail = getattr(exc, "stderr", None) or str(exc)
        raise UpdateError("命令执行失败：" + " ".join(args) + "\n" + str(detail)) from exc


def compose_args(folder):
    return ["docker", "compose", "--project-name", "slavesim", "--project-directory", str(folder),
            "--env-file", str(folder / ".env"), "-f", str(folder / "compose.release.yml")]


def inspect_container(runner):
    try:
        return json.loads(runner(["docker", "inspect", "slavesim"]))[0]
    except UpdateError as exc:
        if "no such object" in str(exc).lower() or "no such container" in str(exc).lower():
            return None
        raise
    except (ValueError, IndexError, TypeError) as exc:
        raise UpdateError("容器检查结果不正确") from exc


def verify_owned(container, folder):
    if container is None:
        return
    labels = container.get("Config", {}).get("Labels") or {}
    if (labels.get("com.docker.compose.project") != "slavesim"
            or labels.get("com.docker.compose.service") != "slavesim"
            or labels.get("com.docker.compose.project.working_dir") != str(folder)):
        raise UpdateError("同名容器不属于当前部署目录管理，拒绝自动接管")


def settings_for(folder):
    settings = read_json(folder / "updater.json")
    validate_url(settings.get("manifest_url", ""))
    for name in ("config_path", "serial_dir", "log_dir"):
        value = settings.get(name)
        if not isinstance(value, str) or not Path(value).is_absolute() or any(char in value for char in "\n\r\x00"):
            raise UpdateError("部署路径必须为绝对路径：" + name)
    if not Path(settings["config_path"]).is_file():
        raise UpdateError("既有 config.json 不存在；更新器不会生成或覆盖现场配置")
    config = read_json(settings["config_path"])
    buses = config.get("buses") if isinstance(config, dict) else None
    if not isinstance(buses, list) or not buses:
        raise UpdateError("现场配置必须包含至少一条总线")
    for bus in buses:
        link = bus.get("link") if isinstance(bus, dict) else None
        if (not isinstance(link, str) or not Path(link).is_absolute()
                or not Path(link).parent.resolve().is_relative_to(Path(settings["serial_dir"]).resolve())):
            raise UpdateError("现场配置的串口软链接必须位于共享目录内")
    if type(settings.get("http_port")) is not int or not 1 <= settings["http_port"] <= 65535:
        raise UpdateError("HTTP 端口必须位于 1 到 65535")
    if not (folder / "compose.release.yml").is_file():
        raise UpdateError("缺少部署 Compose 文件，请先执行 init")
    return settings


def env_content(settings, image):
    values = (image, settings["config_path"], settings["serial_dir"], settings["log_dir"], str(settings["http_port"]))
    # Compose 单引号值避免路径中的空格、# 和 $ 被当作语法或变量替换。
    if any("'" in value or "\n" in value or "\r" in value for value in values):
        raise UpdateError("部署环境值不能包含单引号或换行")
    return "".join(key + "='" + value + "'\n" for key, value in zip(ENV_KEYS, values)).encode("utf-8")


def write_env(folder, settings, image):
    atomic_bytes(folder / ".env", env_content(settings, image))


def wait_healthy(folder, settings, image_id, runner=run, timeout=45):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            container = inspect_container(runner)
            verify_owned(container, folder)
            if container and container.get("Image") == image_id and container.get("State", {}).get("Running"):
                # 健康探测只访问本机，且每条总线都必须已经创建 PTY。
                from urllib.request import urlopen
                with urlopen("http://127.0.0.1:" + str(settings["http_port"]) + "/api/state", timeout=3) as response:
                    health = json.loads(response.read(MAX_MANIFEST + 1))
                buses = health.get("buses")
                if isinstance(buses, list) and buses and all(isinstance(bus, dict) and bus.get("slave_path") for bus in buses):
                    return True
        except (UpdateError, OSError, ValueError):
            pass
        time.sleep(1)
    return False


def check_update(folder):
    settings = settings_for(folder)
    data = fetch_manifest(settings["manifest_url"])
    validate_manifest(data, settings["manifest_url"], platform_key())
    state = read_json(folder / "state.json") if (folder / "state.json").exists() else None
    available = needs_update(data, state)
    log(("发现新版本：" if available else "当前已是最新版本：") + data["version"])
    return available


def apply_update(folder, allow_initial_install=False, runner=run):
    if platform.system() != "Linux":
        raise UpdateError("Docker 设备更新只支持 Linux")
    folder = Path(folder).resolve()
    with file_lock(folder):
        settings = settings_for(folder)
        old_state = read_json(folder / "state.json") if (folder / "state.json").exists() else None
        if not old_state and not allow_initial_install:
            raise UpdateError("首次安装必须显式传入 --allow-initial-install")
        previous = inspect_container(runner)
        verify_owned(previous, folder)
        if previous and not old_state:
            raise UpdateError("已有容器但缺少安装状态，拒绝自动接管")
        if old_state and not (folder / ".env").exists():
            raise UpdateError("部署环境文件丢失，拒绝更新")
        if old_state:
            old_runtime_id = old_state.get("runtime_image_id", old_state.get("image_id"))
            if previous and previous.get("Image") != old_runtime_id:
                raise UpdateError("运行中的镜像与安装状态不一致，拒绝自动更新")
            if (folder / ".env").read_bytes() != env_content(settings, old_state["image"]):
                raise UpdateError("部署环境文件与安装状态不一致，请先人工核对")
            old_image = json.loads(runner(["docker", "image", "inspect", old_state["image"]]))[0]
            if old_image.get("Id") != old_runtime_id:
                raise UpdateError("旧镜像与安装状态不一致，无法保证回滚")
        data = fetch_manifest(settings["manifest_url"])
        key = platform_key()
        artifact = validate_manifest(data, settings["manifest_url"], key)
        if not needs_update(data, old_state):
            log("当前已是最新版本：" + data["version"])
            return False
        with tempfile.TemporaryDirectory(prefix=".download-", dir=folder) as temporary:
            archive = download_artifact(artifact, Path(temporary))
            if image_archive_digest(archive, artifact["image"]) != artifact["image_id"]:
                raise UpdateError("镜像归档配置摘要与发布清单不一致")
            log("镜像包大小和 SHA-256 校验通过，导入镜像：" + artifact["image"])
            runner(["docker", "load", "-i", str(archive)])
            loaded = json.loads(runner(["docker", "image", "inspect", artifact["image"]]))[0]
            if loaded.get("Os") != "linux" or loaded.get("Architecture") != key.split("-")[1]:
                raise UpdateError("导入镜像的内容 ID 或架构与清单不一致")
            verified = Path(temporary) / "loaded-image.tar"
            runner(["docker", "image", "save", "-o", str(verified), artifact["image"]])
            if image_archive_digest(verified, artifact["image"]) != artifact["image_id"]:
                raise UpdateError("导入镜像的配置摘要与发布清单不一致")
            runtime_id = loaded["Id"]
        old_env = (folder / ".env").read_bytes() if (folder / ".env").exists() else None
        command = compose_args(folder)
        try:
            write_env(folder, settings, artifact["image"])
            runner(command + ["up", "-d", "--no-build", "--pull", "never"])
            if not wait_healthy(folder, settings, runtime_id, runner):
                raise UpdateError("新版本运行状态或 PTY/HTTP 健康检查失败")
            state = {name: data[name] for name in ("version", "commit", "channel")}
            state.update(image=artifact["image"], image_id=artifact["image_id"], runtime_image_id=runtime_id, platform=key)
            atomic_json(folder / "state.json", state)
        except Exception as exc:
            log("更新失败，恢复之前的部署：" + str(exc))
            try:
                if old_env is not None:
                    atomic_bytes(folder / ".env", old_env)
                if old_state:
                    # 用旧镜像标签恢复；旧镜像始终保留，不执行镜像清理。
                    runner(command + ["up", "-d", "--no-build", "--pull", "never"])
                    if not wait_healthy(folder, settings, old_state.get("runtime_image_id", old_state["image_id"]), runner):
                        raise UpdateError("旧版本健康检查失败")
                else:
                    container = inspect_container(runner)
                    verify_owned(container, folder)
                    if container:
                        runner(command + ["down"])
                    if old_env is None:
                        (folder / ".env").unlink(missing_ok=True)
            except Exception as rollback:
                raise UpdateError("更新失败且回滚失败，需人工处理：" + str(rollback)) from exc
            raise UpdateError("更新失败，已回滚：" + str(exc)) from exc
        log("更新成功：" + data["version"])
        return True


def init_deployment(folder, config, serial_dir, log_dir, http_port, manifest_url):
    validate_url(manifest_url)
    if any((folder / name).exists() for name in ("updater.json", "state.json", ".env", "update.py", "compose.release.yml")):
        raise UpdateError("部署目录已经初始化，拒绝覆盖")
    if not config.is_file():
        raise UpdateError("请提供既有 config.json，更新器不会生成现场配置")
    if type(http_port) is not int or not 1 <= http_port <= 65535:
        raise UpdateError("HTTP 端口必须位于 1 到 65535")
    settings = dict(manifest_url=manifest_url, config_path=str(config), serial_dir=str(serial_dir),
                    log_dir=str(log_dir), http_port=http_port)
    env_content(settings, "slavesim:pending")
    folder.mkdir(parents=True, exist_ok=True)
    serial_dir.mkdir(parents=True, exist_ok=True)
    log_dir.mkdir(parents=True, exist_ok=True)
    source = Path(__file__).resolve().parents[1] / "deploy/compose.release.yml"
    shutil.copyfile(source, folder / "compose.release.yml")
    shutil.copyfile(Path(__file__).resolve(), folder / "update.py")
    atomic_json(folder / "updater.json", settings)
    settings_for(folder)
    log("部署目录初始化完成，尚未安装镜像或启动容器：" + str(folder))


def install_timer(folder):
    if platform.system() != "Linux" or os.geteuid() != 0:
        raise UpdateError("安装 systemd 定时器需要在 Linux 上以 root 执行")
    settings_for(folder)
    if not (folder / "state.json").exists():
        raise UpdateError("请先成功完成首次安装，再启用自动更新定时器")
    # 固定 Python 与更新脚本绝对路径；不通过 shell 解释设备参数。
    quoted = str(folder / "update.py").replace("\\", "\\\\").replace('"', '\\"').replace("%", "%%")
    directory = str(folder).replace("\\", "\\\\").replace('"', '\\"').replace("%", "%%")
    service = '[Unit]\nDescription=slaveSim 自动更新\nAfter=network-online.target docker.service\nWants=network-online.target\nRequires=docker.service\n\n[Service]\nType=oneshot\nUser=root\nUMask=0077\nExecStart=/usr/bin/python3 "' + quoted + '" apply --deploy-dir "' + directory + '"\nTimeoutStartSec=600\n'
    timer = '[Unit]\nDescription=定期检查 slaveSim 发布版本\n\n[Timer]\nOnBootSec=2min\nOnUnitInactiveSec=5min\nRandomizedDelaySec=30s\nUnit=slavesim-update.service\n\n[Install]\nWantedBy=timers.target\n'
    atomic_bytes(Path("/etc/systemd/system/slavesim-update.service"), service.encode("utf-8"))
    atomic_bytes(Path("/etc/systemd/system/slavesim-update.timer"), timer.encode("utf-8"))
    run(["systemctl", "daemon-reload"])
    run(["systemctl", "enable", "--now", "slavesim-update.timer"])
    log("已启用 slaveSim 更新定时器，每 5 分钟检查一次")


def absolute_path(value):
    path = Path(value).expanduser()
    if not path.is_absolute():
        raise argparse.ArgumentTypeError("路径必须为绝对路径")
    return path.resolve()


def main():
    parser = argparse.ArgumentParser(description="slaveSim R2 发布检查及设备自动更新")
    commands = parser.add_subparsers(dest="command", required=True)
    archive_command = commands.add_parser("image-id", help="读取镜像归档的可移植配置摘要")
    archive_command.add_argument("--archive", type=Path, required=True)
    archive_command.add_argument("--image", required=True)
    for name in ("check", "apply", "init", "install-timer"):
        child = commands.add_parser(name)
        child.add_argument("--deploy-dir", required=True, type=absolute_path, help="独立的更新部署目录")
        if name == "apply":
            child.add_argument("--allow-initial-install", action="store_true", help="明确允许首次安装")
        if name == "init":
            child.add_argument("--config", required=True, type=absolute_path, help="既有配置文件路径")
            child.add_argument("--serial-dir", type=absolute_path, default=Path("/home/daniel/serial-sim"))
            child.add_argument("--log-dir", type=absolute_path)
            child.add_argument("--http-port", type=int, default=16000)
            child.add_argument("--manifest-url", default=DEFAULT_MANIFEST)
    args = parser.parse_args()
    try:
        if args.command == "image-id":
            print(image_archive_digest(args.archive, args.image))
        elif args.command == "init":
            init_deployment(args.deploy_dir, args.config, args.serial_dir,
                            args.log_dir or args.deploy_dir / "log", args.http_port, args.manifest_url)
        elif args.command == "check":
            check_update(args.deploy_dir)
        elif args.command == "apply":
            apply_update(args.deploy_dir, args.allow_initial_install)
        else:
            install_timer(args.deploy_dir)
        return 0
    except (UpdateError, OSError, ValueError, KeyError, IndexError, TypeError) as exc:
        log("操作失败：" + str(exc))
        return 1


if __name__ == "__main__":
    sys.exit(main())
