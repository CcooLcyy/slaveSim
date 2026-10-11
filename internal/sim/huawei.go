package sim

import (
	"fmt"
	"math"
	"time"

	"slavesim/internal/config"
)

// HuaweiView 是设备内部工程量快照；线上整数值仍由既有点表提供。
type HuaweiView struct {
	Kind               string             `json:"kind"`
	ActiveKW           float64            `json:"active_kw"`
	ReactiveKvar       float64            `json:"reactive_kvar"`
	TargetActiveKW     float64            `json:"target_active_kw"`
	TargetReactiveKvar float64            `json:"target_reactive_kvar"`
	PowerFactor        float64            `json:"power_factor"`
	DayEnergyKWH       float64            `json:"day_energy_kwh"`
	TotalEnergyKWH     float64            `json:"total_energy_kwh"`
	Status             string             `json:"status"`
	ActiveCommand      uint16             `json:"active_command"`
	ReactiveCommand    uint16             `json:"reactive_command"`
	Scenario           config.HuaweiModel `json:"scenario"`
}

// huaweiDevice 专注设备状态演算；所有调用由所属 Slave 的互斥锁保护。
type huaweiDevice struct {
	scene                              config.HuaweiModel
	regs                               map[uint16]*Register
	last                               time.Time
	p, q, dayEnergy, totalEnergy, peak float64
	pn, pmax, smax, qmax, qmin         float64
	pCommand, qCommand                 uint16
	pSetting, qSetting                 float64
	timezone                           *time.Location
}

func newHuaweiDevice(cfg config.HuaweiModel, regs []*Register, now time.Time) *huaweiDevice {
	h := &huaweiDevice{scene: cfg, regs: map[uint16]*Register{}, last: now, pCommand: 40125, qCommand: 40122}
	for _, r := range regs {
		if r.Function == 3 {
			h.regs[r.Address] = r
		}
	}
	h.pn = h.raw(30073) / 1000
	h.pmax = h.raw(30075) / 1000
	h.smax = h.raw(30077) / 1000
	h.qmax = h.raw(30079) / 1000
	h.qmin = h.raw(30081) / 1000
	h.pSetting = h.raw(40125)
	h.qSetting = h.raw(40122)
	h.dayEnergy = h.raw(32114) / 100
	h.totalEnergy = h.raw(32106) / 100
	h.peak = h.raw(32078) / 1000
	h.timezone = time.FixedZone("仿真设备时区", int(h.raw(43006))*60)
	h.p = h.targetP()
	h.q = h.targetQ(h.p)
	h.publish()
	return h
}

func (h *huaweiDevice) raw(addr uint16) float64        { return h.regs[addr].Value(h.last) }
func (h *huaweiDevice) set(addr uint16, value float64) { h.regs[addr].Write(value) }
func (h *huaweiDevice) supported(addr uint16) bool {
	switch addr {
	case 40120, 40122, 40123, 40125, 40126, 42015, 42017:
		return true
	}
	return false
}

func (h *huaweiDevice) managed(addr uint16) bool {
	if h.supported(addr) {
		return true
	}
	if addr >= 32017 && addr <= 32023 && addr%2 == 1 {
		return true
	}
	if addr >= 32064 && addr <= 32087 {
		return true
	}
	switch addr {
	case 32000, 32089, 32106, 32114, 35300, 35301, 35303, 35304, 35305, 35307:
		return true
	}
	return false
}

func (h *huaweiDevice) validateCommand(addr uint16, v float64) error {
	if math.IsNaN(v) || math.IsInf(v, 0) || math.Trunc(v) != v {
		return fmt.Errorf("华为命令必须是有限的原始整数")
	}
	valid := false
	switch addr {
	case 40120:
		valid = v >= 0 && v/10 <= h.pmax
	case 40126:
		valid = v >= 0 && v/1000 <= h.pmax
	case 40125:
		valid = v >= 0 && v <= 1000
	case 40122:
		valid = (v > -1000 && v <= -800) || (v >= 800 && v <= 1000)
	case 40123:
		valid = v >= -1000 && v <= 1000
	case 42015, 42017:
		valid = v >= 100 && v <= 1000000
	default:
		return fmt.Errorf("华为地址 %d 的写入行为尚未实现", addr)
	}
	if !valid {
		return fmt.Errorf("华为地址 %d 的原始值 %g 超出点表允许范围", addr, v)
	}
	return nil
}

func (h *huaweiDevice) command(addr uint16, v float64) {
	h.set(addr, v)
	switch addr {
	case 40120, 40125, 40126:
		h.pCommand = addr
		h.pSetting = v
	case 40122, 40123:
		h.qCommand = addr
		h.qSetting = v
	}
	h.publish()
}

func (h *huaweiDevice) availableP() float64 {
	return math.Min(h.scene.DCAvailableKW*h.scene.Efficiency, math.Min(h.pmax, h.smax))
}
func (h *huaweiDevice) targetP() float64 {
	limit := h.pmax * h.pSetting / 1000
	switch h.pCommand {
	case 40120:
		limit = h.pSetting / 10
	case 40126:
		limit = h.pSetting / 1000
	}
	return math.Min(h.availableP(), limit)
}
func (h *huaweiDevice) targetQ(p float64) float64 {
	if p <= 0 {
		return 0
	} // 首版暂未实现夜间无功。
	q := h.qSetting / 1000 * h.smax
	if h.qCommand == 40122 {
		pf := h.qSetting / 1000
		q = math.Copysign(p*math.Sqrt(math.Max(0, 1/(pf*pf)-1)), pf)
	}
	capacity := math.Sqrt(math.Max(0, h.smax*h.smax-p*p))
	return math.Max(math.Max(h.qmin, -capacity), math.Min(math.Min(h.qmax, capacity), q))
}

// moveAndArea 精确积分线性斜坡，抵达目标后保持，不因轮询间隔改变能量。
func moveAndArea(value, target, rate, seconds float64) (float64, float64) {
	if rate <= 0 || value == target {
		return value, value * seconds
	}
	travel := math.Min(seconds, math.Abs(target-value)/rate)
	next := value + math.Copysign(rate*travel, target-value)
	if travel < seconds {
		next = target
	}
	return next, (value+next)*travel/2 + next*(seconds-travel)
}

func (h *huaweiDevice) advance(now time.Time) {
	if !now.After(h.last) {
		return
	}
	for h.last.Before(now) {
		end := h.last.Add(100 * time.Millisecond)
		if end.After(now) {
			end = now
		}
		local := h.last.In(h.timezone)
		midnight := time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, h.timezone)
		if end.After(midnight) {
			end = midnight
		}
		seconds := end.Sub(h.last).Seconds()
		// 光照降低是物理可用功率约束，不能靠慢降额维持不存在的能量。
		h.p = math.Min(h.p, h.availableP())
		next, area := moveAndArea(h.p, h.targetP(), h.raw(42017)/1000/100*h.pn, seconds)
		h.p = next
		h.q, _ = moveAndArea(h.q, h.targetQ(h.p), h.raw(42015)/1000/100*h.smax, seconds)
		capacity := math.Sqrt(math.Max(0, h.smax*h.smax-h.p*h.p))
		h.q = math.Max(-capacity, math.Min(capacity, h.q))
		if h.p == 0 {
			h.q = 0
		}
		h.totalEnergy += area / 3600
		h.dayEnergy += area / 3600
		h.peak = math.Max(h.peak, h.p)
		h.last = end
		if end.Equal(midnight) {
			h.dayEnergy = 0
			h.peak = h.p
		}
	}
	h.publish()
}

func (h *huaweiDevice) status() string {
	if h.availableP() <= 0 {
		return "待机"
	}
	if h.targetP()+1e-6 < h.availableP() {
		if h.p == 0 {
			return "限电停运"
		}
		return "限电降额"
	}
	return "并网"
}
func (h *huaweiDevice) pf() float64 {
	s := math.Hypot(h.p, h.q)
	if s == 0 {
		return 1
	}
	return math.Copysign(h.p/s, h.q)
}
func (h *huaweiDevice) view() *HuaweiView {
	return &HuaweiView{Kind: "huawei_v3", ActiveKW: h.p, ReactiveKvar: h.q, TargetActiveKW: h.targetP(), TargetReactiveKvar: h.targetQ(h.p), PowerFactor: h.pf(), DayEnergyKWH: h.dayEnergy, TotalEnergyKWH: h.totalEnergy, Status: h.status(), ActiveCommand: h.pCommand, ReactiveCommand: h.qCommand, Scenario: h.scene}
}

func (h *huaweiDevice) publish() {
	h.set(32080, h.p*1000)
	h.set(32082, h.q*1000)
	h.set(32084, h.pf()*1000)
	h.set(32078, h.peak*1000)
	h.set(32106, h.totalEnergy*100)
	h.set(32114, h.dayEnergy*100)
	dc := h.p / h.scene.Efficiency
	h.set(32064, dc*1000)
	for a := uint16(32016); a <= 32022; a += 2 {
		voltage := h.raw(a) / 10
		current := 0.0
		if voltage > 0 {
			current = dc * 1000 / 4 / voltage
		}
		h.set(a+1, current*100)
	}
	for a := uint16(32066); a <= 32068; a++ {
		h.set(a, h.scene.GridLineVoltageV*10)
	}
	for a := uint16(32069); a <= 32071; a++ {
		h.set(a, h.scene.GridLineVoltageV/math.Sqrt(3)*10)
	}
	current := math.Hypot(h.p, h.q) * 1000 / (math.Sqrt(3) * h.scene.GridLineVoltageV)
	for a := uint16(32072); a <= 32076; a += 2 {
		h.set(a, current*1000)
	}
	h.set(32085, h.scene.GridFrequencyHZ*100)
	efficiency := h.scene.Efficiency * 10000
	if h.p == 0 {
		efficiency = 0
	}
	h.set(32086, efficiency)
	// 首版热模型为负荷相关的稳态温升，不声称复刻机型温控算法。
	h.set(32087, (h.scene.AmbientTemperatureC+10*h.p/h.pn)*10)
	status := float64(6)
	if h.status() == "限电降额" {
		status = 2 + 8
	}
	if h.status() == "待机" {
		status = 1
	}
	if h.status() == "限电停运" {
		status = 128
	}
	h.set(32000, status)
	// 非正常状态参照华为2017协议旧地址枚举；V3同址兼容性仍待核验。
	deviceStatus := float64(0x0200)
	if h.status() == "限电降额" {
		deviceStatus = 0x0201
	}
	if h.status() == "限电停运" {
		deviceStatus = 0x0304
	}
	if h.status() == "待机" {
		deviceStatus = 0xA000
	}
	h.set(32089, deviceStatus)
	pMode, pValue := float64(0), h.pSetting
	if h.pCommand == 40120 {
		pMode = 1
		pValue = h.pSetting * 100
	}
	if h.pCommand == 40126 {
		pMode = 1
		pValue = h.pSetting
	}
	h.set(35300, pMode)
	h.set(35301, pValue)
	h.set(35303, float64(h.pCommand))
	qMode := float64(0)
	if h.qCommand == 40123 {
		qMode = 2
	}
	// 反馈定义为32位容器；负有符号设定按补码保存，不能钳制成零。
	h.set(35304, qMode)
	h.set(35305, float64(uint32(int32(h.qSetting))))
	h.set(35307, float64(h.qCommand))
}
