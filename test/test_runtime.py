"""单容器管理进程的边界、回滚与代理测试；无需 Docker 或设备。"""
import contextlib
import copy
import hashlib
import io
import json
from pathlib import Path
import sys
import tarfile
import tempfile
import threading
import time
import unittest
from unittest import mock
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.request import Request, urlopen
from urllib.error import HTTPError

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "scripts"))
import runtime
import update
from test_update import manifest, BASE

OLD = dict(version="0.0.0-ci.12.1", commit="a" * 40, channel="ci", release="seed")


class ArchiveTests(unittest.TestCase):
    def archive(self, folder, entries):
        path = Path(folder) / "package.tar.gz"
        with tarfile.open(path, "w:gz") as archive:
            for name, kind, data in entries:
                item = tarfile.TarInfo(name)
                item.type = kind
                item.size = len(data) if kind == tarfile.REGTYPE else 0
                item.linkname = "slavesim" if kind in (tarfile.SYMTYPE, tarfile.LNKTYPE) else ""
                archive.addfile(item, io.BytesIO(data) if item.size else None)
        return path

    def test_only_two_plain_binaries_are_extracted(self):
        good = [("slavesim", tarfile.REGTYPE, b"binary"), ("probe", tarfile.REGTYPE, b"probe")]
        with tempfile.TemporaryDirectory() as folder:
            out = Path(folder) / "out"
            runtime.extract_binaries(self.archive(folder, good), out)
            self.assertEqual((out / "slavesim").read_bytes(), b"binary")
        with tempfile.TemporaryDirectory() as folder:
            out = Path(folder) / "out"
            runtime.extract_binaries(self.archive(folder, good + [("scripts/runtime.py", tarfile.REGTYPE, b"untrusted"),
                                                                  ("config.json", tarfile.REGTYPE, b"untrusted")]), out)
            self.assertEqual({p.name for p in out.iterdir()}, {"slavesim", "probe"})
        for bad in ([good[0]], good + [good[0]],
                    [("../slavesim", tarfile.REGTYPE, b"x"), good[1]],
                    [("/slavesim", tarfile.REGTYPE, b"x"), good[1]],
                    [("slavesim", tarfile.SYMTYPE, b""), good[1]],
                    [("slavesim", tarfile.LNKTYPE, b""), good[1]],
                    [("./slavesim", tarfile.REGTYPE, b"x"), good[1]]):
            with self.subTest(entries=bad), tempfile.TemporaryDirectory() as folder:
                with self.assertRaises(update.UpdateError):
                    runtime.extract_binaries(self.archive(folder, bad), Path(folder) / "out")

    def test_expanded_size_limit(self):
        with tempfile.TemporaryDirectory() as folder:
            path = self.archive(folder, [("slavesim", tarfile.REGTYPE, b"12345"), ("probe", tarfile.REGTYPE, b"12345")])
            with mock.patch.object(runtime, "MAX_EXPANDED", 9), self.assertRaises(update.UpdateError):
                runtime.extract_binaries(path, Path(folder) / "out")

    def test_candidate_must_match_full_commit_and_version(self):
        expected = manifest()
        for output in ("slaveSim 0.0.0-ci.12.1 提交=" + "a" * 40 + " 构建时间=unknown\n",
                       "slaveSim 0.0.0-ci.12.1 提交=" + "b" * 40 + " 构建时间=unknown\n",
                       "slaveSim 0.0.0-ci.12.2 提交=" + "a" * 40 + " 构建时间=unknown\n",
                       "slaveSim 0.0.0-ci.12.1 提交=aaaaaaa 构建时间=unknown\n"):
            with self.subTest(output=output), mock.patch.object(runtime.subprocess, "run", return_value=mock.Mock(stdout=output)):
                if "12.1 提交=" + "a" * 40 in output:
                    runtime.verify_candidate(Path("candidate"), expected)
                else:
                    with self.assertRaises(update.UpdateError):
                        runtime.verify_candidate(Path("candidate"), expected)


class ManagerFixture(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.settings = self.root / "updater.json"
        self.settings.write_text(json.dumps(dict(manifest_url=BASE, command="ignored", automatic=True)), encoding="utf-8")
        self.identity = mock.patch.object(runtime, "binary_identity", return_value=dict(version=OLD["version"], commit=OLD["commit"]))
        self.identity.start()
        self.manager = runtime.Manager(self.root / "runtime", self.settings, seed=Path("seed"), probe=Path("probe"))
        self.lock = mock.patch.object(update, "file_lock", side_effect=lambda _: contextlib.nullcontext())
        self.lock.start()
        self.data = manifest("0.0.0-ci.12.2")
        self.data["platforms"]["linux-amd64"].update(binary_url=BASE.replace("latest.json", "releases/new/bin.tar.gz"), binary_sha256="c" * 64, binary_size=123)

    def tearDown(self):
        self.manager.close()
        self.lock.stop()
        self.identity.stop()
        self.temp.cleanup()

    def mocks(self):
        stack = contextlib.ExitStack()
        stack.enter_context(mock.patch.object(update, "fetch_manifest", return_value=self.data))
        stack.enter_context(mock.patch.object(update, "platform_key", return_value="linux-amd64"))
        stack.enter_context(mock.patch.object(self.manager, "prepare", return_value=self.root / "runtime/releases" / runtime.artifact_version(self.data["version"])))
        stack.enter_context(mock.patch.object(self.manager, "launch"))
        stop = stack.enter_context(mock.patch.object(self.manager, "stop_child"))
        return stack, stop


class ManagerTests(ManagerFixture):

    def test_status_contract_and_config_only_manifest(self):
        self.assertEqual(set(self.manager.status()), set("enabled current_version latest_version source_url channel state message update_available job_id".split()))
        self.assertEqual(self.manager.status()["source_url"], BASE)
        self.assertEqual(self.manager.status()["state"], "idle")
        missing = runtime.Manager(self.root / "disabled", self.root / "missing", seed=Path("seed"))
        self.assertFalse(missing.status()["enabled"])
        missing.close()
        self.settings.write_text('{"manifest_url":"https://example.test"}', encoding="utf-8")
        invalid = runtime.Manager(self.root / "invalid", self.settings, seed=Path("seed"))
        self.assertFalse(invalid.status()["enabled"])
        self.assertIn("配置无效", invalid.status()["message"])
        invalid.close()

    def test_success_commits_and_restarts_from_committed_release(self):
        stack, stop = self.mocks()
        with stack, mock.patch.object(self.manager, "healthy", return_value=True):
            self.manager.run_job("apply")
        self.assertEqual(self.manager.status()["state"], "succeeded")
        saved = update.read_json(self.manager.runtime_dir / "state.json")
        self.assertEqual(saved["version"], self.data["version"])
        self.assertEqual(saved["previous"]["release"], "seed")
        self.assertEqual(stop.call_count, 1)
        # Persisted binaries must exist before a new manager accepts its release.
        release = self.manager.runtime_dir / "releases" / saved["release"]
        release.mkdir(parents=True, exist_ok=True)
        (release / "slavesim").write_bytes(b"x")
        (release / "probe").write_bytes(b"x")
        with mock.patch.object(runtime, "verify_candidate"):
            next_manager = runtime.Manager(self.manager.runtime_dir, self.settings, seed=Path("seed"))
        self.assertEqual(next_manager.current["version"], self.data["version"])
        self.assertEqual(next_manager.binary_path(), release / "slavesim")
        next_manager.close()

    def test_failed_candidate_rolls_back_without_committing(self):
        stack, stop = self.mocks()
        with stack, mock.patch.object(self.manager, "healthy", side_effect=[False, True]):
            self.manager.run_job("apply")
        self.assertEqual(self.manager.current["version"], OLD["version"])
        self.assertEqual(self.manager.status()["state"], "error")
        self.assertIn("已回滚", self.manager.status()["message"])
        self.assertFalse((self.manager.runtime_dir / "state.json").exists())
        self.assertEqual(stop.call_count, 2)

    def test_failed_rollback_is_reported(self):
        stack, _ = self.mocks()
        with stack, mock.patch.object(self.manager, "healthy", return_value=False):
            self.manager.run_job("apply")
        self.assertIn("回滚失败", self.manager.status()["message"])

    def test_validation_failure_keeps_old_process(self):
        stack, stop = self.mocks()
        with stack, mock.patch.object(self.manager, "prepare", side_effect=update.UpdateError("错误包")):
            self.manager.run_job("apply")
        stop.assert_not_called()
        self.assertEqual(self.manager.current["version"], OLD["version"])

    def test_same_version_does_not_restart_and_identity_conflict_fails(self):
        self.data = manifest()
        stack, stop = self.mocks()
        with stack:
            self.manager.run_job("apply")
        stop.assert_not_called()
        self.assertEqual(self.manager.status()["state"], "current")
        self.data["commit"] = "b" * 40
        stack, stop = self.mocks()
        with stack:
            self.manager.run_job("apply")
        self.assertEqual(self.manager.status()["state"], "error")
        stop.assert_not_called()

    def test_duplicate_requests_and_interrupted_task_recovery(self):
        entered, release = threading.Event(), threading.Event()
        def blocking(_):
            entered.set()
            release.wait(5)
            return self.data
        with mock.patch.object(update, "fetch_manifest", side_effect=blocking):
            self.manager.begin("check")
            self.assertTrue(entered.wait(2))
            with self.assertRaises(runtime.BusyError):
                self.manager.begin("apply")
            self.assertEqual(self.manager.status()["state"], "checking")
            # A fresh manager recovers only the last committed state.
            next_manager = runtime.Manager(self.manager.runtime_dir, self.settings, seed=Path("seed"))
            self.assertEqual(next_manager.current["version"], OLD["version"])
            self.assertEqual(next_manager.status()["state"], "error")
            self.assertIn("中断", next_manager.status()["message"])
            next_manager.close()
            release.set()
            self.manager.worker.join(3)

    def test_terminate_then_kill_and_reap(self):
        child = mock.Mock()
        child.poll.return_value = None
        child.wait.side_effect = [runtime.subprocess.TimeoutExpired("child", 5), 0]
        self.manager.child = child
        self.manager.stop_child()
        child.terminate.assert_called_once()
        child.kill.assert_called_once()
        self.assertEqual(child.wait.call_count, 2)

    def test_real_process_is_reaped_on_close(self):
        self.manager.child = runtime.subprocess.Popen([sys.executable, "-c", "import time; time.sleep(60)"])
        child = self.manager.child
        self.manager.close()
        self.assertIsNotNone(child.poll())
        self.assertIsNone(self.manager.child)

    def test_cancelled_preparation_never_stops_old_child(self):
        stack, stop = self.mocks()
        def cancel(*args):
            self.manager.stopping.set()
            return self.manager.runtime_dir / "releases/new"
        with stack, mock.patch.object(self.manager, "prepare", side_effect=cancel):
            self.manager.run_job("apply")
        stop.assert_not_called()
        self.assertFalse((self.manager.runtime_dir / "state.json").exists())
        self.assertIn("取消", self.manager.status()["message"])

    def test_same_binary_with_rebuilt_image_is_current(self):
        self.manager.current.update(platform="linux-amd64", image_id="sha256:" + "d" * 64, binary_sha256="c" * 64)
        self.data["version"] = OLD["version"]
        self.data["platforms"]["linux-amd64"]["image"] = "slavesim:" + OLD["version"] + "-amd64"
        stack, stop = self.mocks()
        with stack:
            self.manager.run_job("apply")
        self.assertEqual(self.manager.status()["state"], "current")
        stop.assert_not_called()
        self.data["platforms"]["linux-amd64"]["binary_sha256"] = "e" * 64
        stack, stop = self.mocks()
        with stack:
            self.manager.run_job("apply")
        self.assertEqual(self.manager.status()["state"], "error")
        stop.assert_not_called()

    def test_prepare_checks_download_and_ignores_package_scripts(self):
        archive = ArchiveTests().archive(self.root, [("slavesim", tarfile.REGTYPE, b"candidate"),
                                                    ("probe", tarfile.REGTYPE, b"probe"),
                                                    ("scripts/runtime.py", tarfile.REGTYPE, b"untrusted")])
        raw = archive.read_bytes()
        artifact = self.data["platforms"]["linux-amd64"]
        artifact.update(binary_size=len(raw), binary_sha256=hashlib.sha256(raw).hexdigest())
        with mock.patch.object(update, "open_https", return_value=io.BytesIO(raw)), mock.patch.object(runtime, "verify_candidate") as verified:
            candidate = self.manager.prepare(self.data, artifact)
        self.assertEqual({path.name for path in candidate.iterdir()}, {"slavesim", "probe"})
        verified.assert_called_once()
        self.assertEqual(list(self.manager.runtime_dir.glob(".download-*")), [])
        with mock.patch.object(update, "open_https", return_value=io.BytesIO(raw + b"bad")):
            with self.assertRaises(update.UpdateError):
                self.manager.prepare(self.data, artifact)
        self.assertEqual(list(self.manager.runtime_dir.glob(".download-*")), [])

    def test_health_requires_every_configured_bus_pty(self):
        config = self.root / "config.json"
        config.write_text('{"buses":[{"name":"a"},{"name":"b"}]}', encoding="utf-8")
        self.manager.config = str(config)
        self.manager.child = mock.Mock()
        self.manager.child.poll.return_value = None
        for buses, expected in (([{"name":"a","slave_path":"/dev/pts/1"}], False),
                                ([{"name":"a","slave_path":"/dev/pts/1"}, {"name":"b","slave_path":""}], False),
                                ([{"name":"a","slave_path":"/dev/pts/1"}, {"name":"c","slave_path":"/dev/pts/2"}], False),
                                ([{"name":"a","slave_path":"/dev/pts/1"}, {"name":"b","slave_path":"/dev/pts/2"}], True)):
            with self.subTest(buses=buses), mock.patch.object(runtime, "urlopen", return_value=io.BytesIO(json.dumps(dict(buses=buses)).encode())):
                self.assertEqual(self.manager.healthy(timeout=0.02), expected)
        self.manager.child = None

    def test_supervision_restarts_are_bounded(self):
        child = mock.Mock()
        child.poll.return_value = 1
        child.returncode = 1
        self.manager.child = child
        def launch(*args):
            self.manager.child = child
        with mock.patch.object(self.manager.stopping, "wait", side_effect=[False] * 4 + [True]), \
                mock.patch.object(self.manager, "launch", side_effect=launch) as started, \
                mock.patch.object(self.manager, "healthy", return_value=True):
            self.manager.supervise()
        self.assertEqual(started.call_count, 3)
        self.assertEqual(self.manager.status()["state"], "error")
        self.assertIn("停止自动重启", self.manager.status()["message"])

    def test_start_cleans_interrupted_downloads_and_keeps_previous(self):
        download = self.manager.runtime_dir / ".download-interrupted"
        download.mkdir()
        (download / "partial").write_bytes(b"x")
        extra = self.manager.runtime_dir / "releases/uncommitted"
        extra.mkdir()
        with mock.patch.object(self.manager, "launch"), mock.patch.object(self.manager, "healthy", return_value=True):
            self.manager.start()
        self.assertFalse(download.exists())
        self.assertFalse(extra.exists())
        self.assertEqual(update.read_json(self.manager.runtime_dir / "state.json")["release"], "seed")

    def test_version_cli_uses_seed_then_committed_binary(self):
        args = ["-version", "--runtime-dir", str(self.manager.runtime_dir), "--seed-binary", "seed"]
        with mock.patch.object(runtime.subprocess, "call", return_value=0) as call:
            self.assertEqual(runtime.main(args), 0)
        call.assert_called_once_with(["seed", "-version"])
        release = runtime.artifact_version(self.data["version"])
        state = dict(version=self.data["version"], commit=self.data["commit"], channel="ci", release=release)
        folder = self.manager.runtime_dir / "releases" / release
        folder.mkdir()
        for name in ("slavesim", "probe"):
            (folder / name).write_bytes(b"x")
        update.atomic_json(self.manager.runtime_dir / "state.json", state)
        with mock.patch.object(runtime, "verify_candidate"), mock.patch.object(runtime.subprocess, "call", return_value=0) as call:
            self.assertEqual(runtime.main(args), 0)
        call.assert_called_once_with([str(folder / "slavesim"), "-version"])


class HTTPTests(ManagerFixture):
    def setUp(self):
        super().setUp()
        self.server = runtime.RuntimeServer(("127.0.0.1", 0), self.manager)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.url = "http://127.0.0.1:" + str(self.server.server_port)

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(2)
        super().tearDown()

    def request(self, path, method="GET", body=None, headers=None):
        try:
            response = urlopen(Request(self.url + path, data=body, method=method, headers=headers or {}), timeout=3)
        except HTTPError as exc:
            response = exc
        with response:
            return response.status, response.read()

    def test_action_origin_body_and_methods(self):
        action = {"X-SlaveSim-Action": "update"}
        for method, body, headers, expected in (
            ("GET", None, {}, 405), ("POST", b"{}", {}, 403),
            ("POST", b"{}", dict(action, Origin="http://evil.test"), 403),
            ("POST", b'{"url":"https://evil.test"}', action, 400),
            ("POST", b"[]", action, 400), ("POST", b"bad", action, 400),
            ("POST", b"x" * 4097, action, 413)):
            self.assertEqual(self.request("/api/update/check", method, body, headers)[0], expected)
        with mock.patch.object(self.manager, "begin"):
            status, body = self.request("/api/update/check", "POST", b"{}", dict(action, Origin=self.url))
        self.assertEqual(status, 202)
        self.assertEqual(json.loads(body)["current_version"], OLD["version"])
        self.assertEqual(self.request("/api/update/status", "POST", b"", action)[0], 405)
        self.assertEqual(self.request("/api/update/check?url=x", "POST", b"{}", action)[0], 400)
        with mock.patch.object(self.manager, "begin", side_effect=runtime.BusyError("已有任务")):
            self.assertEqual(self.request("/api/update/apply", "POST", b"{}", action)[0], 409)

    # 配置读取与保存支持超过 1 MiB、最多 4 MiB；其他请求仍保持原限制。
    def test_proxy_configuration_body_limit(self):
        received = []
        initial = json.dumps({"note": "x" * (1024 * 1024)}).encode()

        class Backend(BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(200)
                self.send_header("Content-Length", str(len(initial)))
                self.end_headers()
                self.wfile.write(initial)

            def do_PUT(self):
                raw = self.rfile.read(int(self.headers["Content-Length"]))
                received.append((self.path, raw))
                self.send_response(200)
                self.send_header("Content-Length", "2")
                self.end_headers()
                self.wfile.write(b"{}")

            def log_message(self, *args):
                pass

        backend = ThreadingHTTPServer(("127.0.0.1", 0), Backend)
        thread = threading.Thread(target=backend.serve_forever, daemon=True)
        thread.start()
        self.manager.child_port = backend.server_port
        try:
            self.assertEqual(self.request("/api/config"), (200, initial))
            for path, size in (("/api/config", 1024 * 1024 + 1),
                               ("/api/config?source=web", 4 * 1024 * 1024)):
                with self.subTest(path=path, size=size):
                    prefix, suffix = b'{"note":"', b'"}'
                    raw = prefix + b"x" * (size - len(prefix) - len(suffix)) + suffix
                    self.assertEqual(self.request(path, "PUT", raw), (200, b"{}"))
                    self.assertEqual(received[-1], (path, raw))

            # 只发送超限长度头，避免提前拒绝后客户端发送大正文造成连接错误。
            for path, method, size in (("/api/config", "PUT", 4 * 1024 * 1024 + 1),
                                       ("/api/config", "POST", 1024 * 1024 + 1),
                                       ("/api/manual", "PUT", 1024 * 1024 + 1)):
                with self.subTest(path=path, method=method, size=size):
                    connection = runtime.http.client.HTTPConnection("127.0.0.1", self.server.server_port, timeout=3)
                    try:
                        connection.putrequest(method, path)
                        connection.putheader("Content-Length", str(size))
                        connection.endheaders()
                        response = connection.getresponse()
                        self.assertEqual(response.status, 413)
                        self.assertIn("超过大小限制", json.loads(response.read())["error"])
                    finally:
                        connection.close()
            self.assertEqual(len(received), 2)
        finally:
            backend.shutdown()
            backend.server_close()
            thread.join(2)

    def test_proxy_http_post_sse_and_backend_unavailable(self):
        class Backend(BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream" if self.path == "/events" else "text/plain")
                self.end_headers()
                self.wfile.write(b"data: first\n\n" if self.path == "/events" else self.path.encode())
                self.wfile.flush()
                if self.path == "/events":
                    time.sleep(3.4)
                    self.wfile.write(b"data: second\n\n")
            def do_POST(self):
                data = self.rfile.read(int(self.headers["Content-Length"]))
                self.send_response(200)
                self.end_headers()
                self.wfile.write(data)
            def log_message(self, *args):
                pass
        backend = ThreadingHTTPServer(("127.0.0.1", 0), Backend)
        thread = threading.Thread(target=backend.serve_forever, daemon=True)
        thread.start()
        self.manager.child_port = backend.server_port
        try:
            self.assertEqual(self.request("/hello?q=1"), (200, b"/hello?q=1"))
            self.assertEqual(self.request("/api/manual", "POST", b'{"value":3}'), (200, b'{"value":3}'))
            start = time.monotonic()
            with urlopen(self.url + "/events", timeout=5) as response:
                self.assertEqual(response.readline(), b"data: first\n")
                self.assertLess(time.monotonic() - start, 0.8)
                self.assertEqual(response.readline(), b"\n")
                self.assertEqual(response.readline(), b"data: second\n")
        finally:
            backend.shutdown()
            backend.server_close()
            thread.join(2)
        self.assertEqual(self.request("/missing")[0], 502)


if __name__ == "__main__":
    unittest.main()
