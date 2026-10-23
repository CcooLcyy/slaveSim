// slaveSim 是一个 Modbus RTU 从站模拟器。
//
// 它通过 PTY 创建虚拟串口设备，供下位机以 TRANSPORT_SERIAL 方式直连，
// 从而在没有真实设备时复现现场环境。
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	// 把时区数据库编进二进制，运行时镜像因此可以小到 scratch。
	_ "time/tzdata"

	"slavesim/internal/config"
	"slavesim/internal/eventbus"
	"slavesim/internal/logfile"
	"slavesim/internal/sim"
	"slavesim/internal/web"
)

func main() {
	configPath := flag.String("config", "/etc/slavesim/config.json", "配置文件路径")
	httpAddr := flag.String("http", ":16000", "WebUI 监听地址")
	logFile := flag.String("logfile", "", "报文日志落盘路径，留空表示只输出到标准输出")
	logMaxSizeMiB := flag.Int64("log-max-size-mib", 50, "单个日志文件大小上限（MiB，必须大于零）")
	logBackups := flag.Int("log-backups", 5, "历史日志文件保留数量（允许为零）")
	showVersion := flag.Bool("version", false, "显示版本信息并退出")
	flag.Parse()
	if *showVersion {
		fmt.Printf("slaveSim %s 提交=%s 构建时间=%s\n", version, commit, buildTime)
		return
	}

	if err := run(*configPath, *httpAddr, *logFile, *logMaxSizeMiB, *logBackups); err != nil {
		log.Printf("[slaveSim] 启动失败: %v", err)
		os.Exit(1)
	}
}

func validateLogPolicy(maxSizeMiB int64, backups int) error {
	if maxSizeMiB <= 0 || maxSizeMiB > 8796093022207 {
		return fmt.Errorf("日志文件大小必须为 1 到 8796093022207 MiB")
	}
	if backups < 0 {
		return fmt.Errorf("历史日志文件保留数量不能为负数")
	}
	return nil
}

func run(configPath, httpAddr, logFile string, maxSizeMiB int64, backups int) error {
	if err := validateLogPolicy(maxSizeMiB, backups); err != nil {
		return err
	}
	log.Printf("[slaveSim] 版本=%s 提交=%s 构建时间=%s", version, commit, buildTime)
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	logs := eventbus.NewLog(1000)
	stopLogger, err := startConsoleLogger(logs, logFile, maxSizeMiB<<20, backups)
	if err != nil {
		return err
	}
	defer stopLogger()
	if logFile != "" {
		logs.Publish(eventbus.Event{Level: eventbus.LevelInfo, Summary: fmt.Sprintf("文件日志已启用: 路径=%s 单文件上限=%d MiB 历史保留=%d 个", logFile, maxSizeMiB, backups)})
	}

	manager, err := sim.NewManager(configPath, cfg, logs)
	if err != nil {
		return err
	}
	defer manager.Close()

	srv := &http.Server{
		Addr:              httpAddr,
		Handler:           web.New(manager, logs).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		displayAddr := httpAddr
		if strings.HasPrefix(displayAddr, ":") {
			displayAddr = "0.0.0.0" + displayAddr
		}
		logs.Publish(eventbus.Event{
			Level:   eventbus.LevelInfo,
			Summary: fmt.Sprintf("WebUI 已启动: http://%s", displayAddr),
		})
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("WebUI 启动失败: %w", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return err
	case sig := <-stop:
		logs.Publish(eventbus.Event{Level: eventbus.LevelInfo, Summary: fmt.Sprintf("收到信号 %v，正在退出", sig)})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	return nil
}

// consoleLogger 把日志同时输出到标准输出与可选的文件。
type consoleLogger struct {
	file        io.WriteCloser
	output      io.Writer
	diagnostics io.Writer
	lastError   time.Time
}

// writeLine 保留标准输出；文件故障独立报告，连续故障最多每分钟提示一次。
func (cl *consoleLogger) writeLine(line string, now time.Time) {
	fmt.Fprintln(cl.output, line)
	if cl.file == nil {
		return
	}
	if _, err := cl.file.Write([]byte(line + "\n")); err != nil {
		if cl.lastError.IsZero() || now.Sub(cl.lastError) >= time.Minute {
			fmt.Fprintf(cl.diagnostics, "%s [error] [slaveSim] 写入文件日志失败，标准输出继续保留（连续故障每分钟最多提示一次）: %v\n", now.Format("2006-01-02 15:04:05"), err)
			cl.lastError = now
		}
	}
}

func startConsoleLogger(logs *eventbus.Log, logFile string, maxBytes int64, backups int) (func(), error) {
	cl := &consoleLogger{output: os.Stdout, diagnostics: os.Stderr}
	if logFile != "" {
		f, err := logfile.New(logFile, maxBytes, backups)
		if err != nil {
			return nil, err
		}
		cl.file = f
	}

	ch, _ := logs.Subscribe()
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case e := <-ch:
				cl.writeLine(formatEvent(e), time.Now())
			case <-stop:
				// 生产者退出后排空已接收日志，再关闭文件。
				for {
					select {
					case e := <-ch:
						cl.writeLine(formatEvent(e), time.Now())
					default:
						return
					}
				}
			}
		}
	}()
	return func() {
		close(stop)
		<-done
		if cl.file != nil {
			if err := cl.file.Close(); err != nil {
				log.Printf("[slaveSim] 关闭日志文件失败: %v", err)
			}
		}
	}, nil
}

// formatEvent 按仓库既有日志风格输出：时间 + 级别 + 模块 + 中文描述 + 十六进制报文。
func formatEvent(e eventbus.Event) string {
	level := e.Level
	if level == "" {
		level = eventbus.LevelInfo
	}

	action := "运行信息"
	switch {
	case e.Direction == eventbus.DirectionRx:
		action = "报文接收"
	case e.Direction == eventbus.DirectionTx:
		action = "报文发送"
	}

	line := fmt.Sprintf("%s [%-5s] [slaveSim] %s:", e.Time.Format("2006-01-02 15:04:05"), level, action)
	if e.Bus != "" {
		line += fmt.Sprintf(" 总线=%s,", e.Bus)
	}
	if e.DeviceID > 0 {
		line += fmt.Sprintf(" 站号=%d,", e.DeviceID)
	}
	if e.Frame != "" {
		line += fmt.Sprintf(" 数据=%s,", e.Frame)
	}
	if e.Summary != "" {
		line += " " + e.Summary
	}
	return line
}
