package sim

import "math"

// disturbanceEnvelope 平滑引入扰动，起始时刻仍采用配置初值。
func (h *huaweiDevice) disturbanceEnvelope() float64 {
	if !h.scene.DisturbanceActive() {
		return 0
	}
	return 1 - math.Exp(-math.Max(0, h.last.Sub(h.origin).Seconds())/3)
}

// disturbance 使用慢环境变化及较快测量变化，不依赖随机数或读取次数。
// 每个通道采用不同相位；有界输出保证配置中的扰动幅度不会被突破。
func (h *huaweiDevice) disturbance(channel int) float64 {
	t := h.last.Sub(h.origin).Seconds()
	phase := h.phase + float64(channel)*1.61803398875
	wave := .65*math.Sin(2*math.Pi*t/29+phase) + .25*math.Sin(2*math.Pi*t/73+phase*1.3) + .1*math.Sin(2*math.Pi*t/2.7+phase*1.7)
	return h.disturbanceEnvelope() * wave
}
