// Package sim 实现模拟核心：寄存器表、取值引擎与从站请求处理。
package sim

import (
	"math"
	"math/bits"
	"math/rand"
	"strings"
	"sync"
	"time"
)

// DataType 是寄存器的数据类型。
type DataType int

const (
	TypeUint16 DataType = iota
	TypeInt16
	TypeUint32
	TypeInt32
	TypeBool
)

// ParseDataType 把配置里的字符串解析为数据类型。
func ParseDataType(s string) DataType {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "INT16":
		return TypeInt16
	case "UINT32":
		return TypeUint32
	case "INT32":
		return TypeInt32
	case "BOOL":
		return TypeBool
	default:
		return TypeUint16
	}
}

// String 返回数据类型的配置写法，用于界面展示。
func (t DataType) String() string {
	switch t {
	case TypeInt16:
		return "INT16"
	case TypeUint32:
		return "UINT32"
	case TypeInt32:
		return "INT32"
	case TypeBool:
		return "BOOL"
	default:
		return "UINT16"
	}
}

// RegCount 返回该类型占用的 16 位寄存器数量。
func (t DataType) RegCount() int {
	switch t {
	case TypeUint32, TypeInt32:
		return 2
	default:
		return 1
	}
}

// WordOrder 是 32 位拼接字序。
type WordOrder int

const (
	WordOrderHL WordOrder = iota // 高字在前（默认）
	WordOrderLH
)

// ByteOrder 是 16 位字节序。
type ByteOrder int

const (
	ByteOrderAB ByteOrder = iota // 默认
	ByteOrderBA
)

// SourceKind 是取值来源类型。
type SourceKind int

const (
	KindConst SourceKind = iota
	KindManual
	KindSine
	KindRamp
	KindRandom
)

// ParseSourceKind 把配置里的字符串解析为取值来源类型。
func ParseSourceKind(s string) SourceKind {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "manual":
		return KindManual
	case "sine":
		return KindSine
	case "ramp":
		return KindRamp
	case "random":
		return KindRandom
	default:
		return KindConst
	}
}

// Source 描述取值来源的参数。
type Source struct {
	Kind      SourceKind
	Value     float64
	Base      float64
	Amplitude float64
	PeriodMs  int
	Min       float64
	Max       float64
}

// Register 是一个可对外提供数据的寄存器配置项。
type Register struct {
	Function  byte
	Address   uint16
	Type      DataType
	WordOrder WordOrder
	ByteOrder ByteOrder
	BitIndex  uint8
	Writable  bool
	Tag       string
	Unit      string
	Source    Source

	startedAt time.Time
	rng       *rand.Rand

	mu          sync.RWMutex
	overridden  bool    // 被写命令覆盖后，读值固定为 override
	overrideVal float64 // 写命令写入的工程量
}

// NewRegister 构造一个寄存器运行时对象。
func NewRegister(now time.Time) *Register {
	return &Register{
		startedAt: now,
		rng:       rand.New(rand.NewSource(now.UnixNano())),
	}
}

// RegCount 返回该寄存器占用的 16 位寄存器数量。
func (r *Register) RegCount() int {
	return r.Type.RegCount()
}

// Write 处理写命令：写入工程量并覆盖后续读值。
//
// 出于行为可预测的考虑，一旦某个寄存器被写过，它的读值就固定为写入值，
// 不再回到正弦/斜坡等来源。控制类寄存器应在配置中声明为 readwrite。
func (r *Register) Write(value float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.overridden = true
	r.overrideVal = value
}

// SetManual 由界面手动设定数值，语义与写命令一致。
func (r *Register) SetManual(value float64) {
	r.Write(value)
}

// Value 返回当前工程量数值。
func (r *Register) Value(now time.Time) float64 {
	r.mu.RLock()
	if r.overridden {
		v := r.overrideVal
		r.mu.RUnlock()
		return v
	}
	kind := r.Source.Kind
	src := r.Source
	r.mu.RUnlock()

	switch kind {
	case KindManual:
		return src.Value
	case KindSine:
		period := time.Duration(src.PeriodMs) * time.Millisecond
		if period <= 0 {
			return src.Base
		}
		phase := float64(now.Sub(r.startedAt)%period) / float64(period)
		return src.Base + src.Amplitude*math.Sin(2*math.Pi*phase)
	case KindRamp:
		period := time.Duration(src.PeriodMs) * time.Millisecond
		if period <= 0 {
			return src.Min
		}
		phase := float64(now.Sub(r.startedAt)%period) / float64(period)
		return src.Min + (src.Max-src.Min)*phase
	case KindRandom:
		period := time.Duration(src.PeriodMs) * time.Millisecond
		step := int64(0)
		if period > 0 {
			step = int64(now.Sub(r.startedAt) / period)
		}
		// 用步号播种，保证同一周期内多次读取得到相同数值。
		r.mu.Lock()
		r.rng.Seed(step*2654435761 + int64(r.Address))
		v := src.Min + r.rng.Float64()*(src.Max-src.Min)
		r.mu.Unlock()
		return v
	default:
		return src.Value
	}
}

// RawRegisters 把当前工程量编码成需要出现在报文里的 16 位寄存器序列。
//
// 编码规则与下位机点表口径一致：
//   - UINT16/INT16：1 个寄存器；
//   - UINT32/INT32：2 个寄存器，按字序 HL/LH 拼接；
//   - BYTE_ORDER_BA 时，每个寄存器的两个字节在线上要交换；
//   - BOOL：按 bit_index 置位。
func (r *Register) RawRegisters(now time.Time) []uint16 {
	v := r.Value(now)

	switch r.Type {
	case TypeUint16:
		return []uint16{r.applyByteOrder(clampUint16(v))}
	case TypeInt16:
		return []uint16{r.applyByteOrder(uint16(int16(clampInt16(v))))}
	case TypeUint32:
		return r.split32(uint32(clampUint32(v)))
	case TypeInt32:
		return r.split32(uint32(int32(clampInt32(v))))
	case TypeBool:
		if v != 0 {
			return []uint16{1 << r.BitIndex}
		}
		return []uint16{0}
	default:
		return []uint16{r.applyByteOrder(clampUint16(v))}
	}
}

// applyByteOrder 按 BYTE_ORDER_BA 交换 16 位寄存器的两个字节。
//
// 该变换是对合的，所以编码与解码共用同一个函数。
func (r *Register) applyByteOrder(v uint16) uint16 {
	if r.ByteOrder == ByteOrderBA {
		return bits.ReverseBytes16(v)
	}
	return v
}

func (r *Register) split32(v uint32) []uint16 {
	var hi, lo uint16
	if r.WordOrder == WordOrderLH {
		hi, lo = uint16(v), uint16(v>>16)
	} else {
		hi, lo = uint16(v>>16), uint16(v)
	}
	return []uint16{r.applyByteOrder(hi), r.applyByteOrder(lo)}
}

// DecodeWrite 把写命令里的 16 位寄存器值还原成工程量数值。
func (r *Register) DecodeWrite(regs []uint16) float64 {
	if len(regs) == 0 {
		return 0
	}
	switch r.Type {
	case TypeInt16:
		return float64(int16(r.applyByteOrder(regs[0])))
	case TypeUint32, TypeInt32:
		if len(regs) < 2 {
			return float64(r.applyByteOrder(regs[0]))
		}
		hi := r.applyByteOrder(regs[0])
		lo := r.applyByteOrder(regs[1])
		var v uint32
		if r.WordOrder == WordOrderLH {
			v = uint32(lo)<<16 | uint32(hi)
		} else {
			v = uint32(hi)<<16 | uint32(lo)
		}
		if r.Type == TypeInt32 {
			return float64(int32(v))
		}
		return float64(v)
	case TypeBool:
		if regs[0]&(1<<r.BitIndex) != 0 {
			return 1
		}
		return 0
	default:
		return float64(r.applyByteOrder(regs[0]))
	}
}

func clampUint16(v float64) uint16 {
	if v <= 0 {
		return 0
	}
	if v >= 65535 {
		return 65535
	}
	return uint16(math.Round(v))
}

func clampInt16(v float64) int16 {
	if v <= -32768 {
		return -32768
	}
	if v >= 32767 {
		return 32767
	}
	return int16(math.Round(v))
}

func clampUint32(v float64) uint32 {
	if v <= 0 {
		return 0
	}
	if v >= 4294967295 {
		return 4294967295
	}
	return uint32(math.Round(v))
}

func clampInt32(v float64) int32 {
	if v <= -2147483648 {
		return -2147483648
	}
	if v >= 2147483647 {
		return 2147483647
	}
	return int32(math.Round(v))
}
