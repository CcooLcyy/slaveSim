"""验证新版本发布包与实际设备更新器的契约及旧状态迁移。"""
from pathlib import Path
import subprocess
import sys
import tarfile
from urllib.parse import unquote
import unittest
from unittest.mock import patch

import test_release as release_tests
import test_update as update_tests

release = release_tests.release
update = update_tests.update
COMMIT = "a" * 40
VERSION = "0.0.1-ci.20261010t083000+sha.aaaaaaa"


class ReleaseMigrationTests(unittest.TestCase):
    setUp = release_tests.ReleaseTests.setUp
    prepare = release_tests.ReleaseTests.prepare
    publish = release_tests.ReleaseTests.publish

    # 发布器生成的真实新格式清单必须被更新器接受，路径不引入编码绕过。
    def test_new_package_update_contract(self):
        item = self.prepare(channel="ci", version=VERSION)
        self.assertEqual(item["version"], VERSION)
        for arch in release.ARCHES:
            entry = item["platforms"]["linux-" + arch]
            self.assertEqual(entry["image"], "slavesim:" + VERSION.replace("+", "_") + "-" + arch)
            self.assertEqual(entry["url"], unquote(entry["url"]))
            update.validate_manifest(item, "https://updates.example/slavesim/ci/latest.json", "linux-" + arch)
            binary = self.output / "assets" / VERSION.replace("+", "_") / arch / entry["binary_url"].rsplit("/", 1)[1]
            with tarfile.open(binary) as archive:
                self.assertIn("scripts/versioning.py", archive.getnames())
                self.assertIn("VERSION", archive.getnames())

    # 新 CI 可替换旧入口清单，上传完资产之后才更新 latest。
    def test_publish_migrates_legacy_latest(self):
        item = self.prepare(channel="ci", version=VERSION)
        old = item | {"version": "0.0.0-ci.99999.9"}
        with patch.object(release, "get_json", side_effect=[None, old, item]), \
             patch.object(release, "upload") as uploaded, patch.object(release, "verify_asset"):
            self.publish()
        self.assertEqual(uploaded.call_args_list[-1].args[1], "slavesim/ci/latest.json")

    # 发布不得倒退时间、基础版本，也不得让同秒不同 SHA 占用同一排序位置。
    def test_publish_rejects_backward_or_same_second(self):
        item = self.prepare(channel="ci", version=VERSION)
        for other in (VERSION.replace("083000", "083001"), VERSION.replace("0.0.1", "0.0.2"),
                      VERSION.replace("aaaaaaa", "ccccccc")):
            newer = item | {"version": other}
            with self.subTest(version=other), patch.object(release, "get_json", side_effect=[None, newer]), \
                 patch.object(release, "upload") as uploaded, self.assertRaises(ValueError):
                self.publish()
            uploaded.assert_not_called()

    # CI 的短 SHA 必须与来源提交一致，非法日期不能产生任何输出。
    def test_prepare_rejects_false_identity_or_date(self):
        for value in (VERSION.replace("aaaaaaa", "ccccccc"), VERSION.replace("20261010", "20260230")):
            with self.subTest(version=value), self.assertRaises(ValueError):
                self.prepare(channel="ci", version=value)
        self.assertFalse(self.output.exists())


class UpdateMigrationTests(unittest.TestCase):
    # 旧安装状态可迁移到新编号，相同版本不重装，迁移后不得退回旧编号。
    def test_migration_and_new_order(self):
        item = update_tests.manifest(VERSION)
        old = {"channel": "ci", "version": "0.0.0-ci.99999.9", "commit": "d" * 40}
        self.assertTrue(update.needs_update(item, old))
        new = {"channel": "ci", "version": VERSION, "commit": COMMIT}
        self.assertFalse(update.needs_update(item, new))
        for value in ("0.0.0-ci.999999.9", VERSION.replace("083000", "082959"),
                      VERSION.replace("0.0.1", "0.0.0"), VERSION.replace("aaaaaaa", "ccccccc")):
            with self.subTest(version=value), self.assertRaises(update.UpdateError):
                update.needs_update(update_tests.manifest(value), new)

    # 新清单中的完整提交与短提交必须一致，保留旧格式的校验行为。
    def test_manifest_identity_validation(self):
        item = update_tests.manifest(VERSION)
        item["platforms"]["linux-amd64"]["image"] = "slavesim:" + VERSION.replace("+", "_") + "-amd64"
        update.validate_manifest(item, update_tests.BASE, "linux-amd64")
        item["commit"] = "c" * 40
        with self.assertRaises(update.UpdateError):
            update.validate_manifest(item, update_tests.BASE, "linux-amd64")


class DeploymentMigrationTests(unittest.TestCase):
    setUp = update_tests.DeploymentTests.setUp
    runner = update_tests.DeploymentTests.runner
    apply = update_tests.DeploymentTests.apply

    # 实际安装从旧状态迁移，state 保留展示版本，环境文件使用转换后的镜像标签。
    def test_install_from_old_state(self):
        item = update_tests.manifest(VERSION)
        item["platforms"]["linux-amd64"]["image"] = "slavesim:" + VERSION.replace("+", "_") + "-amd64"
        before = self.config.read_bytes()
        self.assertTrue(self.apply(item=item))
        self.assertEqual(update.read_json(self.folder / "state.json")["version"], VERSION)
        self.assertIn(item["platforms"]["linux-amd64"]["image"], (self.folder / ".env").read_text())
        self.assertEqual(self.config.read_bytes(), before)

    # 新编号健康检查失败仍恢复旧格式版本、环境与配置。
    def test_new_format_rollback(self):
        item = update_tests.manifest(VERSION)
        item["platforms"]["linux-amd64"]["image"] = "slavesim:" + VERSION.replace("+", "_") + "-amd64"
        with self.assertRaisesRegex(update.UpdateError, "已回滚"):
            self.apply(item=item, health=[False, True])
        self.assertEqual(update.read_json(self.folder / "state.json"), self.old)
        self.assertEqual((self.folder / ".env").read_bytes(), self.old_env)

    # 首次初始化把共享模块与更新脚本一起交付，部署目录可独立执行更新 CLI。
    def test_init_copies_runtime_dependency(self):
        folder = self.folder / "fresh"
        update.init_deployment(folder, self.config, Path(self.settings["serial_dir"]),
                               Path(self.settings["log_dir"]), 16000, update_tests.BASE)
        self.assertTrue((folder / "versioning.py").is_file())
        result = subprocess.run([sys.executable, str(folder / "update.py"), "--help"],
                                capture_output=True, text=True, encoding="utf-8")
        self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == "__main__":
    unittest.main()
