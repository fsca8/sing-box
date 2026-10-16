package monitor

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestCollector 建一个关掉所有裁剪上限的 collector：这些用例只看去重，
// 不希望维护逻辑中途动数据。
func newTestCollector(t *testing.T, limits Limits) *Collector {
	t.Helper()
	limits.MaxSizeBytes = -1
	limits.MaxDNSRows = -1
	limits.MaxConnRows = -1
	limits.RetentionDays = -1
	c, err := NewCollectorWithLimits(filepath.Join(t.TempDir(), "monitor.db"), limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// flushPending 把写入通道里的事件取出并同步提交：否则要等写入协程 200ms 的
// 刷新定时器，测试只能靠 sleep 过关。
func flushPending(d *Database) {
	var batch []dbEvent
	for {
		select {
		case ev := <-d.writeCh:
			batch = append(batch, ev)
		default:
			if len(batch) > 0 {
				d.flushBatch(batch)
			}
			return
		}
	}
}

// waitRows 等到行数达到 atLeast，再多观察一个刷新周期，返回最终行数。行数只增，
// 所以多出来的就是"本该被抑制的记录也落了库"。
func waitRows(t *testing.T, d *Database, table string, atLeast int) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && countRows(t, d, table) < atLeast {
		flushPending(d)
		time.Sleep(10 * time.Millisecond)
	}
	// 写入协程可能正握着一批没提交的事件，等它一个完整的刷新周期再数。
	settle := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(settle) {
		flushPending(d)
		time.Sleep(20 * time.Millisecond)
	}
	flushPending(d)
	return countRows(t, d, table)
}

// TestConnFailDedupe 一次故障会对着同一个目的地反复重试：窗口内只留一条，
// 窗口过后重新落库，不同出站/不同目的地互不抑制。
func TestConnFailDedupe(t *testing.T) {
	c := newTestCollector(t, Limits{FailDedupeWindow: time.Minute})
	now := time.Unix(0, 0)
	c.now = func() time.Time { return now }

	attempt := func(connID, remote, outbound string) TCPRecord {
		// 延迟不能为 0：写入路径会把它当成"没有测量结果"直接丢掉。
		return TCPRecord{ConnID: connID, Remote: remote, Outbound: outbound, LatencyUs: 25000, Error: "dial tcp: i/o timeout"}
	}

	c.RecordTCP(attempt("retry-1", "192.0.2.1:6881", "direct"))
	c.RecordTCP(attempt("retry-2", "192.0.2.1:6881", "direct"))
	if rows := waitRows(t, c.db, "connection_records", 1); rows != 1 {
		t.Fatalf("expected a single row inside the window, got %d", rows)
	}

	now = now.Add(2 * time.Minute)
	c.RecordTCP(attempt("retry-3", "192.0.2.1:6881", "direct"))
	if rows := waitRows(t, c.db, "connection_records", 2); rows != 2 {
		t.Fatalf("expected a new row after the window, got %d", rows)
	}

	c.RecordTCP(attempt("retry-4", "192.0.2.1:6881", "proxy"))
	c.RecordTCP(attempt("retry-5", "198.51.100.9:443", "direct"))
	if rows := waitRows(t, c.db, "connection_records", 4); rows != 4 {
		t.Fatalf("a different outbound or destination must not be suppressed, got %d rows", rows)
	}
}

// TestConnFailDedupeDisabled 窗口取负值表示关掉去重：每条失败都要落库。
func TestConnFailDedupeDisabled(t *testing.T) {
	c := newTestCollector(t, Limits{FailDedupeWindow: -1})
	c.now = func() time.Time { return time.Unix(0, 0) }

	for _, connID := range []string{"retry-1", "retry-2"} {
		c.RecordTCP(TCPRecord{ConnID: connID, Remote: "192.0.2.1:6881", Outbound: "direct", LatencyUs: 25000, Error: "dial tcp: i/o timeout"})
	}
	if rows := waitRows(t, c.db, "connection_records", 2); rows != 2 {
		t.Fatalf("a negative window must disable dedupe, got %d rows", rows)
	}
}

// TestDNSFailDedupe 解析失败与拨号失败是同一类问题（一次故障刷出一片重复记录），
// 用同一个窗口抑制；成功的解析结果一律照落。
func TestDNSFailDedupe(t *testing.T) {
	c := newTestCollector(t, Limits{FailDedupeWindow: time.Minute})
	now := time.Unix(0, 0)
	c.now = func() time.Time { return now }

	failed := DNSRecord{Domain: "example.com", QType: "A", Status: "ERROR", LatencyUs: 10001000}
	c.RecordDNS(failed)
	c.RecordDNS(failed)
	// 缓冲区照旧收下每一条，被抑制的只是落库。
	if history := c.GetDNSHistory(10); len(history) != 2 {
		t.Fatalf("the ring buffer must keep every record, got %d", len(history))
	}
	if rows := waitRows(t, c.db, "dns_records", 1); rows != 1 {
		t.Fatalf("expected a single row inside the window, got %d", rows)
	}

	now = now.Add(2 * time.Minute)
	c.RecordDNS(failed)
	if rows := waitRows(t, c.db, "dns_records", 2); rows != 2 {
		t.Fatalf("expected a new row after the window, got %d", rows)
	}

	c.RecordDNS(DNSRecord{Domain: "www.example.com", QType: "A", Status: "ERROR"})
	c.RecordDNS(DNSRecord{Domain: "example.com", QType: "AAAA", Status: "ERROR"})
	c.RecordDNS(DNSRecord{Domain: "example.com", QType: "A", Status: "NOERROR"})
	c.RecordDNS(DNSRecord{Domain: "example.com", QType: "A", Status: "NOERROR"})
	if rows := waitRows(t, c.db, "dns_records", 6); rows != 6 {
		t.Fatalf("a different domain/qtype or a non-ERROR result must be recorded, got %d rows", rows)
	}
}

// TestDNSFailDedupeDisabled 同一个窗口开关也管着 DNS 侧。
func TestDNSFailDedupeDisabled(t *testing.T) {
	c := newTestCollector(t, Limits{FailDedupeWindow: -1})
	c.now = func() time.Time { return time.Unix(0, 0) }

	for i := 0; i < 2; i++ {
		c.RecordDNS(DNSRecord{Domain: "example.com", QType: "A", Status: "ERROR"})
	}
	if rows := waitRows(t, c.db, "dns_records", 2); rows != 2 {
		t.Fatalf("a negative window must disable dedupe, got %d rows", rows)
	}
}

// nonEmptyLines 去掉空行，方便数维护日志的行数。
func nonEmptyLines(text string) []string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// TestMaintenanceLogThrottle 钉住裁剪日志的节流：窗口内多次裁剪只输出一行，
// 被压掉的次数与行数累计到下一行；异常输出不受节流影响。
func TestMaintenanceLogThrottle(t *testing.T) {
	db, err := NewDatabaseWithLimits(filepath.Join(t.TempDir(), "monitor.db"), Limits{
		MaxSizeBytes:           -1,
		MaxDNSRows:             -1,
		MaxConnRows:            100,
		RetentionDays:          -1,
		MaintenanceLogInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var out bytes.Buffer
	db.logWriter = &out

	prune := func(from int) int64 {
		t.Helper()
		insertConnRows(t, db, from, from+300)
		deleted := db.enforceLimits()
		if deleted == 0 {
			t.Fatalf("expected rows to be pruned (inserted from %d)", from)
		}
		return deleted
	}

	if deleted := prune(0); deleted != 200 {
		t.Fatalf("expected the first pass to drop 200 rows, got %d", deleted)
	}
	prune(1000)
	prune(2000)

	lines := nonEmptyLines(out.String())
	if len(lines) != 1 {
		t.Fatalf("expected a single log line inside the window, got %d: %q", len(lines), out.String())
	}
	if !strings.Contains(lines[0], "pruned 200 records") || !strings.Contains(lines[0], "conn row cap") {
		t.Fatalf("the log line must keep the count and name the trigger, got %q", lines[0])
	}
	if strings.Contains(lines[0], "suppressed") {
		t.Fatalf("nothing was suppressed before the first line, got %q", lines[0])
	}

	// 异常输出不受节流影响。
	db.logMaintenance("[monitor] checkpoint failed: %v\n", errors.New("checkpoint unavailable"))
	db.logMaintenance("[monitor] checkpoint failed: %v\n", errors.New("checkpoint unavailable"))
	if got := strings.Count(out.String(), "checkpoint failed"); got != 2 {
		t.Fatalf("exceptional output must not be throttled, got %d lines", got)
	}

	// 窗口过去后重新输出，并交代被压掉的那两次。
	db.logMu.Lock()
	db.lastLogTime = time.Now().Add(-2 * time.Hour)
	db.logMu.Unlock()
	prune(3000)
	lines = nonEmptyLines(out.String())
	if len(lines) != 4 {
		t.Fatalf("expected one more log line after the window, got %d: %q", len(lines), out.String())
	}
	if !strings.Contains(lines[3], "suppressed 2 passes, 600 records") {
		t.Fatalf("the log line must account for what was suppressed, got %q", lines[3])
	}
}
