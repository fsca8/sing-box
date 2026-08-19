package log

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestRotatingWriter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "engine.log")
	const max = 100
	// 预置旧内容，验证打开时以现有大小计数（写入 60B 即触发轮转）
	if err := os.WriteFile(path, []byte("0123456789abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := NewRotatingWriter(path, max, 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 每次写 50B: 累计 16+50*2 > 100 → 第一次写后应轮转
	for i := 0; i < 6; i++ {
		buf := make([]byte, 50)
		for j := range buf {
			buf[j] = byte('a' + i)
		}
		if _, err := w.Write(buf); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	entries := map[string]int64{}
	files, _ := filepath.Glob(path + "*")
	for _, f := range files {
		st, err := os.Stat(f)
		if err == nil {
			entries[f] = st.Size()
		}
	}
	// 当前文件 ≤ max；.1/.2/.3 存在（keep=3），.4 不应存在
	cur := entries[path]
	if cur > max {
		t.Fatalf("current file %d > max %d (files=%v)", cur, max, files)
	}
	for _, suffix := range []string{".1", ".2", ".3"} {
		if _, ok := entries[path+suffix]; !ok {
			t.Fatalf("missing backup %s (files=%v)", path+suffix, files)
		}
	}
	if _, ok := entries[path+".4"]; ok {
		t.Fatalf("unexpected .4 backup (files=%v)", files)
	}
	// 当前文件是最近一次写入的内容（'f'*50）
	curData, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(curData) != 50 || curData[0] != 'f' {
		t.Fatalf("current file content mismatch: len=%d first=%c", len(curData), curData[0])
	}
}

func TestRotatingWriterConcurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "engine.log")
	w, err := NewRotatingWriter(path, 200, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	for g := 0; g < 4; g++ {
		go func(seed byte) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 100; i++ {
				buf := []byte{seed, '\n'}
				if _, err := w.Write(buf); err != nil {
					t.Error(err)
					return
				}
			}
		}(byte('a' + g))
	}
	for g := 0; g < 4; g++ {
		<-done
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

var _ io.WriteCloser = (*rotatingWriter)(nil)
