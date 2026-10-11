package sim

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"slavesim/internal/config"
	"slavesim/internal/eventbus"
)

// Manager 串行应用配置，保护 Web 查询、手动设值与运行资源的切换。
type Manager struct {
	mu     sync.RWMutex
	path   string
	cfg    *config.Config
	buses  []*Bus
	log    *eventbus.Log
	closed bool
	stop   chan struct{}
	wg     sync.WaitGroup
}

func NewManager(path string, cfg *config.Config, logs *eventbus.Log) (*Manager, error) {
	cfg = cfg.Clone()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	m := &Manager{path: path, log: logs, stop: make(chan struct{})}
	if err := m.apply(cfg, false); err != nil {
		return nil, err
	}
	m.wg.Add(1)
	go m.runModels()
	return m, nil
}

// runModels 使用独立时钟驱动设备，即使总线等待回应或无人轮询也持续演算。
func (m *Manager) runModels() {
	defer m.wg.Done()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-m.stop:
			return
		case now := <-ticker.C:
			m.WithBuses(func(buses []*Bus) {
				for _, b := range buses {
					for _, sl := range b.Slaves() {
						if summary := sl.Step(now); summary != "" {
							m.log.Publish(eventbus.Event{Level: eventbus.LevelInfo, Bus: b.name(), DeviceID: int(sl.DeviceID), Summary: summary})
						}
					}
				}
			})
		}
	}
}

func (m *Manager) Config() *config.Config { m.mu.RLock(); defer m.mu.RUnlock(); return m.cfg.Clone() }

// WithBuses 在读取过程中阻止配置切换，回调不得调用 Apply 或 Close。
func (m *Manager) WithBuses(read func([]*Bus)) { m.mu.RLock(); defer m.mu.RUnlock(); read(m.buses) }

// Apply 预建新增 PTY，落盘成功后再切换；失败时原配置与总线不变。
func (m *Manager) Apply(cfg *config.Config) error {
	cfg = cfg.Clone()
	if err := cfg.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return fmt.Errorf("模拟器正在关闭，不能应用配置")
	}
	return m.apply(cfg, true)
}

func (m *Manager) apply(cfg *config.Config, save bool) error {
	old := make(map[string]*Bus, len(m.buses))
	for _, b := range m.buses {
		old[b.LinkPath] = b
	}
	var added []*Bus
	rollback := func() {
		for _, b := range added {
			if err := b.Close(); err != nil {
				m.log.Publish(eventbus.Event{Level: eventbus.LevelWarn, Bus: b.name(), Summary: fmt.Sprintf("回滚新增总线失败: %v", err)})
			}
		}
	}
	next := make([]*Bus, 0, len(cfg.Buses))
	replacements := make([][]*Slave, 0, len(cfg.Buses))
	for _, bc := range cfg.Buses {
		slaves := make([]*Slave, 0, len(bc.Slaves))
		for _, sc := range bc.Slaves {
			sl, err := NewSlave(sc, time.Now())
			if err != nil {
				rollback()
				return fmt.Errorf("总线 %s 构造从站 %d 失败: %w", bc.Name, sc.DeviceID, err)
			}
			if sc.Model != nil {
				m.log.Publish(eventbus.Event{Level: eventbus.LevelInfo, Bus: bc.Name, DeviceID: int(sc.DeviceID), Summary: "华为逆变器行为模型已构造；控制将联动功率、电流、反馈和电量"})
			}
			slaves = append(slaves, sl)
		}
		b := old[bc.Link]
		if b == nil {
			if !save {
				if err := cleanupStaleLink(bc.Link); err != nil {
					rollback()
					return err
				}
			}
			if err := os.MkdirAll(filepath.Dir(bc.Link), 0755); err != nil {
				rollback()
				return fmt.Errorf("创建串口目录失败: %w", err)
			}
			b = NewBus(bc.Name, bc.Link, slaves, m.log)
			if err := b.Open(); err != nil {
				rollback()
				return err
			}
			added = append(added, b)
		}
		next = append(next, b)
		replacements = append(replacements, slaves)
	}
	if save {
		if err := config.Save(m.path, cfg); err != nil {
			rollback()
			return err
		}
	}
	for i, b := range next {
		b.Replace(cfg.Buses[i].Name, replacements[i])
		delete(old, b.LinkPath)
	}
	m.cfg, m.buses = cfg, next
	for _, b := range added {
		b.Start()
		m.log.Publish(eventbus.Event{Level: eventbus.LevelInfo, Bus: b.name(), Summary: fmt.Sprintf("虚拟串口已就绪: 软链接=%s 实际设备=%s 从站数=%d", b.LinkPath, b.SlavePath(), len(b.Slaves()))})
	}
	for _, b := range old {
		if err := b.Close(); err != nil {
			m.log.Publish(eventbus.Event{Level: eventbus.LevelWarn, Bus: b.name(), Summary: fmt.Sprintf("关闭已删除总线失败: %v", err)})
		}
	}
	if save {
		m.log.Publish(eventbus.Event{Level: eventbus.LevelInfo, Summary: fmt.Sprintf("Web 配置已保存并应用，共 %d 路 RS485 总线", len(next))})
	}
	return nil
}

// 初次启动只清理目标已消失的 PTY 软链接；仍有效的链接属于活跃资源。
func cleanupStaleLink(path string) error {
	target, err := os.Readlink(path)
	if err != nil {
		return nil
	} // 普通文件或有效链接交给 Open 拒绝，缺失路径正常创建。
	if filepath.Dir(target) != "/dev/pts" {
		return nil
	}
	if _, err := strconv.ParseUint(filepath.Base(target), 10, 32); err != nil {
		return nil
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		return nil
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("清理失效串口软链接 %s 失败: %w", path, err)
	}
	return nil
}

func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	close(m.stop)
	var first error
	for _, b := range m.buses {
		if err := b.Close(); err != nil {
			m.log.Publish(eventbus.Event{Level: eventbus.LevelWarn, Bus: b.name(), Summary: fmt.Sprintf("关闭总线失败: %v", err)})
			if first == nil {
				first = err
			}
		}
	}
	m.mu.Unlock()
	m.wg.Wait()
	return first
}
