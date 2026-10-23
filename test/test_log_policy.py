"""日志参数透传和容器容量限制验收；无需 Docker 或设备。"""
import contextlib
import io
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
import runtime
import update


class RuntimeLogPolicyTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.identity = mock.patch.object(runtime, "binary_identity", return_value={
            "version": "0.0.1-dev", "commit": "unknown"})
        self.identity.start()

    def tearDown(self):
        self.identity.stop()
        self.temp.cleanup()

    def manager(self, **kwargs):
        return runtime.Manager(self.root / "runtime", self.root / "updater.json",
                               logfile="/logs/slavesim.log", seed=Path("seed"), **kwargs)

    def test_default_values_preserve_legacy_binary_arguments(self):
        manager = self.manager()
        self.assertEqual(manager.log_max_size_mib, 50)
        self.assertEqual(manager.log_backups, 5)
        with mock.patch.object(runtime.subprocess, "Popen") as launch:
            manager.launch()
        self.assertEqual(launch.call_args.args[0], ["seed", "-config", "/etc/slavesim/config.json",
                         "-http", "127.0.0.1:16001", "-logfile", "/logs/slavesim.log"])

    def test_custom_policy_survives_release_switch_and_restart(self):
        manager = self.manager(log_max_size_mib=12, log_backups=0)
        release = {"release": "installed"}
        with mock.patch.object(runtime.subprocess, "Popen") as launch:
            manager.launch(release)
            manager.launch({"release": "seed"})
            manager.launch()
        for call in launch.call_args_list:
            self.assertEqual(call.args[0][-4:], ["-log-max-size-mib", "12", "-log-backups", "0"])
        self.assertEqual(launch.call_args_list[0].args[0][0], str(manager.runtime_dir / "releases/installed/slavesim"))
        self.assertEqual(launch.call_args_list[1].args[0][0], "seed")

    def test_only_nondefault_setting_is_forwarded(self):
        for settings, expected in (({"log_max_size_mib": 1}, ["-log-max-size-mib", "1"]),
                                   ({"log_backups": 0}, ["-log-backups", "0"])):
            with self.subTest(settings=settings):
                manager = self.manager(**settings)
                with mock.patch.object(runtime.subprocess, "Popen") as launch:
                    manager.launch()
                self.assertEqual(launch.call_args.args[0][-2:], expected)

    def test_largest_supported_values_are_accepted(self):
        manager = self.manager(log_max_size_mib=8796093022207, log_backups=9223372036854775807)
        self.assertEqual(manager.log_max_size_mib, 8796093022207)
        self.assertEqual(manager.log_backups, 9223372036854775807)

    def test_invalid_manager_values_are_rejected(self):
        for settings in ({"log_max_size_mib": 0}, {"log_max_size_mib": -1},
                         {"log_max_size_mib": 8796093022208}, {"log_max_size_mib": 1.5},
                         {"log_max_size_mib": True}, {"log_backups": -1},
                         {"log_backups": 1.5}, {"log_backups": 9223372036854775808}):
            with self.subTest(settings=settings), self.assertRaises(ValueError):
                self.manager(**settings)

    def test_cli_rejects_invalid_parameters_before_startup(self):
        for option, value in (("-log-max-size-mib", "0"), ("-log-max-size-mib", "-1"),
                              ("-log-max-size-mib", "8796093022208"),
                              ("-log-max-size-mib", "1.5"), ("-log-backups", "-1"),
                              ("-log-backups", "1.5"), ("-log-backups", "9223372036854775808")):
            with self.subTest(option=option, value=value), contextlib.redirect_stderr(io.StringIO()), \
                    mock.patch.object(runtime, "Manager") as manager:
                with self.assertRaises(SystemExit) as result:
                    runtime.main([option, value])
                self.assertEqual(result.exception.code, 2)
                manager.assert_not_called()

    def test_cli_passes_policy_to_normal_and_version_manager(self):
        (self.root / "state.json").write_text("{}", encoding="utf-8")
        for version in (False, True):
            with self.subTest(version=version), mock.patch.object(runtime, "Manager") as manager, \
                    mock.patch.object(runtime.subprocess, "call", return_value=0), \
                    mock.patch.object(runtime.signal, "signal"), \
                    mock.patch.object(update, "log"):
                manager.return_value.start.side_effect = update.UpdateError("测试结束")
                runtime.main(["--runtime-dir", str(self.root), "-log-max-size-mib", "12",
                              "-log-backups", "0"] + (["-version"] if version else []))
                self.assertEqual(manager.call_args.kwargs["log_max_size_mib"], 12)
                self.assertEqual(manager.call_args.kwargs["log_backups"], 0)


class ComposeLogPolicyTests(unittest.TestCase):
    def test_both_compose_files_bound_docker_logs(self):
        for path in (ROOT / "compose.yml", ROOT / "deploy/compose.release.yml"):
            with self.subTest(path=path):
                text = path.read_text(encoding="utf-8")
                self.assertRegex(text, r'(?m)^    logging:\s*\n      driver: json-file\s*\n      options:\s*\n        max-size: "?10m"?\s*\n        max-file: "3"\s*$')


if __name__ == "__main__":
    unittest.main()
