"""验证显式单架构发布、共享入口升级与未参与平台的资产保留。"""

import io
import json
import subprocess
import sys
import unittest
from contextlib import redirect_stderr
from unittest.mock import patch

import test_release as release_tests

release = release_tests.release


class SingleArchitectureReleaseTests(unittest.TestCase):
    setUp = release_tests.ReleaseTests.setUp
    prepare = release_tests.ReleaseTests.prepare
    publish = release_tests.ReleaseTests.publish

    def test_prepare_only_requires_selected_architecture(self):
        for arch in release.ARCHES:
            with self.subTest(architecture=arch):
                other = next(candidate for candidate in release.ARCHES if candidate != arch)
                probe = self.input / other / "probe"
                original = probe.read_bytes()
                probe.unlink()
                output = self.root / ("release-" + arch)
                manifest = self.prepare(architectures=[arch], output_dir=output)
                self.assertEqual(set(manifest["platforms"]), {"linux-" + arch})
                sums = (output / "SHA256SUMS").read_text()
                self.assertEqual(len(sums.splitlines()), 2)
                self.assertNotIn(other + "/", sums)
                self.assertEqual({path.parent.name for path in (output / "assets").rglob("*.tar.gz")}, {arch})
                probe.write_bytes(original)

    def test_invalid_selection_is_rejected_before_writing_or_uploading(self):
        for architectures in ([], ["amd64", "amd64"], ["riscv64"], "amd64", None):
            with self.subTest(architectures=architectures), patch.object(release, "upload") as upload:
                with self.assertRaises(ValueError):
                    self.prepare(architectures=architectures)
                with self.assertRaises(ValueError):
                    self.publish(architectures=architectures)
                self.assertFalse(self.output.exists())
                upload.assert_not_called()

    def test_reversed_selection_has_canonical_checksums_and_publishes(self):
        manifest = self.prepare(architectures=["arm64", "amd64"])
        with patch.object(release, "get_json", side_effect=[None, None, manifest]), \
                patch.object(release, "upload"), patch.object(release, "verify_asset"):
            self.publish(architectures=["amd64", "arm64"])

    def test_publish_requires_manifest_to_match_explicit_selection(self):
        self.prepare()
        with patch.object(release, "get_json") as read, patch.object(release, "upload") as upload:
            with self.assertRaisesRegex(ValueError, "架构"):
                self.publish(architectures=["amd64"])
            read.assert_not_called()
            upload.assert_not_called()

    def test_single_architecture_is_not_implicit(self):
        self.prepare(architectures=["amd64"])
        with patch.object(release, "get_json") as read, self.assertRaisesRegex(ValueError, "架构"):
            self.publish()
        read.assert_not_called()

    def test_new_x64_release_replaces_shared_latest_without_old_arm_entry(self):
        manifest = self.prepare(version="0.0.0-ci.42.2", channel="ci", architectures=["amd64"])
        older = json.loads(json.dumps(manifest))
        older["version"] = "0.0.0-ci.42.1"
        older["platforms"]["linux-arm64"] = {"url": "https://updates.example/old-arm.tar.gz"}
        events = []
        def upload(source, key, *args):
            events.append(("upload", key))
            if key.endswith("/latest.json"):
                self.assertEqual(json.loads(source.read_text()), manifest)
        with patch.object(release, "get_json", side_effect=[None, older, manifest]), \
                patch.object(release, "upload", side_effect=upload) as uploaded, \
                patch.object(release, "verify_asset", side_effect=lambda *args: events.append(("verify", args[0]))) as verified:
            result = self.publish(architectures=["amd64"])
        self.assertEqual(set(result["platforms"]), {"linux-amd64"})
        self.assertEqual(uploaded.call_count, 5)
        self.assertEqual(verified.call_count, 4)
        self.assertEqual(events[-1], ("upload", "slavesim/ci/latest.json"))
        self.assertFalse(any("/arm64/" in key for _, key in events))

    def test_single_architecture_still_rejects_downgrade_and_failed_assets(self):
        manifest = self.prepare(version="0.0.0-ci.42.2", channel="ci", architectures=["amd64"])
        with patch.object(release, "get_json", side_effect=[None, manifest | {"version": "0.0.0-ci.43.1"}]), \
                patch.object(release, "upload") as upload:
            with self.assertRaisesRegex(ValueError, "倒退"):
                self.publish(architectures=["amd64"])
            upload.assert_not_called()
        with patch.object(release, "get_json", return_value=None), patch.object(release, "upload") as upload, \
                patch.object(release, "verify_asset", side_effect=ValueError("校验失败")):
            with self.assertRaisesRegex(ValueError, "校验失败"):
                self.publish(architectures=["amd64"])
            self.assertFalse(any(call.args[1].endswith("/latest.json") for call in upload.call_args_list))

    def test_cleanup_receives_exact_architecture_selection(self):
        manifest = self.prepare(architectures=["amd64"])
        with patch.object(release, "get_json", side_effect=[None, None, manifest]), \
                patch.object(release, "upload"), patch.object(release, "verify_asset"), \
                patch.object(release, "cleanup_old_versions") as cleanup:
            release.publish(output_dir=self.output, bucket="mskdsp-update", prefix="slavesim",
                            endpoint="https://account.r2.cloudflarestorage.com",
                            public_base_url="https://updates.example", architectures=["amd64"])
        self.assertEqual(cleanup.call_args.kwargs["architectures"], ("amd64",))
        self.assertEqual(len(cleanup.call_args.kwargs["keep_keys"]), 5)

    def test_cli_passes_explicit_and_default_architectures(self):
        for selected in ([], ["--architectures", "amd64"]):
            expected = ["amd64"] if selected else release.ARCHES
            commands = [
                (["prepare", "--version", "1.2.3", "--channel", "stable", "--commit", "a" * 40,
                  "--public-base-url", "https://updates.example"], "prepare"),
                (["publish", "--bucket", "mskdsp-update", "--endpoint", "https://account.r2.cloudflarestorage.com",
                  "--public-base-url", "https://updates.example"], "publish"),
            ]
            for command, target in commands:
                with self.subTest(command=command[0], selected=selected), \
                        patch.object(sys, "argv", ["release.py", *command, *selected]), \
                        patch.object(release, target) as action:
                    release.main()
                    self.assertEqual(action.call_args.kwargs["architectures"], expected)
        with patch.object(sys, "argv", ["release.py", "publish", "--bucket", "mskdsp-update", "--endpoint",
                                        "https://account.r2.cloudflarestorage.com", "--public-base-url",
                                        "https://updates.example", "--architectures", "riscv64"]), \
                redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as stopped:
            release.main()
        self.assertEqual(stopped.exception.code, 2)


class SingleArchitectureRetentionTests(unittest.TestCase):
    def test_cleanup_only_deletes_selected_platform_and_preserves_shared_metadata(self):
        for selected in release.ARCHES:
            with self.subTest(architecture=selected):
                root = "slavesim/ci/"
                other = next(arch for arch in release.ARCHES if arch != selected)
                keep = {root + "latest.json", root + "new/release.json", root + "new/SHA256SUMS",
                        root + f"new/{selected}/binary.tar.gz", root + f"new/{selected}/image.tar.gz"}
                preserved = {root + f"old/{other}/binary.tar.gz", root + f"old/{other}/image.tar.gz",
                             root + "old/release.json", root + "old/SHA256SUMS",
                             root + f"old/{selected}-other/data", root + "unknown-file"}
                obsolete = {root + f"old/{selected}/binary.tar.gz", root + f"old/{selected}/image.tar.gz"}
                before, after = keep | preserved | obsolete, keep | preserved
                pages = [before, after]
                def aws(args, **kwargs):
                    if "list-objects-v2" in args:
                        return subprocess.CompletedProcess(args, 0, json.dumps({
                            "IsTruncated": False, "Contents": [{"Key": key} for key in sorted(pages.pop(0))]}))
                    return subprocess.CompletedProcess(args, 0)
                manifest = {"version": "new"}
                with patch.object(release.subprocess, "run", side_effect=aws) as run, \
                        patch.object(release, "get_json", return_value=manifest):
                    count = release.cleanup_old_versions(
                        bucket="mskdsp-update", endpoint="https://account.r2.cloudflarestorage.com",
                        channel_root=root, keep_keys=keep, manifest=manifest,
                        latest_url="https://updates.example/" + root + "latest.json", architectures=[selected])
                deleted = {call.args[0][call.args[0].index("--key") + 1] for call in run.call_args_list
                           if "delete-object" in call.args[0]}
                self.assertEqual(count, 2)
                self.assertEqual(deleted, obsolete)

    def test_cleanup_detects_loss_of_preserved_arm_assets(self):
        root = "slavesim/ci/"
        keep = {root + "latest.json", root + "new/amd64/binary.tar.gz"}
        arm = {root + "old/arm64/binary.tar.gz"}
        with patch.object(release, "list_channel_objects", side_effect=[keep | arm, keep]), \
                patch.object(release, "get_json", return_value={"version": "new"}), \
                patch.object(release.subprocess, "run") as aws, self.assertRaisesRegex(ValueError, "清理"):
            release.cleanup_old_versions(bucket="mskdsp-update", endpoint="https://account.r2.cloudflarestorage.com",
                                         channel_root=root, keep_keys=keep, manifest={"version": "new"},
                                         latest_url="https://updates.example/" + root + "latest.json",
                                         architectures=["amd64"])
        aws.assert_not_called()


if __name__ == "__main__":
    unittest.main()
