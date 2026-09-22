// Package modbus 提供 Modbus RTU 的帧编解码与从站处理所需的基础能力。
//
// 本包只依赖标准库，便于在无网络、无第三方依赖的构建容器中编译。
package modbus

import "fmt"

// 功能码
const (
	FuncReadCoils       byte = 0x01
	FuncReadHoldingRegs byte = 0x03
	FuncReadInputRegs   byte = 0x04
	FuncWriteSingleCoil byte = 0x05
	FuncWriteSingleReg  byte = 0x06
	FuncWriteMultiple   byte = 0x10
)

// 异常码
const (
	ExcIllegalFunction byte = 0x01
	ExcIllegalAddress  byte = 0x02
	ExcIllegalValue    byte = 0x03
	ExcSlaveFailure    byte = 0x04
)

// 各功能码的请求数量上限（按 Modbus 规范）
const (
	MaxReadCoils = 2000
	MaxReadRegs  = 125
	MaxWriteRegs = 123
)

// 广播地址：执行写操作，但不产生任何响应。
const (
	BroadcastAddrZero byte = 0x00
	BroadcastAddrFF   byte = 0xFF
)

// IsBroadcast 判断站号是否为广播地址。
func IsBroadcast(deviceID byte) bool {
	return deviceID == BroadcastAddrZero || deviceID == BroadcastAddrFF
}

// FunctionText 返回功能码的中文说明，用于日志。
func FunctionText(fn byte) string {
	switch fn {
	case FuncReadCoils:
		return "读线圈"
	case FuncReadHoldingRegs:
		return "读保持寄存器"
	case FuncReadInputRegs:
		return "读输入寄存器"
	case FuncWriteSingleCoil:
		return "写单线圈"
	case FuncWriteSingleReg:
		return "写单寄存器"
	case FuncWriteMultiple:
		return "写多寄存器"
	default:
		return fmt.Sprintf("未知功能码 0x%02X", fn)
	}
}

// ExceptionText 返回异常码的中文说明，用于日志。
func ExceptionText(code byte) string {
	switch code {
	case ExcIllegalFunction:
		return "非法功能码"
	case ExcIllegalAddress:
		return "非法数据地址"
	case ExcIllegalValue:
		return "非法数据值"
	case ExcSlaveFailure:
		return "从站故障"
	default:
		return fmt.Sprintf("未知异常码 0x%02X", code)
	}
}

// CRC16 计算标准 Modbus CRC16（初值 0xFFFF，多项式 0xA001）。
func CRC16(data []byte) uint16 {
	var crc uint16 = 0xFFFF
	for _, b := range data {
		crc ^= uint16(b)
		for i := 0; i < 8; i++ {
			if crc&0x0001 != 0 {
				crc = (crc >> 1) ^ 0xA001
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}

// AppendCRC 按 Modbus RTU 约定追加 CRC（低字节在前）。
func AppendCRC(frame []byte) []byte {
	crc := CRC16(frame)
	return append(frame, byte(crc&0xFF), byte(crc>>8))
}

// CheckCRC 校验整帧（含末尾两字节 CRC）。
func CheckCRC(frame []byte) bool {
	if len(frame) < 3 {
		return false
	}
	body := frame[:len(frame)-2]
	want := CRC16(body)
	got := uint16(frame[len(frame)-2]) | uint16(frame[len(frame)-1])<<8
	return want == got
}

// ExceptionFrame 构造异常响应帧。
func ExceptionFrame(deviceID, function, code byte) []byte {
	return AppendCRC([]byte{deviceID, function | 0x80, code})
}

// ExpectedRequestLen 返回一个 Modbus RTU 请求帧的总长度。
// ok=false 表示当前字节数还不足以判断长度，需要继续读取。
func ExpectedRequestLen(buf []byte) (int, bool) {
	if len(buf) < 2 {
		return 0, false
	}
	switch buf[1] {
	case FuncReadCoils, FuncReadHoldingRegs, FuncReadInputRegs, FuncWriteSingleCoil, FuncWriteSingleReg:
		return 8, true
	case FuncWriteMultiple:
		if len(buf) < 7 {
			return 0, false
		}
		return 9 + int(buf[6]), true
	default:
		// 未知功能码按最小请求长度处理，由从站回非法功能码。
		return 8, true
	}
}

// Framer 把串口上收到的字节流切分成完整的 Modbus RTU 请求帧。
//
// Modbus RTU 原本依靠 3.5 个字符时间的静默间隔断帧，PTY 上无法感知真实间隔，
// 因此这里按下位机的实际做法处理：按功能码推算定长，再配合空闲超时丢弃残帧。
type Framer struct {
	buf []byte
}

// Push 追加新收到的字节。
func (f *Framer) Push(b []byte) {
	f.buf = append(f.buf, b...)
}

// Buffered 返回当前缓冲区中的字节数。
func (f *Framer) Buffered() int {
	return len(f.buf)
}

// Reset 清空缓冲区，用于丢弃超时残帧。
func (f *Framer) Reset() {
	f.buf = f.buf[:0]
}

// Take 取出并清空当前缓冲区，用于把超时残帧交给日志记录。
func (f *Framer) Take() []byte {
	out := make([]byte, len(f.buf))
	copy(out, f.buf)
	f.buf = f.buf[:0]
	return out
}

// Next 取出一帧完整报文；ok=false 表示缓冲还不足以构成完整帧。
func (f *Framer) Next() ([]byte, bool) {
	n, ok := ExpectedRequestLen(f.buf)
	if !ok || len(f.buf) < n {
		return nil, false
	}
	frame := make([]byte, n)
	copy(frame, f.buf[:n])
	f.buf = f.buf[:copy(f.buf, f.buf[n:])]
	return frame, true
}

// Hex 将字节序列格式化为大写十六进制，字节之间以空格分隔。
func Hex(b []byte) string {
	const digits = "0123456789ABCDEF"
	if len(b) == 0 {
		return ""
	}
	out := make([]byte, 0, len(b)*3-1)
	for i, v := range b {
		if i > 0 {
			out = append(out, ' ')
		}
		out = append(out, digits[v>>4], digits[v&0x0F])
	}
	return string(out)
}
