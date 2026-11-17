package config

import (
	"encoding/json"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func qVoltageTestConfig(t *testing.T) Config {
	t.Helper()
	c := validConfig()
	c.Buses[0].Link = filepath.Join(t.TempDir(), "tty0")
	s := &c.Buses[0].Slaves[0]
	s.Registers[0].Type = " int16 "
	s.Registers[0].Access = " READWRITE "
	s.Registers[0].Source = Source{Kind: " manual "}
	s.Registers = append(s.Registers,
		Register{Function: "0x04", Address: 100, Type: "INT16", Source: Source{Kind: "const"}},
		Register{Function: "0x03", Address: 200}, // 缺省为只读 UINT16，初值来源为 const。
	)
	s.QVoltage = &QVoltageConfig{
		Command:           QVoltagePoint{Function: "0x03", Address: 100, Scale: 0.1},
		MeasuredQ:         QVoltagePoint{Function: "0x04", Address: 100, Scale: 0.1},
		Voltage:           QVoltagePoint{Function: "0x03", Address: 200, Scale: 0.1},
		BaseVoltageV:      230,
		VoltsPerKvar:      0.5,
		MinKvar:           -100,
		MaxKvar:           100,
		RampKvarPerSecond: 20,
	}
	return c
}

func TestQVoltageConfigDecodeAndClone(t *testing.T) {
	c := qVoltageTestConfig(t)
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"q_voltage":`) {
		t.Fatal("配置没有输出 q_voltage")
	}
	withAliases := strings.Replace(string(raw), `"measured_q":{"function":"0x04"`, `"measured_q":{"function":" 4 "`, 1)
	loaded, err := Decode(strings.NewReader(withAliases))
	if err != nil {
		t.Fatal(err)
	}
	got := loaded.Buses[0].Slaves[0].QVoltage
	want := c.Buses[0].Slaves[0].QVoltage
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("无功电压配置保存后改变: 得到 %+v，期望 %+v", got, want)
	}
	clone := loaded.Clone()
	clone.Buses[0].Slaves[0].QVoltage.Command.Scale = 1
	if got.Command.Scale != 0.1 {
		t.Fatal("Clone 共享无功电压配置")
	}
	invalid := strings.Replace(string(raw), `"q_voltage":{`, `"q_voltage":{"unknown":1,`, 1)
	if _, err := Decode(strings.NewReader(invalid)); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("无功电压配置没有拒绝未知字段: %v", err)
	}
	legacy, err := json.Marshal(validConfig())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(legacy), `"q_voltage"`) {
		t.Fatal("未启用模型的配置输出了 q_voltage")
	}
}

func TestQVoltageConfigRejectsInvalidMappingAndBounds(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Slave)
		want   string
	}{
		{"模型互斥", func(s *Slave) { s.Model = &HuaweiModel{} }, "不能同时"},
		{"命令不得引用输入寄存器", func(s *Slave) { s.QVoltage.Command.Function = "0x04" }, "command"},
		{"命令必须配置为读取保持寄存器", func(s *Slave) { s.Registers[0].Function = "0x06" }, "command"},
		{"命令不可只读", func(s *Slave) { s.Registers[0].Access = "" }, "readwrite"},
		{"反馈不可写", func(s *Slave) { s.Registers[1].Access = "readwrite" }, "readonly"},
		{"相同地址空间不能重复", func(s *Slave) { s.QVoltage.Voltage = s.QVoltage.MeasuredQ }, "同一寄存器"},
		{"不得引用双字寄存器的起始地址", func(s *Slave) { s.Registers[1].Type = "INT32" }, "UINT16"},
		{"不得引用双字寄存器的第二字", func(s *Slave) {
			s.Registers[1].Type = "INT32"
			s.QVoltage.MeasuredQ.Address++
		}, "找不到"},
		{"不得使用动态源", func(s *Slave) {
			s.Registers[0].Source = Source{Kind: "ramp", PeriodMs: 100}
		}, "const"},
		{"命令初值必须是整数", func(s *Slave) { s.Registers[0].Source.Value = 0.5 }, "原始整数"},
		{"命令初值必须可编码", func(s *Slave) { s.Registers[0].Source.Value = 40000 }, "原始整数"},
		{"缩放系数必须为正", func(s *Slave) { s.QVoltage.Voltage.Scale = 0 }, "scale"},
		{"斜率必须有限", func(s *Slave) { s.QVoltage.VoltsPerKvar = math.Inf(1) }, "volts_per_kvar"},
		{"反馈速度必须为正", func(s *Slave) { s.QVoltage.RampKvarPerSecond = -1 }, "ramp_kvar_per_second"},
		{"无功区间必须含零", func(s *Slave) { s.QVoltage.MinKvar = 1 }, "min_kvar"},
		{"无功区间必须非空", func(s *Slave) { s.QVoltage.MinKvar, s.QVoltage.MaxKvar = 0, 0 }, "min_kvar"},
		{"命令必须可覆盖无功区间", func(s *Slave) { s.QVoltage.Command.Scale = 0.001 }, "command"},
		{"无符号无功不得填负限", func(s *Slave) { s.Registers[1].Type = "UINT16" }, "measured_q"},
		{"最低电压必须为正", func(s *Slave) { s.QVoltage.BaseVoltageV = 50 }, "voltage"},
		{"最低电压不能读到零", func(s *Slave) { s.QVoltage.Voltage.Scale = 500 }, "voltage"},
		{"最高电压必须可编码", func(s *Slave) { s.QVoltage.Voltage.Scale = 0.001 }, "voltage"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := qVoltageTestConfig(t)
			test.mutate(&c.Buses[0].Slaves[0])
			err := c.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("期望包含 %q 的校验错误，得到 %v", test.want, err)
			}
		})
	}
}
