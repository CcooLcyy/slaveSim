//go:build linux

package sim

import (
	"fmt"
	"os"
	"testing"
	"time"

	"slavesim/internal/config"
	"slavesim/internal/eventbus"
	"slavesim/internal/modbus"
)

// readWithTimeout 在超时时间内读取一次数据。
func readWithTimeout(f *os.File, d time.Duration) ([]byte, error) {
	type result struct {
		data []byte
		err  error
	}
	ch := make(chan result, 1)

	go func() {
		buf := make([]byte, 256)
		n, err := f.Read(buf)
		if err != nil {
			ch <- result{err: err}
			return
		}
		out := make([]byte, n)
		copy(out, buf[:n])
		ch <- result{data: out}
	}()

	select {
	case r := <-ch:
		return r.data, r.err
	case <-time.After(d):
		return nil, fmt.Errorf("读取超时")
	}
}

// newPTYBus 用真实 PTY 构造一条可用的总线，测试结束后自动清理。
func newPTYBus(t *testing.T) *Bus {
	t.Helper()

	link := t.TempDir() + "/tty0"
	cfg := config.Slave{
		DeviceID: 1,
		Registers: []config.Register{
			{
				Function: "0x03", Address: 100, Type: "UINT16", Access: "readwrite",
				Source: config.Source{Kind: "manual"},
			},
		},
	}

	sl, err := NewSlave(cfg, time.Now())
	if err != nil {
		t.Fatalf("构造从站失败: %v", err)
	}

	bus := NewBus("test", link, []*Slave{sl}, eventbus.NewLog(64))
	if err := bus.Open(); err != nil {
		t.Fatalf("创建虚拟串口失败: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })

	bus.Start()
	return bus
}

// exchange 打开从端设备、发送一帧请求并读回响应。
func exchange(t *testing.T, bus *Bus, request []byte) []byte {
	t.Helper()

	f, err := os.OpenFile(bus.SlavePath(), os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("打开从端设备 %s 失败: %v", bus.SlavePath(), err)
	}
	defer f.Close()

	if _, err := f.Write(request); err != nil {
		t.Fatalf("写入请求失败: %v", err)
	}

	resp, err := readWithTimeout(f, 2*time.Second)
	if err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}
	return resp
}

// 验证通过真实 PTY 的完整收发链路：写入请求能收到合法响应。
func TestBusRoundTripOverPTY(t *testing.T) {
	bus := newPTYBus(t)

	request := modbus.AppendCRC([]byte{0x01, 0x03, 0x00, 0x64, 0x00, 0x01})
	resp := exchange(t, bus, request)

	if !modbus.CheckCRC(resp) {
		t.Fatalf("响应 CRC 校验失败: %s", modbus.Hex(resp))
	}
	want := modbus.AppendCRC([]byte{0x01, 0x03, 0x02, 0x00, 0x00})
	if modbus.Hex(resp) != modbus.Hex(want) {
		t.Fatalf("响应内容错误: 得到 %s，期望 %s", modbus.Hex(resp), modbus.Hex(want))
	}
}

// 验证客户端断开后重新连接，总线仍然可用。
//
// 这一条是针对真实缺陷的回归测试：PTY 从端全部关闭时，读取主端会返回 EIO。
// 如果读写循环就此退出，总线会永久失效，后续所有请求都收不到响应——
// 现象是「第一次能通，之后全部超时」。
func TestBusSurvivesClientDisconnect(t *testing.T) {
	bus := newPTYBus(t)

	request := modbus.AppendCRC([]byte{0x01, 0x03, 0x00, 0x64, 0x00, 0x01})

	resp := exchange(t, bus, request)
	if !modbus.CheckCRC(resp) {
		t.Fatalf("第一轮响应 CRC 校验失败: %s", modbus.Hex(resp))
	}

	// 等待总线感知到从端已全部关闭并产生 EIO。
	time.Sleep(500 * time.Millisecond)

	resp = exchange(t, bus, request)
	if !modbus.CheckCRC(resp) {
		t.Fatalf("重连后响应 CRC 校验失败: %s", modbus.Hex(resp))
	}
}

// 验证写命令经真实 PTY 写入后，能被后续读命令读回。
func TestBusWriteThenReadBackOverPTY(t *testing.T) {
	bus := newPTYBus(t)

	// 0x06 写单寄存器：地址 100 值为 0x04D2（1234）
	writeResp := exchange(t, bus, modbus.AppendCRC([]byte{0x01, 0x06, 0x00, 0x64, 0x04, 0xD2}))
	if !modbus.CheckCRC(writeResp) || writeResp[1]&0x80 != 0 {
		t.Fatalf("写命令响应异常: %s", modbus.Hex(writeResp))
	}

	// 0x03 读回同一地址
	readResp := exchange(t, bus, modbus.AppendCRC([]byte{0x01, 0x03, 0x00, 0x64, 0x00, 0x01}))
	want := modbus.AppendCRC([]byte{0x01, 0x03, 0x02, 0x04, 0xD2})
	if modbus.Hex(readResp) != modbus.Hex(want) {
		t.Fatalf("写入后读回的值错误: 得到 %s，期望 %s", modbus.Hex(readResp), modbus.Hex(want))
	}
}
