"""验证双架构发布包、不可变版本与 R2 清单的发布顺序。"""

import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import sys
import unittest
from unittest.mock import patch
from urllib.error import HTTPError

SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "release.py"
sys.path.insert(0, str(SCRIPT.parent))
SPEC = importlib.util.spec_from_file_location("slavesim_release", SCRIPT)
release = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(release)


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.source = self.root / "source"
        self.source.mkdir()
        (self.source / "config.example.json").write_text('{"buses": []}', encoding="utf-8")
        (self.source / "README.md").write_text("模拟器发布说明", encoding="utf-8")
        (self.source / "config.json").write_text("现有设备配置", encoding="utf-8")
        for name in release.PACKAGE_FILES[2:]:
            path = self.source / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("部署工具模板\n", encoding="utf-8")
        self.input = self.root / "dist"
        for arch in release.ARCHES:
            directory = self.input / arch
            directory.mkdir(parents=True)
            for name in ("slavesim", "probe", "slavesim-image.tar.gz"):
                (directory / name).write_bytes(f"{arch}-{name}".encode())
            (directory / "image-id.txt").write_text("sha256:" + "b" * 64 + "\n", encoding="utf-8")
        self.output = self.root / "release"
        self.options = dict(input_dir=self.input, output_dir=self.output, version="1.2.3",
                            channel="stable", commit="a" * 40,
                            public_base_url="https://updates.example", prefix="slavesim",
                            published_at="2026-10-10T01:02:03Z", source_dir=self.source)

    def prepare(self, **changes):
        return release.prepare(**(self.options | changes))

    def publish(self, **changes):
        return release.publish(**(dict(output_dir=self.output, bucket="mskdsp-update",
                                       prefix="slavesim", endpoint="https://account.r2.cloudflarestorage.com",
                                       public_base_url="https://updates.example") | changes))

    def test_prepare_rejects_missing_architecture_before_writing(self):
        # 缺少任一架构时不得发布仅有一半平台的清单。
        (self.input / "arm64" / "probe").unlink()
        with self.assertRaisesRegex(ValueError, "probe"):
            self.prepare()
        self.assertFalse(self.output.exists())

    def test_invalid_metadata_is_rejected(self):
        # 拒绝非法通道、版本、来源提交及可越出发布前缀的地址。
        for change in ({"channel": "beta"}, {"version": "v1.2.3"}, {"version": "1.2"},
                       {"channel": "ci", "version": "1.2.3"}, {"commit": "bad"},
                       {"prefix": "../other"}, {"prefix": "slavesim//other"},
                       {"public_base_url": "http://updates.example"},
                       {"public_base_url": "https://user:password@updates.example"},
                       {"public_base_url": "https://updates.example/?x=1"},
                       {"public_base_url": "https://updates.example:bad"}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                self.prepare(**change)

    def test_manifest_digests_archives_permissions_and_config_preservation(self):
        # 包含模拟器和探针，但不携带设备已有配置；清单校验值与实际产物一致。
        manifest = self.prepare()
        self.assertEqual(manifest["project"], "slavesim")
        self.assertEqual(set(manifest["platforms"]), {"linux-amd64", "linux-arm64"})
        for arch in release.ARCHES:
            entry = manifest["platforms"]["linux-" + arch]
            self.assertEqual(entry["image"], f"slavesim:1.2.3-{arch}")
            self.assertEqual(entry["image_id"], "sha256:" + "b" * 64)
            for binary in (False, True):
                prefix = "binary_" if binary else ""
                name = entry[prefix + "url"].rsplit("/", 1)[1]
                artifact = self.output / "assets" / "1.2.3" / arch / name
                self.assertEqual(entry[prefix + "sha256"], hashlib.sha256(artifact.read_bytes()).hexdigest())
                self.assertEqual(entry[prefix + "size"], artifact.stat().st_size)
            binary_name = entry["binary_url"].rsplit("/", 1)[1]
            with tarfile.open(self.output / "assets" / "1.2.3" / arch / binary_name) as archive:
                self.assertEqual(set(archive.getnames()), {"slavesim", "probe", *release.PACKAGE_FILES})
                self.assertEqual(archive.getmember("slavesim").mode, 0o755)
                self.assertEqual(archive.getmember("probe").mode, 0o755)
        self.assertNotIn("config.json", (self.output / "SHA256SUMS").read_text())

    def test_same_source_recreates_identical_packages(self):
        # 同一输入重跑产生相同的压缩包和清单。
        first = self.prepare()
        second_output = self.root / "second"
        second = self.prepare(output_dir=second_output)
        self.assertEqual(first, second)
        for file in self.output.rglob("*.tar.gz"):
            self.assertEqual(file.read_bytes(), (second_output / file.relative_to(self.output)).read_bytes())

    def test_ci_version_metadata_is_accepted(self):
        # CI 版本使用运行编号和重试编号，避免不同提交共用对象名。
        manifest = self.prepare(version="0.0.0-ci.42.2", channel="ci")
        self.assertEqual(manifest["version"], "0.0.0-ci.42.2")
        self.assertIn("/ci/0.0.0-ci.42.2/amd64/", manifest["platforms"]["linux-amd64"]["url"])

    def test_prepare_rejects_existing_version_with_different_commit(self):
        # 不允许将已有稳定版本改指向另一提交。
        self.prepare()
        with self.assertRaisesRegex(ValueError, "覆盖"):
            self.prepare(commit="c" * 40)

    def test_invalid_image_id_is_rejected(self):
        # 镜像运行身份必须是 Docker config SHA256。
        (self.input / "amd64" / "image-id.txt").write_text("sha256:bad", encoding="utf-8")
        with self.assertRaisesRegex(ValueError, "image_id"):
            self.prepare()

    def test_modified_package_cannot_overwrite_same_version(self):
        # 即使提交 SHA 相同，本地包内容发生变化也不得覆盖已有发布包。
        original = self.prepare()
        (self.input / "amd64" / "probe").write_bytes(b"changed executable")
        with self.assertRaisesRegex(ValueError, "覆盖"):
            self.prepare()
        self.assertEqual(json.loads((self.output / "release.json").read_text()), original)

    def test_upload_failure_never_publishes_latest(self):
        # 任一版本资产上传失败，旧清单继续有效。
        self.prepare()
        calls = []
        def fail(source, key, *args):
            calls.append(key)
            raise RuntimeError("上传失败")
        with patch.object(release, "get_json", return_value=None), patch.object(release, "upload", side_effect=fail):
            with self.assertRaisesRegex(RuntimeError, "上传失败"):
                self.publish()
        self.assertTrue(calls)
        self.assertFalse(any(key.endswith("/latest.json") for key in calls))

    def test_asset_integrity_failure_never_publishes_latest(self):
        # 公共下载地址中的包必须通过 SHA256 与长度核验后才能发布清单。
        self.prepare()
        with patch.object(release, "get_json", return_value=None), patch.object(release, "upload") as upload, \
             patch.object(release, "verify_asset", side_effect=ValueError("校验失败")):
            with self.assertRaisesRegex(ValueError, "校验失败"):
                self.publish()
        self.assertFalse(any(call.args[1].endswith("/latest.json") for call in upload.call_args_list))

    def test_success_publishes_latest_last_and_checks_public_manifest(self):
        # 不可变资产先上传，latest 最后上传且禁用缓存；随后校验公开清单。
        manifest = self.prepare()
        with patch.object(release, "get_json", side_effect=[None, None, manifest]), \
             patch.object(release, "upload") as uploaded, patch.object(release, "verify_asset") as verified:
            self.publish()
        self.assertEqual(uploaded.call_args_list[-1].args[1], "slavesim/stable/latest.json")
        self.assertEqual(uploaded.call_args_list[-1].args[5], "no-store")
        self.assertTrue(all(call.args[5] == "public,max-age=31536000,immutable"
                            for call in uploaded.call_args_list[:-1]))
        self.assertEqual(verified.call_count, 6)

    def test_remote_stable_version_conflict_blocks_all_uploads(self):
        # 远端版本属于另一提交时，在上传任何对象前拒绝覆盖。
        manifest = self.prepare()
        existing = manifest | {"commit": "c" * 40}
        with patch.object(release, "get_json", return_value=existing), patch.object(release, "upload") as uploaded:
            with self.assertRaisesRegex(ValueError, "覆盖"):
                self.publish()
        uploaded.assert_not_called()

    def test_remote_same_commit_with_different_package_blocks_all_uploads(self):
        # 稳定版本还锁定包摘要，不允许同一提交名下更换构建结果。
        manifest = self.prepare()
        existing = json.loads(json.dumps(manifest))
        existing["platforms"]["linux-amd64"]["sha256"] = "d" * 64
        with patch.object(release, "get_json", return_value=existing), patch.object(release, "upload") as uploaded:
            with self.assertRaisesRegex(ValueError, "覆盖"):
                self.publish()
        uploaded.assert_not_called()

    def test_existing_same_release_preserves_original_manifest(self):
        # 重跑同一稳定版本复用首次发布时间，不覆盖不可变对象。
        manifest = self.prepare()
        original = manifest | {"published_at": "2026-10-09T01:02:03Z"}
        uploaded_manifests = []
        def capture(source, *args):
            uploaded_manifests.append(json.loads(Path(source).read_text()))
        with patch.object(release, "get_json", side_effect=[original, original, original]), \
             patch.object(release, "upload", side_effect=capture) as uploaded, patch.object(release, "verify_asset"):
            self.publish()
            self.assertEqual(uploaded_manifests, [original])
        self.assertEqual(uploaded.call_count, 1)

    def test_older_stable_release_cannot_downgrade_latest(self):
        # 发布旧正式版本不得将正在服务的新版本清单倒退。
        manifest = self.prepare()
        newer = manifest | {"version": "1.3.0"}
        with patch.object(release, "get_json", side_effect=[None, newer]), patch.object(release, "upload") as uploaded:
            with self.assertRaisesRegex(ValueError, "倒退"):
                self.publish()
        uploaded.assert_not_called()

    def test_older_ci_run_or_attempt_cannot_downgrade_latest(self):
        # CI 的运行编号和重试编号都参与先后顺序比较。
        manifest = self.prepare(version="0.0.0-ci.42.2", channel="ci")
        for version in ("0.0.0-ci.43.1", "0.0.0-ci.42.3"):
            newer = manifest | {"version": version}
            with self.subTest(version=version), patch.object(release, "get_json", side_effect=[None, newer]), \
                 patch.object(release, "upload") as uploaded:
                with self.assertRaisesRegex(ValueError, "倒退"):
                    self.publish()
                uploaded.assert_not_called()

    def test_redirects_are_rejected_before_following(self):
        # 不允许下载请求跳转到明文连接或其他来源，连同源跳转也拒绝。
        for target in ("http://updates.example/package", "https://other.example/package", "https://updates.example/other"):
            with self.subTest(target=target), self.assertRaisesRegex(ValueError, "重定向"):
                release.RejectRedirect().redirect_request(None, None, 302, "Found", {}, target)

    def test_public_requests_identify_publisher(self):
        # Cloudflare 拒绝 urllib 默认标识；所有公共校验请求应标识发布工具。
        request = release.Request("https://updates.example/release.json",
                                  headers={"Cache-Control": "no-cache"})
        with patch.object(release, "build_opener") as opener:
            release.urlopen(request, timeout=15)
        self.assertEqual(request.get_header("User-agent"), "slavesim-publisher/1")
        self.assertEqual(request.get_header("Cache-control"), "no-cache")
        self.assertIsInstance(opener.call_args.args[0], release.RejectRedirect)
        opener.return_value.open.assert_called_once_with(request, timeout=15)

    def test_download_checks_sha256_and_length(self):
        # 公共文件只要摘要或大小不一致就拒绝，避免上传损坏资产后切换清单。
        data = b"package contents"
        sha = hashlib.sha256(data).hexdigest()
        with patch.object(release, "urlopen", return_value=io.BytesIO(data)):
            release.verify_asset("https://updates.example/package", sha, len(data))
        for size, checksum in ((len(data) - 1, sha), (len(data) + 1, sha), (len(data), "f" * 64)):
            with self.subTest(size=size, checksum=checksum), \
                 patch.object(release, "urlopen", return_value=io.BytesIO(data)), self.assertRaises(ValueError):
                release.verify_asset("https://updates.example/package", checksum, size)

    def test_missing_release_only_accepts_http_404(self):
        # 网络错误与权限错误不可误判为未发布，从而覆盖远端对象。
        for status in (404, 403, 500):
            error = HTTPError("https://updates.example/release.json", status, "failure", {}, None)
            self.addCleanup(error.close)
            with self.subTest(status=status), patch.object(release, "urlopen", side_effect=error):
                if status == 404:
                    self.assertIsNone(release.get_json(error.url, missing_ok=True))
                else:
                    with self.assertRaises(HTTPError):
                        release.get_json(error.url, missing_ok=True)

    def test_aws_upload_uses_argument_list_and_exact_cache_headers(self):
        # 不经 shell 拼接路径，上传的缓存策略和 endpoint 精确传给 AWS CLI。
        source = self.root / "source package.tar.gz"
        with patch.object(release.subprocess, "run") as run:
            release.upload(source, "slavesim/stable/1.2.3/file.tar.gz", "mskdsp-update",
                           "https://account.r2.cloudflarestorage.com", "application/gzip", "no-store")
        args = run.call_args.args[0]
        self.assertEqual(args[:4], ["aws", "s3", "cp", str(source)])
        self.assertIn("s3://mskdsp-update/slavesim/stable/1.2.3/file.tar.gz", args)
        self.assertEqual(args[args.index("--cache-control") + 1], "no-store")
        self.assertTrue(run.call_args.kwargs["check"])


if __name__ == "__main__":
    unittest.main()
