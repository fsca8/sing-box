package monitor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestCollector 建一个 collector。容量与去重窗口都由用例显式给出：
// 环是唯一的存储，容量本身就是被测行为的一部分。
func newTestCollector(t *testing.T, limits Limits) *Collector {
	t.Helper()
	c := NewCollectorWithLimits(limits)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestConnFailDedupe 一次故障会对着同一个目的地反复重试：窗口内只留一条记录，
// 窗口过后重新记录，不同出站/不同目的地互不抑制。
func TestConnFailDedupe(t *testing.T) {
	c := newTestCollector(t, Limits{FailDedupeWindow: time.Minute})
	now := time.Unix(0, 0)
	c.now = func() time.Time { return now }

	attempt := func(connID, remote, outbound string) TCPRecord {
		return TCPRecord{ConnID: connID, Remote: remote, Outbound: outbound, LatencyUs: 25000, Error: "dial tcp: i/o timeout"}
	}

	c.RecordTCP(attempt("retry-1", "192.0.2.1:6881", "direct"))
	c.RecordTCP(attempt("retry-2", "192.0.2.1:6881", "direct"))
	if records := c.GetConnectionHistory(10); len(records) != 1 {
		t.Fatalf("expected a single record inside the window, got %d", len(records))
	}

	now = now.Add(2 * time.Minute)
	c.RecordTCP(attempt("retry-3", "192.0.2.1:6881", "direct"))
	if records := c.GetConnectionHistory(10); len(records) != 2 {
		t.Fatalf("expected a new record after the window, got %d", len(records))
	}

	c.RecordTCP(attempt("retry-4", "192.0.2.1:6881", "proxy"))
	c.RecordTCP(attempt("retry-5", "198.51.100.9:443", "direct"))
	if records := c.GetConnectionHistory(10); len(records) != 4 {
		t.Fatalf("a different outbound or destination must not be suppressed, got %d records", len(records))
	}
}

// TestConnFailDedupeDisabled 窗口取负值表示关掉去重：每条失败都要留下记录。
func TestConnFailDedupeDisabled(t *testing.T) {
	c := newTestCollector(t, Limits{FailDedupeWindow: -1})
	c.now = func() time.Time { return time.Unix(0, 0) }

	for _, connID := range []string{"retry-1", "retry-2"} {
		c.RecordTCP(TCPRecord{ConnID: connID, Remote: "192.0.2.1:6881", Outbound: "direct", LatencyUs: 25000, Error: "dial tcp: i/o timeout"})
	}
	if records := c.GetConnectionHistory(10); len(records) != 2 {
		t.Fatalf("a negative window must disable dedupe, got %d records", len(records))
	}
}

// TestDNSFailDedupe 解析失败与拨号失败是同一类问题（一次故障刷出一片重复记录），
// 用同一个窗口抑制；成功的解析结果一律照记。
func TestDNSFailDedupe(t *testing.T) {
	c := newTestCollector(t, Limits{FailDedupeWindow: time.Minute})
	now := time.Unix(0, 0)
	c.now = func() time.Time { return now }

	failed := DNSRecord{Domain: "example.com", QType: "A", Status: "ERROR", LatencyUs: 10001000}
	c.RecordDNS(failed)
	c.RecordDNS(failed)
	if history := c.GetDNSHistory(10); len(history) != 1 {
		t.Fatalf("the window must suppress the repeated failure, got %d records", len(history))
	}

	now = now.Add(2 * time.Minute)
	c.RecordDNS(failed)
	if history := c.GetDNSHistory(10); len(history) != 2 {
		t.Fatalf("expected a new record after the window, got %d", len(history))
	}

	c.RecordDNS(DNSRecord{Domain: "www.example.com", QType: "A", Status: "ERROR"})
	c.RecordDNS(DNSRecord{Domain: "example.com", QType: "AAAA", Status: "ERROR"})
	c.RecordDNS(DNSRecord{Domain: "example.com", QType: "A", Status: "NOERROR"})
	c.RecordDNS(DNSRecord{Domain: "example.com", QType: "A", Status: "NOERROR"})
	if history := c.GetDNSHistory(10); len(history) != 6 {
		t.Fatalf("a different domain/qtype or a non-ERROR result must be recorded, got %d records", len(history))
	}
}

// TestDNSFailDedupeDisabled 同一个窗口开关也管着 DNS 侧。
func TestDNSFailDedupeDisabled(t *testing.T) {
	c := newTestCollector(t, Limits{FailDedupeWindow: -1})
	c.now = func() time.Time { return time.Unix(0, 0) }

	for i := 0; i < 2; i++ {
		c.RecordDNS(DNSRecord{Domain: "example.com", QType: "A", Status: "ERROR"})
	}
	if history := c.GetDNSHistory(10); len(history) != 2 {
		t.Fatalf("a negative window must disable dedupe, got %d records", len(history))
	}
}

// TestDNSRingRollover 环写满后覆盖最旧的记录：历史保留最新的 N 条，
// 被覆盖的条数通过 dropped 暴露。
func TestDNSRingRollover(t *testing.T) {
	c := newTestCollector(t, Limits{MaxDNSRows: 3, FailDedupeWindow: -1})

	for i := 0; i < 5; i++ {
		c.RecordDNS(DNSRecord{Domain: fmt.Sprintf("d%d.example.com", i), QType: "A", Status: "NOERROR"})
	}

	history := c.GetDNSHistory(10) // 旧 → 新
	if len(history) != 3 {
		t.Fatalf("expected the ring to hold 3 records, got %d", len(history))
	}
	for i, record := range history {
		want := fmt.Sprintf("d%d.example.com", i+2)
		if record.Domain != want {
			t.Fatalf("history[%d] = %q, want %q (oldest record must be evicted)", i, record.Domain, want)
		}
	}

	recent := c.QueryDNS(0, 2) // 新 → 旧
	if len(recent) != 2 || recent[0].Domain != "d4.example.com" || recent[1].Domain != "d3.example.com" {
		t.Fatalf("QueryDNS must return newest first, got %+v", recent)
	}

	if dropped := c.OverwrittenRecords(); dropped != 2 {
		t.Fatalf("expected 2 overwritten records, got %d", dropped)
	}

	dnsRecords, _, dnsCapacity, _ := c.RingStats()
	if dnsRecords != 3 || dnsCapacity != 3 {
		t.Fatalf("ring stats = %d/%d, want 3/3", dnsRecords, dnsCapacity)
	}
}

// TestDNSQuerySinceAndLimit 只取 since 之后的记录，limit 截断最新的若干条。
func TestDNSQuerySinceAndLimit(t *testing.T) {
	c := newTestCollector(t, Limits{MaxDNSRows: 100, FailDedupeWindow: -1})

	c.RecordDNS(DNSRecord{Domain: "old.example.com", QType: "A", Status: "NOERROR"})
	time.Sleep(5 * time.Millisecond)
	marker := nowMs()
	time.Sleep(5 * time.Millisecond)
	c.RecordDNS(DNSRecord{Domain: "new.example.com", QType: "A", Status: "NOERROR"})

	records := c.QueryDNS(marker, 100)
	if len(records) != 1 || records[0].Domain != "new.example.com" {
		t.Fatalf("since must filter out older records, got %+v", records)
	}

	all := c.QueryDNS(0, 1)
	if len(all) != 1 || all[0].Domain != "new.example.com" {
		t.Fatalf("limit must truncate to the newest records, got %+v", all)
	}
}

// TestConnectionLifecycle 一条连接的信息由 TCP/TLS/流量/关闭四条路径补全，
// 它们必须落在同一条历史记录上（而不是各占一条）。
func TestConnectionLifecycleMerge(t *testing.T) {
	c := newTestCollector(t, Limits{MaxConnRows: 10, FailDedupeWindow: -1})

	c.RecordTCP(TCPRecord{
		ConnID:    "conn-1",
		Remote:    "203.0.113.7:443",
		Domain:    "example.com",
		Outbound:  "proxy",
		LatencyUs: 12000,
	})
	c.RecordTLS(TLSRecord{
		ConnID:     "conn-1",
		Remote:     "203.0.113.7:443",
		ServerName: "example.com",
		LatencyUs:  48000,
		Version:    "TLS 1.3",
	})
	c.SyncTraffic([]TrafficRecord{
		{ConnID: "conn-1", DestIP: "203.0.113.7", Host: "example.com", Upload: 1024, Download: 4096, Outbound: "proxy"},
	})
	c.CloseConnection("conn-1", 3200, 1024, 4096)

	records := c.GetConnectionHistory(10)
	if len(records) != 1 {
		t.Fatalf("expected a single merged record, got %d", len(records))
	}
	record := records[0]
	if record.TCPLatencyUs != 12000 || record.TLSLatencyUs != 48000 {
		t.Fatalf("latencies must merge into one record, got tcp=%d tls=%d", record.TCPLatencyUs, record.TLSLatencyUs)
	}
	if record.Host != "example.com" || record.Domain != "example.com" {
		t.Fatalf("host/domain must be filled in, got host=%q domain=%q", record.Host, record.Domain)
	}
	if record.TLSVersion != "TLS 1.3" {
		t.Fatalf("TLS version must be kept, got %q", record.TLSVersion)
	}
	if record.UploadBytes != 1024 || record.DownloadBytes != 4096 {
		t.Fatalf("traffic must merge into the record, got up=%d down=%d", record.UploadBytes, record.DownloadBytes)
	}
	if !record.Closed || record.EndTime == nil || record.DurationMs == nil || *record.DurationMs != 3200 {
		t.Fatalf("close must mark the record closed with its duration, got %+v", record)
	}
}

// TestConnectionFailClosesRecord 拨号失败就是这条连接的终局：记录必须落在
// closed 上，否则时间线里看不到失败。
func TestConnectionFailClosesRecord(t *testing.T) {
	c := newTestCollector(t, Limits{MaxConnRows: 10, FailDedupeWindow: -1})

	c.RecordTCP(TCPRecord{
		ConnID:    "conn-fail",
		Remote:    "203.0.113.9:443",
		Outbound:  "proxy",
		LatencyUs: 20000,
		Error:     "dial tcp: connection refused",
	})

	records := c.GetConnectionHistory(10)
	if len(records) != 1 {
		t.Fatalf("expected a single record, got %d", len(records))
	}
	if !records[0].Closed || records[0].Error == "" {
		t.Fatalf("a failed dial must close the record, got %+v", records[0])
	}
	if records[0].DurationMs == nil || *records[0].DurationMs != 20 {
		t.Fatalf("duration must come from the dial latency, got %+v", records[0].DurationMs)
	}
}

// TestStatsCountsClosedConnections 流量统计只汇总已结束的连接。
func TestStatsCountsClosedConnections(t *testing.T) {
	c := newTestCollector(t, Limits{MaxConnRows: 10, FailDedupeWindow: -1})

	c.RecordTCP(TCPRecord{ConnID: "conn-1", Remote: "203.0.113.1:443", LatencyUs: 1000})
	c.RecordTCP(TCPRecord{ConnID: "conn-2", Remote: "203.0.113.2:443", LatencyUs: 1000})
	c.CloseConnection("conn-1", 10, 100, 200)

	upload, download := c.QueryStats()
	if upload != 100 || download != 200 {
		t.Fatalf("stats = up %d / down %d, want 100/200 (open connections excluded)", upload, download)
	}
}

// TestSyncTrafficCreatesMissingRecord 流量快照里出现、历史里没有的连接要补一条，
// 否则长时间运行的连接会在索引淘汰后丢掉字节数。
func TestSyncTrafficCreatesMissingRecord(t *testing.T) {
	c := newTestCollector(t, Limits{MaxConnRows: 10, FailDedupeWindow: -1})

	c.SyncTraffic([]TrafficRecord{
		{ConnID: "conn-sync", DestIP: "203.0.113.5", Host: "example.org", Upload: 10, Download: 20, Outbound: "direct"},
	})

	records := c.GetConnectionHistory(10)
	if len(records) != 1 {
		t.Fatalf("expected a record to be created, got %d", len(records))
	}
	if records[0].Host != "example.org" || records[0].UploadBytes != 10 || records[0].DownloadBytes != 20 {
		t.Fatalf("unexpected record: %+v", records[0])
	}
}

// TestLimitsFromClamp 配置值 0 用默认、正数生效、离谱的大值被截断。
func TestLimitsFromClamp(t *testing.T) {
	defaults := LimitsFrom(0, 0, 0)
	if defaults.MaxDNSRows != DefaultDNSRingSize || defaults.MaxConnRows != DefaultConnRingSize {
		t.Fatalf("zero must mean default, got %+v", defaults)
	}

	custom := LimitsFrom(5000, 2000, 30)
	if custom.MaxDNSRows != 5000 || custom.MaxConnRows != 2000 || custom.FailDedupeWindow != 30*time.Second {
		t.Fatalf("positive values must be honoured, got %+v", custom)
	}

	clamped := LimitsFrom(1<<30, 1<<30, 0).withDefaults()
	if clamped.MaxDNSRows != maxDNSRingSize || clamped.MaxConnRows != maxConnRingSize {
		t.Fatalf("oversized values must be clamped to the defensive caps, got %+v", clamped)
	}

	if disabled := LimitsFrom(0, 0, -1); disabled.FailDedupeWindow != -1 {
		t.Fatalf("a negative window must disable dedupe, got %+v", disabled)
	}
}

// TestRemoveLegacyDatabase 旧版本的 monitor.db（含边车文件）在启动时删掉，
// 别的东西不能碰；没有可删的文件时返回空串。
func TestRemoveLegacyDatabase(t *testing.T) {
	dir := t.TempDir()
	legacy := []string{"monitor.db", "monitor.db-journal", "monitor.db-wal", "monitor.db-shm"}
	for _, name := range legacy {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("legacy"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	keep := filepath.Join(dir, "config.json")
	if err := os.WriteFile(keep, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	summary := RemoveLegacyDatabase(dir)
	if summary == "" {
		t.Fatal("expected a summary for the removed files")
	}
	if !strings.Contains(summary, "monitor.db") {
		t.Fatalf("the summary must name what was removed, got %q", summary)
	}
	for _, name := range legacy {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s must be removed, stat err = %v", name, err)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("unrelated files must be kept: %v", err)
	}

	if second := RemoveLegacyDatabase(dir); second != "" {
		t.Fatalf("a second pass must be a no-op, got %q", second)
	}
}
