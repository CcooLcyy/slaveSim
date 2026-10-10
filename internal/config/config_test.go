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
		"空总线":  func(c *Config) { c.Buses = nil },
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
