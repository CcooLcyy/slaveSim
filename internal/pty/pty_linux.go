//go:build linux

// Package pty 在 Linux 上创建伪终端（PTY）并把它作为稳定的虚拟串口暴露出去。
//
// 只使用标准库 syscall，不引入第三方依赖，便于在无网络的构建容器中编译。
package pty

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// ioctl 请求号。这些值在 Linux 的 amd64 与 arm64 上一致。
const (
	ioctlTIOCGPTN   = 0x80045430 // 取得 PTY 从端编号
	ioctlTIOCSPTLCK = 0x40045431 // 解锁 PTY 从端
	ioctlTCGETS     = 0x5401     // 读取终端属性
	ioctlTCSETS     = 0x5402     // 设置终端属性
)

// Master 表示已创建的一对 PTY 的主端。
type Master struct {
	file      *os.File
	SlavePath string // 从端真实设备路径，形如 /dev/pts/3
	LinkPath  string // 对外暴露的稳定软链接路径
}

// Open 创建一对 PTY，把从端设置为 raw 模式，并建立稳定软链接。
//
// raw 模式是必须的：否则 tty 行规程会回显输入、转换 CR/LF，并把
// 0x03/0x11/0x13/0x1A 等字节当作控制字符处理，直接破坏二进制 Modbus 帧。
func Open(linkPath string) (*Master, error) {
	// 非阻塞描述符让 os.File 使用 Go 的 poller，Close 可唤醒空闲 Read/Write。
	fd, err := syscall.Open("/dev/ptmx", syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("打开 /dev/ptmx 失败: %w", err)
	}

	ok := false
	defer func() {
		if !ok {
			_ = syscall.Close(fd)
		}
	}()

	var unlock int32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(ioctlTIOCSPTLCK), uintptr(unsafe.Pointer(&unlock))); errno != 0 {
		return nil, fmt.Errorf("解锁 PTY 从端失败: %w", errno)
	}

	var num uint32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(ioctlTIOCGPTN), uintptr(unsafe.Pointer(&num))); errno != 0 {
		return nil, fmt.Errorf("获取 PTY 从端编号失败: %w", errno)
	}
	slavePath := fmt.Sprintf("/dev/pts/%d", num)

	if err := setRaw(fd); err != nil {
		return nil, err
	}

	if linkPath != "" {
		if err := symlink(slavePath, linkPath); err != nil {
			return nil, err
		}
	}

	m := &Master{
		file:      os.NewFile(uintptr(fd), slavePath),
		SlavePath: slavePath,
		LinkPath:  linkPath,
	}
	ok = true
	return m, nil
}

// File 返回主端文件，向外写即相当于从端串口发出数据。
func (m *Master) File() *os.File {
	return m.file
}

// Close 关闭主端并清理软链接。
func (m *Master) Close() error {
	var cleanupErr error
	if m.LinkPath != "" {
		target, err := os.Readlink(m.LinkPath)
		if err != nil && !os.IsNotExist(err) {
			cleanupErr = fmt.Errorf("检查串口软链接 %s 失败: %w", m.LinkPath, err)
		}
		if err == nil && target == m.SlavePath {
			if err := os.Remove(m.LinkPath); err != nil && !os.IsNotExist(err) {
				cleanupErr = fmt.Errorf("删除串口软链接 %s 失败: %w", m.LinkPath, err)
			}
		}
	}
	if m.file != nil {
		return errors.Join(cleanupErr, m.file.Close())
	}
	return cleanupErr
}

// setRaw 把终端设置为 raw 模式。
//
// 对主端调用 tcsetattr 即作用于从端的行规程，效果等价于 C 语言里的 cfmakeraw。
func setRaw(fd int) error {
	var t syscall.Termios
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(ioctlTCGETS), uintptr(unsafe.Pointer(&t))); errno != 0 {
		return fmt.Errorf("读取终端属性失败: %w", errno)
	}

	t.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP |
		syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	t.Oflag &^= syscall.OPOST
	t.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	t.Cflag &^= syscall.CSIZE | syscall.PARENB
	t.Cflag |= syscall.CS8
	t.Cc[syscall.VMIN] = 1
	t.Cc[syscall.VTIME] = 0

	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(ioctlTCSETS), uintptr(unsafe.Pointer(&t))); errno != 0 {
		return fmt.Errorf("设置终端 raw 模式失败: %w", errno)
	}
	return nil
}

// symlink 只创建新链接，避免覆盖不属于本进程的文件或其他串口。
func symlink(slavePath, linkPath string) error {
	if err := os.Symlink(slavePath, linkPath); err != nil {
		return fmt.Errorf("创建软链接 %s -> %s 失败: %w", linkPath, slavePath, err)
	}
	return nil
}
