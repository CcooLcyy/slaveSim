package sim

import (
	"encoding/binary"
	"math"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"slavesim/internal/config"
)

func defaultDisturbedSlave(t *testing.T, now time.Time, id uint8) *Slave {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "..", "configs", "inverters", "huawei-v3-model.json"))
	if err != nil {
		t.Fatal(err)
	}
	station := cfg.Buses[0].Slaves[0]
	station.DeviceID = id
	sl, err := NewSlave(station, now)
	if err != nil {
		t.Fatal(err)
	}
	return sl
}

// 验证固定场景默认产生可编码的微扰，同一时刻读取一致，电量及控制关系保持正确。
func TestHuaweiDisturbanceTelemetry(t *testing.T) {
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	sl := defaultDisturbedSlave(t, now, 1)
	addresses := []uint16{32016, 32017, 32066, 32072, 32080, 32082, 32085, 32087}
	seen := map[uint16]map[float64]bool{}
	for _, addr := range addresses {
		seen[addr] = map[float64]bool{}
	}
	energy := 0.0
	for i := 0; i <= 120; i++ {
		at := now.Add(time.Duration(i) * time.Second)
		snap := sl.Sample(at)
		if !reflect.DeepEqual(snap, sl.Sample(at)) {
			t.Fatal("同一时刻读值发生变化")
		}
		v := snap.Model
		if v.ActiveKW < 0 || v.ActiveKW > 50 || math.Hypot(v.ActiveKW, v.ReactiveKvar) > 55.00001 {
			t.Fatalf("功率越限: %+v", v)
		}
		if v.TotalEnergyKWH < energy {
			t.Fatal("电量倒退")
		}
		energy = v.TotalEnergyKWH
		read := func(addr uint16) float64 { r := sl.Find(3, addr); return r.DecodeWrite(r.RawRegisters(at)) }
		for _, addr := range addresses {
			seen[addr][read(addr)] = true
		}
		frame := sl.HandleAt(req(1, 3, 32080, 2), at).Response
		if len(frame) != 9 || float64(binary.BigEndian.Uint32(frame[3:7])) != read(32080) {
			t.Fatal("扰动后的Modbus功率与后端快照不一致")
		}
		if math.Abs(math.Sqrt(3)*read(32066)/10*read(32072)/1000/1000-math.Hypot(v.ActiveKW, v.ReactiveKvar)) > .02 {
			t.Fatal("电流和P/Q不一致")
		}
		if read(40125) != 1000 || read(30073) != 50000 || v.Status != "并网" {
			t.Fatal("扰动改变了控制设定、身份或状态")
		}
	}
	for _, addr := range addresses {
		if len(seen[addr]) < 2 {
			t.Errorf("地址%d在稳定场景下一直固定", addr)
		}
		min, max := math.Inf(1), math.Inf(-1)
		for value := range seen[addr] {
			min = math.Min(min, value)
			max = math.Max(max, value)
		}
		t.Logf("地址%d原始值波动区间: %g..%g", addr, min, max)
	}
}

// 验证不同站号不同步，限功率及无光待机仍然有效。
func TestHuaweiDisturbanceIndependenceAndLimits(t *testing.T) {
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	a, b := defaultDisturbedSlave(t, now, 1), defaultDisturbedSlave(t, now, 2)
	if a.Sample(now.Add(17*time.Second)).Model.ActiveKW == b.Sample(now.Add(17*time.Second)).Model.ActiveKW {
		t.Fatal("不同站号扰动完全同步")
	}
	if err := a.SetPoint(3, 40125, 500, now.Add(17*time.Second)); err != nil {
		t.Fatal(err)
	}
	for i := 20; i < 80; i++ {
		v := a.Sample(now.Add(time.Duration(i) * time.Second)).Model
		if v.ActiveKW > 27.5 || math.Hypot(v.ActiveKW, v.ReactiveKvar) > 55.00001 {
			t.Fatal("微扰突破控制限制")
		}
	}
	scene := a.Sample(now.Add(80 * time.Second)).Model.Scenario
	scene.DCAvailableKW = 0
	if err := a.SetScenario(scene, now.Add(80*time.Second)); err != nil {
		t.Fatal(err)
	}
	v := a.Sample(now.Add(90 * time.Second)).Model
	if v.ActiveKW != 0 || v.ReactiveKvar != 0 || v.Status != "待机" {
		t.Fatalf("无光仍产生功率: %+v", v)
	}
}

// 验证额外网页或协议读取不会改变同一终点的设备演算结果。
func TestHuaweiDisturbancePollingIndependence(t *testing.T) {
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	a, b := defaultDisturbedSlave(t, now, 1), defaultDisturbedSlave(t, now, 1)
	for i := 1; i <= 777; i++ {
		a.Sample(now.Add(time.Duration(i) * 37 * time.Millisecond))
	}
	end := now.Add(31 * time.Second)
	if !reflect.DeepEqual(a.Sample(end), b.Sample(end)) {
		t.Fatal("额外读取改变了同一时刻的演算结果")
	}
	for _, sl := range []*Slave{a, b} {
		if err := sl.SetPoint(3, 40125, 500, end); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 777; i++ {
		b.Sample(end.Add(time.Duration(i) * 37 * time.Millisecond))
	}
	if !reflect.DeepEqual(a.Sample(end.Add(31*time.Second)), b.Sample(end.Add(31*time.Second))) {
		t.Fatal("控制后额外读取改变演算结果")
	}
}

// 验证场景输入输出中的开关指针相互隔离，关闭后稳态与原控制一致。
func TestHuaweiDisturbanceScenarioIsolationAndDisable(t *testing.T) {
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	sl := defaultDisturbedSlave(t, now, 1)
	scene := sl.Sample(now).Model.Scenario
	enabled := false
	scene.DisturbanceEnabled = &enabled
	if err := sl.SetScenario(scene, now); err != nil {
		t.Fatal(err)
	}
	enabled = true
	if sl.Sample(now).Model.Scenario.DisturbanceActive() {
		t.Fatal("输入开关与运行模型共享指针")
	}
	out := sl.Sample(now).Model.Scenario
	*out.DisturbanceEnabled = true
	if sl.Sample(now).Model.Scenario.DisturbanceActive() {
		t.Fatal("输出开关与运行模型共享指针")
	}
	v := sl.Sample(now.Add(time.Minute)).Model
	if math.Abs(v.ActiveKW-49) > 1e-9 || math.Abs(v.TotalEnergyKWH-49.0/60) > 1e-9 {
		t.Fatalf("关闭扰动未恢复确定性稳态: %+v", v)
	}
}

// 验证场景处在物理边界时，扰动不能把电网与PV电压、频率推出允许范围。
func TestHuaweiDisturbanceBoundaryInputs(t *testing.T) {
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	for _, high := range []bool{false, true} {
		sl := defaultDisturbedSlave(t, now, 1)
		scene := sl.Sample(now).Model.Scenario
		scene.GridLineVoltageV, scene.GridFrequencyHZ = 100, 40
		pv := 80.0
		if high {
			scene.GridLineVoltageV, scene.GridFrequencyHZ, pv = 1000, 70, 1500
		}
		for i := range sl.model.pvVoltages {
			sl.model.pvVoltages[i] = pv
		}
		if err := sl.SetScenario(scene, now); err != nil {
			t.Fatal(err)
		}
		for i := 1; i <= 120; i++ {
			at := now.Add(time.Duration(i) * time.Second)
			sl.Sample(at)
			for addr, bounds := range map[uint16][2]float64{32016: {800, 15000}, 32066: {1000, 10000}, 32085: {4000, 7000}} {
				v := sl.Find(3, addr).Value(at)
				if v < bounds[0] || v > bounds[1] {
					t.Fatalf("边界输入的地址%d越界: %g", addr, v)
				}
			}
		}
	}
}
