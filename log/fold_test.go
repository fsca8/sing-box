package log

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// foldClock is a hand-cranked clock so window rollover does not need real sleeps.
type foldClock struct {
	access sync.Mutex
	now    time.Time
}

func newFoldClock() *foldClock {
	return &foldClock{now: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
}

func (c *foldClock) Now() time.Time {
	c.access.Lock()
	defer c.access.Unlock()
	return c.now
}

func (c *foldClock) Advance(d time.Duration) {
	c.access.Lock()
	defer c.access.Unlock()
	c.now = c.now.Add(d)
}

func newTestFoldingWriter(t *testing.T, limit int, window time.Duration) (*foldingWriter, *bytes.Buffer, *foldClock) {
	t.Helper()
	var buffer bytes.Buffer
	writer := newFoldingWriter(&buffer, FoldOptions{Limit: limit, Window: window})
	if writer == nil {
		t.Fatal("expected an enabled folding writer")
	}
	clock := newFoldClock()
	writer.now = clock.Now
	writer.lastPrune = clock.Now()
	return writer, &buffer, clock
}

func writeFoldLines(t *testing.T, writer *foldingWriter, lines ...string) {
	t.Helper()
	for _, line := range lines {
		n, err := writer.Write([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		if n != len(line) {
			t.Fatalf("short write: %d != %d", n, len(line))
		}
	}
}

// verbatimLines counts log lines that reached the sink unchanged. Summary lines
// start with the [log-fold] marker and are not counted.
func verbatimLines(buffer *bytes.Buffer) int {
	count := 0
	for _, line := range strings.Split(buffer.String(), "\n") {
		if line != "" && !strings.HasPrefix(line, "[log-fold]") {
			count++
		}
	}
	return count
}

func TestFoldShapeNormalization(t *testing.T) {
	testCases := []struct {
		name     string
		first    string
		second   string
		expected bool // true: 两行归一成同一个形状
	}{
		{
			name:     "timestamp connection id address and port",
			first:    "+0000 2026-10-16 11:16:52 ERROR [3077955136 0ms] connection: open connection to 192.0.2.10:6881 using outbound/direct[direct]: dial tcp 192.0.2.10:6881: i/o timeout\n",
			second:   "+0000 2026-10-16 11:16:53 ERROR [3077955137 92ms] connection: open connection to 192.0.2.11:6882 using outbound/direct[direct]: dial tcp 192.0.2.11:6882: i/o timeout\n",
			expected: true,
		},
		{
			name:     "different error text",
			first:    "ERROR connection: open connection to 192.0.2.10:6881: i/o timeout\n",
			second:   "ERROR connection: open connection to 192.0.2.10:6881: connection refused\n",
			expected: false,
		},
		{
			name:     "different level",
			first:    "ERROR connection: dial failed\n",
			second:   "WARN connection: dial failed\n",
			expected: false,
		},
		{
			name:     "different outbound tag",
			first:    "ERROR [1 0ms] connection: open connection to 192.0.2.10:6881 using outbound/direct[direct]: i/o timeout\n",
			second:   "ERROR [2 0ms] connection: open connection to 192.0.2.10:6881 using outbound/proxy[proxy]: i/o timeout\n",
			expected: false,
		},
		{
			name:     "ipv6 peers collapse like ipv4",
			first:    "INFO [12 1ms] inbound/tun: accepted 2001:db8::1:50000\n",
			second:   "INFO [13 2ms] inbound/tun: accepted 2001:db8::2:50001\n",
			expected: true,
		},
		{
			name:     "numbers are masked",
			first:    "INFO router: rule matched 1 of 2 rules\n",
			second:   "INFO router: rule matched 7 of 9 rules\n",
			expected: true,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			firstShape := foldShapeOf([]byte(testCase.first))
			secondShape := foldShapeOf([]byte(testCase.second))
			if same := firstShape == secondShape; same != testCase.expected {
				t.Fatalf("same=%v want %v\nfirst:  %q\nsecond: %q", same, testCase.expected, firstShape, secondShape)
			}
		})
	}
}

// TestFoldUnderLimit keeps every line verbatim while a shape stays within the
// per-window limit.
func TestFoldUnderLimit(t *testing.T) {
	writer, buffer, _ := newTestFoldingWriter(t, 3, 5*time.Minute)
	for i := 0; i < 3; i++ {
		writeFoldLines(t, writer, "ERROR [1 0ms] connection: dial failed\n")
	}
	if count := verbatimLines(buffer); count != 3 {
		t.Fatalf("expected 3 verbatim lines, got %d: %q", count, buffer.String())
	}
	if strings.Contains(buffer.String(), "[log-fold]") {
		t.Fatalf("unexpected summary: %q", buffer.String())
	}
}

// TestFoldOverLimit suppresses repeats past the limit and reports them once the
// window rolls over.
func TestFoldOverLimit(t *testing.T) {
	writer, buffer, clock := newTestFoldingWriter(t, 2, 5*time.Minute)
	for i := 0; i < 5; i++ {
		writeFoldLines(t, writer, "ERROR [1 0ms] connection: dial failed\n")
	}
	if count := verbatimLines(buffer); count != 2 {
		t.Fatalf("expected 2 verbatim lines, got %d: %q", count, buffer.String())
	}
	if strings.Contains(buffer.String(), "[log-fold]") {
		t.Fatalf("summary must wait for the window to roll over: %q", buffer.String())
	}
	clock.Advance(5 * time.Minute)
	writeFoldLines(t, writer, "ERROR [1 0ms] connection: dial failed\n")
	content := buffer.String()
	if !strings.Contains(content, "[log-fold] suppressed 3 of 5 similar lines in 5m0s") {
		t.Fatalf("missing summary: %q", content)
	}
	// 新窗口重新计数：这一行原样输出。
	if count := verbatimLines(buffer); count != 3 {
		t.Fatalf("expected the new window line verbatim, got %d: %q", count, content)
	}
}

// TestFoldDistinctLinesNeverMerged is the safety property: different problems
// keep their own budget.
func TestFoldDistinctLinesNeverMerged(t *testing.T) {
	writer, buffer, _ := newTestFoldingWriter(t, 1, 5*time.Minute)
	writeFoldLines(t, writer,
		"ERROR a: dial failed\n",
		"ERROR b: dial failed\n",
		"ERROR c: dial failed\n",
		"ERROR d: dial failed\n",
	)
	content := buffer.String()
	for _, name := range []string{"a", "b", "c", "d"} {
		if !strings.Contains(content, "ERROR "+name+":") {
			t.Fatalf("line %q was dropped: %q", name, content)
		}
	}
}

// TestFoldVolatileParts mirrors the production flood: one message shape, many
// ids, addresses, ports and durations.
func TestFoldVolatileParts(t *testing.T) {
	writer, buffer, clock := newTestFoldingWriter(t, 2, time.Minute)
	for i := 0; i < 10; i++ {
		writeFoldLines(t, writer, "+0000 2026-10-16 11:16:5"+string(rune('0'+i))+
			" ERROR [307795513"+string(rune('0'+i))+" "+string(rune('0'+i))+"ms] connection: open connection to 192.0.2."+
			string(rune('0'+i))+":688"+string(rune('0'+i))+" using outbound/direct[direct]: dial tcp 192.0.2."+
			string(rune('0'+i))+":688"+string(rune('0'+i))+": i/o timeout\n")
	}
	if count := strings.Count(buffer.String(), "+0000 "); count != 2 {
		t.Fatalf("expected 2 verbatim lines, got %d: %q", count, buffer.String())
	}
	clock.Advance(time.Minute)
	writeFoldLines(t, writer, "+0000 2026-10-16 11:17:00 ERROR [1 0ms] connection: open connection to 192.0.2.1:1 using outbound/direct[direct]: dial tcp 192.0.2.1:1: i/o timeout\n")
	if !strings.Contains(buffer.String(), "[log-fold] suppressed 8 of 10 similar lines in 1m0s") {
		t.Fatalf("unexpected summary: %q", buffer.String())
	}
}

// TestFoldSummaryOnIdleShape covers the case where a noisy shape stops
// appearing: its count still has to be reported, without waiting for the next
// line of that shape.
func TestFoldSummaryOnIdleShape(t *testing.T) {
	writer, buffer, clock := newTestFoldingWriter(t, 1, time.Minute)
	writeFoldLines(t, writer, "ERROR noisy: dial failed\n", "ERROR noisy: dial failed\n")
	clock.Advance(2 * time.Minute)
	writeFoldLines(t, writer, "INFO quiet: started\n")
	content := buffer.String()
	if !strings.Contains(content, "[log-fold] suppressed 1 of 2 similar lines in 1m0s") {
		t.Fatalf("idle shape was not reported: %q", content)
	}
	if !strings.Contains(content, "INFO quiet: started") {
		t.Fatalf("unrelated line was dropped: %q", content)
	}
}

// TestFoldFlush reports pending counts at shutdown.
func TestFoldFlush(t *testing.T) {
	writer, buffer, _ := newTestFoldingWriter(t, 1, 5*time.Minute)
	writeFoldLines(t, writer, "ERROR x: dial failed\n", "ERROR x: dial failed\n", "ERROR x: dial failed\n")
	writer.Flush()
	if !strings.Contains(buffer.String(), "[log-fold] suppressed 2 of 3 similar lines") {
		t.Fatalf("flush did not report: %q", buffer.String())
	}
	// 再 Flush 不应重复报告。
	size := buffer.Len()
	writer.Flush()
	if buffer.Len() != size {
		t.Fatalf("flush reported twice: %q", buffer.String())
	}
}

// TestFoldSummaryHasNoStrayBlankLine guards the exact bytes of a summary: the
// shape key must not keep the line's trailing newline, otherwise every summary
// is followed by a blank line.
func TestFoldSummaryHasNoStrayBlankLine(t *testing.T) {
	writer, buffer, clock := newTestFoldingWriter(t, 1, time.Minute)
	writeFoldLines(t, writer, "ERROR a: dial failed\n", "ERROR a: dial failed\n")
	clock.Advance(time.Minute)
	writeFoldLines(t, writer, "ERROR a: dial failed\n")
	content := buffer.String()
	if !strings.Contains(content, "[log-fold] suppressed 1 of 2 similar lines in 1m0s") {
		t.Fatalf("missing summary: %q", content)
	}
	if strings.Contains(content, "\n\n") {
		t.Fatalf("summary left a stray blank line: %q", content)
	}
	if !strings.HasSuffix(content, "ERROR a: dial failed\n") {
		t.Fatalf("summary did not end cleanly: %q", content)
	}
}

// TestFoldMultiLinePassthrough keeps panic/stack traces intact: a payload with
// embedded newlines goes through verbatim and never counts towards a shape.
func TestFoldMultiLinePassthrough(t *testing.T) {
	writer, buffer, _ := newTestFoldingWriter(t, 1, 5*time.Minute)
	payload := "ERROR panic: runtime error\nERROR goroutine 1 [running]:\nERROR main.main()\n"
	for i := 0; i < 3; i++ {
		writeFoldLines(t, writer, payload)
	}
	content := buffer.String()
	if strings.Contains(content, "[log-fold]") {
		t.Fatalf("multi-line payload must not be folded: %q", content)
	}
	if count := strings.Count(content, "goroutine 1 [running]:"); count != 3 {
		t.Fatalf("expected 3 verbatim stack lines, got %d: %q", count, content)
	}
}

// TestFoldMultiLineKeepsOrdering: pending counts are still reported before a
// multi-line payload is written through.
func TestFoldMultiLineKeepsOrdering(t *testing.T) {
	writer, buffer, clock := newTestFoldingWriter(t, 1, time.Minute)
	writeFoldLines(t, writer, "ERROR a: dial failed\n", "ERROR a: dial failed\n")
	clock.Advance(time.Minute)
	writeFoldLines(t, writer, "ERROR panic: x\nERROR stack: y\n")
	content := buffer.String()
	summaryIndex := strings.Index(content, "[log-fold]")
	payloadIndex := strings.Index(content, "ERROR panic: x")
	if summaryIndex < 0 || payloadIndex < 0 || summaryIndex > payloadIndex {
		t.Fatalf("summary must be reported before the payload: %q", content)
	}
	if !strings.Contains(content, "ERROR stack: y") {
		t.Fatalf("payload was not written through: %q", content)
	}
}

// TestFoldTableCap asserts that a shape explosion degrades to "write
// everything" instead of dropping lines.
func TestFoldTableCap(t *testing.T) {
	writer, buffer, clock := newTestFoldingWriter(t, 1, time.Hour)
	// 冻结时钟等价于窗口永不滚动，强制走到形状表上限那条分支。
	writer.lastPrune = clock.Now()
	total := foldMaxShapes + 16
	for i := 0; i < total; i++ {
		line := "INFO unique-" +
			string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('a'+(i/676)%26)) + string(rune('a'+(i/17576)%26)) +
			"\n"
		writeFoldLines(t, writer, line)
	}
	if count := verbatimLines(buffer); count != total {
		t.Fatalf("expected %d lines to survive the shape cap, got %d", total, count)
	}
}

func TestFoldDisabled(t *testing.T) {
	var buffer bytes.Buffer
	for _, options := range []FoldOptions{
		{},
		{Limit: 0, Window: time.Minute},
		{Limit: -1, Window: time.Minute},
		{Limit: 10, Window: 0},
		{Limit: 10, Window: -time.Minute},
	} {
		if writer := newFoldingWriter(&buffer, options); writer != nil {
			t.Fatalf("expected nil writer for %+v", options)
		}
	}
	if writer := newFoldingWriter(nil, FoldOptions{Limit: 10, Window: time.Minute}); writer != nil {
		t.Fatal("expected nil writer for a nil inner writer")
	}
}

// TestFoldConcurrent is the race detector's target: the writer is shared by
// every goroutine of the engine.
func TestFoldConcurrent(t *testing.T) {
	writer, buffer, _ := newTestFoldingWriter(t, 5, time.Minute)
	var waitGroup sync.WaitGroup
	for goroutine := 0; goroutine < 8; goroutine++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for i := 0; i < 200; i++ {
				if _, err := writer.Write([]byte("ERROR [1 0ms] connection: dial failed\n")); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	waitGroup.Wait()
	writer.Flush()
	if count := verbatimLines(buffer); count != 5 {
		t.Fatalf("expected exactly 5 verbatim lines, got %d", count)
	}
	if !strings.Contains(buffer.String(), "[log-fold] suppressed 1595 of 1600 similar lines") {
		t.Fatalf("expected one summary for 1600 repeats: %q", buffer.String())
	}
}

// TestFactoryFoldWiring asserts the factory installs the folding writer on its
// only output path and flushes it on Close.
func TestFactoryFoldWiring(t *testing.T) {
	var buffer bytes.Buffer
	factory := NewDefaultFactory(
		context.Background(),
		Formatter{BaseTime: time.Now(), DisableColors: true, DisableTimestamp: true},
		&buffer,
		"",
		0,
		nil,
		false,
		FoldOptions{Limit: 1, Window: time.Minute},
	)
	if err := factory.Start(); err != nil {
		t.Fatal(err)
	}
	logger := factory.Logger()
	for i := 0; i < 3; i++ {
		logger.Error("connection: dial failed")
	}
	if count := verbatimLines(&buffer); count != 1 {
		t.Fatalf("expected 1 verbatim line, got %d: %q", count, buffer.String())
	}
	if err := factory.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buffer.String(), "[log-fold] suppressed 2 of 3 similar lines") {
		t.Fatalf("close did not flush: %q", buffer.String())
	}
}

// TestFactoryFoldFile is the production shape: folding in front of the
// size-capped file writer.
func TestFactoryFoldFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "box.log")
	factory := NewDefaultFactory(
		context.Background(),
		Formatter{BaseTime: time.Now(), DisableColors: true, DisableTimestamp: true},
		nil,
		path,
		1<<20,
		nil,
		false,
		FoldOptions{Limit: 2, Window: time.Minute},
	)
	if err := factory.Start(); err != nil {
		t.Fatal(err)
	}
	logger := factory.Logger()
	for i := 0; i < 6; i++ {
		logger.Error("connection: open connection to 192.0.2.", i, ": dial failed")
	}
	if err := factory.Close(); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(content), "dial failed"); count != 3 {
		t.Fatalf("expected 2 verbatim lines plus 1 summary, got %d: %q", count, content)
	}
	if !strings.Contains(string(content), "[log-fold] suppressed 4 of 6 similar lines") {
		t.Fatalf("missing summary in the log file: %q", content)
	}
}

// TestFactoryFoldDisabled keeps the plain writer when folding is off.
func TestFactoryFoldDisabled(t *testing.T) {
	var buffer bytes.Buffer
	factory := NewDefaultFactory(
		context.Background(),
		Formatter{BaseTime: time.Now(), DisableColors: true, DisableTimestamp: true},
		&buffer,
		"",
		0,
		nil,
		false,
		FoldOptions{Limit: -1},
	)
	if err := factory.Start(); err != nil {
		t.Fatal(err)
	}
	logger := factory.Logger()
	for i := 0; i < 4; i++ {
		logger.Error("connection: dial failed")
	}
	if count := verbatimLines(&buffer); count != 4 {
		t.Fatalf("expected 4 verbatim lines, got %d: %q", count, buffer.String())
	}
}
