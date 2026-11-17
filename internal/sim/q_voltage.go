package sim

import (
	"fmt"
	"math"
	"time"

	"slavesim/internal/config"
)

// qVoltageDevice 模拟一个并网点。有功和负荷固定，其影响计入基准电压。
// 所有状态由从站锁保护；寄存器内部仍保存原始计数。
type qVoltageDevice struct {
	cfg                      config.QVoltageConfig
	command, measuredQ, volt *Register
	last                     time.Time
	actualQ                  float64
}

func newQVoltageDevice(cfg config.QVoltageConfig, s *Slave, now time.Time) *qVoltageDevice {
	find := func(point config.QVoltagePoint) *Register {
		fn, _ := config.ParseFunction(point.Function) // 配置已验证，且只引用寄存器起始地址。
		return s.Find(fn, point.Address)
	}
	d := &qVoltageDevice{
		cfg:       cfg,
		command:   find(cfg.Command),
		measuredQ: find(cfg.MeasuredQ),
		volt:      find(cfg.Voltage),
		last:      now,
	}
	d.publish()
	return d
}

func (d *qVoltageDevice) advance(now time.Time) {
	if !now.After(d.last) {
		return
	}
	targetQ := d.command.Value(d.last) * d.cfg.Command.Scale
	targetQ = math.Max(d.cfg.MinKvar, math.Min(d.cfg.MaxKvar, targetQ))
	step := d.cfg.RampKvarPerSecond * now.Sub(d.last).Seconds()
	d.last = now
	delta := targetQ - d.actualQ
	if delta == 0 {
		return
	}
	if math.Abs(delta) <= step {
		d.actualQ = targetQ
	} else {
		d.actualQ += math.Copysign(step, delta)
	}
	d.publish()
}

func (d *qVoltageDevice) publish() {
	d.measuredQ.Write(d.actualQ / d.cfg.MeasuredQ.Scale)
	d.volt.Write((d.cfg.BaseVoltageV + d.cfg.VoltsPerKvar*d.actualQ) / d.cfg.Voltage.Scale)
}

func (d *qVoltageDevice) manages(reg *Register) bool {
	return reg == d.measuredQ || reg == d.volt
}

func (d *qVoltageDevice) setCommand(raw float64, now time.Time) error {
	minRaw, maxRaw := 0.0, 65535.0
	if d.command.Type == TypeInt16 {
		minRaw, maxRaw = -32768, 32767
	}
	if math.Trunc(raw) != raw || raw < minRaw || raw > maxRaw {
		return fmt.Errorf("无功命令原始值必须是 %.0f～%.0f 的整数", minRaw, maxRaw)
	}
	d.advance(now)
	d.command.Write(raw)
	return nil
}
