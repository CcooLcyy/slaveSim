// Package config 定义 slaveSim 的配置文件结构与校验规则。
package config

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config 是配置文件的根结构。
type Config struct {
	Buses []Bus `json:"buses"`
}

// Bus 表示一条虚拟串口总线，对应一个 PTY 从端设备。
type Bus struct {
	// Name 是总线名称，例如 RS485-1，仅用于日志与界面展示。
	Name string `json:"name"`
	// Link 是对外暴露的稳定软链接路径，例如 /srv/serial-sim/tty0。
	// 下位机的 serial.device 就填这个路径。
	Link string `json:"link"`
	// Slaves 是挂在该总线上的一组从站。
	Slaves []Slave `json:"slaves"`
}

// Slave 表示一个 Modbus RTU 从站（一个站号）。
type Slave struct {
	// DeviceID 是站号，取值 1..247。
	DeviceID uint8 `json:"device_id"`
	// ResponseDelayMs 是人为增加的响应延迟，用于模拟慢设备，0 表示不延迟。
	ResponseDelayMs int `json:"response_delay_ms"`
	// UnmappedPolicy 决定读到未配置地址时的行为：
	//   exception（默认）：返回异常码 0x02，对齐真实设备；
	//   zero：返回 0，用于容忍下位机读整段区间。
	UnmappedPolicy string `json:"unmapped_policy"`
	// Registers 是该从站对外提供的寄存器。
	Registers []Register `json:"registers"`
}

// Register 描述一个可读写的数据项。
type Register struct {
	// Function 是功能码字符串，例如 "0x03"、"0x04"、"0x01"。
	Function string `json:"function"`
	// Address 是协议地址。
	Address uint16 `json:"address"`
	// Type 是数据类型：UINT16 / INT16 / UINT32 / INT32 / BOOL。
	Type string `json:"type"`
	// WordOrder 是 32 位拼接字序：HL（默认，高字在前）或 LH。
	WordOrder string `json:"word_order"`
	// ByteOrder 是 16 位字节序：AB（默认）或 BA。
	ByteOrder string `json:"byte_order"`
	// BitIndex 仅 BOOL 使用，表示取第几位，默认 0。
	BitIndex uint8 `json:"bit_index"`
	// Access 是访问权限：readonly（默认）或 readwrite。
	Access string `json:"access"`
	// Source 是取值来源。
	Source Source `json:"source"`
	// Tag 是可选的业务点名，仅用于界面与日志展示。
	Tag string `json:"tag"`
	// Unit 是可选的工程量单位，仅用于展示。
	Unit string `json:"unit"`
}

// Source 描述寄存器取值的来源。
//
// 各字段按 Kind 取用：const/manual 用 Value；sine 用 Base/Amplitude/PeriodMs；
// ramp 用 Min/Max/PeriodMs；random 用 Min/Max/PeriodMs。
type Source struct {
	// Kind 取值 const / manual / sine / ramp / random。
	Kind      string  `json:"kind"`
	Value     float64 `json:"value"`
	Base      float64 `json:"base"`
	Amplitude float64 `json:"amplitude"`
	PeriodMs  int     `json:"period_ms"`
	Min       float64 `json:"min"`
	Max       float64 `json:"max"`
}

// ParseFunction 把 "0x03" / "3" 这类写法解析为功能码字节。
func ParseFunction(s string) (byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("功能码不能为空")
	}
	v, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(s), "0x"), 16, 8)
	if err != nil {
		return 0, fmt.Errorf("功能码 %q 解析失败: %w", s, err)
	}
	return byte(v), nil
}

// Load 读取并校验配置文件。
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件 %s 失败: %w", path, err)
	}

	cfg, err := Decode(strings.NewReader(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("解析配置文件 %s 失败: %w", path, err)
	}
	return cfg, nil
}

// Decode 严格解析唯一一个配置对象，不接受未知字段或尾随内容。
func Decode(reader io.Reader) (*Config, error) {
	var cfg *Config
	dec := json.NewDecoder(reader)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("配置 JSON 解析失败: %w", err)
	}
	if cfg == nil {
		return nil, fmt.Errorf("配置必须是 JSON 对象")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("配置末尾包含多余内容")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Clone 返回独立配置，避免调用者修改正在使用的嵌套切片。
func (c *Config) Clone() *Config {
	copyConfig := &Config{Buses: make([]Bus, len(c.Buses))}
	for i, b := range c.Buses {
		copyConfig.Buses[i] = b
		copyConfig.Buses[i].Slaves = make([]Slave, len(b.Slaves))
		for j, s := range b.Slaves {
			copyConfig.Buses[i].Slaves[j] = s
			copyConfig.Buses[i].Slaves[j].Registers = append([]Register(nil), s.Registers...)
		}
	}
	return copyConfig
}

// Save 使用同目录临时文件原子替换，写入失败时保留原文件。
func Save(path string, cfg *Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("编码配置失败: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".slavesim-config-*.tmp")
	if err != nil {
		return fmt.Errorf("创建配置临时文件失败: %w", err)
	}
	defer os.Remove(f.Name())
	mode := os.FileMode(0644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		f.Close()
		return fmt.Errorf("读取配置文件权限失败: %w", err)
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return fmt.Errorf("设置配置文件权限失败: %w", err)
	}
	if _, err := f.Write(append(raw, '\n')); err != nil {
		f.Close()
		return fmt.Errorf("写入配置文件失败: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("同步配置文件失败: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("关闭配置文件失败: %w", err)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("替换配置文件失败（请确认配置目录可写且未使用单文件挂载）: %w", err)
	}
	return nil
}

// Validate 校验配置的完整性与取值范围。
func (c *Config) Validate() error {
	if len(c.Buses) == 0 {
		return fmt.Errorf("配置中没有任何总线")
	}
	names, links := map[string]bool{}, map[string]bool{}
	for i := range c.Buses {
		b := &c.Buses[i]
		b.Name = strings.TrimSpace(b.Name)
		b.Link = strings.TrimSpace(b.Link)
		if b.Name == "" {
			return fmt.Errorf("第 %d 条总线缺少 name", i+1)
		}
		if b.Link == "" {
			return fmt.Errorf("总线 %s 缺少 link", b.Name)
		}
		if !filepath.IsAbs(b.Link) {
			return fmt.Errorf("总线 %s 的串口路径必须是绝对路径", b.Name)
		}
		b.Link = filepath.Clean(b.Link)
		if names[b.Name] {
			return fmt.Errorf("总线名称 %s 重复", b.Name)
		}
		if links[b.Link] {
			return fmt.Errorf("串口路径 %s 重复", b.Link)
		}
		names[b.Name], links[b.Link] = true, true
		if len(b.Slaves) == 0 {
			return fmt.Errorf("总线 %s 没有配置任何从站", b.Name)
		}

		seenID := map[uint8]bool{}
		for j := range b.Slaves {
			s := &b.Slaves[j]
			if s.DeviceID == 0 || s.DeviceID > 247 {
				return fmt.Errorf("总线 %s 第 %d 个从站的站号 %d 非法，必须在 1..247 之间", b.Name, j+1, s.DeviceID)
			}
			if seenID[s.DeviceID] {
				return fmt.Errorf("总线 %s 中站号 %d 重复", b.Name, s.DeviceID)
			}
			seenID[s.DeviceID] = true
			if s.ResponseDelayMs < 0 || s.ResponseDelayMs > 60000 {
				return fmt.Errorf("总线 %s 站号 %d 响应延迟必须在 0..60000 毫秒之间", b.Name, s.DeviceID)
			}

			switch s.UnmappedPolicy {
			case "", "exception", "zero":
			default:
				return fmt.Errorf("总线 %s 站号 %d 的 unmapped_policy %q 非法，只能是 exception 或 zero", b.Name, s.DeviceID, s.UnmappedPolicy)
			}

			occupied := map[string]bool{}
			for k := range s.Registers {
				if err := s.Registers[k].Validate(); err != nil {
					return fmt.Errorf("总线 %s 站号 %d 第 %d 个寄存器: %w", b.Name, s.DeviceID, k+1, err)
				}
				r := s.Registers[k]
				fn, _ := ParseFunction(r.Function)
				area := "holding"
				if fn == 1 || fn == 5 {
					area = "coil"
				}
				if fn == 4 {
					area = "input"
				}
				count := 1
				if r.Type == "UINT32" || r.Type == "INT32" {
					count = 2
				}
				for offset := 0; offset < count; offset++ {
					key := fmt.Sprintf("%s:%d", area, int(r.Address)+offset)
					if occupied[key] {
						return fmt.Errorf("总线 %s 站号 %d 地址 %d 在同一地址空间中重叠", b.Name, s.DeviceID, int(r.Address)+offset)
					}
					occupied[key] = true
				}
			}
		}
	}
	return nil
}

// Validate 校验单个寄存器配置。
func (r *Register) Validate() error {
	r.Type = strings.ToUpper(strings.TrimSpace(r.Type))
	r.Access = strings.ToLower(strings.TrimSpace(r.Access))
	r.WordOrder = strings.ToUpper(strings.TrimSpace(r.WordOrder))
	r.ByteOrder = strings.ToUpper(strings.TrimSpace(r.ByteOrder))
	r.Source.Kind = strings.ToLower(strings.TrimSpace(r.Source.Kind))
	fn, err := ParseFunction(r.Function)
	if err != nil {
		return err
	}
	switch fn {
	case 0x01, 0x03, 0x04, 0x05, 0x06, 0x10:
	default:
		return fmt.Errorf("功能码 0x%02X 不受支持", fn)
	}

	switch strings.ToUpper(r.Type) {
	case "", "UINT16":
	case "INT16", "UINT32", "INT32", "BOOL":
	default:
		return fmt.Errorf("数据类型 %q 不受支持", r.Type)
	}

	switch strings.ToUpper(r.Access) {
	case "", "READONLY", "READWRITE":
	default:
		return fmt.Errorf("访问权限 %q 非法，只能是 readonly 或 readwrite", r.Access)
	}

	switch strings.ToUpper(r.WordOrder) {
	case "", "HL", "LH":
	default:
		return fmt.Errorf("字序 %q 非法，只能是 HL 或 LH", r.WordOrder)
	}

	switch strings.ToUpper(r.ByteOrder) {
	case "", "AB", "BA":
	default:
		return fmt.Errorf("字节序 %q 非法，只能是 AB 或 BA", r.ByteOrder)
	}

	switch r.Source.Kind {
	case "", "const", "manual", "sine", "ramp", "random":
	default:
		return fmt.Errorf("取值来源 %q 不受支持", r.Source.Kind)
	}
	if (r.Type == "UINT32" || r.Type == "INT32") && r.Address == 65535 {
		return fmt.Errorf("32 位寄存器地址不能超过 65534")
	}
	if r.Type == "BOOL" && r.BitIndex > 15 {
		return fmt.Errorf("BOOL 位号必须在 0..15 之间")
	}
	if (fn == 1 || fn == 5) && (r.Type == "UINT32" || r.Type == "INT32") {
		return fmt.Errorf("线圈不支持 32 位数据类型")
	}
	if (fn == 1 || fn == 5) && r.Type == "BOOL" && r.BitIndex != 0 {
		return fmt.Errorf("线圈 BOOL 位号必须为 0")
	}
	for _, v := range []float64{r.Source.Value, r.Source.Base, r.Source.Amplitude, r.Source.Min, r.Source.Max} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("取值参数必须是有限数值")
		}
	}
	if r.Source.Kind == "sine" || r.Source.Kind == "ramp" || r.Source.Kind == "random" {
		if r.Source.PeriodMs <= 0 || int64(r.Source.PeriodMs) > 2147483647 {
			return fmt.Errorf("取值周期必须在 1..2147483647 毫秒之间")
		}
	}
	if r.Source.Kind == "sine" && r.Source.Amplitude < 0 {
		return fmt.Errorf("正弦振幅不能为负数")
	}
	if r.Source.Kind == "sine" && (math.IsInf(r.Source.Base+r.Source.Amplitude, 0) || math.IsInf(r.Source.Base-r.Source.Amplitude, 0)) {
		return fmt.Errorf("正弦取值范围超出有限数值范围")
	}
	if (r.Source.Kind == "ramp" || r.Source.Kind == "random") && r.Source.Max < r.Source.Min {
		return fmt.Errorf("取值上限不能小于下限")
	}
	if (r.Source.Kind == "ramp" || r.Source.Kind == "random") && math.IsInf(r.Source.Max-r.Source.Min, 0) {
		return fmt.Errorf("取值上下限差值超出有限数值范围")
	}
	return nil
}
