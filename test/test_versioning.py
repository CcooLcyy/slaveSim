"""验证共享版本规则、基础版本与工作流元数据。"""
from datetime import datetime, timezone
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
import versioning

COMMIT = "abcdef1" + "a" * 33
VERSION = "0.0.1-ci.20261010t083000+sha.abcdef1"


class VersioningTests(unittest.TestCase):
    # CI 与正式构建共享基础版本，CI 时间统一为 UTC，标签必须与基础版本一致。
    def test_metadata_and_stable_tag(self):
        result = versioning.build_metadata("ci", COMMIT, "0.0.1", "2026-10-10T16:30:00+08:00")
        self.assertEqual(result, dict(channel="ci", version=VERSION, commit=COMMIT,
                                     published_at="2026-10-10T08:30:00Z",
                                     image_tag=VERSION.replace("+", "_")))
        self.assertEqual(versioning.build_metadata("stable", COMMIT, "0.0.1",
                         "2026-10-10T08:30:00Z", tag="v0.0.1")["version"], "0.0.1")
        for tag in (None, "v0.0.2", "0.0.1", "v00.0.1"):
            with self.subTest(tag=tag), self.assertRaises(ValueError):
                versioning.build_metadata("stable", COMMIT, "0.0.1", tag=tag)

    # 实际元数据生成时间参与版本，重跑同一提交不会沿用旧构建时间。
    def test_rebuild_uses_new_timestamp(self):
        first = versioning.build_metadata("ci", COMMIT, "0.0.1", "2026-10-10T08:30:00Z")
        second = versioning.build_metadata("ci", COMMIT, "0.0.1", "2026-10-10T08:30:01Z")
        self.assertGreater(versioning.version_order("ci", second["version"]),
                           versioning.version_order("ci", first["version"]))

    # 新编号高于所有旧 CI 编号，之后按基础版本和时间排序，SHA 不参与先后判断。
    def test_order_and_legacy_migration(self):
        order = versioning.version_order
        self.assertGreater(order("ci", VERSION), order("ci", "0.0.0-ci.999999.999"))
        self.assertGreater(order("ci", "0.0.0-ci.42.2"), order("ci", "0.0.0-ci.42.1"))
        self.assertGreater(order("ci", VERSION.replace("0.0.1", "0.0.2").replace("20261010", "20261009")), order("ci", VERSION))
        self.assertEqual(order("ci", VERSION), order("ci", VERSION.replace("abcdef1", "0000000")))
        self.assertGreater(order("stable", "0.10.0"), order("stable", "0.9.0"))

    # 日期、大小写、前导零、提交摘要和通道必须严格校验。
    def test_invalid_versions_and_commit(self):
        for value in (VERSION.replace("20261010", "20260230"), VERSION.replace("083000", "253000"),
                      VERSION.replace("-ci.", "-beta."), VERSION.replace("+sha.", "_sha."),
                      VERSION.replace("0.0.1", "00.0.1"), VERSION.replace("abcdef1", "ABCDEF1"),
                      "0.0.0-ci.0.1", "0.0.1"):
            with self.subTest(version=value), self.assertRaises(ValueError):
                versioning.version_order("ci", value)
        for commit in ("c" * 40, "bad", COMMIT.upper()):
            with self.subTest(commit=commit), self.assertRaises(ValueError):
                versioning.validate_commit("ci", VERSION, commit)
        with self.assertRaises(ValueError):
            versioning.build_metadata("ci", COMMIT, "0.0.1", "2026-10-10T08:30:00")
        with self.assertRaises(ValueError):
            versioning.version_order("beta", VERSION)

    # 展示版本保留加号，镜像与路径使用安全转换，旧镜像标签保持原样。
    def test_image_and_artifact_mapping(self):
        slug = VERSION.replace("+", "_")
        self.assertEqual(versioning.artifact_version(VERSION), slug)
        self.assertEqual(versioning.image_name(VERSION, "arm64"), f"slavesim:{slug}-arm64")
        self.assertEqual(versioning.image_name("0.0.0-ci.12.1", "amd64"), "slavesim:0.0.0-ci.12.1-amd64")
        with self.assertRaises(ValueError):
            versioning.image_name(VERSION, "windows")

    # 基础版本从文件读取，空值、预发布版本与非法数字不能用作发布基线。
    def test_base_version_file(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "VERSION"
            path.write_text("0.0.1\n", encoding="utf-8")
            self.assertEqual(versioning.read_base_version(path), "0.0.1")
            for value in ("", "0.0.1-dev", "v0.0.1", "0.00.1"):
                path.write_text(value, encoding="utf-8")
                with self.subTest(value=value), self.assertRaises(ValueError):
                    versioning.read_base_version(path)

    # 工作流 CLI 写出可直接复用的元数据，版本中的时间与清单发布时间一致。
    def test_cli_github_output(self):
        with tempfile.TemporaryDirectory() as temp:
            output = Path(temp) / "outputs"
            result = subprocess.run([sys.executable, str(ROOT / "scripts/versioning.py"),
                                    "--commit", COMMIT, "--github-output", str(output)],
                                    capture_output=True, text=True, encoding="utf-8")
            self.assertEqual(result.returncode, 0, result.stderr)
            data = dict(line.split("=", 1) for line in output.read_text(encoding="utf-8").splitlines())
            self.assertEqual(data["channel"], "ci")
            self.assertTrue(data["version"].startswith("0.0.1-ci."))
            self.assertEqual(data["commit"], COMMIT)
            self.assertEqual(data["image_tag"], data["version"].replace("+", "_"))
            stamp = datetime.fromisoformat(data["published_at"].replace("Z", "+00:00"))
            self.assertIn(stamp.astimezone(timezone.utc).strftime("%Y%m%dt%H%M%S"), data["version"])
            self.assertRegex(data["image_tag"], r"^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$")


if __name__ == "__main__":
    unittest.main()
