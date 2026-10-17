package log

import (
	"bytes"
	"context"
	"io"
	"os"
	"sync"

	"github.com/sagernet/sing/service/filemanager"
)

// DefaultLogFileMaxSize is the size cap applied to the log file written when
// `log.output` points to a file (100 MiB).
//
// Rationale: the log file used to grow without bound (a 19GB log file was
// observed in production).
//
// The cap is intentionally a constant and not a config field: it used to be
// exposed as `log.max_size_mb`, which is not an upstream option, and a config
// carrying such a key is rejected by upstream sing-box's strict decoder.
const DefaultLogFileMaxSize int64 = 100 << 20

// rotateKeepRatio is the fraction of the cap kept when the file is rotated.
// Keeping 3/4 leaves room for new lines before the next rotation, so
// rotation runs once per maxSize/4 bytes written instead of on every line.
const rotateKeepNumerator = 3
const rotateKeepDenominator = 4

// rotatingFileWriter is an append-only, size-capped writer for the engine log
// file.
//
// Circular overwrite semantics: when a write would push the file past
// maxSize, the oldest content is dropped and only the newest lines are kept
// (the file is truncated from the front, never beyond the cap). The most
// recent logs are therefore always available, and the file never exceeds
// maxSize (the steady state oscillates between 75% and 100% of the cap).
//
// The writer is safe for concurrent use (the log factory is called from many
// goroutines). maxSize <= 0 disables the cap entirely.
//
// 不变量：Write 返回后文件大小 <= maxSize（单次写入本身超过上限时也只留它的尾部）。
type rotatingFileWriter struct {
	access  sync.Mutex
	file    *os.File
	maxSize int64
	size    int64
}

// NewFileWriter opens path as an append-only, size-capped log writer with the
// same circular-overwrite semantics as the engine log file: once the cap is
// reached the oldest content is dropped, so the newest lines are always
// available and the file never exceeds maxSize. maxSize <= 0 disables the cap.
//
// Auxiliary logs that share the engine log's constraints use this (e.g. the
// embedded netbird engine log, which must not grow without bound).
func NewFileWriter(ctx context.Context, path string, maxSize int64) (io.Writer, error) {
	return newRotatingFileWriter(ctx, path, maxSize)
}

// newRotatingFileWriter opens (or creates) the log file and applies the cap
// immediately: a file left oversized by a previous unbounded run is trimmed
// on the first write, keeping only its newest lines.
func newRotatingFileWriter(ctx context.Context, path string, maxSize int64) (*rotatingFileWriter, error) {
	// O_RDWR (not O_WRONLY) because rotation reads back the newest tail of the
	// file before truncating it. 不用 O_APPEND：轮转要在文件内做 WriteAt 前移，
	// 而 O_APPEND 的文件禁止 WriteAt；追加改由 WriteAt(p, size) 完成。
	file, err := filemanager.OpenFile(ctx, path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	writer := &rotatingFileWriter{
		file:    file,
		maxSize: maxSize,
	}
	if stat, statErr := file.Stat(); statErr == nil {
		writer.size = stat.Size()
	}
	return writer, nil
}

// Close releases the underlying file handle. The engine keeps its log writer
// open for the whole process lifetime; callers that are done with a writer
// (tests, short-lived auxiliary logs) should close it — on Windows a file that
// is still open cannot be deleted.
func (w *rotatingFileWriter) Close() error {
	w.access.Lock()
	defer w.access.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func (w *rotatingFileWriter) Write(p []byte) (int, error) {
	w.access.Lock()
	defer w.access.Unlock()
	if w.file == nil {
		return 0, os.ErrClosed
	}
	if w.maxSize > 0 {
		// 以文件真实大小为准：同一个文件可能有别的 writer/进程在写，
		// 只靠自己累加的计数器会漏掉那些字节，导致永远不轮转。
		if stat, statErr := w.file.Stat(); statErr == nil {
			w.size = stat.Size()
		}
		if int64(len(p)) >= w.maxSize {
			// 单次写入本身就超过上限（例如 debug 级一次性打印整份配置）：
			// 只保留这次写入的尾部，保证文件不超上限。
			tail := p[int64(len(p))-w.maxSize:]
			if index := bytes.IndexByte(tail, '\n'); index >= 0 && index+1 < len(tail) {
				tail = tail[index+1:]
			}
			if err := w.file.Truncate(0); err != nil {
				return 0, err
			}
			if _, err := w.file.WriteAt(tail, 0); err != nil {
				return 0, err
			}
			w.size = int64(len(tail))
			// 头部被按“循环覆盖”语义丢弃，p 已经处理完，按写满返回。
			return len(p), nil
		}
		if w.size+int64(len(p)) > w.maxSize {
			keep := w.maxSize * rotateKeepNumerator / rotateKeepDenominator
			// 给这次写入留出空间，否则轮转完紧接着写又会超上限。
			if room := w.maxSize - int64(len(p)); keep > room {
				keep = room
			}
			if keep < 0 {
				keep = 0
			}
			if err := w.trimLocked(w.size, keep); err != nil {
				return 0, err
			}
		}
	}
	n, err := w.file.WriteAt(p, w.size)
	w.size += int64(n)
	return n, err
}

// trimLocked 把文件的最后 keep 字节前移到文件头，再截断到新长度。
// 读取按 64KiB/1MiB 分块，避免在默认 100MiB 上限下一次性分配几十 MiB
// （Android 上会被 OOM kill）；找不到换行时保留整个窗口而不是清空。
// Caller must hold w.access.
func (w *rotatingFileWriter) trimLocked(size, keep int64) error {
	if keep >= size {
		w.size = size
		return nil
	}
	start := size - keep
	// 从窗口起点往后找第一个换行，避免保留半行。窗口内一个换行都没有
	// （超长单行）时 align 保持 0：宁可多留，也不要清空全部日志。
	var align int64
	scan := make([]byte, 64<<10)
	for offset := int64(0); offset < keep; {
		length := int64(len(scan))
		if length > keep-offset {
			length = keep - offset
		}
		read, err := w.file.ReadAt(scan[:length], start+offset)
		if err != nil && err != io.EOF {
			return err
		}
		if index := bytes.IndexByte(scan[:read], '\n'); index >= 0 {
			align = offset + int64(index) + 1
			break
		}
		if read == 0 {
			break
		}
		offset += int64(read)
	}
	newStart := start + align
	newSize := size - newStart
	buf := make([]byte, 1<<20)
	for done := int64(0); done < newSize; {
		length := int64(len(buf))
		if length > newSize-done {
			length = newSize - done
		}
		if _, err := w.file.ReadAt(buf[:length], newStart+done); err != nil && err != io.EOF {
			return err
		}
		if _, err := w.file.WriteAt(buf[:length], done); err != nil {
			return err
		}
		done += length
	}
	if err := w.file.Truncate(newSize); err != nil {
		return err
	}
	w.size = newSize
	return nil
}
