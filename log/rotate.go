package log

import (
	"io"
	"os"
	"sync"
)

// 文件日志轮转 writer（fork 新增，不入上游）。
//
// 语义：单写者，Write 并发安全（log 包原有实现多 goroutine 直写 *os.File
// 无锁，本 writer 顺带修掉了行撕裂）。超限时按 close → 滚动 .1/.2/… → 重开
// 的顺序轮转：Windows 上 Go os.OpenFile 默认不带 FILE_SHARE_DELETE，
// 打开中的文件 rename 必失败（共享冲突），因此必须先关闭自己持有的句柄
// 再 rename，期间其他读者（Flutter 查看器）不受影响——dart:io 的读句柄
// 带 FILE_SHARE_DELETE，不会挡 rename。
//
// 大小计数用"打开时 stat + 累计写入字节"，避免每行一次 stat 系统调用；
// 外部 truncate（设置页原地清空）会造成计数偏大 → 一次提前轮转，无害。
type rotatingWriter struct {
	mu     sync.Mutex
	file   *os.File
	path   string
	open   func(path string, flag int, perm os.FileMode) (*os.File, error)
	max    int64
	keep   int
	written int64
}

// NewRotatingWriter 打开 path 并返回自动轮转 writer。
// open 为 nil 时使用 os.OpenFile。keep 为保留的滚动份数（不含当前文件）。
func NewRotatingWriter(path string, maxBytes int64, keep int, open func(path string, flag int, perm os.FileMode) (*os.File, error)) (io.WriteCloser, error) {
	if open == nil {
		open = os.OpenFile
	}
	file, err := open(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	written := int64(0)
	if st, err := file.Stat(); err == nil {
		written = st.Size()
	}
	return &rotatingWriter{
		file:    file,
		path:    path,
		open:    open,
		max:     maxBytes,
		keep:    keep,
		written: written,
	}, nil
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.max > 0 && w.written+int64(len(p)) > w.max {
		w.rotate()
	}
	n, err := w.file.Write(p)
	w.written += int64(n)
	return n, err
}

func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

// rotate 必须持有 w.mu。当前文件已关闭句柄后滚动备份，再重开新文件。
func (w *rotatingWriter) rotate() {
	if w.file == nil {
		return
	}
	_ = w.file.Close()
	w.file = nil
	// shift 旧备份: .k-1 → .k, … , .1 → .2
	for i := w.keep - 1; i >= 1; i-- {
		from := w.path + "." + itoa(i)
		to := w.path + "." + itoa(i+1)
		if _, err := os.Stat(from); err == nil {
			_ = os.Remove(to)
			_ = os.Rename(from, to)
		}
	}
	// 当前文件 → .1
	_ = os.Remove(w.path + ".1")
	if err := os.Rename(w.path, w.path+".1"); err != nil {
		// rename 失败（例如有读者未带 FILE_SHARE_DELETE）：丢弃备份，
		// 直接重开当前文件，数据从新文件继续，不中断日志。
		_ = os.Remove(w.path)
	}
	file, err := w.open(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		// 重开失败：降级为丢弃输出（io.Discard），避免后续 Write panic
		devNull, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if devNull == nil {
			w.file = nil
			return
		}
		w.file = devNull
		return
	}
	w.file = file
	w.written = 0
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
