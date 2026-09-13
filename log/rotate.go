package log

import (
	"bytes"
	"context"
	"io"
	"os"
	"sync"

	"github.com/sagernet/sing/service/filemanager"
)

// DefaultLogFileMaxSize is the default size cap applied to the log file
// written when `log.output` points to a file (100 MiB).
//
// Rationale: the log file used to grow without bound (a 19GB singbox.log was
// observed in production). The cap is now configurable via
// `log.max_size_mb` in the config file.
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
type rotatingFileWriter struct {
	access  sync.Mutex
	file    *os.File
	maxSize int64
	size    int64
}

// newRotatingFileWriter opens (or creates) the log file and applies the cap
// immediately: a file left oversized by a previous unbounded run is trimmed
// on the first write, keeping only its newest lines.
func newRotatingFileWriter(ctx context.Context, path string, maxSize int64) (*rotatingFileWriter, error) {
	// O_RDWR (not O_WRONLY) because rotation reads back the newest tail of the
	// file before truncating it.
	file, err := filemanager.OpenFile(ctx, path, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o644)
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

func (w *rotatingFileWriter) Write(p []byte) (int, error) {
	w.access.Lock()
	defer w.access.Unlock()
	if w.maxSize > 0 && w.size+int64(len(p)) > w.maxSize {
		if err := w.rotateLocked(); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

// rotateLocked rewrites the file with only its newest lines. Caller must hold
// w.access.
func (w *rotatingFileWriter) rotateLocked() error {
	stat, err := w.file.Stat()
	if err != nil {
		return err
	}
	size := stat.Size()
	keep := w.maxSize * rotateKeepNumerator / rotateKeepDenominator
	if size <= keep {
		// Nothing to trim (e.g. the file was truncated by an external tool);
		// just resync the counter with reality.
		w.size = size
		return nil
	}
	keepBuffer := make([]byte, keep)
	_, err = w.file.ReadAt(keepBuffer, size-keep)
	if err != nil && err != io.EOF {
		return err
	}
	// Start at a line boundary so the kept content never begins mid-line.
	if index := bytes.IndexByte(keepBuffer, '\n'); index >= 0 {
		keepBuffer = keepBuffer[index+1:]
	} else {
		keepBuffer = keepBuffer[:0]
	}
	if err := w.file.Truncate(0); err != nil {
		return err
	}
	// The file is opened with O_APPEND: after Truncate the write lands at EOF 0.
	_, err = w.file.Write(keepBuffer)
	if err != nil {
		return err
	}
	w.size = int64(len(keepBuffer))
	return nil
}
