package monitor

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// TestSplitHostPort 覆盖拆分地址的边界：带端口的地址、只有地址、带方括号的
// IPv6、以及空串。拆不出来时宁可原样返回，也不能把地址截错。
func TestSplitHostPort(t *testing.T) {
	for _, test := range []struct {
		addr string
		host string
		port int
	}{
		{"192.0.2.1:6881", "192.0.2.1", 6881},
		{"example.com:443", "example.com", 443},
		{"192.0.2.1", "192.0.2.1", 0},
		{"[2001:db8::1]:6881", "2001:db8::1", 6881},
		{"2001:db8::1", "2001:db8::1", 0},
		{"", "", 0},
	} {
		host, port := splitHostPort(test.addr)
		if host != test.host || port != test.port {
			t.Fatalf("splitHostPort(%q) = (%q, %d), want (%q, %d)", test.addr, host, port, test.host, test.port)
		}
	}
}

// latencyEvent 造一条 connection_latency 事件（写入协程收到的那种）。
func latencyEvent(connID, dest string, tcpUs, tlsUs int64, startMs int64, errStr string) dbEvent {
	return dbEvent{Kind: "connection_latency", Data: map[string]interface{}{
		"conn_id":      connID,
		"dest_ip":      dest,
		"domain":       "example.com",
		"outbound":     "direct",
		"tcp":          tcpUs,
		"tls":          tlsUs,
		"start_time":   startMs,
		"process_path": "",
		"error":        errStr,
	}}
}

// connRow 是 connection_records 里这次断言要看的那几列。
type connRow struct {
	destIP   string
	destPort int
	closed   int
	endTime  sql.NullInt64
	duration sql.NullInt64
}

func readConnRow(t *testing.T, d *Database, connID string) connRow {
	t.Helper()
	var row connRow
	err := d.db.QueryRow(
		"SELECT dest_ip, dest_port, closed, end_time_ms, duration_ms FROM connection_records WHERE conn_id = ?",
		connID).Scan(&row.destIP, &row.destPort, &row.closed, &row.endTime, &row.duration)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

// TestConnectionLatencyStoresDestAndEndsFailures 端到端钉住写入路径：dest_ip
// 只留地址、端口进 dest_port 列，失败的拨号直接落成已结束的行，成功的仍留在
// "进行中"（它后面还可能继续传数据）。
func TestConnectionLatencyStoresDestAndEndsFailures(t *testing.T) {
	db, err := NewDatabaseWithLimits(filepath.Join(t.TempDir(), "monitor.db"), Limits{
		MaxSizeBytes:  -1,
		MaxDNSRows:    -1,
		MaxConnRows:   -1,
		RetentionDays: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	const startMs int64 = 1735689600000
	// 直接喂 flushBatch，不走异步通道：断言不必等 200ms 的刷新定时器。
	db.flushBatch([]dbEvent{
		latencyEvent("failed-conn", "192.0.2.1:6881", 0, 25000, startMs, "dial tcp: i/o timeout"),
		latencyEvent("open-conn", "198.51.100.7:443", 12000, 0, startMs, ""),
		latencyEvent("no-port-conn", "203.0.113.9", 0, 3000, startMs, "dial tcp: connection refused"),
	})

	failed := readConnRow(t, db, "failed-conn")
	if failed.destIP != "192.0.2.1" || failed.destPort != 6881 {
		t.Fatalf("dest must be split into ip/port, got %q/%d", failed.destIP, failed.destPort)
	}
	if failed.closed != 1 || !failed.endTime.Valid || !failed.duration.Valid {
		t.Fatalf("a failed dial must land closed with an end time, got closed=%d end=%v duration=%v",
			failed.closed, failed.endTime, failed.duration)
	}
	if failed.duration.Int64 != 25 || failed.endTime.Int64 != startMs+25 {
		t.Fatalf("unexpected duration/end for the failed dial: %d/%d", failed.duration.Int64, failed.endTime.Int64)
	}

	open := readConnRow(t, db, "open-conn")
	if open.destIP != "198.51.100.7" || open.destPort != 443 {
		t.Fatalf("dest must be split into ip/port, got %q/%d", open.destIP, open.destPort)
	}
	if open.closed != 0 || open.endTime.Valid || open.duration.Valid {
		t.Fatalf("a successful dial must stay open, got closed=%d end=%v duration=%v",
			open.closed, open.endTime, open.duration)
	}

	noPort := readConnRow(t, db, "no-port-conn")
	if noPort.destIP != "203.0.113.9" || noPort.destPort != 0 {
		t.Fatalf("an address without a port must keep the address as-is, got %q/%d", noPort.destIP, noPort.destPort)
	}
	if noPort.closed != 1 || noPort.duration.Int64 != 3 {
		t.Fatalf("unexpected rows for a failure without a port: closed=%d duration=%v", noPort.closed, noPort.duration)
	}

	// 冲突分支：后来的事件不能抹掉已有的端口与结束时间——成功事件本来就不带
	// 结束时间，直接覆盖会把这些列变回 NULL（SQLite 的 max(a,b) 遇 NULL 得 NULL）。
	db.flushBatch([]dbEvent{
		latencyEvent("failed-conn", "192.0.2.1", 5000, 0, startMs, ""),
		latencyEvent("open-conn", "198.51.100.7", 12000, 0, startMs, ""),
	})
	failed = readConnRow(t, db, "failed-conn")
	if failed.destPort != 6881 || failed.closed != 1 || !failed.endTime.Valid || failed.endTime.Int64 != startMs+25 {
		t.Fatalf("upsert must not clear existing values, got port=%d closed=%d end=%v",
			failed.destPort, failed.closed, failed.endTime)
	}
	open = readConnRow(t, db, "open-conn")
	if open.destPort != 443 || open.closed != 0 || open.endTime.Valid {
		t.Fatalf("upsert must keep the recorded port, got port=%d closed=%d end=%v",
			open.destPort, open.closed, open.endTime)
	}
}
