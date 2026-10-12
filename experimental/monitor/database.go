// experimental/monitor/database.go
package monitor

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

// dbEvent is a write event sent to the async batch writer.
type dbEvent struct {
	Kind string      // "dns", "connection", "connection_closed"
	Data interface{} // *DNSRecord, *ConnectionRecord
}

// Database wraps SQLite with async batch writing.
type Database struct {
	db          *sql.DB
	writeCh     chan dbEvent
	done        chan struct{}
	stopCh      chan struct{}
	pruneSignal chan struct{}
	closed      atomic.Bool
	dropped     atomic.Int64
	// upsertDisabled 在建唯一索引失败时置位（只写协程读，启动时设置一次），
	// 此时丢弃依赖 ON CONFLICT(conn_id) 的事件而不是崩进程。
	upsertDisabled bool
	batchSize      int
	flushInterval  time.Duration
	limits         Limits
	dbPath         string
	wg             sync.WaitGroup
}

// NewDatabase opens (or creates) the SQLite database and starts the async
// writer, applying the default growth caps (see limits.go).
func NewDatabase(path string) (*Database, error) {
	return NewDatabaseWithLimits(path, DefaultLimits())
}

// NewDatabaseWithLimits opens (or creates) the SQLite database, applies the
// configured growth caps (size / row / retention), and starts the async writer
// plus the periodic maintenance goroutine.
func NewDatabaseWithLimits(path string, limits Limits) (*Database, error) {
	limits = limits.withDefaults()
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite is single-writer

	d := &Database{
		db:            db,
		writeCh:       make(chan dbEvent, 4096),
		done:          make(chan struct{}),
		stopCh:        make(chan struct{}),
		pruneSignal:   make(chan struct{}, 1),
		batchSize:     100,
		flushInterval: 200 * time.Millisecond,
		limits:        limits,
		dbPath:        path,
	}

	if err := d.migrate(); err != nil {
		db.Close()
		return nil, err
	}

	// Growth caps: auto_vacuum=FULL so pruning actually frees disk space.
	if err := d.enableAutoVacuum(); err != nil {
		fmt.Printf("[monitor] enable auto-vacuum failed: %v\n", err)
	}

	// Time-based retention, then bring an oversized database (from a previous
	// unbounded run) back under the caps before serving.
	if err := d.Cleanup(limits.RetentionDays); err != nil {
		fmt.Printf("[monitor] cleanup failed: %v\n", err)
	}
	if err := d.enforceLimitsAtStartup(); err != nil {
		fmt.Printf("[monitor] initial trim failed: %v\n", err)
	}
	// The hard page limit is applied last: on an oversized legacy database a
	// delete could otherwise fail because the freelist needs a new page.
	if err := d.applyHardSizeLimit(); err != nil {
		fmt.Printf("[monitor] apply size limit failed: %v\n", err)
	}
	fmt.Printf("[monitor] db limits: size cap %s, dns rows %d, conn rows %d, retention %d day(s)\n",
		formatBytes(limits.MaxSizeBytes), limits.MaxDNSRows, limits.MaxConnRows, limits.RetentionDays)

	// Ensure UNIQUE index for per-connection upsert — key is conn_id.
	// 旧版本写的库可能已经有重复 conn_id（那时还没有唯一索引），先按 conn_id
	// 去重只留最新一行，否则建索引失败 → 下面 ON CONFLICT(conn_id) 的语句
	// prepare 不出语句（返回 nil）→ 写入协程空指针崩溃。
	if _, err := db.Exec("DELETE FROM connection_records WHERE id NOT IN (SELECT MAX(id) FROM connection_records GROUP BY conn_id)"); err != nil {
		fmt.Printf("[monitor] dedup connection_records failed: %v\n", err)
	}
	if _, err := db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_conn_id ON connection_records(conn_id)"); err != nil {
		fmt.Printf("[monitor] create unique index failed: %v (per-connection upsert disabled)\n", err)
		d.upsertDisabled = true
	}

	d.wg.Add(1)
	go d.writer()
	d.wg.Add(1)
	go d.maintenance()

	return d, nil
}

func (d *Database) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS dns_records (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp_ms INTEGER NOT NULL,
		domain TEXT NOT NULL,
		qtype TEXT NOT NULL DEFAULT 'A',
		transport TEXT DEFAULT '',
		latency_us INTEGER DEFAULT 0,
		status TEXT DEFAULT '',
		answers TEXT DEFAULT '[]',
		ttl INTEGER DEFAULT 0,
		is_rejected INTEGER DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_dns_timestamp ON dns_records(timestamp_ms);

	CREATE TABLE IF NOT EXISTS connection_records (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		conn_id TEXT NOT NULL,
		host TEXT,
		domain TEXT DEFAULT '',
		dest_ip TEXT DEFAULT '',
		dest_port INTEGER DEFAULT 0,
		rule TEXT DEFAULT '',
		outbound TEXT DEFAULT '',
		tcp_latency_us INTEGER DEFAULT 0,
		tls_latency_us INTEGER DEFAULT 0,
		dns_latency_us INTEGER,
		tls_version TEXT,
		tls_cipher TEXT,
		upload_bytes INTEGER DEFAULT 0,
		download_bytes INTEGER DEFAULT 0,
		start_time_ms INTEGER NOT NULL,
		end_time_ms INTEGER,
		duration_ms INTEGER,
		closed INTEGER DEFAULT 0,
		process_path TEXT DEFAULT '',
		error TEXT DEFAULT ''
	);
	CREATE INDEX IF NOT EXISTS idx_conn_conn_id ON connection_records(conn_id);
	CREATE INDEX IF NOT EXISTS idx_conn_timestamp ON connection_records(start_time_ms);
	`
	_, err := d.db.Exec(schema)
	if err != nil {
		return err
	}
	// Add columns for existing databases (ignore if already exists)
	d.db.Exec("ALTER TABLE connection_records ADD COLUMN process_path TEXT DEFAULT ''")
	d.db.Exec("ALTER TABLE connection_records ADD COLUMN error TEXT DEFAULT ''")
	return nil
}

// ---- Async writer (hot-path safe) ----

func (d *Database) writer() {
	defer d.wg.Done()

	const maxBatch = 200
	batch := make([]dbEvent, 0, maxBatch)
	ticker := time.NewTicker(d.flushInterval)
	defer ticker.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		d.flushBatch(batch)
		batch = batch[:0]
	}

	for {
		select {
		case e, ok := <-d.writeCh:
			if !ok {
				flush()
				return
			}
			batch = append(batch, e)
			if len(batch) >= d.batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-d.stopCh:
			// 退出前把缓冲区里剩下的事件写完，别丢尾批（writeCh 不会关闭，
			// 见 Close：关闭它会让并发写入 panic）。
		drain:
			for {
				select {
				case e := <-d.writeCh:
					batch = append(batch, e)
					if len(batch) >= d.batchSize {
						flush()
					}
				default:
					break drain
				}
			}
			flush()
			return
		}
	}
}

func (d *Database) flushBatch(batch []dbEvent) {
	tx, err := d.db.Begin()
	if err != nil {
		d.dropped.Add(int64(len(batch)))
		return
	}

	insertDNS := `INSERT INTO dns_records (timestamp_ms, domain, qtype, transport, latency_us, status, answers, ttl, is_rejected) VALUES (?,?,?,?,?,?,?,?,?)`
	insertConn := `INSERT INTO connection_records (conn_id, host, domain, dest_ip, dest_port, rule, outbound, tcp_latency_us, tls_latency_us, dns_latency_us, tls_version, tls_cipher, upload_bytes, download_bytes, start_time_ms, end_time_ms, duration_ms, closed, error) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	updateConnClosed := `UPDATE connection_records SET end_time_ms=?, duration_ms=?, upload_bytes=?, download_bytes=?, closed=1 WHERE conn_id=?`
	updateConnClosedByDest := `UPDATE connection_records SET end_time_ms=?, duration_ms=?, closed=1 WHERE dest_ip=?`
	updateConnMeta := `UPDATE connection_records SET host = COALESCE(NULLIF(?, ''), host), outbound = COALESCE(NULLIF(?, ''), outbound) WHERE conn_id = ?`
	upsertConnLat := `INSERT INTO connection_records (conn_id, dest_ip, domain, outbound, tcp_latency_us, tls_latency_us, start_time_ms, process_path, error) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(conn_id) DO UPDATE SET dest_ip = COALESCE(NULLIF(excluded.dest_ip, ''), connection_records.dest_ip), tcp_latency_us = MAX(connection_records.tcp_latency_us, excluded.tcp_latency_us), tls_latency_us = MAX(connection_records.tls_latency_us, excluded.tls_latency_us), domain = COALESCE(NULLIF(excluded.domain, ''), connection_records.domain), outbound = COALESCE(NULLIF(excluded.outbound, ''), connection_records.outbound), process_path = COALESCE(NULLIF(excluded.process_path, ''), connection_records.process_path), error = COALESCE(NULLIF(excluded.error, ''), connection_records.error)`

	stmtDNS, _ := tx.Prepare(insertDNS)
	stmtConn, _ := tx.Prepare(insertConn)
	stmtClosed, _ := tx.Prepare(updateConnClosed)
	stmtClosedDst, _ := tx.Prepare(updateConnClosedByDest)
	stmtMeta, _ := tx.Prepare(updateConnMeta)
	stmtLat, _ := tx.Prepare(upsertConnLat)
	defer func() {
		if stmtDNS != nil {
			stmtDNS.Close()
		}
		if stmtConn != nil {
			stmtConn.Close()
		}
		if stmtClosed != nil {
			stmtClosed.Close()
		}
		if stmtClosedDst != nil {
			stmtClosedDst.Close()
		}
		if stmtMeta != nil {
			stmtMeta.Close()
		}
		if stmtLat != nil {
			stmtLat.Close()
		}
	}()

	for _, e := range batch {
		switch e.Kind {
		case "dns":
			if stmtDNS == nil {
				d.dropped.Add(1)
				continue
			}
			r := e.Data.(*DNSRecord)
			answersJSON, _ := json.Marshal(r.Answers)
			stmtDNS.Exec(r.Timestamp, r.Domain, r.QType, r.Transport, r.LatencyUs, r.Status, string(answersJSON), r.TTL, boolToInt(r.IsRejected))
		case "connection":
			if stmtConn == nil {
				d.dropped.Add(1)
				continue
			}
			r := e.Data.(*ConnectionRecord)
			stmtConn.Exec(
				r.ID, r.Host, r.Domain, r.DestIP, r.DestPort, r.Rule, r.Outbound,
				r.TCPLatencyUs, r.TLSLatencyUs, r.DNSLatencyUs, r.TLSVersion, r.CipherSuite,
				r.UploadBytes, r.DownloadBytes, r.StartTime,
				r.EndTime, r.DurationMs, boolToInt(r.Closed),
				r.Error,
			)
		case "connection_closed":
			if stmtClosed == nil {
				d.dropped.Add(1)
				continue
			}
			r := e.Data.(*ConnectionRecord)
			stmtClosed.Exec(r.EndTime, r.DurationMs, r.UploadBytes, r.DownloadBytes, r.ID)
		case "connection_closed_by_dest":
			if stmtClosedDst == nil {
				d.dropped.Add(1)
				continue
			}
			destKey := e.Data.(string)
			stmtClosedDst.Exec(time.Now().UnixMilli(), 0, destKey)
		case "connection_meta":
			if stmtMeta == nil {
				d.dropped.Add(1)
				continue
			}
			meta := e.Data.(map[string]string)
			stmtMeta.Exec(meta["host"], meta["outbound"], meta["conn_id"])
		case "connection_latency":
			if stmtLat == nil {
				d.dropped.Add(1)
				continue
			}
			r := e.Data.(map[string]interface{})
			pp, _ := r["process_path"].(string)
			errStr, _ := r["error"].(string)
			stmtLat.Exec(r["conn_id"], r["dest_ip"], r["domain"], r["outbound"], r["tcp"], r["tls"], r["start_time"], pp, errStr)
		case "traffic_sync":
			records := e.Data.(*[]TrafficRecord)
			upsertTraffic := `INSERT INTO connection_records
				(conn_id, dest_ip, upload_bytes, download_bytes, outbound, host, domain, start_time_ms, process_path)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(conn_id) DO UPDATE SET
				upload_bytes = excluded.upload_bytes,
				download_bytes = excluded.download_bytes,
				outbound = COALESCE(NULLIF(excluded.outbound, ''), connection_records.outbound),
				host = COALESCE(NULLIF(excluded.host, ''), connection_records.host),
				domain = COALESCE(NULLIF(excluded.domain, ''), connection_records.domain),
				process_path = COALESCE(NULLIF(excluded.process_path, ''), connection_records.process_path)`
			stmtTraffic, err := tx.Prepare(upsertTraffic)
			if err != nil {
				// 没有唯一索引时这里会失败（ON CONFLICT 匹配不到约束）。
				d.dropped.Add(int64(len(*records)))
				continue
			}
			now := time.Now().UnixMilli()
			for _, tr := range *records {
				connID := tr.ConnID
				if connID == "" {
					connID = fmt.Sprintf("%s@%d", tr.Host, now)
				}
				stmtTraffic.Exec(connID, tr.DestIP, tr.Upload, tr.Download, tr.Outbound, tr.Host, tr.Domain, now, tr.ProcessPath)
			}
			stmtTraffic.Close()
		}
	}

	if err := tx.Commit(); err != nil {
		d.dropped.Add(int64(len(batch)))
		// A commit can fail because the hard size cap (PRAGMA max_page_count)
		// was reached: ask maintenance for an immediate prune instead of
		// dropping records until the next tick.
		d.signalPrune()
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---- Non-blocking writes (hot-path) ----

// send 入队一个事件。Close 之后一律丢弃：writeCh 永不关闭，所以不会出现
// “send on closed channel” 崩溃（Close 与热路径写入是并发关系）。
func (d *Database) send(ev dbEvent) {
	if d.closed.Load() {
		d.dropped.Add(1)
		return
	}
	select {
	case d.writeCh <- ev:
	default:
		d.dropped.Add(1)
	}
}

func (d *Database) WriteDNS(r *DNSRecord) {
	d.send(dbEvent{Kind: "dns", Data: r})
}

func (d *Database) WriteConnection(r *ConnectionRecord) {
	d.send(dbEvent{Kind: "connection", Data: r})
}

func (d *Database) WriteConnectionClosed(r *ConnectionRecord) {
	d.send(dbEvent{Kind: "connection_closed", Data: r})
}

func (d *Database) WriteConnectionClosedByDest(destKey string) {
	if destKey == "" {
		return
	}
	d.send(dbEvent{Kind: "connection_closed_by_dest", Data: destKey})
}

func (d *Database) UpdateConnectionMeta(connID, host, outbound string) {
	if connID == "" || (host == "" && outbound == "") {
		return
	}
	d.send(dbEvent{Kind: "connection_meta", Data: map[string]string{"conn_id": connID, "host": host, "outbound": outbound}})
}

func (d *Database) WriteConnectionMeta(connID, host, outbound string) {
	d.UpdateConnectionMeta(connID, host, outbound)
}

func (d *Database) WriteLatency(connID, destIP, domain, outbound string, tcpLatencyUs, tlsLatencyUs int64, processPath, errStr string) {
	if connID == "" || (tcpLatencyUs == 0 && tlsLatencyUs == 0) {
		return
	}
	if d.upsertDisabled {
		return
	}
	d.send(dbEvent{Kind: "connection_latency", Data: map[string]interface{}{"conn_id": connID, "dest_ip": destIP, "domain": domain, "outbound": outbound, "tcp": tcpLatencyUs, "tls": tlsLatencyUs, "start_time": time.Now().UnixMilli(), "process_path": processPath, "error": errStr}})
}

// DroppedRecords returns the count of events dropped due to full buffer.
func (d *Database) DroppedRecords() int64 {
	return d.dropped.Load()
}

// ---- Query methods ----

func (d *Database) QueryDNS(since int64, limit int) ([]DNSRecord, error) {
	rows, err := d.db.Query(
		"SELECT timestamp_ms, domain, qtype, transport, latency_us, status, answers, ttl, is_rejected FROM dns_records WHERE timestamp_ms > ? ORDER BY timestamp_ms DESC LIMIT ?",
		since, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []DNSRecord
	for rows.Next() {
		var r DNSRecord
		var answersJSON string
		if err := rows.Scan(&r.Timestamp, &r.Domain, &r.QType, &r.Transport, &r.LatencyUs, &r.Status, &answersJSON, &r.TTL, &r.IsRejected); err != nil {
			continue
		}
		json.Unmarshal([]byte(answersJSON), &r.Answers)
		records = append(records, r)
	}
	return records, nil
}

func (d *Database) QueryConnections(since int64, limit int) ([]ConnectionRecord, error) {
	rows, err := d.db.Query(
		"SELECT conn_id, host, domain, dest_ip, dest_port, rule, outbound, tcp_latency_us, tls_latency_us, dns_latency_us, tls_version, tls_cipher, upload_bytes, download_bytes, start_time_ms, end_time_ms, duration_ms, closed, process_path, error FROM connection_records WHERE start_time_ms > ? ORDER BY start_time_ms DESC LIMIT ?",
		since, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []ConnectionRecord
	for rows.Next() {
		var r ConnectionRecord
		var (
			dnsLatVal   interface{}
			tlsVerVal   interface{}
			tlsCipVal   interface{}
			endTimeVal  interface{}
			durMsVal    interface{}
			procPathVal interface{}
		)
		if err := rows.Scan(&r.ID, &r.Host, &r.Domain, &r.DestIP, &r.DestPort, &r.Rule, &r.Outbound, &r.TCPLatencyUs, &r.TLSLatencyUs, &dnsLatVal, &tlsVerVal, &tlsCipVal, &r.UploadBytes, &r.DownloadBytes, &r.StartTime, &endTimeVal, &durMsVal, &r.Closed, &procPathVal, &r.Error); err != nil {
			continue
		}
		if dnsLatVal != nil {
			if v, ok := dnsLatVal.(int64); ok {
				r.DNSLatencyUs = &v
			}
		}
		if tlsVerVal != nil {
			if v, ok := tlsVerVal.(string); ok {
				r.TLSVersion = v
			}
		}
		if tlsCipVal != nil {
			if v, ok := tlsCipVal.(string); ok {
				r.CipherSuite = v
			}
		}
		if endTimeVal != nil {
			if v, ok := endTimeVal.(int64); ok {
				r.EndTime = &v
			}
		}
		if durMsVal != nil {
			if v, ok := durMsVal.(int64); ok {
				r.DurationMs = &v
			}
		}
		if procPathVal != nil {
			if v, ok := procPathVal.(string); ok {
				r.ProcessPath = v
			}
		}
		records = append(records, r)
	}
	return records, nil
}

func (d *Database) QueryStats() (uploadTotal, downloadTotal int64, err error) {
	err = d.db.QueryRow(
		"SELECT COALESCE(SUM(upload_bytes), 0), COALESCE(SUM(download_bytes), 0) FROM connection_records WHERE closed = 1",
	).Scan(&uploadTotal, &downloadTotal)
	return
}

// Cleanup deletes records older than the given number of days (<= 0 disables
// time-based retention).
func (d *Database) Cleanup(days int) error {
	if days <= 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	cutoff := now - int64(days)*86400000

	_, err := d.db.Exec("DELETE FROM dns_records WHERE timestamp_ms < ?", cutoff)
	if err != nil {
		return err
	}
	deleted, err := d.db.Exec("DELETE FROM connection_records WHERE start_time_ms < ?", cutoff)
	if err != nil {
		return err
	}
	if deleted != nil {
		if affected, affectedErr := deleted.RowsAffected(); affectedErr == nil && affected > 0 {
			if err = d.checkpoint(); err != nil {
				return err
			}
		}
	}
	return nil
}

// enforceLimitsAtStartup applies the row/size caps once before the writer
// starts, so a database left oversized by an earlier run is trimmed up front.
func (d *Database) enforceLimitsAtStartup() error {
	before := d.fileSize()
	d.enforceLimits()
	after := d.fileSize()
	if after < before {
		fmt.Printf("[monitor] db shrunk %s → %s\n", formatBytes(before), formatBytes(after))
	}
	return nil
}

// Close gracefully shuts down the writer and closes the database.
func (d *Database) Close() error {
	if d.closed.Swap(true) {
		return nil // already closed
	}
	// 注意：不关闭 writeCh。热路径（DNS/dial goroutine）随时可能写，
	// 关闭它会让并发的 send 变成 “send on closed channel” panic；
	// 写入协程靠 stopCh 退出，并在退出前把缓冲区排空。
	close(d.stopCh)
	d.wg.Wait()
	close(d.done)
	return d.db.Close()
}

// SyncTraffic bulk-updates upload/download for active connections.
// Uses async channel — does not block caller.
func (d *Database) SyncTraffic(records []TrafficRecord) {
	if len(records) == 0 {
		return
	}
	select {
	case d.writeCh <- dbEvent{Kind: "traffic_sync", Data: &records}:
	default:
		d.dropped.Add(1)
	}
}
