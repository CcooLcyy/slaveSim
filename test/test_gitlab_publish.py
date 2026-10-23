"""验证 GitLab 发布复用 R2 协议时的权限、产物与凭据边界。"""
from contextlib import redirect_stdout
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
import gitlab_ci


class GitLabPublishTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.folder = self.root / "dist/amd64"
        self.folder.mkdir(parents=True)
        commit = "abcdef1" + "a" * 33
        self.data = gitlab_ci.build_metadata("ci", commit, gitlab_ci.read_base_version(ROOT / "VERSION"),
                                            timestamp="2026-10-23T03:04:05Z")
        self.env = dict(VERSION=self.data["version"], COMMIT=commit, CI_COMMIT_SHA=commit,
                        BUILD_TIME=self.data["published_at"], IMAGE_TAG=self.data["image_tag"], CHANNEL="ci",
                        CI_COMMIT_BRANCH="master", CI_COMMIT_REF_PROTECTED="true", CI_PIPELINE_SOURCE="push",
                        R2_ACCOUNT_ID="a" * 32, R2_ACCESS_KEY_ID="test-access-key",
                        R2_SECRET_ACCESS_KEY="test-secret-key")
        for name in ("slavesim", "probe", "slavesim-image.tar.gz", "image-id.txt"):
            (self.folder / name).write_bytes(b"test-release-input")
        (self.folder / "metadata.json").write_text(json.dumps(self.data), encoding="utf-8")
        gitlab_ci.checksums(self.folder)

    # 使用既有前缀和 x64 架构打包再发布，密钥仅通过子进程环境传递。
    def test_publish_reuses_existing_r2_contract_without_exposing_credentials(self):
        output = io.StringIO()
        with patch.object(gitlab_ci.subprocess, "run") as run, redirect_stdout(output):
            gitlab_ci.publish_release(self.root, self.env)
        self.assertEqual(run.call_count, 2)
        prepare, publish = run.call_args_list
        for call, action in ((prepare, "prepare"), (publish, "publish")):
            args = call.args[0]
            self.assertIn(action, args)
            self.assertEqual(args[args.index("--architectures") + 1], "amd64")
            self.assertEqual(args[args.index("--prefix") + 1], "slavesim")
            self.assertTrue(call.kwargs["check"])
            self.assertNotIn("test-access-key", " ".join(args))
            self.assertNotIn("test-secret-key", " ".join(args))
        args = prepare.args[0]
        self.assertEqual(args[args.index("--published-at") + 1], self.env["BUILD_TIME"])
        args = publish.args[0]
        self.assertEqual(args[args.index("--bucket") + 1], "mskdsp-update")
        env = publish.kwargs["env"]
        self.assertEqual(env["AWS_ACCESS_KEY_ID"], "test-access-key")
        self.assertEqual(env["AWS_SECRET_ACCESS_KEY"], "test-secret-key")
        self.assertEqual(env["AWS_DEFAULT_REGION"], "auto")
        self.assertEqual(env["AWS_REQUEST_CHECKSUM_CALCULATION"], "when_required")
        self.assertEqual(env["AWS_RESPONSE_CHECKSUM_VALIDATION"], "when_required")
        self.assertIn("https://pub-19f3d71852b04011b120b1b814141c12.r2.dev/slavesim/ci/latest.json", output.getvalue())
        self.assertNotIn("test-secret-key", output.getvalue())

    # MR、非主分支、未保护分支和其他来源均不得调用发布器。
    def test_ineligible_pipeline_fails_before_external_commands(self):
        for overrides in ({"CI_COMMIT_BRANCH": "feature/test"}, {"CI_COMMIT_REF_PROTECTED": "false"},
                          {"CI_PIPELINE_SOURCE": "merge_request_event"}, {"CI_PIPELINE_SOURCE": "schedule"},
                          {"CHANNEL": "stable"}, {"CI_COMMIT_SHA": "b" * 40}):
            with self.subTest(overrides=overrides), patch.object(gitlab_ci.subprocess, "run") as run:
                with self.assertRaises(ValueError):
                    gitlab_ci.publish_release(self.root, dict(self.env, **overrides))
                run.assert_not_called()

    # 凭据缺失不能先打包后才发现，避免已失败作业产生发布副作用。
    def test_missing_credentials_fail_before_external_commands(self):
        for name in ("R2_ACCOUNT_ID", "R2_ACCESS_KEY_ID", "R2_SECRET_ACCESS_KEY"):
            env = dict(self.env)
            del env[name]
            with self.subTest(missing=name), patch.object(gitlab_ci.subprocess, "run") as run:
                with self.assertRaises(ValueError):
                    gitlab_ci.publish_release(self.root, env)
                run.assert_not_called()

    # 篡改文件、缺少必需校验项、重复路径和越界路径不能成为发布输入。
    def test_invalid_artifact_checksums_fail_before_external_commands(self):
        path = self.folder / "SHA256SUMS"
        original = path.read_text(encoding="utf-8")
        first = original.splitlines()[0]
        malformed = (original.replace(first.split("  ", 1)[0], "0" * 64, 1),
                     "\n".join(line for line in original.splitlines() if not line.endswith("  probe")) + "\n",
                     original + first + "\n",
                     original + "0" * 64 + "  ../outside\n")
        for content in malformed:
            path.write_text(content, encoding="utf-8")
            with self.subTest(content=content), patch.object(gitlab_ci.subprocess, "run") as run:
                with self.assertRaises(ValueError):
                    gitlab_ci.publish_release(self.root, self.env)
                run.assert_not_called()
        (self.folder / "metadata.json").write_text(json.dumps(dict(self.data, commit="b" * 40)))
        gitlab_ci.checksums(self.folder)
        with patch.object(gitlab_ci.subprocess, "run") as run:
            with self.assertRaises(ValueError):
                gitlab_ci.publish_release(self.root, self.env)
            run.assert_not_called()

    # 打包失败必须直接终止，不能继续上传或更新 latest.json。
    def test_prepare_failure_never_publishes(self):
        with patch.object(gitlab_ci.subprocess, "run", side_effect=subprocess.CalledProcessError(1, "prepare")) as run:
            with self.assertRaises(subprocess.CalledProcessError):
                gitlab_ci.publish_release(self.root, self.env)
        self.assertEqual(run.call_count, 1)
        self.assertIn("prepare", run.call_args.args[0])


if __name__ == "__main__":
    unittest.main()
