package sim

import (
	"fmt"
	"sync"
	"time"

	"slavesim/internal/eventbus"
	"slavesim/internal/modbus"
	"slavesim/internal/pty"
)

const (
	readChunkSize = 512
	// frameIdleTimeout 是残帧判定阈值。Modbus RTU 靠 3.5 个字符时间的静默断帧，
	// PTY 上无法感知真实间隔，因此用固定空闲超时代替。
	frameIdleTimeout = 150 * time.Millisecond
	tickInterval     = 20 * time.Millisecond
)

// Bus 是一条虚拟串口总线，对应一个 PTY 从端设备，可挂多个从站。
type Bus struct {
	Name     string
	LinkPath string

	master *pty.Master
	slaves map[byte]*Slave
	order  []*Slave
	log    *eventbus.Log

	framer   modbus.Framer
	lastByte time.Time

	dataCh chan []byte
	stopCh chan struct{}
	wg     sync.WaitGroup
	once   sync.Once
}

// NewBus 构造一条总线；此时尚未创建 PTY，需要再调用 Open。
func NewBus(name, linkPath string, slaves []*Slave, log *eventbus.Log) *Bus {
	b := &Bus{
		Name:     name,
		LinkPath: linkPath,
		slaves:   map[byte]*Slave{},
		log:      log,
		dataCh:   make(chan []byte, 16),
		stopCh:   make(chan struct{}),
	}
	for _, s := range slaves {
		b.slaves[s.DeviceID] = s
		b.order = append(b.order, s)
	}
	return b
}

// Open 创建 PTY 并建立对外软链接。
func (b *Bus) Open() error {
	m, err := pty.Open(b.LinkPath)
	if err != nil {
		return fmt.Errorf("总线 %s 创建虚拟串口失败: %w", b.Name, err)
	}
	b.master = m
	return nil
}

// SlavePath 返回 PTY 从端的真实设备路径，形如 /dev/pts/3。
func (b *Bus) SlavePath() string {
	if b.master == nil {
		return ""
	}
	return b.master.SlavePath
}

// Slaves 返回该总线上的从站列表。
func (b *Bus) Slaves() []*Slave {
	return b.order
}

// Start 启动读写循环。
func (b *Bus) Start() {
	b.wg.Add(2)
	go b.readLoop()
	go b.runLoop()
}

// Close 停止读写循环并释放 PTY 与软链接。
func (b *Bus) Close() error {
	b.once.Do(func() { close(b.stopCh) })

	var err error
	if b.master != nil {
		// 先关闭主端，让阻塞中的 Read 返回，再等待协程退出。
		err = b.master.Close()
	}
	b.wg.Wait()
	return err
}

func (b *Bus) readLoop() {
	defer b.wg.Done()

	buf := make([]byte, readChunkSize)
	idle := false

	for {
		n, err := b.master.File().Read(buf)
		if n > 0 {
			idle = false
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			select {
			case b.dataCh <- chunk:
			case <-b.stopCh:
				return
			}
		}
		if err != nil {
			select {
			case <-b.stopCh:
				return
			default:
			}

			// 从端全部关闭时，读取主端会返回 EIO。这是**暂时**状态：
			// 有新的程序重新打开从端后即可继续收发，因此绝不能就此退出循环，
			// 否则总线会永久失效，之后所有请求都得不到响应。
			if !idle {
				idle = true
				b.log.Publish(eventbus.Event{
					Level:   eventbus.LevelWarn,
					Bus:     b.Name,
					Summary: fmt.Sprintf("串口读取中断（通常是暂时没有程序打开从端设备）: %v", err),
				})
			}
			time.Sleep(200 * time.Millisecond)
			continue
		}
	}
}

func (b *Bus) runLoop() {
	defer b.wg.Done()

	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	for {
		select {
		case <-b.stopCh:
			return
		case chunk := <-b.dataCh:
			b.framer.Push(chunk)
			b.lastByte = time.Now()
			b.drain()
		case now := <-ticker.C:
			if b.framer.Buffered() > 0 && now.Sub(b.lastByte) > frameIdleTimeout {
				residual := b.framer.Take()
				b.log.Publish(eventbus.Event{
					Level:   eventbus.LevelWarn,
					Bus:     b.Name,
					Frame:   modbus.Hex(residual),
					Summary: fmt.Sprintf("残帧超时（%d 字节）已丢弃", len(residual)),
				})
			}
		}
	}
}

func (b *Bus) drain() {
	for {
		frame, ok := b.framer.Next()
		if !ok {
			return
		}
		b.handleFrame(frame)
	}
}

func (b *Bus) handleFrame(frame []byte) {
	id := frame[0]

	// 广播：对全部从站执行写操作，但按规范不产生任何响应。
	if modbus.IsBroadcast(id) {
		for _, sl := range b.order {
			_ = sl.Handle(frame)
		}
		b.log.Publish(eventbus.Event{
			Level:     eventbus.LevelInfo,
			Bus:       b.Name,
			DeviceID:  int(id),
			Direction: eventbus.DirectionRx,
			Frame:     modbus.Hex(frame),
			Summary:   "广播地址，已对全部从站执行写操作，按规范不响应",
		})
		return
	}

	sl, ok := b.slaves[id]
	if !ok {
		// 站号不属于本站时，真机行为是静默不响应。
		b.log.Publish(eventbus.Event{
			Level:     eventbus.LevelWarn,
			Bus:       b.Name,
			DeviceID:  int(id),
			Direction: eventbus.DirectionRx,
			Frame:     modbus.Hex(frame),
			Summary:   fmt.Sprintf("站号 %d 未配置，已丢弃", id),
		})
		return
	}

	res := sl.Handle(frame)

	summary := res.Summary
	if res.DropReason != "" {
		summary = res.DropReason
	}
	level := eventbus.LevelInfo
	if res.Response == nil {
		level = eventbus.LevelWarn
	}
	b.log.Publish(eventbus.Event{
		Level:     level,
		Bus:       b.Name,
		DeviceID:  int(id),
		Direction: eventbus.DirectionRx,
		Frame:     modbus.Hex(frame),
		Summary:   summary,
	})

	if res.Response == nil {
		return
	}

	if sl.ResponseDelay > 0 {
		time.Sleep(sl.ResponseDelay)
	}

	if _, err := b.master.File().Write(res.Response); err != nil {
		b.log.Publish(eventbus.Event{
			Level:   eventbus.LevelError,
			Bus:     b.Name,
			Summary: fmt.Sprintf("发送响应失败: %v", err),
		})
		return
	}

	b.log.Publish(eventbus.Event{
		Level:     eventbus.LevelInfo,
		Bus:       b.Name,
		DeviceID:  int(id),
		Direction: eventbus.DirectionTx,
		Frame:     modbus.Hex(res.Response),
		Summary:   res.Summary,
	})
}
