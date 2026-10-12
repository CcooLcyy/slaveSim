package inverters

import (
	"testing"
)

func TestHuaweiModelDefaultDCMatchesRatedCapacity(t *testing.T) {
	catalog, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, preset := range catalog {
		if preset.ID != "huawei_v3" {
			continue
		}
		for _, register := range preset.Slave.Registers {
			if register.Address == 30073 {
				if got, want := preset.Slave.Model.DCAvailableKW, register.Source.Value/1000; got != want {
					t.Fatalf("默认太阳直流功率为%g kW，应等于额定容量%g kW", got, want)
				}
				return
			}
		}
		t.Fatal("华为行为模板缺少额定容量")
	}
	t.Fatal("缺少华为行为模板")
}

func TestCatalogIncludesEveryPointTableAndIndependentCopies(t *testing.T) {
	entries, err := files.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != len(entries) {
		t.Fatalf("预置目录有%d个点表，但新增入口仅有%d个", len(entries), len(catalog))
	}
	ids := map[string]bool{}
	for _, preset := range catalog {
		if preset.ID == "" || ids[preset.ID] || preset.Name == "" || len(preset.Slave.Registers) == 0 {
			t.Fatalf("不完整或重复的预置: %+v", preset)
		}
		ids[preset.ID] = true
		if (preset.ID == "huawei_v3") != (preset.Slave.Model != nil) {
			t.Fatal("点表预置不应冒充行为仿真")
		}
	}
	catalog[0].Slave.Model.DCAvailableKW = 0
	catalog[0].Slave.Registers[0].Source.Value = 999
	again, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if again[0].Slave.Model.DCAvailableKW == 0 || again[0].Slave.Registers[0].Source.Value == 999 {
		t.Fatal("修改一次预置污染了后续新增设备")
	}
}
