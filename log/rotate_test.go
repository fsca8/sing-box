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
