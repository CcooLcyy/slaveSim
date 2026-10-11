package config

import (
	"fmt"
	"math"
)

// HuaweiModel 描述华为 V3 逆变器的场景输入，功率单位为 kW。
type HuaweiModel struct {
	Kind                string  `json:"kind"`
	DCAvailableKW       float64 `json:"dc_available_kw"`
	GridLineVoltageV    float64 `json:"grid_line_voltage_v"`
	GridFrequencyHZ     float64 `json:"grid_frequency_hz"`
	AmbientTemperatureC float64 `json:"ambient_temperature_c"`
	Efficiency          float64 `json:"efficiency"`
}

// Validate 校验模型类别以及全部场景参数的有限数值和物理范围。
func (m HuaweiModel) Validate() error {
	if m.Kind != "huawei_v3" {
		return fmt.Errorf("模型 kind %q 不受支持，只能是 huawei_v3", m.Kind)
	}
	for _, field := range []struct {
		name            string
		value, min, max float64
		openMin         bool
	}{
		{"dc_available_kw", m.DCAvailableKW, 0, 100, false},
		{"grid_line_voltage_v", m.GridLineVoltageV, 100, 1000, false},
		{"grid_frequency_hz", m.GridFrequencyHZ, 40, 70, false},
		{"ambient_temperature_c", m.AmbientTemperatureC, -40, 80, false},
		{"efficiency", m.Efficiency, 0, 1, true},
	} {
		if math.IsNaN(field.value) || math.IsInf(field.value, 0) {
			return fmt.Errorf("%s 必须是有限数值", field.name)
		}
		if field.value < field.min || field.value > field.max || (field.openMin && field.value == field.min) {
			if field.openMin {
				return fmt.Errorf("%s 必须大于 %g 且不超过 %g", field.name, field.min, field.max)
			}
			return fmt.Errorf("%s 必须在 %g..%g 之间", field.name, field.min, field.max)
		}
	}
	return nil
}

// ValidateModel 以协议地址校验行为模型映射，无模型的从站沿用普通寄存器行为。
func (s Slave) ValidateModel() error {
	if s.Model == nil {
		return nil
	}
	if err := s.Model.Validate(); err != nil {
		return err
	}
	registers := make(map[uint16]*Register, len(s.Registers))
	for i := range s.Registers {
		fn, _ := ParseFunction(s.Registers[i].Function)
		if fn == 3 {
			registers[s.Registers[i].Address] = &s.Registers[i]
		}
	}
	values := map[uint16]float64{}
	groups := []struct {
		typ, access string
		addresses   []uint16
	}{
		{"UINT16", "readonly", []uint16{30071, 32000, 32066, 32067, 32068, 32069, 32070, 32071, 32085, 32086, 32089, 35300, 35303, 35304, 35307}},
		{"INT16", "readonly", []uint16{32016, 32017, 32018, 32019, 32020, 32021, 32022, 32023, 32084, 32087}},
		{"UINT32", "readonly", []uint16{30073, 30075, 30077, 32106, 32114, 35301, 35305}},
		{"INT32", "readonly", []uint16{30079, 30081, 32064, 32072, 32074, 32076, 32078, 32080, 32082}},
		{"UINT16", "readwrite", []uint16{40120, 40125}},
		{"INT16", "readwrite", []uint16{40122, 40123, 43006}},
		{"UINT32", "readwrite", []uint16{40126, 42015, 42017, 42019}},
	}
	for _, group := range groups {
		for _, address := range group.addresses {
			r, ok := registers[address]
			if !ok {
				return fmt.Errorf("缺少必需的寄存器 %d", address)
			}
			fn, _ := ParseFunction(r.Function)
			if fn != 3 {
				return fmt.Errorf("寄存器 %d 必须使用 0x03 功能码", address)
			}
			if r.Type != group.typ {
				return fmt.Errorf("寄存器 %d 必须使用 %s 数据类型", address, group.typ)
			}
			if (r.ByteOrder != "" && r.ByteOrder != "AB") || (r.WordOrder != "" && r.WordOrder != "HL") {
				return fmt.Errorf("寄存器 %d 必须使用 AB 字节序和 HL 字序", address)
			}
			access := r.Access
			if access == "" {
				access = "readonly"
			}
			if access != group.access {
				return fmt.Errorf("寄存器 %d 必须具有 %s 权限", address, group.access)
			}
			if r.Source.Kind != "" && r.Source.Kind != "const" && r.Source.Kind != "manual" {
				return fmt.Errorf("寄存器 %d 的模型初值必须使用 const 或 manual 来源", address)
			}
			v := r.Source.Value
			min, max := float64(0), float64(math.MaxUint16)
			switch r.Type {
			case "INT16":
				min, max = math.MinInt16, math.MaxInt16
			case "UINT32":
				max = math.MaxUint32
			case "INT32":
				min, max = math.MinInt32, math.MaxInt32
			}
			if math.Trunc(v) != v || v < min || v > max {
				return fmt.Errorf("寄存器 %d 的初值必须是 %s 范围内的整数", address, r.Type)
			}
			values[address] = v
		}
	}
	if values[30071] != 4 {
		return fmt.Errorf("组串个数 30071 必须为 4")
	}
	pn, pmax, smax := values[30073], values[30075], values[30077]
	if pn < 1000 || pmax < pn || smax < pmax || smax > 100000 {
		return fmt.Errorf("首版功率能力必须满足 1 <= Pn <= Pmax <= Smax <= 100（kW/kVA）")
	}
	// 四路均分最大100kW直流功率时，80V下的312.5A仍可使用INT16及100倍率编码。
	for _, address := range []uint16{32016, 32018, 32020, 32022} {
		if values[address] < 800 || values[address] > 15000 {
			return fmt.Errorf("PV电压初值 %d 必须在 800..15000 之间（80..1500V）", address)
		}
	}
	if values[30079] <= 0 || values[30079] > smax || values[30081] >= 0 || -values[30081] > smax {
		return fmt.Errorf("无功馈入能力必须为正、吸收能力必须为负，幅值不能超过 Smax")
	}
	if values[32114] > values[32106] {
		return fmt.Errorf("当日发电量 32114 不能超过累计发电量 32106")
	}
	if values[32078] < 0 || values[32078] > pmax {
		return fmt.Errorf("当天峰值有功功率 32078 必须在 0..Pmax 之间")
	}
	if values[40120]*100 > pmax || values[40126] > pmax {
		return fmt.Errorf("有功固定限值 40120/40126 不能超过 Pmax")
	}
	if values[40125] > 1000 {
		return fmt.Errorf("有功百分比限值 40125 必须在 0..1000 之间")
	}
	pf := values[40122]
	if !((pf >= 800 && pf <= 1000) || (pf > -1000 && pf <= -800)) {
		return fmt.Errorf("功率因数初值 40122 必须大于 -1000 且不超过 -800，或在 800..1000 之间")
	}
	if values[40123] < -1000 || values[40123] > 1000 {
		return fmt.Errorf("Q/S 初值 40123 必须在 -1000..1000 之间")
	}
	for _, address := range []uint16{42015, 42017} {
		if values[address] < 100 || values[address] > 1000000 {
			return fmt.Errorf("梯度初值 %d 必须在 100..1000000 之间", address)
		}
	}
	if values[42019] != 0 {
		return fmt.Errorf("尚未实现调度指令时限，42019 初值必须为 0")
	}
	if values[43006] < -720 || values[43006] > 840 {
		return fmt.Errorf("时区初值 43006 必须在 -720..840 分钟之间")
	}
	return nil
}
