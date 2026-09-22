// slaveSim 是一个 Modbus RTU 从站模拟器。
//
// 它通过 PTY 创建虚拟串口设备，供下位机以 TRANSPORT_SERIAL 方式直连，
// 从而在没有真实设备时复现现场环境。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	// 把时区数据库编进二进制，运行时镜像因此可以小到 scratch。
	_ "time/tzdata"

	"slavesim/internal/config"
	"slavesim/internal/eventbus"
	"slavesim/internal/sim"
	"slavesim/internal/web"
)

func main() {
	configPath := flag.String("config", "/etc/slavesim/config.json", "配置文件路径")
	httpAddr := flag.String("http", ":16000", "WebUI 监听地址")
	logFile := flag.String("logfile", "", "报文日志落盘路径，留空表示只输出到标准输出")
	flag.Parse()

	if err := run(*configPath, *httpAddr, *logFile); err != nil {
		log.Printf("[slaveSim] 启动失败: %v", err)
		os.Exit(1)
	}
}

func run(configPath, httpAddr, logFile string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	logs := eventbus.NewLog(1000)
	if err := startConsoleLogger(logs, logFile); err != nil {
		return err
	}

	now := time.Now()
	var buses []*sim.Bus

	for _, bc := range cfg.Buses {
		slaves := make([]*sim.Slave, 0, len(bc.Slaves))
		for _, sc := range bc.Slaves {
			sl, err := sim.NewSlave(sc, now)
			if err != nil {
				return fmt.Errorf("总线 %s 构造从站 %d 失败: %w", bc.Name, sc.DeviceID, err)
			}
			slaves = append(slaves, sl)
		}

		bus := sim.NewBus(bc.Name, bc.Link, slaves, logs)
		if err := os.MkdirAll(filepath.Dir(bc.Link), 0o755); err != nil {
			return fmt.Errorf("创建软链接目录 %s 失败: %w", filepath.Dir(bc.Link), err)
		}
		if err := bus.Open(); err != nil {
			return err
		}
		buses = append(buses, bus)
	}

	for _, b := range buses {
		b.Start()
		logs.Publish(eventbus.Event{
			Level:   eventbus.LevelInfo,
			Bus:     b.Name,
			Summary: fmt.Sprintf("虚拟串口已就绪: 软链接=%s 实际设备=%s 从站数=%d", b.LinkPath, b.SlavePath(), len(b.Slaves())),
		})
	}

	srv := &http.Server{
		Addr:              httpAddr,
		Handler:           newHandler(buses, logs),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logs.Publish(eventbus.Event{
			Level:   eventbus.LevelInfo,
			Summary: fmt.Sprintf("WebUI 已启动: http://0.0.0.0%s", httpAddr),
		})
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("WebUI 启动失败: %w", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		closeBuses(buses, logs)
		return err
	case sig := <-stop:
		logs.Publish(eventbus.Event{Level: eventbus.LevelInfo, Summary: fmt.Sprintf("收到信号 %v，正在退出", sig)})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	closeBuses(buses, logs)
	return nil
}

func closeBuses(buses []*sim.Bus, logs *eventbus.Log) {
	for _, b := range buses {
		if err := b.Close(); err != nil {
			logs.Publish(eventbus.Event{Level: eventbus.LevelWarn, Bus: b.Name, Summary: fmt.Sprintf("关闭总线失败: %v", err)})
		}
	}
}

// consoleLogger 把日志同时输出到标准输出与可选的文件。
type consoleLogger struct {
	file *os.File
}

func startConsoleLogger(logs *eventbus.Log, logFile string) error {
	cl := &consoleLogger{}
	if logFile != "" {
		if err := os.MkdirAll(filepath.Dir(logFile), 0o755); err != nil {
			return fmt.Errorf("创建日志目录失败: %w", err)
		}
		f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("打开日志文件 %s 失败: %w", logFile, err)
		}
		cl.file = f
	}

	ch, _ := logs.Subscribe()
	go func() {
		for e := range ch {
			line := formatEvent(e)
			fmt.Println(line)
			if cl.file != nil {
				_, _ = cl.file.WriteString(line + "\n")
			}
		}
	}()
	return nil
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

// newHandler 组装 WebUI 的 HTTP 处理器。
func newHandler(buses []*sim.Bus, logs *eventbus.Log) http.Handler {
	return web.New(buses, logs).Handler()
}
