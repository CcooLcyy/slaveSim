"""验证 R2 清理时序、分页与删除边界。"""

import json
import subprocess
import unittest
from unittest.mock import patch

import test_release as release_tests

release = release_tests.release
ROOT = "slavesim/ci/"
KEEP = {ROOT + "latest.json", ROOT + "new/release.json",
        ROOT + "new/SHA256SUMS", *(ROOT + f"new/{arch}/{kind}.tar.gz"
                                   for arch in ("amd64", "arm64")
                                   for kind in ("binary", "image"))}
MANIFEST = {"version": "new"}


def page(keys, **changes):
    return {"Contents": [{"Key": key} for key in sorted(keys)],
            "IsTruncated": False} | changes


class RetentionTests(unittest.TestCase):
    def cleanup(self):
        return release.cleanup_old_versions(
            bucket="mskdsp-update", endpoint="https://account.r2.cloudflarestorage.com",
            channel_root=ROOT, keep_keys=KEEP, manifest=MANIFEST,
            latest_url="https://updates.example/" + ROOT + "latest.json")

    def test_paginated_listing_then_exact_deletes_preserves_all_latest_assets(self):
        old = {ROOT + "0.0.0-ci.2.1/amd64/old-image.tar.gz", ROOT + "older/release.json"}
        pages = [page(KEEP, IsTruncated=True, NextContinuationToken="second"),
                 page(old), page(KEEP)]
        calls = []
        def aws(args, **kwargs):
            calls.append(args)
            if "list-objects-v2" in args:
                self.assertEqual(kwargs["capture_output"], True)
                return subprocess.CompletedProcess(args, 0, json.dumps(pages.pop(0)))
            return subprocess.CompletedProcess(args, 0)
        with patch.object(release.subprocess, "run", side_effect=aws), \
             patch.object(release, "get_json", return_value=MANIFEST):
            self.assertEqual(self.cleanup(), 2)
        self.assertTrue(all("list-objects-v2" in args for args in calls[:2]))
        self.assertEqual(calls[1][calls[1].index("--continuation-token") + 1], "second")
        deleted = {args[args.index("--key") + 1] for args in calls if "delete-object" in args}
        self.assertEqual(deleted, old)
        for args in calls:
            self.assertEqual(args[args.index("--bucket") + 1], "mskdsp-update")
            if "--prefix" in args:
                self.assertEqual(args[args.index("--prefix") + 1], ROOT)
                self.assertIn("--no-paginate", args)

    def test_bad_listing_or_missing_keep_objects_never_deletes(self):
        bad_pages = [page(KEEP | {"slavesim/stable/1.0.0/file"}),
                     page(KEEP | {"mskdsp-upper/ci/file"}),
                     page(KEEP | {"slavesim/ci-other/file"}),
                     page(KEEP - {ROOT + "new/arm64/image.tar.gz"}),
                     {"Contents": "invalid"}, page(KEEP, IsTruncated=True),
                     page(KEEP, IsTruncated="false"),
                     page(KEEP, Contents=[{"Key": 123}])]
        for response in bad_pages:
            with self.subTest(response=response), \
                 patch.object(release.subprocess, "run", return_value=subprocess.CompletedProcess(
                     [], 0, json.dumps(response))) as aws, self.assertRaises(ValueError):
                self.cleanup()
            self.assertFalse(any("delete-object" in call.args[0] for call in aws.call_args_list))

    def test_repeated_pagination_token_or_later_page_failure_never_deletes(self):
        for failure in (subprocess.CalledProcessError(1, ["aws"]),
                        subprocess.CompletedProcess([], 0, json.dumps(
                            page({ROOT + "old/file"}, IsTruncated=True, NextContinuationToken="repeat")))):
            first = subprocess.CompletedProcess([], 0, json.dumps(
                page(KEEP, IsTruncated=True, NextContinuationToken="repeat")))
            with self.subTest(failure=failure), \
                 patch.object(release.subprocess, "run", side_effect=[first, failure]) as aws, \
                 self.assertRaises((ValueError, subprocess.CalledProcessError)):
                self.cleanup()
            self.assertFalse(any("delete-object" in call.args[0] for call in aws.call_args_list))

    def test_changed_latest_never_deletes(self):
        with patch.object(release.subprocess, "run", return_value=subprocess.CompletedProcess(
                [], 0, json.dumps(page(KEEP | {ROOT + "old/file"})))) as aws, \
             patch.object(release, "get_json", return_value={"version": "newer"}), \
             self.assertRaisesRegex(ValueError, "入口"):
            self.cleanup()
        self.assertEqual(aws.call_count, 1)

    def test_delete_failure_is_reported_and_does_not_modify_latest(self):
        responses = [subprocess.CompletedProcess([], 0, json.dumps(page(KEEP | {ROOT + "old/file"}))),
                     subprocess.CalledProcessError(1, ["aws", "s3api", "delete-object"])]
        with patch.object(release.subprocess, "run", side_effect=responses) as aws, \
             patch.object(release, "get_json", return_value=MANIFEST), \
             self.assertRaises(subprocess.CalledProcessError):
            self.cleanup()
        self.assertIn("delete-object", aws.call_args_list[-1].args[0])
        self.assertNotIn(ROOT + "latest.json", aws.call_args_list[-1].args[0])

    def test_remaining_old_objects_fail_verification(self):
        response = subprocess.CompletedProcess([], 0, json.dumps(page(KEEP | {ROOT + "old/file"})))
        with patch.object(release.subprocess, "run", return_value=response), \
             patch.object(release, "get_json", return_value=MANIFEST), \
             self.assertRaisesRegex(ValueError, "清理"):
            self.cleanup()

    def test_no_old_objects_is_idempotent(self):
        with patch.object(release.subprocess, "run", return_value=subprocess.CompletedProcess(
                [], 0, json.dumps(page(KEEP)))) as aws, \
             patch.object(release, "get_json", return_value=MANIFEST):
            self.assertEqual(self.cleanup(), 0)
        self.assertFalse(any("delete-object" in call.args[0] for call in aws.call_args_list))


class PublicationRetentionTests(unittest.TestCase):
    setUp = release_tests.ReleaseTests.setUp
    prepare = release_tests.ReleaseTests.prepare

    def publish(self):
        return release.publish(output_dir=self.output, bucket="mskdsp-update", prefix="slavesim",
                               endpoint="https://account.r2.cloudflarestorage.com",
                               public_base_url="https://updates.example")

    def test_cleanup_only_after_public_latest_verified(self):
        manifest = self.prepare()
        events = []
        def read(*args, **kwargs):
            events.append("read")
            return manifest if events.count("read") == 3 else None
        def clean(**kwargs):
            self.assertEqual(events[-1], "read")
            self.assertEqual(events.count("read"), 3)
            self.assertEqual(len(kwargs["keep_keys"]), 7)
            self.assertEqual(kwargs["channel_root"], "slavesim/stable/")
            for arch in ("amd64", "arm64"):
                self.assertEqual(sum(f"/{arch}/" in key for key in kwargs["keep_keys"]), 2)
            events.append("clean")
        with patch.object(release, "get_json", side_effect=read), \
             patch.object(release, "upload", side_effect=lambda *args: events.append("upload")), \
             patch.object(release, "verify_asset", side_effect=lambda *args: events.append("verify")), \
             patch.object(release, "cleanup_old_versions", side_effect=clean):
            self.publish()
        self.assertEqual(events[-1], "clean")

    def test_publication_failure_never_cleans_old_versions(self):
        manifest = self.prepare()
        scenarios = [(RuntimeError("upload"), None, [None, None]),
                     (None, ValueError("asset"), [None, None]),
                     (None, None, [None, None, manifest | {"version": "wrong"}])]
        for upload_error, verify_error, reads in scenarios:
            with self.subTest(reads=reads), patch.object(release, "get_json", side_effect=reads), \
                 patch.object(release, "upload", side_effect=upload_error), \
                 patch.object(release, "verify_asset", side_effect=verify_error), \
                 patch.object(release, "cleanup_old_versions") as cleanup, \
                 self.assertRaises((RuntimeError, ValueError)):
                self.publish()
            cleanup.assert_not_called()
