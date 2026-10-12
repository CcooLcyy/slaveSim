//go:build linux

package sim

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"slavesim/internal/config"
	"slavesim/internal/eventbus"
	"slavesim/internal/modbus"
)

// 模拟器重启并更换 PTY 编号后，同一固定路径应指向新设备。
func TestManagerRestartKeepsFixedSerialPath(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "tty0")
	c := &config.Config{Buses: []config.Bus{{Name: "RS485-1", Link: link, Slaves: []config.Slave{{DeviceID: 1}}}}}
	path := filepath.Join(dir, "config.json")
	m, err := NewManager(path, c, eventbus.NewLog(16))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	oldTarget, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	// 保留旧从端描述符，确保重启时不能复用旧 PTY 编号。
	oldDevice, err := os.OpenFile(link, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer oldDevice.Close()
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewManager(path, c, eventbus.NewLog(16))
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	newTarget, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	if oldTarget == newTarget {
		t.Fatal("重启验证未产生不同的 PTY 编号")
	}
	device, err := os.OpenFile(link, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("重启后无法通过固定路径打开串口: %v", err)
	}
	defer device.Close()
	fixedInfo, err := device.Stat()
	if err != nil {
		t.Fatal(err)
	}
	actualInfo, err := os.Stat(newTarget)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(fixedInfo, actualInfo) {
		t.Fatal("固定路径没有指向重启后的实际设备")
	}
}

func TestManagerRecoversOnlyMissingPTYLinks(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "tty0")
	if err := os.Symlink("/dev/pts/4294967295", link); err != nil {
		t.Fatal(err)
	}
	c := &config.Config{Buses: []config.Bus{{Name: "RS485-1", Link: link, Slaves: []config.Slave{{DeviceID: 1}}}}}
	m, err := NewManager(filepath.Join(dir, "config.json"), c, eventbus.NewLog(16))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	regular := filepath.Join(dir, "regular")
	if err := os.WriteFile(regular, []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(regular, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(filepath.Join(dir, "config.json"), c, eventbus.NewLog(16)); err == nil {
		t.Fatal("覆盖了普通文件软链接")
	}
	if target, err := os.Readlink(link); err != nil || target != regular {
		t.Fatal("已有链接被改变")
	}
}

func TestBusCloseInterruptsLongResponseDelay(t *testing.T) {
	logs := eventbus.NewLog(32)
	events, cancel := logs.Subscribe()
	defer cancel()
	sl, err := NewSlave(config.Slave{DeviceID: 1, ResponseDelayMs: 60000, Registers: []config.Register{{Function: "0x03", Address: 100}}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	b := NewBus("delayed", filepath.Join(t.TempDir(), "tty0"), []*Slave{sl}, logs)
	if err := b.Open(); err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	b.Start()
	f, err := os.OpenFile(b.SlavePath(), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(modbus.AppendCRC([]byte{1, 3, 0, 100, 0, 1})); err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case e := <-events:
			if e.Direction == eventbus.DirectionRx {
				goto received
			}
		case <-timer.C:
			t.Fatal("没有收到请求")
		}
	}
received:
	done := make(chan error, 1)
	go func() { done <- b.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("延迟响应阻塞了总线关闭")
	}
}
