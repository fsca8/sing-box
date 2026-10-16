// experimental/monitor/collector.go
package monitor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

type Collector struct {
	mu      sync.RWMutex
	dnsBuf  *RingBuffer[DNSRecord]
	connBuf *RingBuffer[ConnectionRecord]
	db      *Database
	connMap map[string]*ConnectionRecord // remote addr → pending connection
	// connOrder 记录 connMap 的插入顺序，用于按 maxConnMapEntries 淘汰最旧的条目：
	// connMap 是"连接建立→关闭"之间的匹配表，关闭链路目前不保证被调用，
	// 不设上限会在长期运行的网关上无界增长。
	connOrder []string
	// failConn / failDNS 抑制窗口内成片重复的失败记录（见 failDedupe）。
	// 两侧分开一个实例，键的构成不同（连接是 remote|outbound，DNS 是
	// domain|qtype|status），混在一起也没有好处。都由 c.mu 保护。
	failConn *failDedupe
	failDNS  *failDedupe
	// now 是去重用的时钟，默认 time.Now；测试注入它就不必按窗口真等。
	now func() time.Time
	// eventCB 用 atomic 存：热路径（DNS/dial goroutine）读，Kotlin 侧随时可设。
	eventCB atomic.Pointer[func(string)] // callback to push JSON to Android
}

// globalCollector 用 atomic 存指针：Get() 在数据面热路径调用，
// 与 NewCollectorWithLimits 的写入是并发关系。
var globalCollector atomic.Pointer[Collector]

// maxConnMapEntries 内存中待匹配连接表的上限（超出按插入顺序淘汰最旧的一批）。
const maxConnMapEntries = 4096

// maxFailDedupeEntries 是失败去重表的上限：上游故障/重试风暴会在短时间对同一
// 目标产生成片失败记录，但长期运行的目标又会不断更换，表本身同样要有上限。
const maxFailDedupeEntries = 4096

// failDedupe 只回答一个问题：窗口内重复的同键失败，这次还要不要落库。
// 它不碰 ring buffer，也不碰事件推送——那两条路径必须照旧。
// 键由调用方拼，调用者需自行保证并发安全（Collector 用 c.mu）。
type failDedupe struct {
	window time.Duration
	seen   map[string]time.Time
	// order 记录入表顺序，用于按 maxFailDedupeEntries 淘汰最旧的条目，
	// 写法与 connMap/connOrder 一致。
	order []failEntry
}

type failEntry struct {
	key  string
	seen time.Time
}

func newFailDedupe(window time.Duration) *failDedupe {
	return &failDedupe{window: window, seen: make(map[string]time.Time)}
}

// allow 判断这次失败是否要落库：窗口内同一个键只放行第一条。
// window <= 0 表示关闭去重；nil 接收者（手工构造的 Collector）一律放行。
func (f *failDedupe) allow(key string, now time.Time) bool {
	if f == nil || f.window <= 0 {
		return true
	}
	if seen, ok := f.seen[key]; ok && now.Sub(seen) < f.window {
		return false
	}
	f.seen[key] = now
	f.order = append(f.order, failEntry{key: key, seen: now})
	for len(f.order) > maxFailDedupeEntries {
		oldest := f.order[0]
		f.order = f.order[1:]
		// 同一个键在窗口过后会再次入表，顺序表里就留着它的旧条目；时间对不上
		// 说明这条已被更新的条目取代，不能顺手把这个键删掉。
		if seen, ok := f.seen[oldest.key]; ok && seen.Equal(oldest.seen) {
			delete(f.seen, oldest.key)
		}
	}
	// order 反复切片后底层数组会一直挂着，积到一定量就搬一次家。
	if cap(f.order) > 4*maxFailDedupeEntries && len(f.order) < 2*maxFailDedupeEntries {
		f.order = append([]failEntry(nil), f.order...)
	}
	return true
}

// nowFn 读去重时钟；未注入时退回 time.Now。
func (c *Collector) nowFn() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now()
}

// NewCollector creates the collector with the default growth caps
// (100 MB / 200k DNS rows / 100k connection rows / 7 days).
func NewCollector(dbPath string) (*Collector, error) {
	return NewCollectorWithLimits(dbPath, DefaultLimits())
}

// NewCollectorWithLimits creates the collector with explicit growth caps
// (see LimitsFrom for the config file mapping).
func NewCollectorWithLimits(dbPath string, limits Limits) (*Collector, error) {
	limits = limits.withDefaults()
	dir := filepath.Dir(dbPath)
	os.MkdirAll(dir, 0755)

	db, err := NewDatabaseWithLimits(dbPath, limits)
	if err != nil {
		return nil, err
	}

	c := &Collector{
		dnsBuf:   NewRingBuffer[DNSRecord](500),
		connBuf:  NewRingBuffer[ConnectionRecord](200),
		db:       db,
		connMap:  make(map[string]*ConnectionRecord),
		failConn: newFailDedupe(limits.FailDedupeWindow),
		failDNS:  newFailDedupe(limits.FailDedupeWindow),
		now:      time.Now,
	}
	// 重载时替换全局 collector：先把旧的关掉，避免泄漏库句柄与写入协程。
	if old := globalCollector.Swap(c); old != nil {
		_ = old.Close()
	}
	return c, nil
}

func Get() *Collector {
	return globalCollector.Load()
}

// Close 关闭全局 collector（进程退出/配置重载前调用），并清空全局指针。
// 不调用它的话：SQLite 写入协程与连接池会一直留着，重载还会再开一个库句柄。
func Close() error {
	collector := globalCollector.Swap(nil)
	if collector == nil {
		return nil
	}
	return collector.Close()
}

// putConnLocked 写入 connMap 并维持上限，调用者必须已持有写锁。
func (c *Collector) putConnLocked(key string, conn *ConnectionRecord) {
	if _, exists := c.connMap[key]; !exists {
		c.connOrder = append(c.connOrder, key)
	}
	c.connMap[key] = conn
	for len(c.connOrder) > maxConnMapEntries {
		delete(c.connMap, c.connOrder[0])
		c.connOrder = c.connOrder[1:]
	}
	// connOrder 反复切片后底层数组会一直挂着，积到一定量就搬一次家。
	if cap(c.connOrder) > 4*maxConnMapEntries && len(c.connOrder) < 2*maxConnMapEntries {
		c.connOrder = append([]string(nil), c.connOrder...)
	}
}

func (c *Collector) SetEventCallback(cb func(string)) {
	c.eventCB.Store(&cb)
}

func (c *Collector) pushJSON(v any) {
	cb := c.eventCB.Load()
	if cb == nil {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	(*cb)(string(b))
}

// ---- DNS ----

func (c *Collector) RecordDNS(r DNSRecord) {
	r.Timestamp = nowMs()
	c.mu.Lock()
	c.dnsBuf.Push(r)
	// 失败的解析在故障期间会成片重复，只让窗口内的第一条落库；
	// 缓冲区与事件推送照旧，界面上的实时流水不受影响。
	writeRecord := true
	if r.Status == "ERROR" {
		writeRecord = c.failDNS.allow(dnsFailKey(r), c.nowFn())
	}
	c.mu.Unlock()

	// Async SQLite write (non-blocking)
	if c.db != nil && writeRecord {
		c.db.WriteDNS(&r)
	}

	ev := Event{Type: "dns", Timestamp: r.Timestamp, DNS: &r}
	c.pushJSON(ev)
}

// dnsFailKey 是 DNS 失败去重的键；status 写进键里，便于把键读成
// "哪个域名、哪种记录、什么结果"。
func dnsFailKey(r DNSRecord) string {
	return r.Domain + "|" + r.QType + "|" + r.Status
}

func (c *Collector) RecordDNSRejected(domain string) {
	c.RecordDNS(DNSRecord{
		Domain:     domain,
		QType:      "A",
		Status:     "REFUSED",
		IsRejected: true,
	})
}

// ---- TCP ----

func (c *Collector) RecordTCP(r TCPRecord) {
	r.Timestamp = nowMs()

	// Use ConnID if available (per-connection), fallback to Remote (per-address)
	key := r.Remote
	if r.ConnID != "" {
		key = r.ConnID
	}

	// Only track in connMap for latency matching — SQLite is populated by traffic_sync
	c.mu.Lock()
	conn, exists := c.connMap[key]
	if !exists {
		conn = &ConnectionRecord{
			ID:           key,
			Domain:       r.Domain,
			DestIP:       r.Remote,
			Outbound:     r.Outbound,
			TCPLatencyUs: r.LatencyUs,
			StartTime:    r.Timestamp,
			Error:        r.Error,
		}
		if r.ProcessPath != "" {
			conn.ProcessPath = r.ProcessPath
		}
		c.putConnLocked(key, conn)
	} else {
		conn.TCPLatencyUs = r.LatencyUs
		if r.Domain != "" {
			conn.Domain = r.Domain
		}
		if r.Outbound != "" {
			conn.Outbound = r.Outbound
		}
		if r.ProcessPath != "" {
			conn.ProcessPath = r.ProcessPath
		}
		if r.Error != "" {
			conn.Error = r.Error
		}
	}
	// 拨号失败在故障期间会成片重复，只让窗口内的第一条落库；connMap
	// 与事件推送照旧，热路径上的其它行为不变。
	writeLatency := true
	if r.Error != "" {
		writeLatency = c.failConn.allow(connFailKey(r), c.nowFn())
	}
	c.mu.Unlock()

	// Sync latency to SQLite (per-connection INSERT, traffic_sync upserts same row by conn_id)
	if c.db != nil && writeLatency {
		c.db.WriteLatency(key, r.Remote, r.Domain, r.Outbound, r.LatencyUs, 0, r.ProcessPath, r.Error)
	}

	ev := Event{Type: "tcp", Timestamp: r.Timestamp}
	b, _ := json.Marshal(r)
	ev.Message = string(b)
	c.pushJSON(ev)
}

// connFailKey 是连接失败去重的键：同一个目的地同一个出站才算同一类失败。
func connFailKey(r TCPRecord) string {
	return r.Remote + "|" + r.Outbound
}

// ---- TLS ----

func (c *Collector) RecordTLS(r TLSRecord) {
	r.Timestamp = nowMs()

	// Use ConnID if available (per-connection), fallback to Remote (per-address)
	key := r.Remote
	if r.ConnID != "" {
		key = r.ConnID
	}

	// Find matching connection by key (same ConnID as TCP hook)
	c.mu.Lock()
	conn, exists := c.connMap[key]
	if exists {
		conn.TLSLatencyUs = r.LatencyUs
		conn.Host = r.ServerName
		conn.TLSVersion = r.Version
		if r.Domain != "" {
			conn.Domain = r.Domain
		}
		if r.Outbound != "" {
			conn.Outbound = r.Outbound
		}
		if r.Error != "" {
			conn.Error = r.Error
		}
		// Sync latency + host to SQLite
		if c.db != nil {
			c.db.WriteLatency(key, r.Remote, r.Domain, r.Outbound, 0, r.LatencyUs, r.ProcessPath, r.Error)
			c.db.WriteConnectionMeta(key, r.ServerName, "")
		}
	} else {
		// TCP hook not fired yet or came from a different path — track standalone
		conn = &ConnectionRecord{
			ID:           key,
			Domain:       r.Domain,
			DestIP:       r.Remote,
			Outbound:     r.Outbound,
			Host:         r.ServerName,
			TLSLatencyUs: r.LatencyUs,
			TLSVersion:   r.Version,
			StartTime:    r.Timestamp,
			Error:        r.Error,
		}
		if r.ProcessPath != "" {
			conn.ProcessPath = r.ProcessPath
		}
		c.putConnLocked(key, conn)
		if c.db != nil {
			c.db.WriteLatency(key, r.Remote, r.Domain, r.Outbound, 0, r.LatencyUs, r.ProcessPath, r.Error)
			c.db.WriteConnectionMeta(key, r.ServerName, "")
		}
	}
	c.mu.Unlock()

	ev := Event{Type: "tls", Timestamp: r.Timestamp}
	b, _ := json.Marshal(r)
	ev.Message = string(b)
	c.pushJSON(ev)
}

// ---- Connection ----

func (c *Collector) RecordConnection(r ConnectionRecord) {
	r.StartTime = nowMs()

	// Use the same key scheme as RecordTCP/RecordTLS (ConnID), so per-connection
	// TCP/TLS latency hooks can merge into this entry. Fallback to DestIP when
	// no ConnID is available.
	key := r.ID
	if key == "" {
		key = r.DestIP
	}

	c.mu.Lock()
	// Merge with existing connMap entry (from RecordTCP) if present
	if existing, ok := c.connMap[key]; ok {
		if existing.Host == "" {
			existing.Host = r.Host
		}
		if existing.Outbound == "" {
			existing.Outbound = r.Outbound
		}
		if existing.StartTime == 0 {
			existing.StartTime = r.StartTime
		}
		c.connBuf.Push(*existing)
		c.mu.Unlock()
		// Update existing SQLite row with metadata (not INSERT)
		if c.db != nil {
			c.db.UpdateConnectionMeta(key, r.Host, r.Outbound)
		}
		ev := Event{Type: "connection", Timestamp: r.StartTime, Connection: existing}
		c.pushJSON(ev)
		return
	}
	// No TCP hook entry yet — create new
	r.ID = key
	c.putConnLocked(key, &r)
	c.connBuf.Push(r)
	c.mu.Unlock()

	if c.db != nil {
		c.db.WriteConnection(&r)
	}

	ev := Event{Type: "connection", Timestamp: r.StartTime, Connection: &r}
	c.pushJSON(ev)
}

func (c *Collector) CloseConnection(id string, durationMs int64, upload, download int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Update in buffer
	for i := range c.connBuf.Size() {
		idx := (c.connBuf.head + i) % c.connBuf.cap
		rec := &c.connBuf.buf[idx]
		if rec.ID == id {
			now := nowMs()
			rec.EndTime = &now
			dur := durationMs
			rec.DurationMs = &dur
			rec.UploadBytes = upload
			rec.DownloadBytes = download
			rec.Closed = true

			// Async SQLite write (UPDATE)
			if c.db != nil {
				c.db.WriteConnectionClosed(rec)
			}

			ev := Event{Type: "connection_closed", Timestamp: *rec.EndTime, Connection: rec}
			c.pushJSON(ev)
			break
		}
	}
}

// CloseConnByDest marks connection closed by dest_ip key (called from route/conn.go).
func (c *Collector) CloseConnByDest(destKey string) {
	c.mu.Lock()
	conn, exists := c.connMap[destKey]
	if exists {
		conn.Closed = true
		now := nowMs()
		conn.EndTime = &now
		dur := now - conn.StartTime
		conn.DurationMs = &dur
		if c.db != nil {
			c.db.WriteConnectionClosed(conn)
		}
	}
	c.mu.Unlock()

	// Also try to find and close by looking at connMap values
	if !exists {
		c.mu.Lock()
		for _, conn := range c.connMap {
			if conn.Host == destKey || (conn.Host == "" && conn.DestIP == destKey) {
				conn.Closed = true
				now := nowMs()
				conn.EndTime = &now
				dur := now - conn.StartTime
				conn.DurationMs = &dur
				if c.db != nil {
					c.db.WriteConnectionClosed(conn)
				}
				break
			}
		}
		c.mu.Unlock()
	}

	// Always try to mark closed in SQLite by dest_ip
	if c.db != nil {
		c.db.WriteConnectionClosedByDest(destKey)
	}
}

// ---- Query methods ----

func (c *Collector) GetDNSHistory(limit int) []DNSRecord {
	return c.dnsBuf.Last(limit)
}

func (c *Collector) GetConnectionHistory(limit int) []ConnectionRecord {
	return c.connBuf.Last(limit)
}

// ---- SQLite-backed history queries (for HTTP API) ----

func (c *Collector) QueryDNS(since int64, limit int) ([]DNSRecord, error) {
	if c.db == nil {
		return nil, nil
	}
	return c.db.QueryDNS(since, limit)
}

func (c *Collector) QueryConnections(since int64, limit int) ([]ConnectionRecord, error) {
	if c.db == nil {
		return nil, nil
	}
	return c.db.QueryConnections(since, limit)
}

func (c *Collector) QueryStats() (int64, int64, error) {
	if c.db == nil {
		return 0, 0, nil
	}
	return c.db.QueryStats()
}

func (c *Collector) DroppedRecords() int64 {
	if c.db == nil {
		return 0
	}
	return c.db.DroppedRecords()
}

// Close shuts down the database writer. Call on service shutdown.
func (c *Collector) Close() error {
	if c.db != nil {
		return c.db.Close()
	}
	return nil
}

// TrafficRecord holds per-connection traffic at a point in time.
type TrafficRecord struct {
	ConnID      string
	DestIP      string
	Upload      int64
	Download    int64
	Outbound    string
	Host        string
	Domain      string
	ProcessPath string
}

// SyncTraffic bulk-updates upload/download for active connections.
// Called periodically from Clash API snapshot.
func (c *Collector) SyncTraffic(records []TrafficRecord) {
	if c.db == nil || len(records) == 0 {
		return
	}
	c.db.SyncTraffic(records)
}
