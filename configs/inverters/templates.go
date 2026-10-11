// Package inverters 将仓库点表预置直接嵌入程序，网页新增设备无需另行安装配置文件。
package inverters

import (
	"bytes"
	"embed"
	"fmt"

	"slavesim/internal/config"
)

//go:embed *.json
var files embed.FS

// Template 只提供从站配置；总线名称、PTY 路径和站号由用户当前草稿决定。
type Template struct {
	ID    string       `json:"id"`
	Name  string       `json:"name"`
	Slave config.Slave `json:"slave"`
}

var definitions = []struct{ id, name, file string }{
	{"huawei_v3", "华为逆变器（V3，行为仿真）", "huawei-v3-model.json"},
	{"huawei_v3_static", "华为逆变器（V3，静态点表）", "huawei-v3.json"},
	{"aotai-hf-tl", "奥太 HF/TL（遥测点表）", "aotai-hf-tl-telemetry.json"},
	{"aotai-tld", "奥太 TLD（遥测点表）", "aotai-tld-telemetry.json"},
	{"aotai-tlc10-30", "奥太 TLC 10–30 kW（遥测点表）", "aotai-tlc10-30-telemetry.json"},
	{"aotai-asp250", "奥太 ASP250（遥测点表）", "aotai-asp250-telemetry.json"},
	{"aotai-asp500", "奥太 ASP500（遥测点表）", "aotai-asp500-telemetry.json"},
	{"aotai-asp630", "奥太 ASP630（遥测点表）", "aotai-asp630-telemetry.json"},
	{"aotai-controls-common", "奥太公共控制（独立控制点表）", "aotai-controls-common.json"},
	{"aotai-controls-large", "奥太大功率控制（独立控制点表）", "aotai-controls-large.json"},
	{"growatt-tlx-min", "古瑞瓦特 TL-X / MIN（点表预置）", "growatt-tlx-min.json"},
	{"growatt-tl3x-mid-mac", "古瑞瓦特 TL3-X / MID / MAC（点表预置）", "growatt-tl3x-mid-mac.json"},
	{"growatt-tl3x-max", "古瑞瓦特 TL3-X / MAX（点表预置）", "growatt-tl3x-max.json"},
	{"goodwe-mtg2", "固德威 MT G2（点表预置）", "goodwe-mtg2.json"},
	{"goodwe-smt50-60k", "固德威 SMT 50/60 kW（点表预置）", "goodwe-smt50-60k.json"},
	{"goodwe-sdtg2", "固德威 SDT G2（点表预置）", "goodwe-sdtg2.json"},
	{"goodwe-single-mppt", "固德威单相单路 MPPT（点表预置）", "goodwe-single-mppt.json"},
	{"goodwe-single-dual-mppt", "固德威单相双路 MPPT（点表预置）", "goodwe-single-dual-mppt.json"},
}

// Load 每次解析出独立副本，避免客户端修改预置或多台设备共享模型状态。
func Load() ([]Template, error) {
	templates := make([]Template, 0, len(definitions))
	for _, def := range definitions {
		raw, err := files.ReadFile(def.file)
		if err != nil {
			return nil, fmt.Errorf("读取设备预置 %s: %w", def.id, err)
		}
		cfg, err := config.Decode(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("解析设备预置 %s: %w", def.id, err)
		}
		if len(cfg.Buses) != 1 || len(cfg.Buses[0].Slaves) != 1 {
			return nil, fmt.Errorf("设备预置 %s 必须包含一台设备", def.id)
		}
		templates = append(templates, Template{def.id, def.name, cfg.Buses[0].Slaves[0]})
	}
	return templates, nil
}
