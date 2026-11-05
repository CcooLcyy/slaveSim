package config

import (
	"os"
	"path/filepath"
	"testing"
)

func validConfig() Config {
	return Config{Buses: []Bus{{Name: "RS485-1", Link: "/tmp/slavesim-test/tty0", Slaves: []Slave{{DeviceID: 1, Registers: []Register{{Function: "0x03", Address: 100, Type: "UINT16", Source: Source{Kind: "const", Value: 10}}}}}}}}
}

func TestValidateRejectsInvalidConfigurations(t *testing.T) {
	tests := map[string]func(*Config){
		"空白名称": func(c *Config) { c.Buses[0].Name = "  " },
		"重复名称": func(c *Config) { b := c.Buses[0]; b.Link += "-2"; c.Buses = append(c.Buses, b) },
		"规范化重复路径": func(c *Config) {
			b := c.Buses[0]
			b.Name += "-2"
			b.Link = "/tmp/slavesim-test/sub/../tty0"
			c.Buses = append(c.Buses, b)
		},
		"重复站号": func(c *Config) { c.Buses[0].Slaves = append(c.Buses[0].Slaves, c.Buses[0].Slaves[0]) },
		"负延迟":  func(c *Config) { c.Buses[0].Slaves[0].ResponseDelayMs = -1 },
		"共享地址空间重叠": func(c *Config) {
			r := c.Buses[0].Slaves[0].Registers[0]
			r.Function = "0x06"
			c.Buses[0].Slaves[0].Registers = append(c.Buses[0].Slaves[0].Registers, r)
		},
		"32位越界": func(c *Config) {
			c.Buses[0].Slaves[0].Registers[0].Type = "UINT32"
			c.Buses[0].Slaves[0].Registers[0].Address = 65535
		},
		"BOOL位越界": func(c *Config) {
			c.Buses[0].Slaves[0].Registers[0].Type = "BOOL"
			c.Buses[0].Slaves[0].Registers[0].BitIndex = 16
		},
		"正弦周期无效": func(c *Config) { c.Buses[0].Slaves[0].Registers[0].Source = Source{Kind: "sine", PeriodMs: 0} },
		"斜坡边界倒置": func(c *Config) {
			c.Buses[0].Slaves[0].Registers[0].Source = Source{Kind: "ramp", Min: 10, Max: 1, PeriodMs: 10}
		},
		"随机周期负数": func(c *Config) { c.Buses[0].Slaves[0].Registers[0].Source = Source{Kind: "random", PeriodMs: -1} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			c := validConfig()
			mutate(&c)
			if err := c.Validate(); err == nil {
				t.Fatal("非法配置被接受")
			}
		})
	}
}

// 零总线和空总线均可持久化重读，仍须校验已保留总线的名称与路径。
func TestZeroDevicesConfigurationRoundTrip(t *testing.T) {
	for _, c := range []Config{{Buses: []Bus{}}, {Buses: []Bus{{Name: "空总线", Link: "/tmp/slavesim-test/empty", Slaves: []Slave{}}}}} {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := Save(path, &c); err != nil {
			t.Fatal(err)
		}
		got, err := Load(path)
		if err != nil || len(got.Buses) != len(c.Buses) {
			t.Fatalf("零设备配置未能保存重读: %+v, %v", got, err)
		}
		for _, bus := range got.Buses {
			if len(bus.Slaves) != 0 {
				t.Fatal("零设备配置出现额外从站")
			}
		}
	}
	invalid := Config{Buses: []Bus{{Name: "空总线", Link: "relative", Slaves: []Slave{}}}}
	if err := invalid.Validate(); err == nil {
		t.Fatal("空总线的非法串口路径未被拒绝")
	}
}

func TestSaveAndLoadStrictJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	c := validConfig()
	if err := Save(path, &c); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(" {}")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("尾随 JSON 未被拒绝")
	}
}
