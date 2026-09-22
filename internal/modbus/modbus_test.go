package modbus

import "testing"

// 验证标准 Modbus CRC16 的计算结果与字节序（低字节在前）。
//
// 期望值取自仓库文档 MskDSP/doc/协议/锦浪逆变器协议/报文整理.md 中的真实报文：
// 请求 01 03 0B B7 00 01 的 CRC 为 36 08。
func TestCRC16MatchesKnownFrame(t *testing.T) {
	body := []byte{0x01, 0x03, 0x0B, 0xB7, 0x00, 0x01}
	crc := CRC16(body)
	if crc != 0x0836 {
		t.Fatalf("CRC16 计算结果错误: 得到 0x%04X，期望 0x0836", crc)
	}

	frame := AppendCRC(append([]byte{}, body...))
	want := []byte{0x01, 0x03, 0x0B, 0xB7, 0x00, 0x01, 0x36, 0x08}
	if Hex(frame) != Hex(want) {
		t.Fatalf("追加 CRC 的结果错误: 得到 %s，期望 %s", Hex(frame), Hex(want))
	}
}

// 验证文档中写多寄存器请求的 CRC（51 65）。
func TestCRC16MatchesWriteMultipleFrame(t *testing.T) {
	body := []byte{0x01, 0x10, 0x0B, 0xB7, 0x00, 0x03, 0x06, 0x00, 0x13, 0x00, 0x02, 0x00, 0x14}
	frame := AppendCRC(append([]byte{}, body...))
	want := []byte{0x01, 0x10, 0x0B, 0xB7, 0x00, 0x03, 0x06, 0x00, 0x13, 0x00, 0x02, 0x00, 0x14, 0x51, 0x65}
	if Hex(frame) != Hex(want) {
		t.Fatalf("追加 CRC 的结果错误: 得到 %s，期望 %s", Hex(frame), Hex(want))
	}
}

// 验证 CRC 校验能识别被篡改的报文。
func TestCheckCRCRejectsCorruptedFrame(t *testing.T) {
	good := AppendCRC([]byte{0x02, 0x03, 0x7D, 0x00, 0x00, 0x01})
	if !CheckCRC(good) {
		t.Fatalf("合法报文未被识别: %s", Hex(good))
	}

	bad := append([]byte{}, good...)
	bad[3] ^= 0xFF
	if CheckCRC(bad) {
		t.Fatalf("被篡改的报文被误判为合法: %s", Hex(bad))
	}
}

// 验证定长功能码的请求长度推算。
func TestExpectedRequestLenFixedFunctions(t *testing.T) {
	for _, fn := range []byte{FuncReadCoils, FuncReadHoldingRegs, FuncReadInputRegs, FuncWriteSingleCoil, FuncWriteSingleReg} {
		n, ok := ExpectedRequestLen([]byte{0x01, fn})
		if !ok || n != 8 {
			t.Fatalf("功能码 0x%02X 的请求长度推算错误: n=%d ok=%v", fn, n, ok)
		}
	}
}

// 验证写多寄存器的长度依赖字节数字段，且字节数不足时要求继续读取。
func TestExpectedRequestLenWriteMultiple(t *testing.T) {
	if _, ok := ExpectedRequestLen([]byte{0x01, FuncWriteMultiple, 0x0B}); ok {
		t.Fatal("字节数不足时不应给出长度")
	}

	buf := []byte{0x01, FuncWriteMultiple, 0x0B, 0xB7, 0x00, 0x03, 0x06}
	n, ok := ExpectedRequestLen(buf)
	if !ok || n != 15 {
		t.Fatalf("写多寄存器长度推算错误: n=%d ok=%v，期望 15", n, ok)
	}
}

// 验证 Framer 能把粘包的两个请求逐帧切出，并保留不完整的残帧。
func TestFramerSplitsConcatenatedFrames(t *testing.T) {
	f1 := AppendCRC([]byte{0x01, 0x03, 0x7D, 0x00, 0x00, 0x01})
	f2 := AppendCRC([]byte{0x02, 0x04, 0x0B, 0xEA, 0x00, 0x02})

	var fr Framer
	fr.Push(append(append([]byte{}, f1...), f2[:3]...))

	got, ok := fr.Next()
	if !ok || Hex(got) != Hex(f1) {
		t.Fatalf("第一帧解析错误: ok=%v 得到 %s，期望 %s", ok, Hex(got), Hex(f1))
	}

	if _, ok := fr.Next(); ok {
		t.Fatal("第二帧尚未接收完整，不应解析成功")
	}
	if fr.Buffered() != 3 {
		t.Fatalf("残帧长度错误: 得到 %d，期望 3", fr.Buffered())
	}

	fr.Push(f2[3:])
	got, ok = fr.Next()
	if !ok || Hex(got) != Hex(f2) {
		t.Fatalf("第二帧解析错误: ok=%v 得到 %s，期望 %s", ok, Hex(got), Hex(f2))
	}
	if fr.Buffered() != 0 {
		t.Fatalf("缓冲区应已清空，实际剩余 %d 字节", fr.Buffered())
	}
}

// 验证 Hex 的输出格式（大写、空格分隔）。
func TestHexFormat(t *testing.T) {
	if got := Hex([]byte{0x02, 0x03, 0x7D, 0x00, 0x00, 0x01, 0x9C, 0x55}); got != "02 03 7D 00 00 01 9C 55" {
		t.Fatalf("Hex 输出错误: %s", got)
	}
	if got := Hex(nil); got != "" {
		t.Fatalf("空输入的 Hex 输出应为空串，实际为 %q", got)
	}
}

// 验证广播地址判定覆盖 0x00 与 0xFF。
func TestIsBroadcast(t *testing.T) {
	if !IsBroadcast(0x00) || !IsBroadcast(0xFF) {
		t.Fatal("0x00 与 0xFF 都应判定为广播地址")
	}
	if IsBroadcast(0x01) || IsBroadcast(0x02) {
		t.Fatal("普通站号不应判定为广播地址")
	}
}
