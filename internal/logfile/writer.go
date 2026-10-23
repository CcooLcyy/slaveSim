// Package logfile 提供单进程独占的有界日志文件写入器。
package logfile

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Writer 按大小轮转日志，历史文件 .1 最新。调用方负责串行调用。
type Writer struct {
	path       string
	maxBytes   int64
	backups    int
	file       *os.File
	size       int64
	closed     bool
	rotation   *rotation
	renameFile func(string, string) error
}

// rotation 记录已完成的步骤，重试时不会再次移动已经归档的历史文件。
type rotation struct {
	next  int
	steps []rotationStep
}

type rotationStep struct{ source, target string }

// New 打开日志，清理超出保留数量的历史普通文件，并轮转已达到阈值的当前文件。
// 自动创建父目录，不跟随当前文件或参与轮转的历史软链接。
func New(path string, maxBytes int64, backups int) (*Writer, error) {
	if path == "" {
		return nil, fmt.Errorf("日志路径不能为空")
	}
	if maxBytes <= 0 {
		return nil, fmt.Errorf("日志文件大小限制必须为正数")
	}
	if backups < 0 {
		return nil, fmt.Errorf("日志历史数量不能为负数")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("创建日志目录失败: %w", err)
	}
	w := &Writer{path: path, maxBytes: maxBytes, backups: backups, renameFile: os.Rename}
	if err := w.open(false); err != nil {
		return nil, err
	}
	if err := w.cleanup(); err != nil {
		_ = w.Close()
		return nil, err
	}
	if w.size >= w.maxBytes {
		if err := w.rotate(); err != nil {
			_ = w.Close()
			return nil, err
		}
	}
	return w, nil
}

func regular(path string) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("检查日志文件 %q 失败: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("日志路径 %q 不是普通文件", path)
	}
	return true, nil
}

func (w *Writer) open(truncate bool) error {
	if _, err := regular(w.path); err != nil {
		return err
	}
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if truncate {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(w.path, flags, 0644)
	if err != nil {
		return fmt.Errorf("打开日志文件 %q 失败: %w", w.path, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("读取日志大小 %q 失败: %w", w.path, err)
	}
	w.file, w.size = f, info.Size()
	return nil
}

// cleanup 只识别 .1、.2 等规范正整数后缀；目录、软链接及其他名称均不删除。
func (w *Writer) cleanup() error {
	dir, base := filepath.Dir(w.path), filepath.Base(w.path)+"."
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("读取日志目录 %q 失败: %w", dir, err)
	}
	limit := strconv.Itoa(w.backups)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), base) {
			continue
		}
		suffix := strings.TrimPrefix(entry.Name(), base)
		if !canonicalNumber(suffix) || (len(suffix) < len(limit) || len(suffix) == len(limit) && suffix <= limit) {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("检查历史日志 %q 失败: %w", entry.Name(), err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("清理历史日志 %q 失败: %w", path, err)
		}
	}
	return nil
}

func canonicalNumber(s string) bool {
	if s == "" || s[0] < '1' || s[0] > '9' {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func (w *Writer) history(index int) string { return w.path + "." + strconv.Itoa(index) }

func (w *Writer) planRotation() (*rotation, error) {
	if _, err := regular(w.path); err != nil {
		return nil, err
	}
	r := &rotation{}
	if w.backups == 0 {
		return r, nil
	}
	entries, err := os.ReadDir(filepath.Dir(w.path))
	if err != nil {
		return nil, fmt.Errorf("读取轮转日志目录失败: %w", err)
	}
	base, limit := filepath.Base(w.path)+".", strconv.Itoa(w.backups)
	var indices []int
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), base) {
			continue
		}
		suffix := strings.TrimPrefix(entry.Name(), base)
		if !canonicalNumber(suffix) || len(suffix) > len(limit) || len(suffix) == len(limit) && suffix > limit {
			continue
		}
		index, err := strconv.Atoi(suffix)
		if err != nil {
			return nil, fmt.Errorf("解析历史日志编号 %q 失败: %w", suffix, err)
		}
		if _, err := regular(w.history(index)); err != nil {
			return nil, err
		}
		indices = append(indices, index)
	}
	// 只遍历实际存在的历史文件，极大的保留数不会造成空槽位遍历。
	sort.Sort(sort.Reverse(sort.IntSlice(indices)))
	for _, index := range indices {
		step := rotationStep{source: w.history(index)}
		if index < w.backups {
			step.target = w.history(index + 1)
		}
		r.steps = append(r.steps, step)
	}
	r.steps = append(r.steps, rotationStep{source: w.path, target: w.history(1)})
	return r, nil
}

func (w *Writer) rotate() error {
	if w.rotation == nil {
		// 先检查所有参与轮转的路径，避免目录或软链接导致常规错误时提前删除历史。
		r, err := w.planRotation()
		if err != nil {
			return err
		}
		w.rotation = r
	}
	if w.file != nil {
		f := w.file
		w.file = nil
		if err := f.Close(); err != nil {
			return fmt.Errorf("轮转前关闭日志 %q 失败: %w", w.path, err)
		}
	}
	if w.backups == 0 {
		if err := w.open(true); err != nil {
			return err
		}
		w.rotation = nil
		return nil
	}
	for w.rotation.next < len(w.rotation.steps) {
		step := w.rotation.steps[w.rotation.next]
		if step.target == "" {
			exists, err := regular(step.source)
			if err != nil {
				return err
			}
			if exists {
				if err := os.Remove(step.source); err != nil {
					return fmt.Errorf("删除最旧日志 %q 失败: %w", step.source, err)
				}
			}
		} else {
			exists, err := regular(step.source)
			if err != nil {
				return err
			}
			if exists {
				occupied, err := regular(step.target)
				if err != nil {
					return err
				}
				if occupied {
					return fmt.Errorf("轮转目标日志 %q 已存在", step.target)
				}
				if err := w.renameFile(step.source, step.target); err != nil {
					return fmt.Errorf("轮转日志 %q 至 %q 失败: %w", step.source, step.target, err)
				}
			}
		}
		w.rotation.next++
	}
	if err := w.open(false); err != nil {
		return err
	}
	w.rotation = nil
	return nil
}

// Write 完整保留单次写入；超大单条拒绝写入，轮转失败后不继续追加。
func (w *Writer) Write(p []byte) (int, error) {
	if w.closed {
		return 0, fmt.Errorf("日志写入器已关闭")
	}
	if int64(len(p)) > w.maxBytes {
		return 0, fmt.Errorf("单条日志大小 %d 超过文件限制 %d", len(p), w.maxBytes)
	}
	if len(p) == 0 {
		return 0, nil
	}
	if w.rotation != nil {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	if w.file == nil {
		if err := w.open(false); err != nil {
			return 0, err
		}
	}
	if w.size > w.maxBytes-int64(len(p)) {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err != nil {
		_ = w.file.Close()
		w.file = nil
		return n, fmt.Errorf("写入日志文件 %q 失败: %w", w.path, err)
	}
	return n, nil
}

// Close 可以重复调用；关闭后拒绝写入。
func (w *Writer) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	if w.file == nil {
		return nil
	}
	f := w.file
	w.file = nil
	if err := f.Close(); err != nil {
		return fmt.Errorf("关闭日志文件 %q 失败: %w", w.path, err)
	}
	return nil
}
