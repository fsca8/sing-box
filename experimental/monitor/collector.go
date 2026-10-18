// experimental/monitor/collector.go
package monitor

import (
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Collector 把所有监控数据留在内存里：
//
//   - dns 是 DNS 记录的环形缓冲（值语义，只追加）；
//   - conns 是连接记录的环形缓冲，存的是指针——一条连接的信息由 TCP 拨号、
//     TLS 握手、流量同步、连接关闭四条路径先后补全，必须落在同一条记录上；
//   - connIndex 按连接 key 索引同一批指针，让上面的更新是 O(1)，并且与环保持
//     同样的淘汰顺序（环覆盖掉一条，索引也会随容量淘汰它，更新则新建一条记录，
//     这正是"这条连接已经不在历史里"应有的行为）。
//
// 所有字段都由 c.mu 保护；环自己不加锁（见 ringbuf.go）。
type Collector struct {
	mu        sync.RWMutex
	dns       *recordRing[DNSRecord]
	conns     *recordRing[*ConnectionRecord]
	connIndex map[string]*ConnectionRecord // key → pending/recent connection
	connOrder []string                     // connIndex 的插入顺序，用于淘汰
	// maxIndexEntries 略大于连接环容量：还在环里的记录一定还能被后续事件更新到。
	maxIndexEntries int
	// failConn / failDNS 抑制窗口内成片重复的失败记录（见 failDedupe）。
	// 两侧分开一个实例，键的构成不同（连接是 remote|outbound，DNS 是
	// domain|qtype|status），混在一起也没有好处。都由 c.mu 保护。
	failConn *failDedupe
	failDNS  *failDedupe
	// now 是去重用的时钟，默认 time.Now；测试注入它就不必按窗口真等。
	now func() time.Time
	// eventCB 用 atomic 存：热路径（DNS/dial goroutine）读，Kotlin 侧随时可设。
	eventCB atomic.Pointer[func(string)] // callback to push JSON to Android
	// overwritten 统计被环覆盖掉的记录数（界面上显示为 dropped）。
	overwritten atomic.Int64
	limits      Limits
}

// globalCollector 用 atomic 存指针：Get() 在数据面热路径调用，
// 与 NewCollectorWithLimits 的写入是并发关系。
var globalCollector atomic.Pointer[Collector]

// maxFailDedupeEntries 是失败去重表的上限：上游故障/重试风暴会在短时间对同一
// 目标产生成片失败记录，但长期运行的目标又会不断更换，表本身同样要有上限。
const maxFailDedupeEntries = 4096

// failDedupe 只回答一个问题：窗口内重复的同键失败，这次还要不要记。
// 键由调用方拼，调用者需自行保证并发安全（Collector 用 c.mu）。
type failDedupe struct {
	window time.Duration
	seen   map[string]time.Time
	// order 记录入表顺序，用于按 maxFailDedupeEntries 淘汰最旧的条目，
	// 写法与 connIndex/connOrder 一致。
	order []failEntry
}

type failEntry struct {
	key  string
	seen time.Time
}

func newFailDedupe(window time.Duration) *failDedupe {
	return &failDedupe{window: window, seen: make(map[string]time.Time)}
}

// allow 判断这次失败是否要记：窗口内同一个键只放行第一条。
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

// NewCollector creates the collector with the default ring sizes
// (DNS 2000 / connections 1000).
func NewCollector() *Collector {
	return NewCollectorWithLimits(DefaultLimits())
}

// NewCollectorWithLimits creates the collector with explicit ring sizes
// (see LimitsFrom for the config-file mapping).
func NewCollectorWithLimits(limits Limits) *Collector {
	limits = limits.withDefaults()

	c := &Collector{
		dns:             newRecordRing[DNSRecord](limits.MaxDNSRows),
		conns:           newRecordRing[*ConnectionRecord](limits.MaxConnRows),
		connIndex:       make(map[string]*ConnectionRecord),
		maxIndexEntries: limits.MaxConnRows * 2,
		failConn:        newFailDedupe(limits.FailDedupeWindow),
		failDNS:         newFailDedupe(limits.FailDedupeWindow),
		now:             time.Now,
		limits:          limits,
	}
	if c.maxIndexEntries < 256 {
		c.maxIndexEntries = 256
	}
	// 重载时替换全局 collector：旧的在内存里，不需要收尾，直接换掉即可。
	globalCollector.Swap(c)
	return c
}

func Get() *Collector {
	return globalCollector.Load()
}

// Close 清空全局 collector（进程退出/配置重载前调用）。数据都在内存里，
// 没有需要落盘或收尾的东西。
func Close() error {
	globalCollector.Swap(nil)
	return nil
}

// Limits 返回采集层实际生效的容量（已填默认值并截断）。
func (c *Collector) Limits() Limits {
	return c.limits
}

// Close 释放采集器。数据都在内存里，没有句柄或写协程需要收尾，这里只是为了
// 与落库时代的方法集保持兼容。
func (c *Collector) Close() error {
	return nil
}

// putConnLocked 写入 connIndex 并维持上限，调用者必须已持有写锁。
func (c *Collector) putConnLocked(key string, conn *ConnectionRecord) {
	if _, exists := c.connIndex[key]; !exists {
		c.connOrder = append(c.connOrder, key)
	}
	c.connIndex[key] = conn
	for len(c.connOrder) > c.maxIndexEntries {
		delete(c.connIndex, c.connOrder[0])
		c.connOrder = c.connOrder[1:]
	}
	// connOrder 反复切片后底层数组会一直挂着，积到一定量就搬一次家。
	if cap(c.connOrder) > 4*c.maxIndexEntries && len(c.connOrder) < 2*c.maxIndexEntries {
		c.connOrder = append([]string(nil), c.connOrder...)
	}
}

// pushConnLocked 把一条连接记录放进历史环，调用者必须已持有写锁。
func (c *Collector) pushConnLocked(conn *ConnectionRecord) {
	if c.conns.push(conn) {
		c.overwritten.Add(1)
	}
}

// closeRecordOnFailure 记下失败的结局：拨号失败就是这条连接的终局，否则这些
// 记录会永远停在"进行中"，时间线里看不到失败。
func closeRecordOnFailure(conn *ConnectionRecord) {
	latencyUs := conn.TCPLatencyUs
	if conn.TLSLatencyUs > latencyUs {
		latencyUs = conn.TLSLatencyUs
	}
	durationMs := latencyUs / 1000
	endTime := conn.StartTime + durationMs
	conn.Closed = true
	conn.DurationMs = &durationMs
	conn.EndTime = &endTime
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
	// 失败的解析在故障期间会成片重复：窗口内只留第一条，不然环会被这类记录
	// 冲刷掉。非 ERROR 的结果一律照记。
	write := true
	if r.Status == "ERROR" {
		write = c.failDNS.allow(dnsFailKey(r), c.nowFn())
	}
	if write && c.dns.push(r) {
		c.overwritten.Add(1)
	}
	c.mu.Unlock()

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

	c.mu.Lock()
	conn, exists := c.connIndex[key]
	switch {
	case exists:
		// 同一条连接的重试/后续拨号：就地合并，延迟取较大值（与落库时代的
		// ON CONFLICT ... MAX(tcp_latency_us) 一致）。
		if r.LatencyUs > conn.TCPLatencyUs {
			conn.TCPLatencyUs = r.LatencyUs
		}
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
			closeRecordOnFailure(conn)
		}
	case r.Error != "" && !c.failConn.allow(connFailKey(r), c.nowFn()):
		// 窗口内重复的失败：不占历史（事件推送照旧）。
	default:
		conn = &ConnectionRecord{
			ID:           key,
			Domain:       r.Domain,
			DestIP:       r.Remote,
			Outbound:     r.Outbound,
			TCPLatencyUs: r.LatencyUs,
			StartTime:    r.Timestamp,
			Error:        r.Error,
			ProcessPath:  r.ProcessPath,
		}
		if r.Error != "" {
			closeRecordOnFailure(conn)
		}
		c.putConnLocked(key, conn)
		c.pushConnLocked(conn)
	}
	c.mu.Unlock()

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

	c.mu.Lock()
	conn, exists := c.connIndex[key]
	switch {
	case exists:
		if r.LatencyUs > conn.TLSLatencyUs {
			conn.TLSLatencyUs = r.LatencyUs
		}
		if r.ServerName != "" {
			conn.Host = r.ServerName
		}
		if r.Domain != "" {
			conn.Domain = r.Domain
		}
		if r.Outbound != "" {
			conn.Outbound = r.Outbound
		}
		if r.ProcessPath != "" {
			conn.ProcessPath = r.ProcessPath
		}
		if r.Version != "" {
			conn.TLSVersion = r.Version
		}
		if r.CipherSuite != "" {
			conn.CipherSuite = r.CipherSuite
		}
		if r.Error != "" {
			conn.Error = r.Error
			closeRecordOnFailure(conn)
		}
	case r.LatencyUs > 0 || r.Error != "":
		// TCP hook 还没到（或走了别的路径）：单独记一条。
		conn = &ConnectionRecord{
			ID:           key,
			Domain:       r.Domain,
			DestIP:       r.Remote,
			Outbound:     r.Outbound,
			Host:         r.ServerName,
			TLSLatencyUs: r.LatencyUs,
			TLSVersion:   r.Version,
			CipherSuite:  r.CipherSuite,
			StartTime:    r.Timestamp,
			Error:        r.Error,
			ProcessPath:  r.ProcessPath,
		}
		if r.Error != "" {
			closeRecordOnFailure(conn)
		}
		c.putConnLocked(key, conn)
		c.pushConnLocked(conn)
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
	existing, ok := c.connIndex[key]
	if ok {
		// Merge with the existing record (from RecordTCP) if present.
		if existing.Host == "" {
			existing.Host = r.Host
		}
		if existing.Outbound == "" {
			existing.Outbound = r.Outbound
		}
		if existing.StartTime == 0 {
			existing.StartTime = r.StartTime
		}
	} else {
		r.ID = key
		existing = &r
		c.putConnLocked(key, existing)
		c.pushConnLocked(existing)
	}
	snapshot := *existing
	c.mu.Unlock()

	ev := Event{Type: "connection", Timestamp: snapshot.StartTime, Connection: &snapshot}
	c.pushJSON(ev)
}

// CloseConnection 记下一条连接的结局（时长与字节数），按 key 找同一条记录。
func (c *Collector) CloseConnection(id string, durationMs int64, upload, download int64) {
	c.mu.Lock()
	conn := c.findConnLocked(id)
	if conn == nil {
		c.mu.Unlock()
		return
	}
	endTime := nowMs()
	conn.EndTime = &endTime
	conn.DurationMs = &durationMs
	conn.UploadBytes = upload
	conn.DownloadBytes = download
	conn.Closed = true
	snapshot := *conn
	c.mu.Unlock()

	ev := Event{Type: "connection_closed", Timestamp: endTime, Connection: &snapshot}
	c.pushJSON(ev)
}

// CloseConnByDest 按目的地（dest_ip，或只有 host 时的 host）关闭记录。
func (c *Collector) CloseConnByDest(destKey string) {
	c.mu.Lock()
	endTime := nowMs()
	durationMs := int64(0)
	for _, conn := range c.conns.newest(c.conns.len()) {
		if conn.DestIP != destKey && conn.Host != destKey {
			continue
		}
		conn.Closed = true
		conn.EndTime = &endTime
		conn.DurationMs = &durationMs
	}
	c.mu.Unlock()
}

// findConnLocked 先查索引；索引已淘汰（但记录还在环里）时扫环。
// 调用者必须已持有锁。
func (c *Collector) findConnLocked(id string) *ConnectionRecord {
	if conn, ok := c.connIndex[id]; ok {
		return conn
	}
	if id == "" {
		return nil
	}
	for _, conn := range c.conns.newest(c.conns.len()) {
		if conn.ID == id {
			return conn
		}
	}
	return nil
}

// ---- Query methods ----

// GetDNSHistory 按「旧 → 新」返回最近 limit 条 DNS 记录。
func (c *Collector) GetDNSHistory(limit int) []DNSRecord {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.dns.oldest(limit)
}

// GetConnectionHistory 按「旧 → 新」返回最近 limit 条连接记录。
func (c *Collector) GetConnectionHistory(limit int) []ConnectionRecord {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.copyConnsLocked(c.conns.oldest(limit))
}

// QueryDNS 按「新 → 旧」返回 limit 条 DNS 记录（只取 timestamp 晚于 since 的；
// since <= 0 表示不限）。
func (c *Collector) QueryDNS(since int64, limit int) []DNSRecord {
	c.mu.RLock()
	defer c.mu.RUnlock()
	records := make([]DNSRecord, 0, limit)
	for _, r := range c.dns.newest(c.dns.len()) {
		if since > 0 && r.Timestamp <= since {
			continue
		}
		records = append(records, r)
		if len(records) >= limit {
			break
		}
	}
	return records
}

// QueryConnections 按「新 → 旧」返回 limit 条连接记录（只取 start_time 晚于
// since 的；since <= 0 表示不限）。
func (c *Collector) QueryConnections(since int64, limit int) []ConnectionRecord {
	c.mu.RLock()
	defer c.mu.RUnlock()
	records := make([]ConnectionRecord, 0, limit)
	for _, conn := range c.conns.newest(c.conns.len()) {
		if since > 0 && conn.StartTime <= since {
			continue
		}
		records = append(records, *conn)
		if len(records) >= limit {
			break
		}
	}
	return records
}

// QueryStats 汇总已结束连接的上传/下载字节数。
func (c *Collector) QueryStats() (uploadTotal, downloadTotal int64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, conn := range c.conns.newest(c.conns.len()) {
		if !conn.Closed {
			continue
		}
		uploadTotal += conn.UploadBytes
		downloadTotal += conn.DownloadBytes
	}
	return
}

// OverwrittenRecords 返回被环覆盖掉的记录数（HTTP 上仍是 `dropped` 字段）。
func (c *Collector) OverwrittenRecords() int64 {
	return c.overwritten.Load()
}

// RingStats 返回两个环的占用与容量，供 /monitor/health 观察内存占用。
func (c *Collector) RingStats() (dnsRecords, connRecords, dnsCapacity, connCapacity int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.dns.len(), c.conns.len(), c.dns.capacity(), c.conns.capacity()
}

// copyConnsLocked 把指针快照复制成值，避免把还会被就地更新的记录交出去。
// 调用者必须已持有锁。
func (c *Collector) copyConnsLocked(conns []*ConnectionRecord) []ConnectionRecord {
	out := make([]ConnectionRecord, 0, len(conns))
	for _, conn := range conns {
		out = append(out, *conn)
	}
	return out
}

// ---- Traffic ----

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

// SyncTraffic 更新活跃连接的上传/下载字节数（由 Clash API 的连接快照驱动）。
// 快照里出现但历史里没有的连接会新建一条记录。
func (c *Collector) SyncTraffic(records []TrafficRecord) {
	if len(records) == 0 {
		return
	}
	now := nowMs()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, tr := range records {
		connID := tr.ConnID
		if connID == "" {
			connID = fmt.Sprintf("%s@%d", tr.Host, now)
		}
		conn, ok := c.connIndex[connID]
		if !ok {
			conn = &ConnectionRecord{
				ID:            connID,
				DestIP:        tr.DestIP,
				UploadBytes:   tr.Upload,
				DownloadBytes: tr.Download,
				Outbound:      tr.Outbound,
				Host:          tr.Host,
				Domain:        tr.Domain,
				StartTime:     now,
				ProcessPath:   tr.ProcessPath,
			}
			c.putConnLocked(connID, conn)
			c.pushConnLocked(conn)
			continue
		}
		conn.UploadBytes = tr.Upload
		conn.DownloadBytes = tr.Download
		if tr.DestIP != "" {
			conn.DestIP = tr.DestIP
		}
		if conn.Outbound == "" {
			conn.Outbound = tr.Outbound
		}
		if conn.Host == "" {
			conn.Host = tr.Host
		}
		if conn.Domain == "" {
			conn.Domain = tr.Domain
		}
		if conn.ProcessPath == "" {
			conn.ProcessPath = tr.ProcessPath
		}
	}
}
