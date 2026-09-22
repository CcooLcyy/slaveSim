// Package eventbus 提供报文日志的环形缓冲与订阅能力。
//
// 日志既供 WebUI 实时展示，也用于落盘。环形缓冲避免长时间运行后内存无界增长。
package eventbus

import (
	"sync"
	"time"
)

// Direction 是报文方向。
type Direction string

const (
	// DirectionRx 表示收到下位机的请求。
	DirectionRx Direction = "接收"
	// DirectionTx 表示向串口发出响应。
	DirectionTx Direction = "发送"
)

// 日志级别。
const (
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// Event 是一条报文日志。
type Event struct {
	Time      time.Time `json:"time"`
	Level     string    `json:"level"`
	Bus       string    `json:"bus"`
	DeviceID  int       `json:"device_id"`
	Direction Direction `json:"direction"`
	Frame     string    `json:"frame"`
	Summary   string    `json:"summary"`
}

// Log 是日志环形缓冲与订阅中心。
type Log struct {
	mu       sync.RWMutex
	ring     []Event
	next     int
	full     bool
	subs     map[int]chan Event
	nextSub  int
}

// NewLog 创建容量为 capacity 条的日志缓冲。
func NewLog(capacity int) *Log {
	if capacity <= 0 {
		capacity = 500
	}
	return &Log{
		ring: make([]Event, capacity),
		subs: map[int]chan Event{},
	}
}

// Publish 写入一条日志并推送给订阅者。
func (l *Log) Publish(e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	if e.Level == "" {
		e.Level = LevelInfo
	}

	l.mu.Lock()
	l.ring[l.next] = e
	l.next = (l.next + 1) % len(l.ring)
	if l.next == 0 {
		l.full = true
	}
	subs := make([]chan Event, 0, len(l.subs))
	for _, ch := range l.subs {
		subs = append(subs, ch)
	}
	l.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- e:
		default:
			// 订阅者处理不过来时丢弃这一条，不影响主流程。
		}
	}
}

// Recent 按时间顺序返回缓冲区中的全部日志。
func (l *Log) Recent() []Event {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if !l.full {
		out := make([]Event, l.next)
		copy(out, l.ring[:l.next])
		return out
	}
	out := make([]Event, 0, len(l.ring))
	out = append(out, l.ring[l.next:]...)
	out = append(out, l.ring[:l.next]...)
	return out
}

// Subscribe 订阅后续日志，返回接收通道与取消函数。
func (l *Log) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 128)

	l.mu.Lock()
	id := l.nextSub
	l.nextSub++
	l.subs[id] = ch
	l.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			l.mu.Lock()
			delete(l.subs, id)
			l.mu.Unlock()
			close(ch)
		})
	}
	return ch, cancel
}
