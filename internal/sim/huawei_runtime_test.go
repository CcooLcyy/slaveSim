//go:build linux

package sim

import (
	"encoding/binary"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"slavesim/internal/config"
	"slavesim/internal/eventbus"
	"slavesim/internal/modbus"
)

// 验证真实PTY控制帧驱动设备，设备在没有后续轮询时仍自动改变输出。
func TestHuaweiPTYAndAutonomousRuntime(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "..", "configs", "inverters", "huawei-v3-model.json"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg.Buses[0].Link = filepath.Join(dir, "tty0")
	m, err := NewManager(filepath.Join(dir, "config.json"), cfg, eventbus.NewLog(128))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	var bus *Bus
	m.WithBuses(func(buses []*Bus) { bus = buses[0] })
	sl := bus.Slaves()[0]
	// 最大梯度令设备在一次独立tick内到达27.5kW，避免慢测试。
	grad := modbus.AppendCRC([]byte{1, 0x10, byte(42017 >> 8), byte(42017 & 255), 0, 2, 4, 0, 15, 0x42, 0x40})
	if resp := exchange(t, bus, grad); resp[1]&0x80 != 0 {
		t.Fatalf("梯度写入失败: % X", resp)
	}
	command := req(1, 6, 40125, 500)
	if resp := exchange(t, bus, command); modbus.Hex(resp) != modbus.Hex(command) {
		t.Fatalf("PTY控制响应不符: % X", resp)
	}
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		// 仅读取已存值，不调用Sample/Step/Handle，证明独立调度器确实推进了模型。
		if sl.Find(3, 32080).Value(time.Now()) == 27500 {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("无人轮询时设备模型没有推进")
		case <-tick.C:
		}
	}
	resp := exchange(t, bus, req(1, 3, 32080, 2))
	if !modbus.CheckCRC(resp) || binary.BigEndian.Uint32(resp[3:7]) != 27500 {
		t.Fatalf("PTY功率编码不符: % X", resp)
	}
}

// 验证配置独立区域的同地址不会覆盖华为保持寄存器映射。
func TestHuaweiSeparateAddressSpaces(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "..", "configs", "inverters", "huawei-v3-model.json"))
	if err != nil {
		t.Fatal(err)
	}
	station := cfg.Buses[0].Slaves[0]
	station.Registers = append(station.Registers, config.Register{Function: "0x04", Address: 30073, Type: "UINT32", Source: config.Source{Kind: "const", Value: 1}})
	now := time.Now()
	sl, err := NewSlave(station, now)
	if err != nil {
		t.Fatal(err)
	}
	if sl.Sample(now).Model.ActiveKW < 49.99 {
		t.Fatal("输入区域覆盖了保持区域的额定能力")
	}
}

// 验证网页场景、控制、快照和定时演算同时访问时没有数据竞争。
func TestHuaweiConcurrentCommandsAndSnapshots(t *testing.T) {
	now := time.Now()
	sl := modelSlave(t, now)
	scene := sl.Sample(now).Model.Scenario
	var wg sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(kind int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				switch kind {
				case 0:
					sl.Step(time.Now())
				case 1:
					sl.Sample(time.Now())
				case 2:
					if err := sl.SetPoint(3, 40125, 500, time.Now()); err != nil {
						t.Error(err)
					}
				case 3:
					if err := sl.SetScenario(scene, time.Now()); err != nil {
						t.Error(err)
					}
				}
			}
		}(worker)
	}
	wg.Wait()
}
