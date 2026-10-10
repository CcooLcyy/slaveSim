//go:build linux

package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"slavesim/internal/config"
	"slavesim/internal/eventbus"
	"slavesim/internal/sim"
)

func configFixture(t *testing.T) (config.Config, string) {
	t.Helper()
	dir := t.TempDir()
	c := config.Config{Buses: []config.Bus{{Name: "RS485-1", Link: filepath.Join(dir, "tty0"), Slaves: []config.Slave{{DeviceID: 1, Registers: []config.Register{{Function: "0x03", Address: 100, Type: "UINT16", Source: config.Source{Kind: "manual", Value: 10}}}}}}}}
	return c, filepath.Join(dir, "config.json")
}

func TestConfigurationAPIAppliesAndPreservesPTY(t *testing.T) {
	c, path := configFixture(t)
	logs := eventbus.NewLog(64)
	if err := config.Save(path, &c); err != nil {
		t.Fatal(err)
	}
	m, err := sim.NewManager(path, &c, logs)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	s := New(m, logs)
	handler := s.Handler()
	before := s.Snapshot().Buses[0].SlavePath
	manual := httptest.NewRecorder()
	handler.ServeHTTP(manual, httptest.NewRequest(http.MethodPost, "/api/manual", bytes.NewBufferString(`{"bus":"RS485-1","device_id":1,"function":"0x03","address":100,"value":999}`)))
	if manual.Code != http.StatusOK || s.Snapshot().Buses[0].Slaves[0].Registers[0].Value != 999 {
		t.Fatal("手动设置失败")
	}
	c.Buses[0].Name = "RS485-改名"
	c.Buses[0].Slaves[0].Registers[0].Source.Value = 55
	b := c.Buses[0]
	b.Name = "RS485-2"
	b.Link += "-2"
	c.Buses = append(c.Buses, b)
	raw, _ := json.Marshal(c)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(raw)))
	if w.Code != http.StatusOK {
		t.Fatalf("保存失败 %d %s", w.Code, w.Body.String())
	}
	st := s.Snapshot()
	if len(st.Buses) != 2 || st.Buses[0].SlavePath != before || st.Buses[0].Slaves[0].Registers[0].Value != 55 {
		t.Fatalf("应用结果错误: %+v", st)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	var got config.Config
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	disk, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, *disk) {
		t.Fatal("GET 与文件不一致")
	}
	removed := c.Buses[1].Link
	c.Buses = c.Buses[:1]
	raw, _ = json.Marshal(c)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(raw)))
	if w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if _, err := os.Lstat(removed); !os.IsNotExist(err) {
		t.Fatalf("删除总线没有释放软链接: %v", err)
	}
}

func TestConfigurationAPICreateFailurePreservesFiles(t *testing.T) {
	c, path := configFixture(t)
	logs := eventbus.NewLog(64)
	if err := config.Save(path, &c); err != nil {
		t.Fatal(err)
	}
	m, err := sim.NewManager(path, &c, logs)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	s := New(m, logs)
	before := s.Snapshot().Buses[0].SlavePath
	b := c.Buses[0]
	b.Name = "RS485-2"
	b.Link += "-occupied"
	if err := os.WriteFile(b.Link, []byte("原有文件"), 0644); err != nil {
		t.Fatal(err)
	}
	c.Buses = append(c.Buses, b)
	raw, _ := json.Marshal(c)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(raw)))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("错误状态 %d %s", w.Code, w.Body.String())
	}
	data, err := os.ReadFile(b.Link)
	if err != nil || string(data) != "原有文件" {
		t.Fatalf("原有文件被改动: %v %q", err, data)
	}
	if len(s.Snapshot().Buses) != 1 || s.Snapshot().Buses[0].SlavePath != before {
		t.Fatal("运行状态被改变")
	}
	disk, err := config.Load(path)
	if err != nil || len(disk.Buses) != 1 {
		t.Fatal("文件被改变")
	}
}

func TestConfigurationAPIConcurrentPollingManualAndApply(t *testing.T) {
	c, path := configFixture(t)
	logs := eventbus.NewLog(64)
	if err := config.Save(path, &c); err != nil {
		t.Fatal(err)
	}
	m, err := sim.NewManager(path, &c, logs)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	s := New(m, logs)
	handler := s.Handler()
	var wg sync.WaitGroup
	for worker := 0; worker < 3; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				w := httptest.NewRecorder()
				switch worker {
				case 0:
					handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/state", nil))
				case 1:
					handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/manual", bytes.NewBufferString(`{"bus":"RS485-1","device_id":1,"function":"0x03","address":100,"value":99}`)))
				case 2:
					next := c.Clone()
					next.Buses[0].Slaves[0].Registers[0].Source.Value = float64(i)
					raw, _ := json.Marshal(next)
					handler.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(raw)))
				}
				if w.Code != http.StatusOK {
					t.Errorf("并发请求失败: %d %s", w.Code, w.Body.String())
				}
			}
		}(worker)
	}
	wg.Wait()
}

func TestConfigurationAPIRejectsWithoutChangingState(t *testing.T) {
	c, path := configFixture(t)
	logs := eventbus.NewLog(64)
	if err := config.Save(path, &c); err != nil {
		t.Fatal(err)
	}
	m, err := sim.NewManager(path, &c, logs)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	s := New(m, logs)
	handler := s.Handler()
	original, _ := os.ReadFile(path)
	before := s.Snapshot().Buses[0].SlavePath
	for _, body := range []string{`{"buses":[]}`, `{"buses":[]} {}`, `{"unknown":true}`, `null`} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewBufferString(body)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("非法请求得到 %d: %s", w.Code, w.Body.String())
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(original, after) || s.Snapshot().Buses[0].SlavePath != before {
			t.Fatal("拒绝配置改变了文件或总线")
		}
	}
	// 配置目标变成目录，原子替换失败；新增 PTY 必须回滚。
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0755); err != nil {
		t.Fatal(err)
	}
	b := c.Buses[0]
	b.Name = "RS485-2"
	b.Link += "-2"
	c.Buses = append(c.Buses, b)
	raw, _ := json.Marshal(c)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(raw)))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("保存失败状态 %d", w.Code)
	}
	if len(s.Snapshot().Buses) != 1 || s.Snapshot().Buses[0].SlavePath != before {
		t.Fatal("保存失败改变了运行状态")
	}
	if _, err := os.Lstat(b.Link); !os.IsNotExist(err) {
		t.Fatal("新增软链接没有回滚")
	}
}
