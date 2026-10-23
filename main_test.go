package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"slavesim/internal/eventbus"
)

type failingLogFile struct {
	fail bool
	bytes.Buffer
}

func (f *failingLogFile) Write(p []byte) (int, error) {
	if f.fail {
		return 0, errors.New("模拟磁盘写入失败")
	}
	return f.Buffer.Write(p)
}

func (f *failingLogFile) Close() error { return nil }

// 验证文件故障不会中断标准输出，连续故障每分钟最多报告一次，恢复后继续落盘。
func TestConsoleLoggerReportsFailuresWithoutDroppingStdout(t *testing.T) {
	var output, diagnostics bytes.Buffer
	file := &failingLogFile{fail: true}
	cl := &consoleLogger{file: file, output: &output, diagnostics: &diagnostics}
	now := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	for _, elapsed := range []time.Duration{0, time.Second, 59 * time.Second, time.Minute} {
		cl.writeLine("完整报文", now.Add(elapsed))
	}
	if strings.Count(output.String(), "完整报文\n") != 4 {
		t.Fatalf("文件故障期间标准输出不完整: %q", output.String())
	}
	if strings.Count(diagnostics.String(), "模拟磁盘写入失败") != 2 {
		t.Fatalf("错误提示未限频: %q", diagnostics.String())
	}
	file.fail = false
	cl.writeLine("恢复报文", now.Add(61*time.Second))
	if file.Buffer.String() != "恢复报文\n" {
		t.Fatalf("恢复后未落盘: %q", file.Buffer.String())
	}
}

// 验证未启用文件日志时仍完整输出到标准输出。
func TestConsoleLoggerStdoutOnly(t *testing.T) {
	var output, diagnostics bytes.Buffer
	cl := &consoleLogger{output: &output, diagnostics: &diagnostics}
	cl.writeLine("标准输出报文", time.Now())
	if output.String() != "标准输出报文\n" || diagnostics.Len() != 0 {
		t.Fatalf("仅标准输出模式异常: %q %q", output.String(), diagnostics.String())
	}
}

// 验证大小参数不会溢出，保留数量允许零但不能为负。
func TestValidateLogPolicy(t *testing.T) {
	for _, tc := range []struct {
		size    int64
		backups int
		valid   bool
	}{{50, 5, true}, {1, 0, true}, {8796093022207, 1, true}, {0, 5, false}, {-1, 5, false}, {50, -1, false}, {8796093022208, 5, false}} {
		if err := validateLogPolicy(tc.size, tc.backups); (err == nil) != tc.valid {
			t.Fatalf("大小=%d 保留数=%d 校验结果=%v", tc.size, tc.backups, err)
		}
	}
}

// 验证停止日志输出时排空订阅缓冲并关闭文件，退出前的日志不会遗漏。
func TestConsoleLoggerDrainsOnStop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sim.log")
	logs := eventbus.NewLog(10)
	stop, err := startConsoleLogger(logs, path, 1024, 2)
	if err != nil {
		t.Fatal(err)
	}
	logs.Publish(eventbus.Event{Summary: "退出前的完整报文"})
	stop()
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "退出前的完整报文") {
		t.Fatalf("退出日志未落盘: %q, %v", data, err)
	}
}
