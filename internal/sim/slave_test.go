package sim

import (
	"encoding/binary"
	"testing"
	"time"

	"slavesim/internal/config"
	"slavesim/internal/modbus"
)

// newTestSlave 构造一个用于测试的从站。
//
// 寄存器布局刻意覆盖三个地址空间与三类典型点位：
//   - 0x04 @3004：INT32 常量，占两个寄存器（输入寄存器空间）；
//   - 0x03 @32000：UINT16 只读常量（保持寄存器空间）；
//   - 0x06 @40120：UINT16 可写控制点（同一个保持寄存器空间）；
//   - 0x05 @10：可写线圈（线圈空间）。
func newTestSlave(t *testing.T) *Slave {
	t.Helper()

	cfg := config.Slave{
		DeviceID: 1,
		Registers: []config.Register{
			{
				Function: "0x04", Address: 3004, Type: "INT32", Tag: "有功功率",
				Source: config.Source{Kind: "const", Value: 100},
			},
			{
				Function: "0x03", Address: 32000, Type: "UINT16", Tag: "只读量",
				Source: config.Source{Kind: "const", Value: 7},
			},
			{
				Function: "0x06", Address: 40120, Type: "UINT16", Access: "readwrite", Tag: "功率限值命令",
				Source: config.Source{Kind: "manual"},
			},
			{
				Function: "0x10", Address: 40129, Type: "INT32", Access: "readwrite", Tag: "功率设定",
				Source: config.Source{Kind: "manual"},
			},
			{
				Function: "0x05", Address: 10, Type: "BOOL", Access: "readwrite", Tag: "开关机",
				Source: config.Source{Kind: "manual"},
			},
		},
	}

	s, err := NewSlave(cfg, time.Now())
	if err != nil {
		t.Fatalf("构造从站失败: %v", err)
	}
	return s
}

// req 拼装一个定长请求帧（站号 + 功能码 + 两个 16 位字段 + CRC）。
func req(deviceID, fn byte, addr, qty uint16) []byte {
	return modbus.AppendCRC([]byte{
		deviceID, fn,
		byte(addr >> 8), byte(addr),
		byte(qty >> 8), byte(qty),
	})
}

// respRegs 从响应帧中解出 16 位寄存器数组。
func respRegs(t *testing.T, resp []byte) []uint16 {
	t.Helper()
	if len(resp) < 5 {
		t.Fatalf("响应帧过短: %s", modbus.Hex(resp))
	}
	byteCount := int(resp[2])
	if byteCount%2 != 0 {
		t.Fatalf("响应字节数不是偶数: %d", byteCount)
	}
	if len(resp) != 3+byteCount+2 {
		t.Fatalf("响应帧长度与字节数不匹配: %s", modbus.Hex(resp))
	}
	out := make([]uint16, 0, byteCount/2)
	for i := 0; i < byteCount; i += 2 {
		out = append(out, binary.BigEndian.Uint16(resp[3+i:5+i]))
	}
	return out
}

// 验证 INT32 常量按 HL 字序展开成两个寄存器，且能被 0x04 读取。
func TestReadInputRegistersInt32(t *testing.T) {
	s := newTestSlave(t)

	res := s.Handle(req(1, modbus.FuncReadInputRegs, 3004, 2))
	if res.Response == nil {
		t.Fatalf("应当有响应，实际被丢弃: %s", res.DropReason)
	}

	got := respRegs(t, res.Response)
	want := []uint16{0, 100}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("寄存器值错误: 得到 %v，期望 %v", got, want)
	}
}

// 验证 0x06 写入后能被 0x03 从同一保持寄存器空间读回。
//
// 这是验收标准里的关键一条：读写必须共享同一片地址空间，否则 AGC 闭环根本不成立。
func TestWriteThenReadBackHoldingRegister(t *testing.T) {
	s := newTestSlave(t)

	write := req(1, modbus.FuncWriteSingleReg, 40120, 1234)
	res := s.Handle(write)
	if res.Response == nil {
		t.Fatalf("写命令应当有响应，实际被丢弃: %s", res.DropReason)
	}
	if modbus.Hex(res.Response) != modbus.Hex(write) {
		t.Fatalf("写单寄存器的响应应原样回显: 得到 %s，期望 %s", modbus.Hex(res.Response), modbus.Hex(write))
	}

	read := s.Handle(req(1, modbus.FuncReadHoldingRegs, 40120, 1))
	got := respRegs(t, read.Response)
	if len(got) != 1 || got[0] != 1234 {
		t.Fatalf("写入后读回的值错误: 得到 %v，期望 [1234]", got)
	}
}

// 验证 0x10 写多寄存器后能被 0x03 读回（含 32 位寄存器的双字回读）。
func TestWriteMultipleThenReadBack(t *testing.T) {
	s := newTestSlave(t)

	body := []byte{
		1, modbus.FuncWriteMultiple,
		0x9C, 0xC1, // 40129
		0x00, 0x02, // 数量 2
		0x04,       // 字节数
		0x00, 0x00, // 高字
		0x00, 0x11, // 低字 -> INT32 值 17
	}
	res := s.Handle(modbus.AppendCRC(body))
	if res.Response == nil || res.IsException {
		t.Fatalf("写多寄存器应当正常响应，实际: %s", modbus.Hex(res.Response))
	}

	read := s.Handle(req(1, modbus.FuncReadHoldingRegs, 40129, 2))
	got := respRegs(t, read.Response)
	if len(got) != 2 || got[0] != 0x0000 || got[1] != 0x0011 {
		t.Fatalf("写多寄存器后读回的值错误: 得到 %v，期望 [0 17]", got)
	}
}

// 验证写多寄存器的字节数与数量不匹配时返回非法数据值 0x03。
func TestWriteMultipleByteCountMismatch(t *testing.T) {
	s := newTestSlave(t)

	body := []byte{
		1, modbus.FuncWriteMultiple,
		0x9C, 0xC1, // 40129
		0x00, 0x02, // 数量 2，但只给了 1 个寄存器的数据
		0x02,
		0x00, 0x11,
	}
	res := s.Handle(modbus.AppendCRC(body))
	if !res.IsException || res.ExceptionCode != modbus.ExcIllegalValue {
		t.Fatalf("应当返回非法数据值 0x03，实际: %s", modbus.Hex(res.Response))
	}
}

// 验证站号不属于本站时静默不响应（对齐真机行为）。
func TestForeignDeviceIDIsSilentlyDropped(t *testing.T) {
	s := newTestSlave(t)

	res := s.Handle(req(9, modbus.FuncReadHoldingRegs, 40120, 1))
	if res.Response != nil {
		t.Fatalf("站号不符时不应产生响应，实际: %s", modbus.Hex(res.Response))
	}
	if res.DropReason == "" {
		t.Fatal("站号不符时应当给出丢弃原因")
	}
}

// 验证 CRC 校验失败时静默不响应。
func TestBadCRCIsSilentlyDropped(t *testing.T) {
	s := newTestSlave(t)

	frame := req(1, modbus.FuncReadHoldingRegs, 40120, 1)
	frame[len(frame)-1] ^= 0xFF

	res := s.Handle(frame)
	if res.Response != nil {
		t.Fatalf("CRC 错误时不应产生响应，实际: %s", modbus.Hex(res.Response))
	}
	if res.DropReason == "" {
		t.Fatal("CRC 错误时应当给出丢弃原因")
	}
}

// 验证读未配置地址时按默认策略返回非法数据地址 0x02。
func TestUnmappedAddressReturnsIllegalAddress(t *testing.T) {
	s := newTestSlave(t)

	res := s.Handle(req(1, modbus.FuncReadHoldingRegs, 12345, 1))
	if !res.IsException || res.ExceptionCode != modbus.ExcIllegalAddress {
		t.Fatalf("应当返回非法数据地址 0x02，实际: %s", modbus.Hex(res.Response))
	}
}

// 验证 unmapped_policy=zero 时读未配置地址返回 0。
func TestUnmappedAddressWithZeroPolicy(t *testing.T) {
	cfg := config.Slave{
		DeviceID:       1,
		UnmappedPolicy: "zero",
		Registers: []config.Register{
			{Function: "0x03", Address: 100, Type: "UINT16", Source: config.Source{Kind: "const", Value: 5}},
		},
	}
	s, err := NewSlave(cfg, time.Now())
	if err != nil {
		t.Fatalf("构造从站失败: %v", err)
	}

	res := s.Handle(req(1, modbus.FuncReadHoldingRegs, 100, 3))
	got := respRegs(t, res.Response)
	if len(got) != 3 || got[0] != 5 || got[1] != 0 || got[2] != 0 {
		t.Fatalf("zero 策略下的返回值错误: 得到 %v，期望 [5 0 0]", got)
	}
}

// 验证不支持的功能码返回非法功能码 0x01。
func TestUnsupportedFunctionReturnsIllegalFunction(t *testing.T) {
	s := newTestSlave(t)

	res := s.Handle(req(1, 0x08, 0, 0))
	if !res.IsException || res.ExceptionCode != modbus.ExcIllegalFunction {
		t.Fatalf("应当返回非法功能码 0x01，实际: %s", modbus.Hex(res.Response))
	}
}

// 验证读数量为 0 或超过上限时返回非法数据值 0x03。
func TestReadQuantityOutOfRange(t *testing.T) {
	s := newTestSlave(t)

	for _, qty := range []uint16{0, 126} {
		res := s.Handle(req(1, modbus.FuncReadHoldingRegs, 40120, qty))
		if !res.IsException || res.ExceptionCode != modbus.ExcIllegalValue {
			t.Fatalf("数量 %d 应当返回非法数据值 0x03，实际: %s", qty, modbus.Hex(res.Response))
		}
	}
}

// 验证写只读寄存器返回非法数据地址 0x02。
func TestWriteToReadOnlyRegisterRejected(t *testing.T) {
	s := newTestSlave(t)

	res := s.Handle(req(1, modbus.FuncWriteSingleReg, 32000, 999))
	if !res.IsException || res.ExceptionCode != modbus.ExcIllegalAddress {
		t.Fatalf("写只读寄存器应当返回非法数据地址 0x02，实际: %s", modbus.Hex(res.Response))
	}
}

// 验证写单线圈只接受 FF00 / 0000，其它值返回非法数据值 0x03。
func TestWriteSingleCoilValueValidation(t *testing.T) {
	s := newTestSlave(t)

	bad := s.Handle(req(1, modbus.FuncWriteSingleCoil, 10, 0x1234))
	if !bad.IsException || bad.ExceptionCode != modbus.ExcIllegalValue {
		t.Fatalf("非法线圈值应当返回 0x03，实际: %s", modbus.Hex(bad.Response))
	}

	on := s.Handle(req(1, modbus.FuncWriteSingleCoil, 10, 0xFF00))
	if on.Response == nil || on.IsException {
		t.Fatalf("写线圈 ON 应当正常响应，实际: %s", modbus.Hex(on.Response))
	}

	read := s.Handle(req(1, modbus.FuncReadCoils, 10, 1))
	if len(read.Response) < 4 || read.Response[2] != 1 || read.Response[3] != 0x01 {
		t.Fatalf("写线圈 ON 后读回应为 0x01，实际: %s", modbus.Hex(read.Response))
	}
}

// 验证广播地址执行写操作但不产生任何响应。
func TestBroadcastWriteExecutesWithoutResponse(t *testing.T) {
	s := newTestSlave(t)

	res := s.Handle(req(0x00, modbus.FuncWriteSingleReg, 40120, 4321))
	if res.Response != nil {
		t.Fatalf("广播不应产生响应，实际: %s", modbus.Hex(res.Response))
	}

	read := s.Handle(req(1, modbus.FuncReadHoldingRegs, 40120, 1))
	got := respRegs(t, read.Response)
	if len(got) != 1 || got[0] != 4321 {
		t.Fatalf("广播写入未生效: 得到 %v，期望 [4321]", got)
	}
}

// 验证 BYTE_ORDER_BA 时寄存器的两个字节在线上是交换的。
func TestByteOrderBA(t *testing.T) {
	cfg := config.Slave{
		DeviceID: 1,
		Registers: []config.Register{
			{Function: "0x03", Address: 200, Type: "UINT16", ByteOrder: "BA",
				Source: config.Source{Kind: "const", Value: 0x1234}},
		},
	}
	s, err := NewSlave(cfg, time.Now())
	if err != nil {
		t.Fatalf("构造从站失败: %v", err)
	}

	res := s.Handle(req(1, modbus.FuncReadHoldingRegs, 200, 1))
	got := respRegs(t, res.Response)
	if len(got) != 1 || got[0] != 0x3412 {
		t.Fatalf("BYTE_ORDER_BA 应当交换字节: 得到 0x%04X，期望 0x3412", got[0])
	}
}

// 验证同一地址空间内地址重复定义会被拒绝。
func TestDuplicateAddressRejected(t *testing.T) {
	cfg := config.Slave{
		DeviceID: 1,
		Registers: []config.Register{
			{Function: "0x03", Address: 100, Type: "UINT16"},
			{Function: "0x06", Address: 100, Type: "UINT16", Access: "readwrite"},
		},
	}
	if _, err := NewSlave(cfg, time.Now()); err == nil {
		t.Fatal("同一地址空间内的重复地址应当被拒绝")
	}
}
