package sim

import (
	"encoding/binary"
	"fmt"
	"time"

	"slavesim/internal/config"
	"slavesim/internal/modbus"
)

// UnmappedPolicy 决定读到未配置地址时的行为。
type UnmappedPolicy int

const (
	// PolicyException 返回异常码 0x02，对齐真实设备。
	PolicyException UnmappedPolicy = iota
	// PolicyZero 返回 0，用于容忍下位机读整段区间。
	PolicyZero
)

// Area 是 Modbus 的地址空间。
//
// 这一点必须按规范区分：0x03 读保持寄存器与 0x06/0x10 写保持寄存器共享**同一片**
// 地址空间，所以写进去的值能被 0x03 读回来；0x04 输入寄存器与 0x01/0x05 线圈各自独立。
type Area int

const (
	AreaCoil Area = iota
	AreaHolding
	AreaInput
)

// AreaOf 返回功能码所属的地址空间。
func AreaOf(fn byte) (Area, bool) {
	switch fn {
	case modbus.FuncReadCoils, modbus.FuncWriteSingleCoil:
		return AreaCoil, true
	case modbus.FuncReadHoldingRegs, modbus.FuncWriteSingleReg, modbus.FuncWriteMultiple:
		return AreaHolding, true
	case modbus.FuncReadInputRegs:
		return AreaInput, true
	}
	return 0, false
}

// slot 表示某个地址落在哪个寄存器的第几个 16 位字上。
type slot struct {
	reg *Register
	idx int
}

// Result 是一次请求处理的结果，同时携带供日志与界面展示的信息。
type Result struct {
	// Response 是完整响应帧（已含 CRC）；nil 表示不产生响应。
	Response []byte
	// DropReason 非空时说明不响应的原因。
	DropReason string
	// Summary 是人类可读的处理摘要。
	Summary string
	// IsException 表示响应是异常响应。
	IsException bool
	// ExceptionCode 是异常码，仅在 IsException 为真时有意义。
	ExceptionCode byte
}

// Slave 是一个 Modbus RTU 从站。
type Slave struct {
	DeviceID       uint8
	ResponseDelay  time.Duration
	UnmappedPolicy UnmappedPolicy

	areas map[Area]map[uint16]slot
	order []*Register
}

// NewSlave 由配置构造一个从站。
func NewSlave(cfg config.Slave, now time.Time) (*Slave, error) {
	s := &Slave{
		DeviceID:      cfg.DeviceID,
		ResponseDelay: time.Duration(cfg.ResponseDelayMs) * time.Millisecond,
		areas:         map[Area]map[uint16]slot{},
	}
	switch cfg.UnmappedPolicy {
	case "zero":
		s.UnmappedPolicy = PolicyZero
	default:
		s.UnmappedPolicy = PolicyException
	}

	for i := range cfg.Registers {
		rc := cfg.Registers[i]

		fn, err := config.ParseFunction(rc.Function)
		if err != nil {
			return nil, err
		}
		area, ok := AreaOf(fn)
		if !ok {
			return nil, fmt.Errorf("功能码 0x%02X 不属于任何地址空间", fn)
		}

		reg := NewRegister(now)
		reg.Function = fn
		reg.Address = rc.Address
		reg.Type = ParseDataType(rc.Type)
		reg.BitIndex = rc.BitIndex
		reg.Tag = rc.Tag
		reg.Unit = rc.Unit
		reg.Writable = isWriteFunction(fn) || isReadWrite(rc.Access)
		if rc.WordOrder == "LH" {
			reg.WordOrder = WordOrderLH
		}
		if rc.ByteOrder == "BA" {
			reg.ByteOrder = ByteOrderBA
		}
		reg.Source = Source{
			Kind:      ParseSourceKind(rc.Source.Kind),
			Value:     rc.Source.Value,
			Base:      rc.Source.Base,
			Amplitude: rc.Source.Amplitude,
			PeriodMs:  rc.Source.PeriodMs,
			Min:       rc.Source.Min,
			Max:       rc.Source.Max,
		}

		if s.areas[area] == nil {
			s.areas[area] = map[uint16]slot{}
		}
		for idx := 0; idx < reg.RegCount(); idx++ {
			addr := rc.Address + uint16(idx)
			if _, dup := s.areas[area][addr]; dup {
				return nil, fmt.Errorf("站号 %d 的地址 %d 在同一地址空间内重复定义", cfg.DeviceID, addr)
			}
			s.areas[area][addr] = slot{reg: reg, idx: idx}
		}
		s.order = append(s.order, reg)
	}
	return s, nil
}

func isWriteFunction(fn byte) bool {
	switch fn {
	case modbus.FuncWriteSingleCoil, modbus.FuncWriteSingleReg, modbus.FuncWriteMultiple:
		return true
	}
	return false
}

func isReadWrite(access string) bool {
	return access == "readwrite" || access == "READWRITE"
}

// Registers 按配置顺序返回全部寄存器，供界面展示。
func (s *Slave) Registers() []*Register {
	return s.order
}

// Find 按功能码与地址查找寄存器，用于界面手动设置数值。
func (s *Slave) Find(function byte, address uint16) *Register {
	area, ok := AreaOf(function)
	if !ok {
		return nil
	}
	sl, ok := s.areas[area][address]
	if !ok {
		return nil
	}
	return sl.reg
}

// Handle 处理一个请求帧。
//
// 不响应的情形严格对齐真实设备：站号不符、CRC 校验失败、报文结构非法、
// 以及广播地址（广播要执行写操作但不响应）。
func (s *Slave) Handle(frame []byte) Result {
	if len(frame) < 4 {
		return Result{DropReason: "报文长度不足，已丢弃"}
	}

	id := frame[0]
	fn := frame[1]

	if !modbus.CheckCRC(frame) {
		return Result{DropReason: "CRC 校验失败，已丢弃"}
	}

	broadcast := modbus.IsBroadcast(id)
	if !broadcast && id != s.DeviceID {
		return Result{DropReason: fmt.Sprintf("站号 %d 不属于本站（本站 %d），已丢弃", id, s.DeviceID)}
	}

	res := s.dispatch(frame, id, fn)

	if broadcast {
		// 广播不产生响应；写操作已经在 dispatch 中生效。
		res.Response = nil
		res.DropReason = "广播地址，已执行写操作但不响应"
	}
	return res
}

func (s *Slave) dispatch(frame []byte, id, fn byte) Result {
	switch fn {
	case modbus.FuncReadCoils:
		return s.readCoils(frame, id)
	case modbus.FuncReadHoldingRegs, modbus.FuncReadInputRegs:
		return s.readRegisters(frame, id, fn)
	case modbus.FuncWriteSingleCoil:
		return s.writeSingleCoil(frame, id)
	case modbus.FuncWriteSingleReg:
		return s.writeSingleRegister(frame, id)
	case modbus.FuncWriteMultiple:
		return s.writeMultipleRegisters(frame, id)
	default:
		return s.exception(id, fn, modbus.ExcIllegalFunction)
	}
}

func (s *Slave) exception(id, fn, code byte) Result {
	return Result{
		Response:      modbus.ExceptionFrame(id, fn, code),
		Summary:       fmt.Sprintf("%s 响应异常码 0x%02X（%s）", modbus.FunctionText(fn), code, modbus.ExceptionText(code)),
		IsException:   true,
		ExceptionCode: code,
	}
}

// readRegisters 处理 0x03 读保持寄存器与 0x04 读输入寄存器。
func (s *Slave) readRegisters(frame []byte, id, fn byte) Result {
	addr := binary.BigEndian.Uint16(frame[2:4])
	qty := binary.BigEndian.Uint16(frame[4:6])

	if qty < 1 || qty > modbus.MaxReadRegs {
		return s.exception(id, fn, modbus.ExcIllegalValue)
	}

	area, _ := AreaOf(fn)
	table := s.areas[area]

	// 一次性算出每个寄存器当前的值，避免同一帧内跨越周期边界时前后不一致。
	cache := map[*Register][]uint16{}
	now := time.Now()

	regs := make([]uint16, 0, qty)
	for i := uint16(0); i < qty; i++ {
		sl, ok := table[addr+i]
		if !ok {
			if s.UnmappedPolicy == PolicyZero {
				regs = append(regs, 0)
				continue
			}
			return s.exception(id, fn, modbus.ExcIllegalAddress)
		}
		vals, ok := cache[sl.reg]
		if !ok {
			vals = sl.reg.RawRegisters(now)
			cache[sl.reg] = vals
		}
		if sl.idx >= len(vals) {
			return s.exception(id, fn, modbus.ExcSlaveFailure)
		}
		regs = append(regs, vals[sl.idx])
	}

	data := make([]byte, 0, 3+2*len(regs))
	data = append(data, id, fn, byte(len(regs)*2))
	for _, v := range regs {
		data = append(data, byte(v>>8), byte(v))
	}

	return Result{
		Response: modbus.AppendCRC(data),
		Summary: fmt.Sprintf("%s 起始地址=%d 数量=%d 返回=%s",
			modbus.FunctionText(fn), addr, qty, formatRegs(regs)),
	}
}

// readCoils 处理 0x01 读线圈。
func (s *Slave) readCoils(frame []byte, id byte) Result {
	fn := modbus.FuncReadCoils
	addr := binary.BigEndian.Uint16(frame[2:4])
	qty := binary.BigEndian.Uint16(frame[4:6])

	if qty < 1 || qty > modbus.MaxReadCoils {
		return s.exception(id, fn, modbus.ExcIllegalValue)
	}

	table := s.areas[AreaCoil]
	now := time.Now()

	bitsOut := make([]byte, (qty+7)/8)
	for i := uint16(0); i < qty; i++ {
		sl, ok := table[addr+i]
		if !ok {
			if s.UnmappedPolicy == PolicyZero {
				continue
			}
			return s.exception(id, fn, modbus.ExcIllegalAddress)
		}
		vals := sl.reg.RawRegisters(now)
		v := uint16(0)
		if sl.idx < len(vals) {
			v = vals[sl.idx]
		}
		if v != 0 {
			bitsOut[i/8] |= 1 << (i % 8)
		}
	}

	data := make([]byte, 0, 3+len(bitsOut))
	data = append(data, id, fn, byte(len(bitsOut)))
	data = append(data, bitsOut...)

	return Result{
		Response: modbus.AppendCRC(data),
		Summary:  fmt.Sprintf("%s 起始地址=%d 数量=%d 返回=%s", modbus.FunctionText(fn), addr, qty, modbus.Hex(bitsOut)),
	}
}

// writeSingleCoil 处理 0x05 写单线圈。
func (s *Slave) writeSingleCoil(frame []byte, id byte) Result {
	fn := modbus.FuncWriteSingleCoil
	addr := binary.BigEndian.Uint16(frame[2:4])
	value := binary.BigEndian.Uint16(frame[4:6])

	if value != 0xFF00 && value != 0x0000 {
		return s.exception(id, fn, modbus.ExcIllegalValue)
	}

	sl, ok := s.areas[AreaCoil][addr]
	if !ok || !sl.reg.Writable {
		return s.exception(id, fn, modbus.ExcIllegalAddress)
	}

	var v float64
	if value == 0xFF00 {
		v = 1
	}
	sl.reg.Write(v)

	return Result{
		Response: modbus.AppendCRC(append([]byte{}, frame[:6]...)),
		Summary:  fmt.Sprintf("%s 地址=%d 值=%s", modbus.FunctionText(fn), addr, onOffText(value == 0xFF00)),
	}
}

// writeSingleRegister 处理 0x06 写单寄存器。
func (s *Slave) writeSingleRegister(frame []byte, id byte) Result {
	fn := modbus.FuncWriteSingleReg
	addr := binary.BigEndian.Uint16(frame[2:4])
	value := binary.BigEndian.Uint16(frame[4:6])

	sl, ok := s.areas[AreaHolding][addr]
	if !ok || !sl.reg.Writable {
		return s.exception(id, fn, modbus.ExcIllegalAddress)
	}

	sl.reg.Write(sl.reg.DecodeWrite([]uint16{value}))

	return Result{
		Response: modbus.AppendCRC(append([]byte{}, frame[:6]...)),
		Summary:  fmt.Sprintf("%s 地址=%d 原始值=0x%04X 换算值=%s", modbus.FunctionText(fn), addr, value, formatFloat(sl.reg.Value(time.Now()))),
	}
}

// writeMultipleRegisters 处理 0x10 写多寄存器。
func (s *Slave) writeMultipleRegisters(frame []byte, id byte) Result {
	fn := modbus.FuncWriteMultiple
	addr := binary.BigEndian.Uint16(frame[2:4])
	qty := binary.BigEndian.Uint16(frame[4:6])
	byteCount := int(frame[6])

	if qty < 1 || qty > modbus.MaxWriteRegs {
		return s.exception(id, fn, modbus.ExcIllegalValue)
	}
	if byteCount != int(qty)*2 || len(frame) < 7+byteCount+2 {
		return s.exception(id, fn, modbus.ExcIllegalValue)
	}

	table := s.areas[AreaHolding]

	// 先整帧校验可写性，避免写了一半才失败。
	type target struct {
		reg *Register
		idx int
	}
	targets := make([]target, 0, qty)
	for i := uint16(0); i < qty; i++ {
		sl, ok := table[addr+i]
		if !ok || !sl.reg.Writable {
			return s.exception(id, fn, modbus.ExcIllegalAddress)
		}
		targets = append(targets, target{reg: sl.reg, idx: sl.idx})
	}

	// 按寄存器聚合写入值：32 位寄存器需要把两个 16 位字合起来再换算。
	grouped := map[*Register]map[int]uint16{}
	for i := uint16(0); i < qty; i++ {
		raw := binary.BigEndian.Uint16(frame[7+int(i)*2 : 9+int(i)*2])
		t := targets[i]
		if grouped[t.reg] == nil {
			grouped[t.reg] = map[int]uint16{}
		}
		grouped[t.reg][t.idx] = raw
	}

	for reg, words := range grouped {
		n := reg.RegCount()
		vals := make([]uint16, n)
		for idx, w := range words {
			if idx < n {
				vals[idx] = w
			}
		}
		reg.Write(reg.DecodeWrite(vals))
	}

	data := append([]byte{}, frame[:6]...)
	return Result{
		Response: modbus.AppendCRC(data),
		Summary:  fmt.Sprintf("%s 起始地址=%d 数量=%d", modbus.FunctionText(fn), addr, qty),
	}
}

func onOffText(on bool) string {
	if on {
		return "ON"
	}
	return "OFF"
}

func formatRegs(regs []uint16) string {
	out := "["
	for i, v := range regs {
		if i > 0 {
			out += " "
		}
		out += fmt.Sprintf("%d", v)
	}
	return out + "]"
}

func formatFloat(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%.4f", v)
}
