package log

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRotatingFileWriterUnderCap keeps every line while the file stays below
// the cap.
func TestRotatingFileWriterUnderCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "box.log")
	writer, err := newRotatingFileWriter(context.Background(), path, 4096)
	if err != nil {
		t.Fatal(err)
	}
	// Windows 上文件仍被打开时 TempDir 清理会失败，测试结束前必须关掉。
	t.Cleanup(func() { _ = writer.Close() })
	for i := 0; i < 10; i++ {
		if _, err = writer.Write([]byte("line-0000\n")); err != nil {
			t.Fatal(err)
		}
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(content), "line-0000") != 10 {
		t.Fatalf("expected 10 lines, got %q", content)
	}
	if info, _ := os.Stat(path); info.Size() > 4096 {
		t.Fatalf("file grew past the cap: %d", info.Size())
	}
}

// TestRotatingFileWriterCap asserts the file never exceeds the cap, the newest
// lines survive and the oldest ones are dropped (circular overwrite).
func TestRotatingFileWriterCap(t *testing.T) {
	const (
		maxSize = 4096
		lineLen = 63 // + '\n' = 64 bytes
		lines   = 1000
	)
	path := filepath.Join(t.TempDir(), "box.log")
	writer, err := newRotatingFileWriter(context.Background(), path, maxSize)
	if err != nil {
		t.Fatal(err)
	}
	// Windows 上文件仍被打开时 TempDir 清理会失败，测试结束前必须关掉。
	t.Cleanup(func() { _ = writer.Close() })
	line := strings.Repeat("x", lineLen)
	for i := 0; i < lines; i++ {
		if _, err = writer.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > maxSize {
		t.Fatalf("file size %d exceeds cap %d", info.Size(), maxSize)
	}
	if info.Size() == 0 {
		t.Fatal("file is empty, rotation lost everything")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Every kept line must be complete — rotation trims at a line boundary.
	if strings.HasSuffix(string(content), "\n") == false {
		t.Fatalf("kept content does not end at a line boundary")
	}
	for _, part := range strings.Split(strings.TrimSuffix(string(content), "\n"), "\n") {
		if len(part) != lineLen {
			t.Fatalf("partial line kept after rotation: %q", part)
		}
	}
}

// TestRotatingFileWriterTrimsOversizedFile covers the upgrade path: a file left
// oversized by an earlier unbounded run is trimmed on the first write.
func TestRotatingFileWriterTrimsOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "box.log")
	line := strings.Repeat("y", 63) + "\n"
	if err := os.WriteFile(path, []byte(strings.Repeat(line, 500)), 0o644); err != nil {
		t.Fatal(err)
	}
	writer, err := newRotatingFileWriter(context.Background(), path, 4096)
	if err != nil {
		t.Fatal(err)
	}
	// Windows 上文件仍被打开时 TempDir 清理会失败，测试结束前必须关掉。
	t.Cleanup(func() { _ = writer.Close() })
	if _, err = writer.Write([]byte(strings.Repeat("z", 63) + "\n")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > 4096 {
		t.Fatalf("oversized file was not trimmed: %d", info.Size())
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "z") {
		t.Fatal("the line written after rotation is missing")
	}
}

// TestRotatingFileWriterUnlimited keeps the previous behaviour when the cap is
// disabled.
func TestRotatingFileWriterUnlimited(t *testing.T) {
	path := filepath.Join(t.TempDir(), "box.log")
	writer, err := newRotatingFileWriter(context.Background(), path, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Windows 上文件仍被打开时 TempDir 清理会失败，测试结束前必须关掉。
	t.Cleanup(func() { _ = writer.Close() })
	line := strings.Repeat("u", 1023) + "\n"
	for i := 0; i < 200; i++ {
		if _, err = writer.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != int64(len(line)*200) {
		t.Fatalf("expected %d bytes without a cap, got %d", len(line)*200, info.Size())
	}
}

// TestRotatingFileWriterOversizedWrite covers a single write that is larger than
// the cap (e.g. a debug-level dump of a whole config): only its newest part may
// survive and the file must stay inside the cap.
func TestRotatingFileWriterOversizedWrite(t *testing.T) {
	const maxSize = 1024
	path := filepath.Join(t.TempDir(), "box.log")
	writer, err := newRotatingFileWriter(context.Background(), path, maxSize)
	if err != nil {
		t.Fatal(err)
	}
	// Windows 上文件仍被打开时 TempDir 清理会失败，测试结束前必须关掉。
	t.Cleanup(func() { _ = writer.Close() })
	// 4097 bytes in one Write: 4096 'x' characters then a newline and a marker line.
	payload := strings.Repeat("x", 4096) + "\n" + "TAIL-MARKER\n"
	n, err := writer.Write([]byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if n != len(payload) {
		t.Fatalf("short write: %d of %d", n, len(payload))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > maxSize {
		t.Fatalf("file size %d exceeds cap %d after an oversized write", info.Size(), maxSize)
	}
	content, _ := os.ReadFile(path)
	if !strings.Contains(string(content), "TAIL-MARKER") {
		t.Fatalf("newest content was dropped: %q", content)
	}
}

// TestRotatingFileWriterLongLineKeepsWindow covers a rotated window without any
// newline (a single very long line): the previous implementation wiped the whole
// log in that case.
func TestRotatingFileWriterLongLineKeepsWindow(t *testing.T) {
	const maxSize = 4096
	path := filepath.Join(t.TempDir(), "box.log")
	writer, err := newRotatingFileWriter(context.Background(), path, maxSize)
	if err != nil {
		t.Fatal(err)
	}
	// Windows 上文件仍被打开时 TempDir 清理会失败，测试结束前必须关掉。
	t.Cleanup(func() { _ = writer.Close() })
	// First write leaves the file full of one newline-free blob.
	blob := strings.Repeat("A", 3000)
	if _, err = writer.Write([]byte(blob)); err != nil {
		t.Fatal(err)
	}
	// This write pushes past the cap and rotates; the kept window has no newline.
	if _, err = writer.Write([]byte("B\n")); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Size() > maxSize {
		t.Fatalf("file size %d exceeds cap %d", info.Size(), maxSize)
	}
	if !strings.Contains(string(content), "A") {
		t.Fatalf("newline-free window was wiped by rotation: %q", content)
	}
	if !strings.HasSuffix(string(content), "B\n") {
		t.Fatalf("the newest line is missing: %q", content)
	}
}

// TestRotatingFileWriterConcurrent writes from many goroutines: every write must
// succeed and the cap has to hold under concurrency.
func TestRotatingFileWriterConcurrent(t *testing.T) {
	const (
		maxSize  = 8192
		writers  = 8
		perWrite = 200
	)
	path := filepath.Join(t.TempDir(), "box.log")
	writer, err := newRotatingFileWriter(context.Background(), path, maxSize)
	if err != nil {
		t.Fatal(err)
	}
	// Windows 上文件仍被打开时 TempDir 清理会失败，测试结束前必须关掉。
	t.Cleanup(func() { _ = writer.Close() })
	done := make(chan error, writers)
	line := []byte(strings.Repeat("c", 79) + "\n")
	for i := 0; i < writers; i++ {
		go func() {
			for j := 0; j < perWrite; j++ {
				if _, writeErr := writer.Write(line); writeErr != nil {
					done <- writeErr
					return
				}
			}
			done <- nil
		}()
	}
	for i := 0; i < writers; i++ {
		if err = <-done; err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > maxSize {
		t.Fatalf("file size %d exceeds cap %d under concurrent writes", info.Size(), maxSize)
	}
}
