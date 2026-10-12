package sim

import (
	"encoding/binary"
	"math"
	"path/filepath"
	"testing"
	"time"

	"slavesim/internal/config"
	"slavesim/internal/modbus"
)

func modelSlave(t *testing.T, now time.Time) *Slave {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "..", "configs", "inverters", "huawei-v3-model.json"))
	if err != nil {
		t.Fatal(err)
	}
	sl, err := NewSlave(cfg.Buses[0].Slaves[0], now)
	if err != nil {
		t.Fatal(err)
	}
	return sl
}

func modelWrite(sl *Slave, now time.Time, addr uint16, words ...uint16) Result {
	data := []byte{1, 0x10, byte(addr >> 8), byte(addr), 0, byte(len(words)), byte(len(words) * 2)}
	for _, word := range words {
		data = append(data, byte(word>>8), byte(word))
	}
	return sl.HandleAt(modbus.AppendCRC(data), now)
}

// 验证默认当日电量和累计电量均为零，运行一分钟后按实际49kW有功累加。
func TestHuaweiDefaultEnergyStartsAtZero(t *testing.T) {
	now := time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)
	sl := modelSlave(t, now)
	initial := sl.Sample(now)
	if initial.Model.DayEnergyKWH != 0 || initial.Model.TotalEnergyKWH != 0 {
		t.Fatalf("默认电量初值应为零: %+v", initial.Model)
	}
	for _, addr := range []uint16{32106, 32114} {
		if sl.Find(3, addr).Value(now) != 0 {
			t.Fatalf("电量寄存器%d初值应为零", addr)
		}
	}
	v := sl.Sample(now.Add(time.Minute)).Model
	if math.Abs(v.DayEnergyKWH-49.0/60) > 1e-5 || math.Abs(v.TotalEnergyKWH-49.0/60) > 1e-5 {
		t.Fatalf("电量没有从零按实际功率累加: %+v", v)
	}
}

// 验证显式配置的电量初值仍被采用，默认值修改不会清空已有历史初值。
func TestHuaweiConfiguredEnergyInitialValues(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "..", "configs", "inverters", "huawei-v3-model.json"))
	if err != nil {
		t.Fatal(err)
	}
	station := cfg.Buses[0].Slaves[0]
	for i := range station.Registers {
		switch station.Registers[i].Address {
		case 32106:
			station.Registers[i].Source.Value = 10000
		case 32114:
			station.Registers[i].Source.Value = 1200
		}
	}
	now := time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)
	sl, err := NewSlave(station, now)
	if err != nil {
		t.Fatal(err)
	}
	v := sl.Sample(now).Model
	if v.DayEnergyKWH != 12 || v.TotalEnergyKWH != 100 {
		t.Fatalf("已配置的电量初值被覆盖: %+v", v)
	}
}

func TestHuaweiSnapshotMatchesWireValues(t *testing.T) {
	now := time.Date(2026, 10, 11, 1, 0, 0, 0, time.UTC)
	sl := modelSlave(t, now)
	if err := sl.SetPoint(3, 40122, -900, now); err != nil {
		t.Fatal(err)
	}
	snap := sl.Sample(now.Add(10 * time.Second))
	for i, reg := range sl.order {
		if snap.Managed[i] && snap.Values[i] != reg.DecodeWrite(snap.Raw[i]) {
			t.Fatalf("点表地址%d的快照与线上整数不一致", reg.Address)
		}
	}
}

// 验证真实控制帧使有功逐步变化，且电流、输入功率与模式反馈来自同一设备状态。
func TestHuaweiPowerControlAndPhysicalRelations(t *testing.T) {
	now := time.Date(2026, 10, 11, 1, 0, 0, 0, time.UTC)
	sl := modelSlave(t, now)
	// 显式写入1%/秒，保留慢速斜坡的回归覆盖，不依赖默认快速梯度。
	if err := sl.SetPoint(3, 42017, 1000, now); err != nil {
		t.Fatal(err)
	}
	if res := modelWrite(sl, now, 40125, 500); res.IsException {
		t.Fatal(res.Summary)
	}
	sl.Step(now.Add(10 * time.Second))
	view := sl.Sample(now.Add(10 * time.Second)).Model
	if math.Abs(view.ActiveKW-44) > .001 {
		t.Fatalf("初始49kW，10秒后按1%%/秒应为44kW: %+v", view)
	}
	sl.Step(now.Add(60 * time.Second))
	snap := sl.Sample(now.Add(60 * time.Second))
	if math.Abs(snap.Model.ActiveKW-27.5) > .001 {
		t.Fatalf("50%%Pmax限值应收敛27.5kW: %+v", snap.Model)
	}
	raw := func(addr uint16) float64 { return sl.Find(3, addr).Value(now) }
	current := raw(32072) / 1000
	if math.Abs(math.Sqrt(3)*400*current/1000-27.5) > .001 {
		t.Fatal("三相电流与输出功率不一致")
	}
	pvKW := 0.0
	for addr := uint16(32016); addr <= 32022; addr += 2 {
		pvKW += raw(addr) / 10 * raw(addr+1) / 100 / 1000
	}
	if math.Abs(pvKW-raw(32064)/1000) > .001 {
		t.Fatal("PV组串与DC总功率不一致")
	}
	if raw(35300) != 0 || raw(35301) != 500 || raw(35303) != 40125 {
		t.Fatal("百分比模式反馈没有联动")
	}
	if res := modelWrite(sl, now.Add(60*time.Second), 40126, 0, 10000); res.IsException {
		t.Fatal(res.Summary)
	}
	sl.Step(now.Add(100 * time.Second))
	if math.Abs(sl.Sample(now.Add(100*time.Second)).Model.ActiveKW-10) > .001 {
		t.Fatal("固定W命令没有切换控制模式")
	}
	if raw(35300) != 1 || raw(35301) != 10000 || raw(35303) != 40126 {
		t.Fatal("固定功率模式反馈不正确")
	}
}

// 验证默认有功和无功梯度均为100%/秒，分别按Pn与Smax在亚秒时间内抵达目标。
func TestHuaweiDefaultGradientsAndSettlingTime(t *testing.T) {
	now := time.Date(2026, 10, 12, 1, 0, 0, 0, time.UTC)
	sl := modelSlave(t, now)
	for _, addr := range []uint16{42017, 42015} {
		if sl.Find(3, addr).Value(now) != 100000 {
			t.Fatalf("梯度寄存器%d默认应为100%%/秒", addr)
		}
	}
	if err := sl.SetPoint(3, 40125, 500, now); err != nil {
		t.Fatal(err)
	}
	if v := sl.Sample(now.Add(200 * time.Millisecond)).Model; math.Abs(v.ActiveKW-39) > .001 {
		t.Fatalf("按50kW/秒调节200ms后应为39kW: %+v", v)
	}
	if v := sl.Sample(now.Add(500 * time.Millisecond)).Model; math.Abs(v.ActiveKW-27.5) > .001 {
		t.Fatalf("默认梯度500ms内应抵达27.5kW: %+v", v)
	}
	q := modelSlave(t, now)
	if err := q.SetPoint(3, 40122, 900, now); err != nil {
		t.Fatal(err)
	}
	if v := q.Sample(now.Add(100 * time.Millisecond)).Model; math.Abs(v.ReactiveKvar-5.5) > .001 {
		t.Fatalf("按55kvar/秒调节100ms后应为5.5kvar: %+v", v)
	}
	if v := q.Sample(now.Add(500 * time.Millisecond)).Model; math.Abs(v.ReactiveKvar-v.TargetReactiveKvar) > .001 || math.Abs(v.PowerFactor-.9) > .001 {
		t.Fatalf("默认无功梯度500ms内应抵达PF0.9: %+v", v)
	}
}

// 验证非法命令、半个32位点及含非法项的批量写全部拒绝，保持原命令。
func TestHuaweiRejectsInvalidAndPartialCommandsAtomically(t *testing.T) {
	now := time.Now()
	sl := modelSlave(t, now)
	for _, tc := range []struct {
		addr  uint16
		words []uint16
	}{
		{40125, []uint16{1001}}, {40122, []uint16{0}}, {40122, []uint16{64536}},
		{40123, []uint16{1001}}, {40126, []uint16{1}}, {40127, []uint16{10000}},
		{40196, []uint16{10}}, {42017, []uint16{0, 99}},
		{40120, []uint16{551}}, {40122, []uint16{900, 1001}},
	} {
		res := modelWrite(sl, now, tc.addr, tc.words...)
		if !res.IsException {
			t.Fatalf("地址%d 非法命令不应成功: %+v", tc.addr, res)
		}
	}
	if sl.Find(3, 40122).Value(now) != 1000 || sl.Find(3, 40125).Value(now) != 1000 {
		t.Fatal("非法批量写产生部分副作用")
	}
	if err := sl.SetPoint(3, 32080, 123, now); err == nil {
		t.Fatal("模型遥测不能直接手改")
	}
	if err := sl.SetPoint(3, 40125, 500, now); err != nil {
		t.Fatal(err)
	}
	if sl.Sample(now).Model.ActiveCommand != 40125 || sl.Sample(now).Model.TargetActiveKW != 27.5 {
		t.Fatal("Web控制路径没有作用于模型")
	}
}

// 验证有符号Q/S写入、PF反馈、能力圆约束和同帧模式优先顺序。
func TestHuaweiReactiveControlsAndLimits(t *testing.T) {
	now := time.Now()
	sl := modelSlave(t, now)
	if res := modelWrite(sl, now, 40122, 900); res.IsException {
		t.Fatal(res.Summary)
	}
	sl.Step(now.Add(time.Minute))
	v := sl.Sample(now.Add(time.Minute)).Model
	if math.Abs(v.PowerFactor-.9) > .01 || v.ReactiveKvar <= 0 {
		t.Fatalf("PF没有联动Q: %+v", v)
	}
	// -1000是合法Q/S命令，按INT16补码发送；49kW有功时能力圆限制到约-25kvar。
	if res := modelWrite(sl, now.Add(time.Minute), 40123, 64536); res.IsException {
		t.Fatal(res.Summary)
	}
	sl.Step(now.Add(3 * time.Minute))
	v = sl.Sample(now.Add(3 * time.Minute)).Model
	if v.ReactiveKvar >= 0 || math.Hypot(v.ActiveKW, v.ReactiveKvar) > 55.0001 {
		t.Fatalf("无功符号或容量约束错误: %+v", v)
	}
	// 连续地址同时写PF和Q/S时，较后地址的Q/S模式确定生效。
	for i := 0; i < 20; i++ {
		if res := modelWrite(sl, now.Add(3*time.Minute), 40122, 950, 200); res.IsException {
			t.Fatal(res.Summary)
		}
	}
	if sl.Sample(now.Add(3*time.Minute)).Model.ReactiveCommand != 40123 {
		t.Fatal("批量模式切换顺序不确定")
	}
}

// 验证无需轮询也会积分电量，跨本地午夜分段并保留累计电量。
func TestHuaweiEnergyAndDayBoundary(t *testing.T) {
	now := time.Date(2026, 10, 11, 15, 59, 30, 0, time.UTC)
	sl := modelSlave(t, now)
	start := sl.Sample(now).Model.TotalEnergyKWH
	sl.Step(now.Add(time.Minute))
	v := sl.Sample(now.Add(time.Minute)).Model
	if math.Abs(v.TotalEnergyKWH-start-49.0/60) > 1e-5 {
		t.Fatalf("累计电量积分错误: %+v", v)
	}
	if math.Abs(v.DayEnergyKWH-49.0/120) > 1e-5 {
		t.Fatalf("跨日当日电量错误: %+v", v)
	}
}

// 验证场景减光后不能超过可用功率，并且在无光时输出归零进入待机。
func TestHuaweiScenarioAndWireEncoding(t *testing.T) {
	now := time.Now()
	sl := modelSlave(t, now)
	scene := sl.Sample(now).Model.Scenario
	scene.DCAvailableKW = 10
	if err := sl.SetScenario(scene, now); err != nil {
		t.Fatal(err)
	}
	sl.Step(now.Add(time.Second))
	v := sl.Sample(now.Add(time.Second)).Model
	if math.Abs(v.ActiveKW-9.8) > .001 {
		t.Fatalf("可用功率没有约束输出: %+v", v)
	}
	frame := sl.HandleAt(req(1, 3, 32080, 2), now.Add(time.Second)).Response
	if binary.BigEndian.Uint32(frame[3:7]) != 9800 {
		t.Fatalf("华为kW增益编码错误: % X", frame)
	}
	scene.DCAvailableKW = 0
	if err := sl.SetScenario(scene, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	v = sl.Sample(now.Add(2 * time.Second)).Model
	if v.ActiveKW != 0 || v.ReactiveKvar != 0 || v.Status != "待机" {
		t.Fatalf("无光状态不正确: %+v", v)
	}
	scene.Efficiency = 0
	if err := sl.SetScenario(scene, now.Add(2*time.Second)); err == nil {
		t.Fatal("非法场景应拒绝")
	}
}
