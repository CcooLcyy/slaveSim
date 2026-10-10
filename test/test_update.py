"""设备更新的边界、校验与回滚测试，不连接设备。"""
import copy
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import sys
import tarfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
spec = importlib.util.spec_from_file_location("slavesim_update", ROOT / "scripts/update.py")
update = importlib.util.module_from_spec(spec)
spec.loader.exec_module(update)
BASE = "https://example.test/slavesim/ci/latest.json"
IMAGE_ID = "sha256:" + "b" * 64


def manifest(version="0.0.0-ci.12.1", commit="a" * 40):
    return {"schema_version": 1, "project": "slavesim", "channel": "ci",
            "version": version, "commit": commit, "published_at": "2026-10-10T12:00:00Z",
            "platforms": {"linux-amd64": {"url": "https://example.test/slavesim/ci/releases/x/image.tar.gz",
                "sha256": hashlib.sha256(b"archive").hexdigest(), "size": 7,
                "image": "slavesim:" + version + "-amd64", "image_id": IMAGE_ID}}}


class ManifestTests(unittest.TestCase):
    # 非同源、路径越界和编码穿越不能成为下载地址。
    def test_reject_download_escape(self):
        for url in ("http://example.test/slavesim/ci/x", "https://evil.test/slavesim/ci/x",
                    "https://example.test/slavesim/ci-evil/x",
                    "https://example.test/slavesim/ci/%2e%2e/stable/x",
                    "https://user@example.test/slavesim/ci/x"):
            item = manifest()
            item["platforms"]["linux-amd64"]["url"] = url
            with self.subTest(url=url), self.assertRaises(update.UpdateError):
                update.validate_manifest(item, BASE, "linux-amd64")

    # 清单字段必须完整且与通道、架构、镜像一致。
    def test_validate_manifest(self):
        update.validate_manifest(manifest(), BASE, "linux-amd64")
        for field, value in (("size", True), ("size", 0), ("sha256", "bad"),
                             ("image", "other:latest"), ("image_id", "bad")):
            item = manifest()
            item["platforms"]["linux-amd64"][field] = value
            with self.subTest(field=field), self.assertRaises(update.UpdateError):
                update.validate_manifest(item, BASE, "linux-amd64")

    # 同版本不同提交不能悄悄覆盖，CI 版本不能回退。
    def test_version_conflict_and_ci_downgrade(self):
        old = {"version": "0.0.0-ci.12.1", "commit": "a" * 40, "channel": "ci"}
        self.assertFalse(update.needs_update(manifest(), old))
        with self.assertRaises(update.UpdateError):
            update.needs_update(manifest(commit="c" * 40), old)
        with self.assertRaises(update.UpdateError):
            update.needs_update(manifest("0.0.0-ci.11.9"), old)
        self.assertTrue(update.needs_update(manifest("0.0.0-ci.12.2"), old))

    # stable 按数字比较，禁止通道切换和降级。
    def test_stable_order(self):
        item = manifest("2.10.0")
        item["channel"] = "stable"
        old = {"version": "2.9.0", "commit": "a" * 40, "channel": "stable"}
        self.assertTrue(update.needs_update(item, old))
        old["version"] = "3.0.0"
        with self.assertRaises(update.UpdateError):
            update.needs_update(item, old)
        old["channel"] = "ci"
        with self.assertRaises(update.UpdateError):
            update.needs_update(item, old)


class DownloadTests(unittest.TestCase):
    # 包的大小和哈希都必须匹配，下载超限或损坏时不留下临时文件。
    def test_verified_download_and_cleanup(self):
        artifact = manifest()["platforms"]["linux-amd64"]
        for data, valid in ((b"archive", True), (b"bad", False), (b"archive!", False)):
            with tempfile.TemporaryDirectory() as folder:
                response = mock.MagicMock()
                response.__enter__.return_value = io.BytesIO(data)
                with mock.patch.object(update, "open_https", return_value=response):
                    if valid:
                        path = update.download_artifact(artifact, Path(folder))
                        self.assertEqual(path.read_bytes(), data)
                    else:
                        with self.assertRaises(update.UpdateError):
                            update.download_artifact(artifact, Path(folder))
                        self.assertEqual(list(Path(folder).iterdir()), [])


class ImageArchiveTests(unittest.TestCase):
    # 从 Docker archive 的实际配置内容取摘要，不依赖不同存储驱动返回的索引 ID。
    def test_portable_config_digest_and_tag_validation(self):
        image = "slavesim:0.0.0-ci.12.1-amd64"
        config = b'{"architecture":"amd64","os":"linux"}'
        expected = "sha256:" + hashlib.sha256(config).hexdigest()
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "image.tar.gz"
            with tarfile.open(path, "w:gz") as archive:
                entries = {"manifest.json": json.dumps([{"Config": "config.json", "RepoTags": [image]}]).encode(),
                           "config.json": config}
                for name, value in entries.items():
                    info = tarfile.TarInfo(name)
                    info.size = len(value)
                    archive.addfile(info, io.BytesIO(value))
            self.assertEqual(update.image_archive_digest(path, image), expected)
            with self.assertRaisesRegex(update.UpdateError, "标签"):
                update.image_archive_digest(path, "slavesim:other")

    # 新旧 Docker 客户端的不存在提示大小写不同，首次安装都应识别为空。
    def test_missing_container_message_case(self):
        for message in ("No such object: slavesim", "error: no such object: slavesim"):
            with self.subTest(message=message):
                runner = mock.Mock(side_effect=update.UpdateError(message))
                self.assertIsNone(update.inspect_container(runner))


class LockTests(unittest.TestCase):
    # Linux 上独立任务不能同时持有更新锁，持有者退出后可再次获取。
    @unittest.skipIf(update.fcntl is None, "文件锁只在 Linux 上验证")
    def test_exclusive_lock_and_release(self):
        with tempfile.TemporaryDirectory() as temp:
            folder = Path(temp)
            with update.file_lock(folder):
                with self.assertRaisesRegex(update.UpdateError, "已有更新"):
                    with update.file_lock(folder):
                        self.fail("重复任务不应获得文件锁")
            with update.file_lock(folder):
                self.assertTrue((folder / ".update.lock").exists())


class DeploymentTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.folder = Path(self.temp.name)
        self.config = self.folder / "config.json"
        self.config.write_text(json.dumps({"buses": [{"link": str(self.folder / "serial" / "tty0")}]}), encoding="utf-8")
        self.settings = {"manifest_url": BASE, "config_path": str(self.config),
                         "serial_dir": str(self.folder / "serial"),
                         "log_dir": str(self.folder / "log"), "http_port": 16000}
        update.atomic_json(self.folder / "updater.json", self.settings)
        (self.folder / "compose.release.yml").write_text("services: {}", encoding="utf-8")
        self.old = {"version": "0.0.0-ci.11.1", "commit": "d" * 40, "channel": "ci",
                    "image": "slavesim:0.0.0-ci.11.1-amd64", "image_id": "sha256:" + "d" * 64,
                    "platform": "linux-amd64"}
        update.atomic_json(self.folder / "state.json", self.old)
        update.write_env(self.folder, self.settings, self.old["image"])
        self.old_env = (self.folder / ".env").read_bytes()
        self.calls = []

    def runner(self, args):
        self.calls.append(args)
        if args[:3] == ["docker", "image", "inspect"]:
            image_id = self.old["image_id"] if args[3] == self.old["image"] else IMAGE_ID
            return json.dumps([{"Id": image_id, "Os": "linux", "Architecture": "amd64"}])
        if args[:2] == ["docker", "inspect"]:
            return json.dumps([{"Image": self.old["image_id"], "State": {"Running": True}, "Config": {"Labels": {
                "com.docker.compose.project": "slavesim",
                "com.docker.compose.project.working_dir": str(self.folder),
                "com.docker.compose.service": "slavesim"}}}])
        return ""

    def apply(self, health=True, digests=None, item=None):
        archive = self.folder / "download.tar.gz"
        archive.write_bytes(b"archive")
        with mock.patch.object(update.platform, "system", return_value="Linux"), \
             mock.patch.object(update, "file_lock"), \
             mock.patch.object(update, "fetch_manifest", return_value=item or manifest()), \
             mock.patch.object(update, "platform_key", return_value="linux-amd64"), \
             mock.patch.object(update, "download_artifact", return_value=archive), \
             mock.patch.object(update, "image_archive_digest", **({"side_effect": digests} if digests else {"return_value": IMAGE_ID})), \
             mock.patch.object(update, "wait_healthy", **({"side_effect": health} if isinstance(health, list) else {"return_value": health})):
            return update.apply_update(self.folder, runner=self.runner)

    # 健康成功后才推进 state，保留用户配置。
    def test_success_preserves_configuration(self):
        before = self.config.read_bytes()
        self.assertTrue(self.apply())
        state = json.loads((self.folder / "state.json").read_text(encoding="utf-8"))
        self.assertEqual(state["version"], "0.0.0-ci.12.1")
        self.assertEqual(self.config.read_bytes(), before)

    # 新版本健康失败必须恢复旧环境并重新启动旧镜像，state 不变。
    def test_health_failure_rolls_back(self):
        with self.assertRaisesRegex(update.UpdateError, "已回滚"):
            self.apply(health=[False, True])
        self.assertEqual((self.folder / ".env").read_bytes(), self.old_env)
        self.assertEqual(json.loads((self.folder / "state.json").read_text()), self.old)
        up = [call for call in self.calls if "up" in call]
        self.assertEqual(len(up), 2)

    # 未受本更新器管理的同名容器不能被接管。
    def test_reject_foreign_container_before_download(self):
        self.runner = lambda args: json.dumps([{"Config": {"Labels": {}}}])
        with self.assertRaisesRegex(update.UpdateError, "管理"):
            self.apply()
        self.assertEqual((self.folder / ".env").read_bytes(), self.old_env)

    # docker load 后的镜像内容 ID 不匹配时不能修改现有部署。
    def test_reject_loaded_image_mismatch(self):
        with self.assertRaisesRegex(update.UpdateError, "镜像"):
            self.apply(digests=[IMAGE_ID, "sha256:" + "c" * 64])
        self.assertEqual((self.folder / ".env").read_bytes(), self.old_env)

    # 尚未初始化的部署须显式授权首次安装。
    def test_initial_install_requires_flag(self):
        (self.folder / "state.json").unlink()
        with self.assertRaisesRegex(update.UpdateError, "首次"):
            self.apply()

    # 首次安装必须显式授权，且成功后记录完整镜像身份。
    def test_explicit_initial_install(self):
        (self.folder / "state.json").unlink()
        (self.folder / ".env").unlink()
        original = self.runner
        def runner(args):
            if args[:2] == ["docker", "inspect"]:
                raise update.UpdateError("No such object: slavesim")
            return original(args)
        archive = self.folder / "download.tar.gz"
        archive.write_bytes(b"archive")
        with mock.patch.object(update.platform, "system", return_value="Linux"), \
             mock.patch.object(update, "file_lock"), mock.patch.object(update, "fetch_manifest", return_value=manifest()), \
             mock.patch.object(update, "platform_key", return_value="linux-amd64"), \
             mock.patch.object(update, "image_archive_digest", return_value=IMAGE_ID), \
             mock.patch.object(update, "download_artifact", return_value=archive), mock.patch.object(update, "wait_healthy", return_value=True):
            self.assertTrue(update.apply_update(self.folder, allow_initial_install=True, runner=runner))
        self.assertEqual(update.read_json(self.folder / "state.json")["image_id"], IMAGE_ID)

    # 当前容器与状态不一致时拒绝替换，避免回滚到未经确认的版本。
    def test_reject_running_image_state_mismatch(self):
        original = self.runner
        def altered(args):
            result = original(args)
            if args[:2] == ["docker", "inspect"]:
                value = json.loads(result)
                value[0]["Image"] = IMAGE_ID
                return json.dumps(value)
            return result
        self.runner = altered
        with self.assertRaisesRegex(update.UpdateError, "状态"):
            self.apply()
        self.assertFalse(any(call[:2] == ["docker", "load"] for call in self.calls))

    # 环境文件被人工改动后不可自动恢复到错误镜像或不同挂载路径。
    def test_reject_modified_environment(self):
        (self.folder / ".env").write_bytes(self.old_env.replace(b"11.1", b"10.1"))
        with self.assertRaisesRegex(update.UpdateError, "环境"):
            self.apply()
        self.assertFalse(any(call[:2] == ["docker", "load"] for call in self.calls))

    # 相同版本必须保持镜像身份，不能只比较来源提交。
    def test_same_version_different_image_is_rejected(self):
        self.old.update(version="0.0.0-ci.12.1", commit="a" * 40)
        update.atomic_json(self.folder / "state.json", self.old)
        with self.assertRaisesRegex(update.UpdateError, "镜像"):
            self.apply()

    # 清单记录的软链接必须在共享目录内，否则其他容器看不到新 PTY。
    def test_reject_unshared_serial_path(self):
        self.config.write_text('{"buses": [{"link": "/not-shared/tty0"}]}', encoding="utf-8")
        with self.assertRaisesRegex(update.UpdateError, "共享"):
            update.settings_for(self.folder)

    # 已建立的软链接指向 /dev/pts，校验共享路径时不能解析软链接的最终目标。
    @unittest.skipIf(os.name == "nt", "软链接目录行为在 Linux 上验证")
    def test_existing_pty_symlink_keeps_shared_directory(self):
        serial = Path(self.settings["serial_dir"])
        serial.mkdir()
        (serial / "tty0").symlink_to("/dev/pts/99999")
        self.assertEqual(update.settings_for(self.folder)["serial_dir"], str(serial))


class HealthTests(unittest.TestCase):
    # 空总线或缺少 PTY 的状态不能通过部署健康检查。
    def test_requires_nonempty_pty_buses(self):
        with tempfile.TemporaryDirectory() as temp:
            folder = Path(temp)
            container = {"Image": IMAGE_ID, "State": {"Running": True}, "Config": {"Labels": {
                "com.docker.compose.project": "slavesim", "com.docker.compose.service": "slavesim",
                "com.docker.compose.project.working_dir": str(folder)}}}
            for buses, expected in (([], False), ([{}], False), ([{"slave_path": "/dev/pts/1"}], True)):
                with self.subTest(buses=buses), mock.patch.object(update, "inspect_container", return_value=container), \
                     mock.patch("urllib.request.urlopen", return_value=io.BytesIO(json.dumps({"buses": buses}).encode())), \
                     mock.patch.object(update.time, "monotonic", side_effect=[0, 0, 2]), mock.patch.object(update.time, "sleep"):
                    self.assertEqual(update.wait_healthy(folder, {"http_port": 16000}, IMAGE_ID, timeout=1), expected)


if __name__ == "__main__":
    unittest.main()
