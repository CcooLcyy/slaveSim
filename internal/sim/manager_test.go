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

func TestManagerApplyKeepsUnchangedSlaveRuntime(t *testing.T) {
	dir := t.TempDir()
	cfg, err := config.Load(filepath.Join("..", "..", "configs", "inverters", "huawei-v3-model.json"))
	if err != nil {
		t.Fatal(err)
	}
	bus := &cfg.Buses[0]
	bus.Link = filepath.Join(dir, "tty0")
	manual := config.Register{Function: "3", Address: 100, Source: config.Source{Kind: "manual", Value: 10}}
	bus.Slaves = append(bus.Slaves,
		config.Slave{DeviceID: 2, Registers: []config.Register{manual}},
		config.Slave{DeviceID: 3, Registers: []config.Register{manual}},
		config.Slave{DeviceID: 4})
	m, err := NewManager(filepath.Join(dir, "config.json"), cfg, eventbus.NewLog(64))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	oldBus := m.buses[0]
	oldPTY := oldBus.SlavePath()
	oldSlaves := oldBus.Slaves()
	now := time.Now()
	for _, sl := range oldSlaves[1:3] {
		if err := sl.SetPoint(3, 100, 99, now); err != nil {
			t.Fatal(err)
		}
	}
	scene := bus.Slaves[0].Model.Clone()
	scene.GridLineVoltageV++
	if err := oldSlaves[0].SetScenario(scene, now); err != nil {
		t.Fatal(err)
	}
	if err := oldSlaves[0].SetPoint(3, 40123, -200, now); err != nil {
		t.Fatal(err)
	}

	next := cfg.Clone()
	changedBus := &next.Buses[0]
	changedBus.Name = "renamed"
	unchanged := &changedBus.Slaves[1]
	unchanged.UnmappedPolicy = "exception"
	point := &unchanged.Registers[0]
	point.Function, point.Type, point.WordOrder, point.ByteOrder, point.Access = "0x03", "UINT16", "HL", "AB", "readonly"
	// 网页填充了手动来源未使用的默认参数，保存后应继续读到运行时覆盖值。
	point.Source.Amplitude, point.Source.PeriodMs, point.Source.Max = 1, 1000, 100
	changedBus.Slaves[2].Registers[0].Source.Value = 20
	changedBus.Slaves[3] = config.Slave{DeviceID: 5}
	added := config.Bus{Name: "new", Link: filepath.Join(dir, "tty1"), Slaves: []config.Slave{{DeviceID: 1}}}
	next.Buses = append([]config.Bus{added}, next.Buses...)
	if err := m.Apply(next); err != nil {
		t.Fatal(err)
	}
	if m.buses[1] != oldBus || oldBus.SlavePath() != oldPTY || oldBus.name() != "renamed" {
		t.Fatal("重新排序或改名不应替换固定路径对应的总线及 PTY")
	}
	slaves := oldBus.Slaves()
	if len(slaves) != 4 || slaves[0] != oldSlaves[0] || slaves[1] != oldSlaves[1] || slaves[2] == oldSlaves[2] || slaves[3].DeviceID != 5 {
		t.Fatal("仅未变更的从站应保留原运行对象")
	}
	if value := slaves[1].Find(3, 100).Value(time.Now()); value != 99 {
		t.Fatalf("未变更从站的临时值丢失：%g", value)
	}
	if value := slaves[2].Find(3, 100).Value(time.Now()); value != 20 {
		t.Fatalf("变更从站未使用新配置初值：%g", value)
	}
	view := slaves[0].Sample(time.Now()).Model
	if view.ReactiveCommand != 40123 || view.Scenario.GridLineVoltageV != scene.GridLineVoltageV {
		t.Fatalf("未变更华为从站的临时场景或控制命令丢失：%+v", view)
	}

	next.Buses = next.Buses[1:]
	if err := m.Apply(next); err != nil {
		t.Fatal(err)
	}
	if m.buses[0] != oldBus || oldBus.Slaves()[0] != oldSlaves[0] || oldBus.Slaves()[1] != oldSlaves[1] {
		t.Fatal("删除新增总线时不应重建其他总线或从站")
	}
	if _, err := os.Lstat(added.Link); !os.IsNotExist(err) {
		t.Fatalf("删除新增总线后没有释放串口：%v", err)
	}
}

func TestManagerApplySaveFailureDoesNotReplaceSlaves(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "tty0")
	cfg := &config.Config{Buses: []config.Bus{{Name: "existing", Link: link, Slaves: []config.Slave{{DeviceID: 1}}}}}
	m, err := NewManager(filepath.Join(dir, "missing", "config.json"), cfg, eventbus.NewLog(16))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	oldBus := m.buses[0]
	oldSlave := oldBus.Slaves()[0]
	addedLink := filepath.Join(dir, "tty1")
	next := cfg.Clone()
	next.Buses[0].Name = "renamed"
	next.Buses[0].Slaves[0].ResponseDelayMs = 100
	next.Buses = append(next.Buses, config.Bus{Name: "new", Link: addedLink})
	if err := m.Apply(next); err == nil {
		t.Fatal("配置目录不存在时应保存失败")
	}
	if len(m.buses) != 1 || m.buses[0] != oldBus || oldBus.name() != "existing" || oldBus.Slaves()[0] != oldSlave {
		t.Fatal("保存失败改动了原运行总线或从站")
	}
	if _, err := os.Lstat(addedLink); !os.IsNotExist(err) {
		t.Fatalf("保存失败没有回滚新增串口：%v", err)
	}
}

func TestComparableRegisterUsesEffectiveSourceParameters(t *testing.T) {
	for _, tc := range []struct {
		name       string
		before     config.Source
		fromEditor config.Source
		changed    config.Source
	}{
		{
			name:       "const",
			before:     config.Source{Value: 5},
			fromEditor: config.Source{Kind: "const", Value: 5, Amplitude: 1, PeriodMs: 1000, Max: 100},
			changed:    config.Source{Kind: "const", Value: 6},
		},
		{
			name:       "manual",
			before:     config.Source{Kind: "manual", Value: 5},
			fromEditor: config.Source{Kind: "manual", Value: 5, Amplitude: 1, PeriodMs: 1000, Max: 100},
			changed:    config.Source{Kind: "manual", Value: 6},
		},
		{
			name:       "sine",
			before:     config.Source{Kind: "sine", Base: 5, Amplitude: 1, PeriodMs: 1200},
			fromEditor: config.Source{Kind: "sine", Base: 5, Amplitude: 1, PeriodMs: 1200, Max: 100},
			changed:    config.Source{Kind: "sine", Base: 5, Amplitude: 2, PeriodMs: 1200},
		},
		{
			name:       "ramp",
			before:     config.Source{Kind: "ramp", Min: 5, Max: 20, PeriodMs: 1200},
			fromEditor: config.Source{Kind: "ramp", Min: 5, Max: 20, PeriodMs: 1200, Amplitude: 1},
			changed:    config.Source{Kind: "ramp", Min: 5, Max: 21, PeriodMs: 1200},
		},
		{
			name:       "random",
			before:     config.Source{Kind: "random", Min: 5, Max: 20, PeriodMs: 1200},
			fromEditor: config.Source{Kind: "random", Min: 5, Max: 20, PeriodMs: 1200, Amplitude: 1},
			changed:    config.Source{Kind: "random", Min: 5, Max: 20, PeriodMs: 1300},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := comparableRegister(config.Register{Function: "0x03", Source: tc.before})
			if before != comparableRegister(config.Register{Function: "0x03", Source: tc.fromEditor}) {
				t.Fatal("网页补全无关参数后应视为相同配置")
			}
			if before == comparableRegister(config.Register{Function: "0x03", Source: tc.changed}) {
				t.Fatal("修改来源有效参数应视为配置变化")
			}
		})
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
