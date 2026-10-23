"""验证 GitLab x64 构建身份、运行验证与 BuildKit 凭据边界。"""
import base64
import hashlib
import json
import os
from pathlib import Path
import stat
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
import gitlab_ci

COMMIT = "abcdef1" + "a" * 33


def metadata_env():
    data = gitlab_ci.build_metadata("ci", COMMIT, gitlab_ci.read_base_version(ROOT / "VERSION"),
                                    timestamp="2026-10-23T03:04:05Z")
    return dict(VERSION=data["version"], COMMIT=COMMIT, BUILD_TIME=data["published_at"],
                IMAGE_TAG=data["image_tag"], CHANNEL="ci", CI_COMMIT_SHA=COMMIT, CI_JOB_ID="123")


def buildkit_env():
    return dict(BUILDKIT_HOST="tcp://buildkit.example:1234", **{
        name: base64.b64encode(value).decode("ascii")
        for name, value in (("BUILDKIT_CA_B64", b"test-ca"),
                            ("BUILDKIT_CERT_B64", b"test-cert"), ("BUILDKIT_KEY_B64", b"test-key"))})


class GitLabCiTests(unittest.TestCase):
    # dotenv 可完整传给后续作业，采用大写变量且没有 Windows 行尾或 BOM。
    def test_metadata_dotenv_round_trip(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "metadata.env"
            data = gitlab_ci.create_metadata(COMMIT, output)
            raw = output.read_bytes()
            self.assertNotIn(b"\r", raw)
            self.assertFalse(raw.startswith(b"\xef\xbb\xbf"))
            env = dict(line.split("=", 1) for line in raw.decode("utf-8").splitlines())
            self.assertEqual(set(env), {"VERSION", "COMMIT", "BUILD_TIME", "IMAGE_TAG", "CHANNEL"})
            self.assertEqual(gitlab_ci.load_metadata(dict(env, CI_COMMIT_SHA=COMMIT)), data)

    # 缺失字段、流水线 SHA、版本时间和镜像标签不一致必须在构建前失败。
    def test_metadata_rejects_missing_or_tampered_identity(self):
        original = metadata_env()
        for key in ("VERSION", "COMMIT", "BUILD_TIME", "IMAGE_TAG", "CHANNEL", "CI_COMMIT_SHA"):
            env = dict(original)
            del env[key]
            with self.subTest(missing=key), self.assertRaises(ValueError):
                gitlab_ci.load_metadata(env)
        for key, value in (("CI_COMMIT_SHA", "b" * 40), ("COMMIT", "b" * 40),
                           ("BUILD_TIME", "2026-10-23T03:04:06Z"),
                           ("BUILD_TIME", "2026-10-23T03:04:05"),
                           ("IMAGE_TAG", original["IMAGE_TAG"] + "-other"),
                           ("VERSION", original["VERSION"].replace("030405", "030406")),
                           ("CHANNEL", "stable")):
            with self.subTest(field=key, value=value), self.assertRaises(ValueError):
                gitlab_ci.load_metadata(dict(original, **{key: value}))

    # 临时 Dockerfile 保留生产构建，仅追加真实运行验证与导出标记阶段。
    def test_smoke_dockerfile_preserves_runtime_and_requires_nonce(self):
        source = (ROOT / "Dockerfile").read_text(encoding="utf-8")
        result = gitlab_ci.make_ci_dockerfile(source)
        self.assertTrue(result.startswith(source.replace("FROM ${PYTHON_IMAGE}",
                                                        "FROM ${PYTHON_IMAGE} AS ci-runtime", 1)))
        smoke = result.split("FROM ci-runtime AS ci-smoke\n", 1)[1]
        self.assertIn("ARG CI_SMOKE_NONCE", smoke)
        self.assertIn('RUN test -n "$CI_SMOKE_NONCE" && /slavesim -version', smoke)
        self.assertIn("python3 -B /ci-smoke-runtime.py", smoke)
        self.assertIn('"$CI_SMOKE_NONCE" > /ci-smoke-ok', smoke)
        self.assertIn("FROM scratch AS ci-smoke-result", smoke)
        self.assertIn("COPY --from=ci-smoke /ci-smoke-ok /ci-smoke-ok", smoke)
        for invalid in ("FROM alpine\n", "FROM ${PYTHON_IMAGE}\nFROM ${PYTHON_IMAGE}\n"):
            with self.subTest(source=invalid), self.assertRaises(ValueError):
                gitlab_ci.make_ci_dockerfile(invalid)

    # 不完整或非法连接配置不能退回明文、Unix socket 或默认 Docker 服务。
    def test_buildkit_rejects_missing_variables_and_invalid_host(self):
        original = buildkit_env()
        candidates = []
        for key in original:
            env = dict(original)
            del env[key]
            candidates.append(env)
        for host in ("unix:///var/run/docker.sock", "http://buildkit:1234", "tcp://host:1234/path"):
            candidates.append(dict(original, BUILDKIT_HOST=host))
        for env in candidates:
            with self.subTest(keys=sorted(env), host=env.get("BUILDKIT_HOST")), self.assertRaises(ValueError):
                with gitlab_ci.buildkit_client(env):
                    self.fail("非法配置不应创建客户端")

    # 私钥只写入受限临时文件；正常退出和异常退出都必须清除凭据。
    def test_buildkit_credentials_permissions_and_cleanup(self):
        for abort in (False, True):
            folder = None
            with self.subTest(abort=abort), patch.object(gitlab_ci.os, "chmod", wraps=os.chmod) as chmod:
                try:
                    with gitlab_ci.buildkit_client(buildkit_env()) as (command, folder):
                        self.assertEqual(command[:3], ["buildctl", "--addr", "tcp://buildkit.example:1234"])
                        self.assertEqual((folder / "key.pem").read_bytes(), b"test-key")
                        self.assertNotIn("test-key", " ".join(command))
                        if os.name == "posix":
                            self.assertEqual(stat.S_IMODE(folder.stat().st_mode), 0o700)
                            for name in ("ca.pem", "cert.pem", "key.pem"):
                                self.assertEqual(stat.S_IMODE((folder / name).stat().st_mode), 0o600)
                        if abort:
                            raise RuntimeError("测试异常退出")
                except RuntimeError:
                    self.assertTrue(abort)
                self.assertIsNotNone(folder)
                self.assertFalse(folder.exists())
                self.assertEqual([call.args[1] for call in chmod.call_args_list], [0o700, 0o600, 0o600, 0o600])

    # 证书编码错误在任何 BuildKit 命令前失败。
    def test_buildkit_rejects_invalid_base64(self):
        for key in ("BUILDKIT_CA_B64", "BUILDKIT_CERT_B64", "BUILDKIT_KEY_B64"):
            with self.subTest(key=key), self.assertRaises(ValueError):
                with gitlab_ci.buildkit_client(dict(buildkit_env(), **{key: "%%%invalid%%%"})):
                    self.fail("编码非法不应创建客户端")

    # 构建固定为无 CGO 的 Linux x64，并为全部二进制与元数据生成校验和。
    def test_binary_build_target_and_checksums(self):
        env = metadata_env()
        def fake_build(command, **kwargs):
            self.assertEqual(kwargs["env"]["CGO_ENABLED"], "0")
            self.assertEqual(kwargs["env"]["GOOS"], "linux")
            self.assertEqual(kwargs["env"]["GOARCH"], "amd64")
            self.assertTrue(kwargs["check"])
            Path(command[command.index("-o") + 1]).write_bytes(b"test-binary")
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(gitlab_ci.subprocess, "run", side_effect=fake_build) as run, \
                patch.object(gitlab_ci.subprocess, "check_output", return_value=" ".join(
                    env[key] for key in ("VERSION", "COMMIT", "BUILD_TIME"))):
            root = Path(directory)
            data = gitlab_ci.build_binaries(root, env)
            self.assertEqual([call.args[0][-1] for call in run.call_args_list], [".", "./cmd/probe"])
            folder = root / "dist/amd64"
            self.assertEqual(json.loads((folder / "metadata.json").read_text()), data)
            sums = dict(line.split("  ", 1)[::-1] for line in (folder / "SHA256SUMS").read_text().splitlines())
            self.assertEqual(set(sums), {"slavesim", "probe", "metadata.json"})
            for name, digest in sums.items():
                self.assertEqual(digest, hashlib.sha256((folder / name).read_bytes()).hexdigest())

    # 编译成功但版本身份错误时不得生成可供后续镜像任务消费的元数据。
    def test_binary_version_mismatch_fails_before_metadata(self):
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(gitlab_ci.subprocess, "run"), \
                patch.object(gitlab_ci.subprocess, "check_output", return_value="slavesim development"):
            root = Path(directory)
            with self.assertRaises(ValueError):
                gitlab_ci.build_binaries(root, metadata_env())
            self.assertFalse((root / "dist/amd64/metadata.json").exists())

    # 缺少 mTLS 配置或作业 nonce 时不调用 Git、BuildKit 或导出镜像。
    def test_image_missing_configuration_runs_no_commands(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            folder = root / "dist/amd64"
            folder.mkdir(parents=True)
            env = metadata_env()
            (folder / "metadata.json").write_text(json.dumps(gitlab_ci.load_metadata(env)))
            for name in ("slavesim", "probe"):
                (folder / name).write_bytes(b"test-binary")
            for overrides in ({}, {"CI_JOB_ID": ""}, {"CI_JOB_ID": "invalid"}):
                with self.subTest(overrides=overrides), \
                        patch.object(gitlab_ci.subprocess, "check_output") as output, \
                        patch.object(gitlab_ci, "run_logged") as run:
                    with self.assertRaises(ValueError):
                        gitlab_ci.build_image(root, dict(env, **overrides))
                    output.assert_not_called()
                    run.assert_not_called()

    # 每次镜像验证携带当前作业编号，旧缓存标记不能解锁运行镜像导出。
    def test_image_smoke_uses_job_nonce_and_rejects_stale_marker(self):
        for marker in ("123", "122"):
            with self.subTest(marker=marker), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                folder = root / "dist/amd64"
                folder.mkdir(parents=True)
                env = dict(metadata_env(), **buildkit_env())
                data = gitlab_ci.load_metadata(env)
                (folder / "metadata.json").write_text(json.dumps(data))
                (root / "Dockerfile").write_text("FROM ${PYTHON_IMAGE}\n")
                (root / ".dockerignore").write_text("scripts/\n")
                for name in ("slavesim", "probe"):
                    (folder / name).write_bytes(b"test-binary")
                def fake_logged(command, log, workdir):
                    destination = command[command.index("--output") + 1]
                    if destination.startswith("type=local,"):
                        self.assertIn("build-arg:CI_SMOKE_NONCE=123", command)
                        result = Path(destination.split("dest=", 1)[1])
                        result.mkdir()
                        (result / "ci-smoke-ok").write_text(marker)
                    else:
                        self.assertIn("target=ci-runtime", command)
                        Path(destination.split("dest=", 1)[1]).write_bytes(b"test-image")
                with patch.object(gitlab_ci, "run_logged", side_effect=fake_logged) as run, \
                        patch.object(gitlab_ci.subprocess, "check_output", side_effect=["1792724645", "sha256:test-image"]):
                    if marker == "123":
                        gitlab_ci.build_image(root, env)
                        self.assertEqual(run.call_count, 2)
                        self.assertEqual((folder / "image-id.txt").read_text(), "sha256:test-image\n")
                    else:
                        with self.assertRaises(ValueError):
                            gitlab_ci.build_image(root, env)
                        self.assertEqual(run.call_count, 1)
                        self.assertFalse((folder / "slavesim-image.tar.gz").exists())


if __name__ == "__main__":
    unittest.main()
