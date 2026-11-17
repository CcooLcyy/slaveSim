package config

import (
	"fmt"
	"math"
)

// QVoltageConfig 描述一个并网点的无功反馈和电压随无功变化的关系。
type QVoltageConfig struct {
	Command           QVoltagePoint `json:"command"`
	MeasuredQ         QVoltagePoint `json:"measured_q"`
	Voltage           QVoltagePoint `json:"voltage"`
	BaseVoltageV      float64       `json:"base_voltage_v"`
	VoltsPerKvar      float64       `json:"volts_per_kvar"`
	MinKvar           float64       `json:"min_kvar"`
	MaxKvar           float64       `json:"max_kvar"`
	RampKvarPerSecond float64       `json:"ramp_kvar_per_second"`
}

// QVoltagePoint 按功能码和协议地址引用已有寄存器。
// Scale 为每一原始计数对应的工程量，命令和无功反馈使用 kvar，电压使用 V。
type QVoltagePoint struct {
	Function string  `json:"function"`
	Address  uint16  `json:"address"`
	Scale    float64 `json:"scale"`
}

func (m *QVoltageConfig) normalizeFunctions() {
	for _, point := range []*QVoltagePoint{&m.Command, &m.MeasuredQ, &m.Voltage} {
		fn, _ := ParseFunction(point.Function) // 只在校验成功后调用。
		point.Function = fmt.Sprintf("0x%02X", fn)
	}
}

// ValidateQVoltage 校验已规范化寄存器上的无功电压联动映射和取值范围。
func (s Slave) ValidateQVoltage() error {
	m := s.QVoltage
	if m == nil {
		return nil
	}
	if s.Model != nil {
		return fmt.Errorf("q_voltage 与设备行为模型不能同时启用")
	}
	for _, field := range []struct {
		name  string
		value float64
	}{
		{"base_voltage_v", m.BaseVoltageV},
		{"volts_per_kvar", m.VoltsPerKvar},
		{"ramp_kvar_per_second", m.RampKvarPerSecond},
	} {
		if !finiteQVoltageValue(field.value) || field.value <= 0 {
			return fmt.Errorf("%s 必须是有限正数", field.name)
		}
	}
	if !finiteQVoltageValue(m.MinKvar) || !finiteQVoltageValue(m.MaxKvar) ||
		!finiteQVoltageValue(m.MaxKvar-m.MinKvar) ||
		m.MinKvar > 0 || m.MaxKvar < 0 || m.MinKvar >= m.MaxKvar {
		return fmt.Errorf("min_kvar 和 max_kvar 必须组成包含 0 的有限非空区间")
	}

	type address struct {
		fn  byte
		reg uint16
	}
	type rawRange struct {
		min, max float64
		initial  float64
	}
	seen := make(map[address]string)
	resolve := func(name string, point QVoltagePoint, requiredAccess string) (rawRange, error) {
		if !finiteQVoltageValue(point.Scale) || point.Scale <= 0 {
			return rawRange{}, fmt.Errorf("%s.scale 必须是有限正数", name)
		}
		fn, err := ParseFunction(point.Function)
		if err != nil {
			return rawRange{}, fmt.Errorf("%s.function: %w", name, err)
		}
		if requiredAccess == "readwrite" && fn != 0x03 {
			return rawRange{}, fmt.Errorf("%s.function 必须为 0x03", name)
		}
		if fn != 0x03 && fn != 0x04 {
			return rawRange{}, fmt.Errorf("%s.function 必须为 0x03 或 0x04", name)
		}
		key := address{fn, point.Address}
		if prev, ok := seen[key]; ok {
			return rawRange{}, fmt.Errorf("%s 与 %s 不能引用同一寄存器", prev, name)
		}
		seen[key] = name

		for _, reg := range s.Registers {
			regFn, _ := ParseFunction(reg.Function)
			if regFn != fn || reg.Address != point.Address {
				continue
			}
			access := reg.Access
			if access == "" {
				access = "readonly"
			}
			if access != requiredAccess {
				return rawRange{}, fmt.Errorf("%s 引用的寄存器必须具有 %s 权限", name, requiredAccess)
			}
			switch reg.Source.Kind {
			case "", "const", "manual":
			default:
				return rawRange{}, fmt.Errorf("%s 引用的寄存器必须使用 const 或 manual 来源", name)
			}
			switch reg.Type {
			case "", "UINT16":
				return rawRange{0, 65535, reg.Source.Value}, nil
			case "INT16":
				return rawRange{-32768, 32767, reg.Source.Value}, nil
			default:
				return rawRange{}, fmt.Errorf("%s 引用的寄存器必须为 UINT16 或 INT16", name)
			}
		}
		return rawRange{}, fmt.Errorf("%s 找不到功能码 0x%02X、起始地址 %d 的寄存器", name, fn, point.Address)
	}
	command, err := resolve("command", m.Command, "readwrite")
	if err != nil {
		return err
	}
	measuredQ, err := resolve("measured_q", m.MeasuredQ, "readonly")
	if err != nil {
		return err
	}
	voltage, err := resolve("voltage", m.Voltage, "readonly")
	if err != nil {
		return err
	}

	if math.Trunc(command.initial) != command.initial || command.initial < command.min || command.initial > command.max {
		return fmt.Errorf("command 初值必须是 %g..%g 之间的原始整数", command.min, command.max)
	}
	if m.MinKvar < command.min*m.Command.Scale || m.MaxKvar > command.max*m.Command.Scale {
		return fmt.Errorf("command 可表示的无功范围必须覆盖 min_kvar..max_kvar")
	}
	if m.MinKvar < measuredQ.min*m.MeasuredQ.Scale || m.MaxKvar > measuredQ.max*m.MeasuredQ.Scale {
		return fmt.Errorf("measured_q 寄存器无法表示配置的无功范围")
	}
	minVoltage := m.BaseVoltageV + m.VoltsPerKvar*m.MinKvar
	maxVoltage := m.BaseVoltageV + m.VoltsPerKvar*m.MaxKvar
	if minVoltage <= 0 || math.Round(minVoltage/m.Voltage.Scale) == 0 {
		return fmt.Errorf("voltage 的最低反馈电压及其编码必须大于 0")
	}
	if !finiteQVoltageValue(maxVoltage) || maxVoltage > voltage.max*m.Voltage.Scale {
		return fmt.Errorf("voltage 寄存器无法表示配置的最高反馈电压")
	}
	return nil
}

func finiteQVoltageValue(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
