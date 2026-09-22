// Package web 提供 slaveSim 的 HTTP 接口与内嵌界面。
//
// 实时日志用 Server-Sent Events 推送而不是 WebSocket：本项目不引入任何第三方依赖，
// 手写 WebSocket 协议得不偿失，而日志推送本来就是单向的，SSE 用标准库即可实现。
package web

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"time"

	"slavesim/internal/eventbus"
	"slavesim/internal/modbus"
	"slavesim/internal/sim"
)

//go:embed static
var staticFiles embed.FS

// RegisterView 是界面上的一个寄存器。
type RegisterView struct {
	Function string  `json:"function"`
	Address  uint16  `json:"address"`
	Type     string  `json:"type"`
	Tag      string  `json:"tag"`
	Unit     string  `json:"unit"`
	Writable bool    `json:"writable"`
	Value    float64 `json:"value"`
	Raw      string  `json:"raw"`
}

// SlaveView 是界面上的一个从站。
type SlaveView struct {
	DeviceID  int            `json:"device_id"`
	Registers []RegisterView `json:"registers"`
}

// BusView 是界面上的一条总线。
type BusView struct {
	Name      string      `json:"name"`
	LinkPath  string      `json:"link_path"`
	SlavePath string      `json:"slave_path"`
	Slaves    []SlaveView `json:"slaves"`
}

// State 是界面轮询的完整运行状态。
type State struct {
	Now   time.Time `json:"now"`
	Buses []BusView `json:"buses"`
}

// Server 是 slaveSim 的 HTTP 服务。
type Server struct {
	buses []*sim.Bus
	log   *eventbus.Log
}

// New 构造 HTTP 服务。
func New(buses []*sim.Bus, log *eventbus.Log) *Server {
	return &Server{buses: buses, log: log}
}

// Handler 返回注册好全部路由的处理器。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/api/manual", s.handleManual)

	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(fmt.Sprintf("内嵌静态资源不可用: %v", err))
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))
	return mux
}

// Snapshot 采集当前状态，供界面展示。
func (s *Server) Snapshot() State {
	now := time.Now()
	st := State{Now: now}

	for _, b := range s.buses {
		bv := BusView{
			Name:      b.Name,
			LinkPath:  b.LinkPath,
			SlavePath: b.SlavePath(),
		}
		for _, sl := range b.Slaves() {
			sv := SlaveView{DeviceID: int(sl.DeviceID)}
			for _, reg := range sl.Registers() {
				raw := reg.RawRegisters(now)
				bytesOut := make([]byte, 0, len(raw)*2)
				for _, v := range raw {
					bytesOut = append(bytesOut, byte(v>>8), byte(v))
				}
				sv.Registers = append(sv.Registers, RegisterView{
					Function: fmt.Sprintf("0x%02X", reg.Function),
					Address:  reg.Address,
					Type:     reg.Type.String(),
					Tag:      reg.Tag,
					Unit:     reg.Unit,
					Writable: reg.Writable,
					Value:    reg.Value(now),
					Raw:      modbus.Hex(bytesOut),
				})
			}
			bv.Slaves = append(bv.Slaves, sv)
		}
		st.Buses = append(st.Buses, bv)
	}
	return st
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Snapshot())
}

// manualRequest 是界面手动设置寄存器数值的请求体。
type manualRequest struct {
	Bus       string  `json:"bus"`
	DeviceID  int     `json:"device_id"`
	Function  string  `json:"function"`
	Address   uint16  `json:"address"`
	Value     float64 `json:"value"`
}

func (s *Server) handleManual(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "只支持 POST"})
		return
	}

	var req manualRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("请求体解析失败: %v", err)})
		return
	}

	fn, err := parseFunction(req.Function)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	for _, b := range s.buses {
		if b.Name != req.Bus {
			continue
		}
		for _, sl := range b.Slaves() {
			if int(sl.DeviceID) != req.DeviceID {
				continue
			}
			reg := sl.Find(fn, req.Address)
			if reg == nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "未找到对应的寄存器"})
				return
			}
			reg.SetManual(req.Value)
			s.log.Publish(eventbus.Event{
				Level:   eventbus.LevelInfo,
				Bus:     b.Name,
				Summary: fmt.Sprintf("界面手动设置: 功能码=0x%02X 地址=%d 值=%v", fn, req.Address, req.Value),
			})
			writeJSON(w, http.StatusOK, map[string]string{"result": "ok"})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "未找到对应的总线或从站"})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "当前连接不支持流式响应"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	ch, cancel := s.log.Subscribe()
	defer cancel()

	for _, e := range s.log.Recent() {
		if err := writeSSE(w, e); err != nil {
			return
		}
	}
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			if err := writeSSE(w, e); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeSSE(w http.ResponseWriter, e eventbus.Event) error {
	payload, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", payload)
	return err
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// parseFunction 解析界面传来的功能码字符串。
func parseFunction(s string) (byte, error) {
	var v uint64
	if _, err := fmt.Sscanf(s, "0x%02X", &v); err == nil {
		return byte(v), nil
	}
	if _, err := fmt.Sscanf(s, "%d", &v); err == nil {
		return byte(v), nil
	}
	return 0, fmt.Errorf("功能码 %q 解析失败", s)
}
