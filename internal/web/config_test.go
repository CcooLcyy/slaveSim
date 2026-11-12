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

func TestStationResponseAPIChangesOnlyTargetRuntimeState(t *testing.T) {
	c, path := configFixture(t)
	c.Buses[0].Slaves = append(c.Buses[0].Slaves, config.Slave{DeviceID: 2})
	logs := eventbus.NewLog(64)
	m, err := sim.NewManager(path, &c, logs)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	before := m.Config()
	handler := New(m, logs).Handler()
	request := func(method, endpoint, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(method, endpoint, bytes.NewBufferString(body)))
		return w
	}
	state := func() (string, [2]bool) {
		w := request(http.MethodGet, "/api/state", "")
		var got struct {
			Buses []struct {
				SlavePath string `json:"slave_path"`
				Slaves    []struct {
					ResponsePaused *bool `json:"response_paused"`
				} `json:"slaves"`
			} `json:"buses"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusOK || len(got.Buses) != 1 || got.Buses[0].SlavePath == "" || len(got.Buses[0].Slaves) != 2 {
			t.Fatalf("总线运行状态异常: %d %s", w.Code, w.Body.String())
		}
		slaves := got.Buses[0].Slaves
		if slaves[0].ResponsePaused == nil || slaves[1].ResponsePaused == nil {
			t.Fatalf("响应缺少从站应答状态: %s", w.Body.String())
		}
		return got.Buses[0].SlavePath, [2]bool{*slaves[0].ResponsePaused, *slaves[1].ResponsePaused}
	}
	ptyPath, initial := state()
	if initial != [2]bool{false, false} {
		t.Fatalf("从站应默认应答: %v", initial)
	}
	const endpoint = "/api/station-response"
	for _, tc := range []struct {
		method, body string
		status       int
	}{
		{http.MethodGet, "", http.StatusMethodNotAllowed},
		{http.MethodPost, `{"bus":"","device_id":1,"paused":true}`, http.StatusBadRequest},
		{http.MethodPost, `{"bus":"RS485-1","device_id":257,"paused":true}`, http.StatusBadRequest},
		{http.MethodPost, `{"bus":"RS485-1","device_id":1}`, http.StatusBadRequest},
		{http.MethodPost, `{"bus":"RS485-1","device_id":1,"paused":null}`, http.StatusBadRequest},
		{http.MethodPost, `{"bus":"其他总线","device_id":1,"paused":true}`, http.StatusNotFound},
		{http.MethodPost, `{"bus":"RS485-1","device_id":3,"paused":true}`, http.StatusNotFound},
	} {
		if w := request(tc.method, endpoint, tc.body); w.Code != tc.status {
			t.Errorf("请求 %s %s 的状态码为 %d，期望 %d: %s", tc.method, tc.body, w.Code, tc.status, w.Body.String())
		}
	}
	if currentPTY, paused := state(); currentPTY != ptyPath || paused != initial {
		t.Fatalf("无效请求改变了总线或从站状态: %s %v", currentPTY, paused)
	}
	for _, tc := range []struct {
		body   string
		paused bool
	}{
		{`{"bus":"RS485-1","device_id":1,"paused":true}`, true},
		{`{"bus":"RS485-1","device_id":1,"paused":true}`, true},
		{`{"bus":"RS485-1","device_id":1,"paused":false}`, false},
	} {
		w := request(http.MethodPost, endpoint, tc.body)
		var reply map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || w.Code != http.StatusOK || reply["result"] != "ok" {
			t.Fatalf("切换从站应答失败: %d %s", w.Code, w.Body.String())
		}
		if currentPTY, paused := state(); currentPTY != ptyPath || paused != [2]bool{tc.paused, false} {
			t.Fatalf("总线或从站状态异常: %s %v", currentPTY, paused)
		}
		if !reflect.DeepEqual(before, m.Config()) {
			t.Fatal("切换应答改变了持久配置")
		}
	}
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
	for _, body := range []string{`{"buses":[]} {}`, `{"unknown":true}`, `null`} {
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

// 删除全部设备或全部总线后实际状态为零，落盘重启仍为零，再添加设备恢复运行。
func TestConfigurationAPIClearDevicesAndCreateAgain(t *testing.T) {
	for _, keepBus := range []bool{true, false} {
		t.Run(map[bool]string{true: "保留空总线", false: "删除所有总线"}[keepBus], func(t *testing.T) {
			c, path := configFixture(t)
			logs := eventbus.NewLog(64)
			m, err := sim.NewManager(path, &c, logs)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = m.Close() })
			cleared := c.Clone()
			if keepBus {
				cleared.Buses[0].Slaves = []config.Slave{}
			} else {
				cleared.Buses = []config.Bus{}
			}
			raw, _ := json.Marshal(cleared)
			w := httptest.NewRecorder()
			s := New(m, logs)
			s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(raw)))
			if w.Code != http.StatusOK {
				t.Fatalf("清空设备失败: %d %s", w.Code, w.Body.String())
			}
			state := s.Snapshot()
			if len(state.Buses) != len(cleared.Buses) {
				t.Fatal("清空配置未应用到运行总线")
			}
			for _, bus := range state.Buses {
				if len(bus.Slaves) != 0 {
					t.Fatal("运行状态仍存在已删除设备")
				}
			}
			if !keepBus {
				if _, err := os.Lstat(c.Buses[0].Link); !os.IsNotExist(err) {
					t.Fatalf("零总线未释放旧PTY路径: %v", err)
				}
			}
			disk, err := config.Load(path)
			if err != nil || !reflect.DeepEqual(disk, cleared) {
				t.Fatalf("清空配置未持久化: %+v, %v", disk, err)
			}
			if err := m.Close(); err != nil {
				t.Fatal(err)
			}
			restarted, err := sim.NewManager(path, disk, logs)
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			if err := restarted.Apply(&c); err != nil {
				t.Fatalf("零设备状态不能重新添加设备: %v", err)
			}
			if state := New(restarted, logs).Snapshot(); len(state.Buses) != 1 || len(state.Buses[0].Slaves) != 1 {
				t.Fatal("重新添加设备后运行状态错误")
			}
		})
	}
}
