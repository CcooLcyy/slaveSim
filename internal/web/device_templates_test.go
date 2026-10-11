package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"slavesim/configs/inverters"
	"slavesim/internal/config"
)

func TestDeviceTemplatesCatalogUsesCanonicalPointTables(t *testing.T) {
	s := New(nil, nil)
	handler := s.Handler()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/device-templates", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("读取预置失败: %d %s", w.Code, w.Body.String())
	}
	var catalog []inverters.Template
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 18 {
		t.Fatalf("设备预置数量不符: %d", len(catalog))
	}
	for _, template := range catalog {
		// 接口只返回设备。用户的现有总线与共享PTY路径不会被模板覆盖。
		cfg := config.Config{Buses: []config.Bus{{Name: "现有链路", Link: "/tmp/user-serial", Slaves: []config.Slave{template.Slave}}}}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("%s无法直接保存: %v", template.ID, err)
		}
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/device-templates", nil))
	if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET" {
		t.Fatal("模板接口不应接受修改请求")
	}
}
