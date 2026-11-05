"""后台每分钟自动更新、退出及零总线健康检查契约，不连接发布源或设备。"""
import io
import json
from datetime import datetime, timedelta
from pathlib import Path
import threading
import unittest
from unittest import mock

from test_runtime import BASE, OLD, ManagerFixture, manifest, runtime


class AutomaticUpdateTests(ManagerFixture):
    def configured_manager(self, **settings):
        path = self.root / "automatic-updater.json"
        path.write_text(json.dumps(dict(manifest_url=BASE, **settings)), encoding="utf-8")
        manager = runtime.Manager(self.root / "automatic-runtime", path, seed=Path("seed"))
        self.addCleanup(manager.close)
        return manager

    def test_default_is_unattended_apply_every_sixty_seconds(self):
        """有效更新源默认启用每60秒自动安装，不需要网页请求。"""
        manager = self.configured_manager()
        status = manager.status()
        self.assertTrue(status["enabled"])
        self.assertTrue(status["auto_update_enabled"])
        self.assertEqual(status["check_interval_seconds"], 60)
        self.assertEqual(status["next_check_at"], "")

    def test_explicit_disable_and_custom_interval_are_preserved(self):
        """允许管理员关闭自动安装或显式指定检查间隔，手动更新仍可用。"""
        manager = self.configured_manager(auto_update=False, check_interval_seconds=120)
        self.assertTrue(manager.status()["enabled"])
        self.assertFalse(manager.status()["auto_update_enabled"])
        self.assertEqual(manager.status()["check_interval_seconds"], 120)
        with mock.patch.object(manager, "begin") as begin:
            self.assertFalse(manager.automatic_tick())
        begin.assert_not_called()

    def test_missing_source_does_not_enable_automatic_update(self):
        """没有有效更新源时不启动自动更新，不能把空来源交给下载器。"""
        manager = runtime.Manager(self.root / "no-source", self.root / "absent.json", seed=Path("seed"))
        self.addCleanup(manager.close)
        self.assertFalse(manager.status()["auto_update_enabled"])
        with mock.patch.object(manager, "begin") as begin:
            self.assertFalse(manager.automatic_tick())
        begin.assert_not_called()

    def test_invalid_automatic_configuration_is_explicitly_disabled(self):
        """自动开关严格为布尔值，间隔严格为1至86400的整数，错误配置明确禁用。"""
        invalid = ([dict(auto_update=value) for value in (1, "true", None)]
                   + [dict(check_interval_seconds=value) for value in (0, 86401, 1.5, True, "60", None)])
        for settings in invalid:
            with self.subTest(settings=settings):
                manager = self.configured_manager(**settings)
                self.assertFalse(manager.status()["enabled"])
                self.assertFalse(manager.status()["auto_update_enabled"])
                self.assertIn("配置无效", manager.status()["message"])

    def test_tick_reuses_serial_apply_and_skips_busy_disabled_or_stopping(self):
        """自动任务走同一apply入口；忙碌、禁用、无来源或退出时不重复提交。"""
        self.manager.auto_update_enabled = True
        with mock.patch.object(self.manager, "begin") as begin:
            self.assertTrue(self.manager.automatic_tick())
            begin.assert_called_once_with("apply")
            begin.reset_mock()
            self.manager.busy = True
            self.assertFalse(self.manager.automatic_tick())
            self.manager.busy = False
            self.manager.auto_update_enabled = False
            self.assertFalse(self.manager.automatic_tick())
            self.manager.auto_update_enabled = True
            source = self.manager.source_url
            self.manager.source_url = ""
            self.assertFalse(self.manager.automatic_tick())
            self.manager.source_url = source
            self.manager.stopping.set()
            self.assertFalse(self.manager.automatic_tick())
            begin.assert_not_called()

    def test_tick_failure_does_not_prevent_next_period(self):
        """一次调度异常只记录错误，下一个周期仍可正常发起apply。"""
        self.manager.auto_update_enabled = True
        with mock.patch.object(self.manager, "begin", side_effect=[runtime.BusyError("已有任务"), None]) as begin:
            self.assertFalse(self.manager.automatic_tick())
            self.assertTrue(self.manager.automatic_tick())
        self.assertEqual(begin.call_args_list, [mock.call("apply"), mock.call("apply")])

    def test_loop_waits_sixty_seconds_before_each_tick_without_webpage(self):
        """后台循环先等60秒再触发，状态可见下次时间，退出后清空计划。"""
        self.manager.auto_update_enabled = True
        waits, ticks = [], []
        def wait(seconds):
            waits.append(seconds)
            scheduled = self.manager.status()["next_check_at"]
            self.assertEqual(datetime.fromisoformat(scheduled).utcoffset(), timedelta(0))
            self.assertEqual(len(ticks), len(waits) - 1)
            return len(waits) == 3
        with mock.patch.object(self.manager.stopping, "wait", side_effect=wait), \
                mock.patch.object(self.manager, "automatic_tick", side_effect=lambda: ticks.append("apply")):
            self.manager.automatic_loop()
        self.assertEqual(waits, [60, 60, 60])
        self.assertEqual(ticks, ["apply", "apply"])
        self.assertEqual(self.manager.status()["next_check_at"], "")

    def test_start_creates_background_scheduler_and_close_stops_it(self):
        """进程启动即创建自动线程，不依赖HTTP访问；关闭会唤醒并回收线程。"""
        self.manager.auto_update_enabled = True
        entered = threading.Event()
        def loop():
            entered.set()
            self.manager.stopping.wait()
        with mock.patch.object(self.manager, "launch"), mock.patch.object(self.manager, "healthy", return_value=True), \
                mock.patch.object(self.manager, "supervise"), mock.patch.object(self.manager, "automatic_loop", side_effect=loop):
            self.manager.start()
            self.assertTrue(entered.wait(2))
            scheduler = self.manager.automatic
            self.assertIsNotNone(scheduler)
            self.assertTrue(scheduler.is_alive())
            self.manager.close()
            self.assertTrue(self.manager.stopping.is_set())
            self.assertFalse(scheduler.is_alive())

    def test_disabled_start_does_not_create_scheduler(self):
        """关闭自动更新后仍能启动模拟器，但不能创建自动安装线程。"""
        with mock.patch.object(self.manager, "launch"), mock.patch.object(self.manager, "healthy", return_value=True), \
                mock.patch.object(self.manager, "supervise"), mock.patch.object(self.manager, "automatic_loop") as loop:
            self.manager.start()
            self.assertIsNone(self.manager.automatic)
            loop.assert_not_called()

    def test_automatic_candidate_failure_rolls_back_then_next_tick_can_succeed(self):
        """自动安装沿用健康失败回滚，保留失败结果，下一周期仍可安装。"""
        self.manager.auto_update_enabled = True
        stack, stop = self.mocks()
        with stack, mock.patch.object(self.manager, "healthy", side_effect=[False, True]):
            self.assertTrue(self.manager.automatic_tick())
            self.manager.worker.join(3)
            self.assertFalse(self.manager.worker.is_alive())
        self.assertEqual(self.manager.current["version"], OLD["version"])
        self.assertEqual(self.manager.status()["state"], "error")
        self.assertIn("已回滚", self.manager.status()["message"])
        self.assertEqual(stop.call_count, 2)
        stack, _ = self.mocks()
        with stack, mock.patch.object(self.manager, "healthy", return_value=True):
            self.assertTrue(self.manager.automatic_tick())
            self.manager.worker.join(3)
            self.assertFalse(self.manager.worker.is_alive())
        self.assertEqual(self.manager.status()["state"], "succeeded")
        self.assertEqual(self.manager.current["version"], self.data["version"])

    def test_automatic_same_version_does_not_restart(self):
        """每分钟检查到相同版本时不能反复重启模拟器子进程。"""
        self.manager.auto_update_enabled = True
        self.data = manifest()
        stack, stop = self.mocks()
        with stack:
            self.assertTrue(self.manager.automatic_tick())
            self.manager.worker.join(3)
            self.assertFalse(self.manager.worker.is_alive())
        self.assertEqual(self.manager.status()["state"], "current")
        stop.assert_not_called()


class ZeroBusHealthTests(ManagerFixture):
    def test_zero_bus_state_is_healthy_only_when_running_state_is_also_zero(self):
        """零总线允许[]或Go的null反馈；缺字段、非对象或残留总线均不能通过。"""
        config = self.root / "empty-config.json"
        config.write_text('{"buses":[]}', encoding="utf-8")
        self.manager.config = str(config)
        self.manager.child = mock.Mock()
        self.manager.child.poll.return_value = None
        for state, expected in (({"buses": []}, True), ({"buses": None}, True), ({}, False),
                                ([], False), ({"buses": [{"name": "旧总线", "slave_path": "/dev/pts/1"}]}, False)):
            with self.subTest(state=state), mock.patch.object(runtime, "urlopen", return_value=io.BytesIO(json.dumps(state).encode())):
                self.assertEqual(self.manager.healthy(timeout=0.02), expected)
        self.manager.child = None


if __name__ == "__main__":
    unittest.main()
