package config

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
)

func huaweiTestModel() HuaweiModel {
	return HuaweiModel{Kind: "huawei_v3", DCAvailableKW: 50, GridLineVoltageV: 400, GridFrequencyHZ: 50, AmbientTemperatureC: 25, Efficiency: .98}
}

func huaweiTestConfig(t *testing.T) *Config {
	t.Helper()
	cfg, err := Load("../../configs/inverters/huawei-v3.json")
	if err != nil {
		t.Fatal(err)
	}
	model := huaweiTestModel()
	cfg.Buses[0].Slaves[0].Model = &model
	return cfg
}

func huaweiTestRegister(t *testing.T, cfg *Config, address uint16) *Register {
	t.Helper()
	for i := range cfg.Buses[0].Slaves[0].Registers {
		r := &cfg.Buses[0].Slaves[0].Registers[i]
		if r.Address == address {
			return r
		}
	}
	t.Fatalf("测试点表缺少地址 %d", address)
	return nil
}

// 场景参数接受边界值，拒绝超界、未知模型及所有非有限数值。
func TestHuaweiModelValidate(t *testing.T) {
	fields := []struct {
		name     string
		set      func(*HuaweiModel, float64)
		min, max float64
		openMin  bool
	}{
		{"dc_available_kw", func(m *HuaweiModel, v float64) { m.DCAvailableKW = v }, 0, 100, false},
		{"grid_line_voltage_v", func(m *HuaweiModel, v float64) { m.GridLineVoltageV = v }, 100, 1000, false},
		{"grid_frequency_hz", func(m *HuaweiModel, v float64) { m.GridFrequencyHZ = v }, 40, 70, false},
		{"ambient_temperature_c", func(m *HuaweiModel, v float64) { m.AmbientTemperatureC = v }, -40, 80, false},
		{"efficiency", func(m *HuaweiModel, v float64) { m.Efficiency = v }, 0, 1, true},
	}
	for _, field := range fields {
		t.Run(field.name, func(t *testing.T) {
			for _, v := range []float64{field.min - 1, field.max + 1, math.NaN(), math.Inf(1), math.Inf(-1)} {
				m := huaweiTestModel()
				field.set(&m, v)
				if err := m.Validate(); err == nil {
					t.Fatalf("非法值 %v 被接受", v)
				}
			}
			for _, v := range []float64{field.min, field.max} {
				m := huaweiTestModel()
				field.set(&m, v)
				err := m.Validate()
				if v == field.min && field.openMin {
					if err == nil {
						t.Fatal("零效率被接受")
					}
				} else if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	for _, kind := range []string{"", "Huawei_v3", "other"} {
		m := huaweiTestModel()
		m.Kind = kind
		if err := m.Validate(); err == nil {
			t.Fatalf("模型种类 %q 被接受", kind)
		}
	}
}

// 行为模板电量默认归零，其他点表保持兼容，旧模板无模型且Clone不共享模型指针。
func TestHuaweiTemplateAndClone(t *testing.T) {
	old, err := Load("../../configs/inverters/huawei-v3.json")
	if err != nil {
		t.Fatal(err)
	}
	modeled, err := Load("../../configs/inverters/huawei-v3-model.json")
	if err != nil {
		t.Fatal(err)
	}
	if old.Buses[0].Slaves[0].Model != nil {
		t.Fatal("旧模板启用了模型")
	}
	if modeled.Buses[0].Slaves[0].Model == nil {
		t.Fatal("行为模板缺少模型")
	}
	if !reflect.DeepEqual(*modeled.Buses[0].Slaves[0].Model, huaweiTestModel()) {
		t.Fatal("模型默认值与约定不同")
	}
	clone := modeled.Clone()
	clone.Buses[0].Slaves[0].Model.DCAvailableKW = 10
	if modeled.Buses[0].Slaves[0].Model.DCAvailableKW == 10 {
		t.Fatal("Clone 共享模型指针")
	}
	modeled.Buses[0].Slaves[0].Model = nil
	expected := old.Clone()
	for i := range expected.Buses[0].Slaves[0].Registers {
		r := &expected.Buses[0].Slaves[0].Registers[i]
		if r.Address == 32106 || r.Address == 32114 {
			r.Source.Value = 0
		}
	}
	if !reflect.DeepEqual(expected, modeled) {
		t.Fatal("行为模板除电量初值外改动了原点表")
	}
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"model"`) {
		t.Fatal("无模型配置产生 model 字段")
	}
	// model 内未知字段必须遵循全局严格解码规则。
	raw, err = os.ReadFile("../../configs/inverters/huawei-v3-model.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(strings.NewReader(strings.Replace(string(raw), `"kind": "huawei_v3"`, `"extra": 1, "kind": "huawei_v3"`, 1))); err == nil {
		t.Fatal("未知模型字段被接受")
	}
}

// 映射由地址决定，每个模型映射点必须保留指定类型、0x03、AB/HL 与权限。
func TestHuaweiRequiredMappings(t *testing.T) {
	addresses := []uint16{30071, 30073, 30075, 30077, 30079, 30081, 32000, 32016, 32017, 32018, 32019, 32020, 32021, 32022, 32023, 32064, 32066, 32067, 32068, 32069, 32070, 32071, 32072, 32074, 32076, 32078, 32080, 32082, 32084, 32085, 32086, 32087, 32089, 32106, 32114, 35300, 35301, 35303, 35304, 35305, 35307, 40120, 40122, 40123, 40125, 40126, 42015, 42017, 42019, 43006}
	for _, address := range addresses {
		for _, mutation := range []string{"缺失", "类型", "功能码", "字序", "字节序", "权限"} {
			t.Run(fmt.Sprintf("%d/%s", address, mutation), func(t *testing.T) {
				cfg := huaweiTestConfig(t)
				r := huaweiTestRegister(t, cfg, address)
				switch mutation {
				case "缺失":
					r.Address = 60000
				case "类型":
					if r.Type == "UINT16" {
						r.Type = "INT16"
					} else {
						r.Type = "UINT16"
					}
				case "功能码":
					r.Function = "0x04"
				case "字序":
					r.WordOrder = "LH"
				case "字节序":
					r.ByteOrder = "BA"
				case "权限":
					if r.Access == "readonly" {
						r.Access = "readwrite"
					} else {
						r.Access = "readonly"
					}
				}
				if err := cfg.Validate(); err == nil {
					t.Fatalf("地址 %d 的非法%s被接受", address, mutation)
				}
			})
		}
	}
	cfg := huaweiTestConfig(t)
	for i := range cfg.Buses[0].Slaves[0].Registers {
		cfg.Buses[0].Slaves[0].Registers[i].Tag = "无业务点名"
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("标签影响地址映射: %v", err)
	}
	// 同地址的输入寄存器不会覆盖必需的保持寄存器映射。
	r := *huaweiTestRegister(t, cfg, 30073)
	r.Function = "0x04"
	cfg.Buses[0].Slaves[0].Registers = append(cfg.Buses[0].Slaves[0].Registers, r)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("独立输入地址空间影响模型: %v", err)
	}
}

// 能力和初值必须与模型一致，未实现的限时命令不能静默接受。
func TestHuaweiInvalidInitialValues(t *testing.T) {
	tests := []struct {
		address uint16
		value   float64
	}{
		{30071, 3}, {30071, 4.5}, {30073, 0}, {30075, 0}, {30077, 0}, {30079, 0}, {30081, 0},
		{30079, -100}, {30081, 100}, {30073, 60000}, {30075, 56000}, {30079, 56000}, {30081, -56000},
		{30073, .5}, {30075, 4294967296}, {32106, -1}, {32114, .5}, {32106, 4294967296},
		{40120, 551}, {40126, 55001}, {40123, 1001}, {40123, -1001}, {40125, -1}, {40125, 1001},
		{40122, 0}, {40122, 799}, {40122, -799}, {40122, 1001}, {40122, -1000}, {40122, -1001},
		{42015, 99}, {42015, 1000001}, {42017, 99}, {42017, 1000001}, {42019, 1},
		{43006, -721}, {43006, 841},
	}
	for _, test := range tests {
		cfg := huaweiTestConfig(t)
		huaweiTestRegister(t, cfg, test.address).Source.Value = test.value
		if err := cfg.Validate(); err == nil {
			t.Errorf("地址 %d 非法初值 %v 被接受", test.address, test.value)
		}
	}
	for _, address := range []uint16{30071, 30073, 32106, 40122, 42019, 43006} {
		cfg := huaweiTestConfig(t)
		huaweiTestRegister(t, cfg, address).Source = Source{Kind: "sine", PeriodMs: 1000, Base: 1000}
		if err := cfg.Validate(); err == nil {
			t.Errorf("地址 %d 的动态初值被接受", address)
		}
	}
	for _, pf := range []float64{-999, -800, 800, 1000} {
		cfg := huaweiTestConfig(t)
		huaweiTestRegister(t, cfg, 40122).Source.Value = pf
		if err := cfg.Validate(); err != nil {
			t.Errorf("合法 PF %v 被拒绝: %v", pf, err)
		}
	}
	cfg := huaweiTestConfig(t)
	for _, address := range []uint16{42015, 42017} {
		huaweiTestRegister(t, cfg, address).Source.Value = 1000000
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

// 首版能力与四路PV电压限制保证所有派生遥测都可以按协议类型编码。
func TestHuaweiProfileEncodingLimits(t *testing.T) {
	for _, test := range []struct {
		address uint16
		value   float64
	}{
		{30073, 999}, {30077, 100001}, {30077, 4294967295},
		{32016, 0}, {32018, -1}, {32020, 799}, {32022, 15001},
	} {
		t.Run(fmt.Sprintf("%d/%g", test.address, test.value), func(t *testing.T) {
			cfg := huaweiTestConfig(t)
			huaweiTestRegister(t, cfg, test.address).Source.Value = test.value
			if err := cfg.Validate(); err == nil {
				t.Fatal("会造成不一致或编码钳位的配置被接受")
			}
			// 未启用模型的旧点表仍遵循原寄存器规则。
			cfg.Buses[0].Slaves[0].Model = nil
			if err := cfg.Validate(); err != nil {
				t.Fatalf("模型范围限制影响旧配置: %v", err)
			}
		})
	}
	for _, voltage := range []float64{800, 15000} {
		cfg := huaweiTestConfig(t)
		cfg.Buses[0].Slaves[0].Model.DCAvailableKW = 100
		cfg.Buses[0].Slaves[0].Model.Efficiency = 1
		huaweiTestRegister(t, cfg, 30073).Source.Value = 1000
		huaweiTestRegister(t, cfg, 30077).Source.Value = 100000
		for _, address := range []uint16{32016, 32018, 32020, 32022} {
			huaweiTestRegister(t, cfg, address).Source.Value = voltage
		}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("首版能力/PV边界值被拒绝: %v", err)
		}
	}
}
