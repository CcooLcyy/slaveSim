package sim

import (
	"bytes"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"slavesim/internal/config"
	"slavesim/internal/modbus"
)

func TestQVoltageModbusFeedback(t *testing.T) {
	cfg, err := config.Load(filepath.Join("..", "..", "config.q-voltage.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	sl, err := NewSlave(cfg.Buses[0].Slaves[0], start)
	if err != nil {
		t.Fatal(err)
	}

	const (
		voltageAddr = 0
		actualQAddr = 1
		commandAddr = 2
	)
	readFeedback := func(at time.Time, voltage uint16, actualQ, command int16) {
		t.Helper()
		res := sl.HandleAt(req(1, modbus.FuncReadHoldingRegs, voltageAddr, 3), at)
		if res.IsException || len(res.Response) < 2 || res.Response[0] != 1 ||
			res.Response[1] != modbus.FuncReadHoldingRegs || !modbus.CheckCRC(res.Response) {
			t.Fatalf("读取无功和电压失败: 响应=% X, %s %s", res.Response, res.Summary, res.DropReason)
		}
		want := []uint16{voltage, uint16(actualQ), uint16(command)}
		if got := respRegs(t, res.Response); !slices.Equal(got, want) {
			t.Fatalf("%s 测量和命令计数不符: 得到 %v，期望 %v", at, got, want)
		}
	}
	writeSingle := func(at time.Time, command int16) {
		t.Helper()
		frame := req(1, modbus.FuncWriteSingleReg, commandAddr, uint16(command))
		if res := sl.HandleAt(frame, at); !bytes.Equal(res.Response, frame) {
			t.Fatalf("写入无功命令 %d 失败: 响应=% X, %s %s", command, res.Response, res.Summary, res.DropReason)
		}
	}

	readFeedback(start, 2200, 0, 0)
	injectAt := start.Add(2 * time.Second)
	writeSingle(injectAt, 600) // 请求注入 60 kvar。
	readFeedback(injectAt, 2200, 0, 600)

	// 斜坡中实际无功为 50 kvar，电压为 232.5 V；命令仍为 60 kvar。
	midRamp := injectAt.Add(500 * time.Millisecond)
	sample := sl.Sample(midRamp)
	if got, want := sample.Values, []float64{2325, 500, 600}; !slices.Equal(got, want) {
		t.Fatalf("斜坡中的采样计数不符: 得到 %v，期望 %v", got, want)
	}
	readFeedback(midRamp, 2325, 500, 600)
	absorbAt := injectAt.Add(time.Second)
	readFeedback(absorbAt, 2350, 600, 600)

	// 使用写多寄存器帧请求吸收 130 kvar，实际无功最多吸收到 -100 kvar。
	absorb := int16(-1300)
	res := modelWrite(sl, absorbAt, commandAddr, uint16(absorb))
	if want := req(1, modbus.FuncWriteMultiple, commandAddr, 1); !bytes.Equal(res.Response, want) {
		t.Fatalf("写入吸收无功命令失败: 响应=% X, %s %s", res.Response, res.Summary, res.DropReason)
	}
	readFeedback(absorbAt, 2350, 600, -1300)
	// 检查调度推进产生的原始值，避免后续 Modbus 读取推进模型而掩盖缺失的 Step。
	tick := absorbAt.Add(time.Second)
	sl.Step(tick)
	if got := int16(sl.Find(modbus.FuncReadHoldingRegs, actualQAddr).RawRegisters(tick)[0]); got != -400 {
		t.Fatalf("无轮询推进 1 s 后的无功计数: 得到 %d，期望 -400", got)
	}
	readFeedback(tick, 2100, -400, -1300)
	reverseAt := absorbAt.Add(3 * time.Second)
	readFeedback(reverseAt, 1950, -1000, -1300)

	writeSingle(reverseAt, 1200)
	limitedAt := reverseAt.Add(3 * time.Second)
	readFeedback(limitedAt, 2450, 1000, 1200)
	for _, addr := range []uint16{voltageAddr, actualQAddr} {
		if err := sl.SetPoint(modbus.FuncReadHoldingRegs, addr, 42, limitedAt); err == nil {
			t.Fatalf("不应允许手工覆盖派生量测地址 %d", addr)
		}
	}
	readFeedback(limitedAt, 2450, 1000, 1200)
}
