// probe 是一个极简的 Modbus RTU 主站，用于验证 slaveSim 的虚拟串口
// 能否被外部程序正常收发。
//
// 它走的是和下位机完全相同的路径：打开一个串口设备文件，写入请求帧，读回响应帧。
//
// 用法示例：
//
//	probe -device /dev/pts/10 -id 1 -func 4 -addr 3004 -qty 2
//	probe -device /dev/pts/10 -id 1 -func 6 -addr 40120 -write 1234
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"slavesim/internal/modbus"
)

func main() {
	device := flag.String("device", "", "串口设备路径，例如 /dev/pts/10")
	deviceID := flag.Int("id", 1, "站号")
	function := flag.Int("func", 3, "功能码")
	address := flag.Int("addr", 0, "起始地址")
	quantity := flag.Int("qty", 1, "数量")
	writeValue := flag.Int("write", -1, "写单寄存器的值，配合 -func 6 使用")
	timeout := flag.Duration("timeout", 1500*time.Millisecond, "等待响应的超时")
	flag.Parse()

	if *device == "" {
		fmt.Println("必须指定 -device")
		flag.Usage()
		os.Exit(2)
	}

	f, err := os.OpenFile(*device, os.O_RDWR, 0)
	if err != nil {
		fmt.Printf("打开设备 %s 失败: %v\n", *device, err)
		os.Exit(1)
	}
	defer f.Close()

	var body []byte
	if *writeValue >= 0 {
		body = []byte{
			byte(*deviceID), byte(*function),
			byte(*address >> 8), byte(*address),
			byte(*writeValue >> 8), byte(*writeValue),
		}
	} else {
		body = []byte{
			byte(*deviceID), byte(*function),
			byte(*address >> 8), byte(*address),
			byte(*quantity >> 8), byte(*quantity),
		}
	}
	req := modbus.AppendCRC(body)
	fmt.Printf("请求: %s\n", modbus.Hex(req))

	if _, err := f.Write(req); err != nil {
		fmt.Printf("写入失败: %v\n", err)
		os.Exit(1)
	}

	type readResult struct {
		data []byte
		err  error
	}
	ch := make(chan readResult, 1)
	go func() {
		buf := make([]byte, 512)
		n, err := f.Read(buf)
		if err != nil {
			ch <- readResult{err: err}
			return
		}
		out := make([]byte, n)
		copy(out, buf[:n])
		ch <- readResult{data: out}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			fmt.Printf("读取失败: %v\n", r.err)
			os.Exit(1)
		}
		fmt.Printf("响应: %s\n", modbus.Hex(r.data))
		if !modbus.CheckCRC(r.data) {
			fmt.Println("响应 CRC 校验失败")
			os.Exit(1)
		}
		if len(r.data) >= 2 && r.data[1]&0x80 != 0 {
			code := byte(0)
			if len(r.data) >= 3 {
				code = r.data[2]
			}
			fmt.Printf("从站返回异常码 0x%02X（%s）\n", code, modbus.ExceptionText(code))
			os.Exit(4)
		}
		fmt.Println("响应 CRC 校验通过")
	case <-time.After(*timeout):
		fmt.Println("读取超时：从站没有响应（站号不符、CRC 错误或地址未配置时，真机行为就是不响应）")
		os.Exit(3)
	}
}
