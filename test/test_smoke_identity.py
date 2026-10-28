"""验证镜像运行时必须匹配当前构建版本和目标架构。"""
from pathlib import Path
import sys
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "scripts"))
import smoke_runtime


class SmokeIdentityTests(unittest.TestCase):
    def expected(self, arch):
        return dict(CI_EXPECTED_VERSION="0.0.1-ci.20261023t030405+sha.abcdef1",
                    CI_EXPECTED_COMMIT="abcdef1" + "a" * 33,
                    CI_EXPECTED_BUILD_TIME="2026-10-23T03:04:05Z", CI_EXPECTED_ARCH=arch)

    def version(self, env):
        return (f"slaveSim {env['CI_EXPECTED_VERSION']} "
                f"提交={env['CI_EXPECTED_COMMIT']} 构建时间={env['CI_EXPECTED_BUILD_TIME']}")

    # 两个架构均要求程序真实执行并报告与当前发布完全一致的版本身份。
    def test_matching_version_and_architecture_pass(self):
        for arch, machine in (("amd64", "x86_64"), ("arm64", "aarch64")):
            env = self.expected(arch)
            with self.subTest(arch=arch), \
                    patch.object(smoke_runtime.platform, "machine", return_value=machine), \
                    patch.object(smoke_runtime.subprocess, "check_output", return_value=self.version(env)) as output:
                smoke_runtime.verify_runtime_identity(env)
                output.assert_called_once()
                self.assertEqual(output.call_args.args[0], ["/slavesim", "-version"])

    # 架构错误、版本前缀碰撞、提交或构建时间不一致必须阻止验证通过。
    def test_mismatched_identity_fails(self):
        env = self.expected("arm64")
        original = self.version(env)
        for version, machine in ((original, "x86_64"),
                                 (original.replace(env["CI_EXPECTED_VERSION"], env["CI_EXPECTED_VERSION"] + "0"), "aarch64"),
                                 (original.replace(env["CI_EXPECTED_COMMIT"], "b" * 40), "aarch64"),
                                 (original.replace(env["CI_EXPECTED_BUILD_TIME"], "unknown"), "aarch64")):
            with self.subTest(version=version, machine=machine), \
                    patch.object(smoke_runtime.platform, "machine", return_value=machine), \
                    patch.object(smoke_runtime.subprocess, "check_output", return_value=version):
                with self.assertRaises((ValueError, RuntimeError)):
                    smoke_runtime.verify_runtime_identity(env)

    # 缺少任何身份参数不能降级为仅检查进程可执行。
    def test_missing_identity_fails(self):
        for name in self.expected("arm64"):
            env = self.expected("arm64")
            del env[name]
            with self.subTest(missing=name), self.assertRaises((ValueError, RuntimeError)):
                smoke_runtime.verify_runtime_identity(env)


if __name__ == "__main__":
    unittest.main()
