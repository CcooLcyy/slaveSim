#!/usr/bin/env python3
"""在发布镜像内验证管理进程、网页模块和真实 PTY 收发；不访问更新源。"""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
from urllib.request import urlopen


def main():
    with tempfile.TemporaryDirectory(prefix="slavesim-smoke-") as temporary:
        root = Path(temporary)
        config = json.loads(Path("/etc/slavesim/config.example.json").read_text())
        for index, bus in enumerate(config["buses"]):
            bus["link"] = str(root / f"tty{index}")
        config_path = root / "config.json"
        config_path.write_text(json.dumps(config))
        manager = subprocess.Popen([sys.executable, "-B", "/app/runtime.py",
                                    "-config", str(config_path), "-http", "127.0.0.1:16000",
                                    "--runtime-dir", str(root / "runtime"),
                                    "--updater-config", str(root / "missing.json")])
        try:
            deadline = time.monotonic() + 40
            while True:
                if manager.poll() is not None:
                    raise RuntimeError("管理进程提前退出")
                try:
                    with urlopen("http://127.0.0.1:16000/api/update/status", timeout=2) as response:
                        status = json.load(response)
                    break
                except OSError:
                    if time.monotonic() >= deadline:
                        raise RuntimeError("管理进程启动超时")
                    time.sleep(0.2)
            assert status["state"] == "idle" and not status["enabled"], status
            with urlopen("http://127.0.0.1:16000/", timeout=2) as response:
                assert b'id="update-check"' in response.read()
            for module in ("update-panel.mjs", "update-model.mjs"):
                with urlopen("http://127.0.0.1:16000/" + module, timeout=2) as response:
                    assert "javascript" in response.headers["Content-Type"], response.headers
                    assert response.read()
            with urlopen("http://127.0.0.1:16000/api/state", timeout=2) as response:
                state = json.load(response)
            assert len(state["buses"]) == len(config["buses"])
            for bus in state["buses"]:
                assert bus["slave_path"] and os.path.exists(bus["link_path"]), bus
            # 示例的两站都应返回频率 5000；探针同时校验协议帧 CRC。
            for station in (1, 2):
                result = subprocess.run(["/probe", "-device", config["buses"][0]["link"],
                                         "-id", str(station), "-func", "4", "-addr", "3042", "-qty", "1"],
                                        check=True, capture_output=True, text=True, timeout=5)
                assert "13 88" in result.stdout, result.stdout
            print("管理进程、网页模块 MIME、两站真实 PTY 收发验证通过")
        finally:
            manager.terminate()
            try:
                manager.wait(timeout=10)
            except subprocess.TimeoutExpired:
                manager.kill()
                manager.wait(timeout=5)


if __name__ == "__main__":
    main()
