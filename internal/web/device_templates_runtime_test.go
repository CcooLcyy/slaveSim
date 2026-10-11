//go:build linux

package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"slavesim/configs/inverters"
	"slavesim/internal/config"
	"slavesim/internal/eventbus"
	"slavesim/internal/sim"
)

// 验证一条既有PTY总线可混合自定义点表、静态预置和多台独立的华为模型。
func TestAddPresetDevicesPreservesBusAndIndependentControls(t *testing.T) {
	c, path := configFixture(t)
	logs := eventbus.NewLog(32)
	m, err := sim.NewManager(path, &c, logs)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	s := New(m, logs)
	handler := s.Handler()
	before := s.Snapshot().Buses[0].SlavePath
	catalog, err := inverters.Load()
	if err != nil {
		t.Fatal(err)
	}
	for id := uint8(2); id <= 3; id++ {
		// JSON round-trip simulates independent browser draft copies.
		raw, err := json.Marshal(catalog[0].Slave)
		if err != nil {
			t.Fatal(err)
		}
		var device config.Slave
		if err := json.Unmarshal(raw, &device); err != nil {
			t.Fatal(err)
		}
		device.DeviceID = id
		c.Buses[0].Slaves = append(c.Buses[0].Slaves, device)
	}
	static := catalog[10].Slave
	static.DeviceID = 4
	c.Buses[0].Slaves = append(c.Buses[0].Slaves, static)
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(raw)))
	if w.Code != http.StatusOK {
		t.Fatalf("混合新增保存失败: %d %s", w.Code, w.Body.String())
	}
	control := httptest.NewRecorder()
	handler.ServeHTTP(control, httptest.NewRequest(http.MethodPost, "/api/manual", bytes.NewBufferString(`{"bus":"RS485-1","device_id":2,"function":"0x03","address":40125,"value":500}`)))
	if control.Code != http.StatusOK {
		t.Fatal("新增华为设备不能接收控制")
	}
	state := s.Snapshot().Buses[0]
	if state.SlavePath != before || len(state.Slaves) != 4 {
		t.Fatal("新增设备更换了既有串口或覆盖了旧从站")
	}
	if state.Slaves[0].Registers[0].Value != 10 || state.Slaves[0].Model != nil || state.Slaves[3].Model != nil {
		t.Fatal("自定义或静态点表被改成了行为模型")
	}
	if state.Slaves[1].Model.TargetActiveKW != 27.5 || state.Slaves[2].Model.TargetActiveKW < 49.99 {
		t.Fatal("多台华为模型的命令互相污染")
	}
	saved, err := config.Load(path)
	if err != nil || len(saved.Buses[0].Slaves) != 4 {
		t.Fatal("新增设备没有持久化配置", err)
	}
}
