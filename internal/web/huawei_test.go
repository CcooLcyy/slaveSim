//go:build linux

package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"slavesim/internal/config"
	"slavesim/internal/eventbus"
	"slavesim/internal/sim"
)

// 验证网页控制同样改变设备目标、场景改变可用功率，非法写入不影响设备。
func TestHuaweiWebModelControlAndScenario(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "..", "configs", "inverters", "huawei-v3-model.json"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg.Buses[0].Link = filepath.Join(dir, "tty0")
	log := eventbus.NewLog(32)
	m, err := sim.NewManager(filepath.Join(dir, "config.json"), cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	s := New(m, log)
	handler := s.Handler()
	call := func(path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body)))
		return w
	}
	w := call("/api/manual", `{"bus":"Huawei-V3","device_id":1,"function":"0x03","address":40125,"value":500}`)
	if w.Code != 200 || s.Snapshot().Buses[0].Slaves[0].Model.TargetActiveKW != 27.5 {
		t.Fatalf("网页限功率未联动: %d %s", w.Code, w.Body.String())
	}
	w = call("/api/manual", `{"bus":"Huawei-V3","device_id":1,"function":"0x03","address":32080,"value":100}`)
	if w.Code != 400 {
		t.Fatalf("不应允许手改派生遥测: %d", w.Code)
	}
	w = call("/api/scenario", `{"bus":"Huawei-V3","device_id":1,"scenario":{"dc_available_kw":10,"grid_line_voltage_v":400,"grid_frequency_hz":50,"ambient_temperature_c":25,"efficiency":0.98}}`)
	if w.Code != 200 || s.Snapshot().Buses[0].Slaves[0].Model.TargetActiveKW != 9.8 {
		t.Fatalf("减光场景没有生效: %d %s", w.Code, w.Body.String())
	}
	w = call("/api/scenario", `{"bus":"Huawei-V3","device_id":1,"scenario":{"dc_available_kw":10,"grid_line_voltage_v":0,"grid_frequency_hz":50,"ambient_temperature_c":25,"efficiency":0.98}}`)
	if w.Code != 400 || s.Snapshot().Buses[0].Slaves[0].Model.Scenario.GridLineVoltageV != 400 {
		t.Fatal("非法场景没有原子拒绝")
	}
	w = call("/api/scenario", `{"bus":"不存在","device_id":1,"scenario":{"dc_available_kw":10,"grid_line_voltage_v":400,"grid_frequency_hz":50,"ambient_temperature_c":25,"efficiency":0.98}}`)
	if w.Code != 404 {
		t.Fatal("不存在从站应返回404")
	}
	// 厂家尚未实现的RW参数不能伪装成可用模型控制。
	for _, reg := range s.Snapshot().Buses[0].Slaves[0].Registers {
		if reg.Address == 40196 && reg.Writable {
			t.Fatal("未实现参数应在网页禁写")
		}
	}
}
