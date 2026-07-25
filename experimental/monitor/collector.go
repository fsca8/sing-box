// experimental/monitor/collector.go
package monitor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

type Collector struct {
	mu       sync.RWMutex
	dnsBuf   *RingBuffer[DNSRecord]
	connBuf  *RingBuffer[ConnectionRecord]
	alertBuf *RingBuffer[AlertEvent]
	rules    []AlertRule
	store    *fileStore
	db       *Database
	connMap  map[string]*ConnectionRecord // remote addr → pending connection
	eventCB  func(string)                 // callback to push JSON to Android
}

var globalCollector *Collector

func NewCollector(dbPath string) (*Collector, error) {
	dir := filepath.Dir(dbPath)
	os.MkdirAll(dir, 0755)

	db, err := NewDatabase(dbPath)
	if err != nil {
		return nil, err
	}

	c := &Collector{
		dnsBuf:   NewRingBuffer[DNSRecord](500),
		connBuf:  NewRingBuffer[ConnectionRecord](200),
		alertBuf: NewRingBuffer[AlertEvent](100),
		store:    newFileStore(dbPath),
		db:       db,
		connMap:  make(map[string]*ConnectionRecord),
	}
	c.loadAlertRules()
	globalCollector = c
	return c, nil
}

func Get() *Collector {
	return globalCollector
}

func (c *Collector) SetEventCallback(cb func(string)) {
	c.eventCB = cb
}

func (c *Collector) pushJSON(v any) {
	if c.eventCB == nil {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	c.eventCB(string(b))
}

// ---- DNS ----

func (c *Collector) RecordDNS(r DNSRecord) {
	r.Timestamp = nowMs()
	c.mu.Lock()
	c.dnsBuf.Push(r)
	c.mu.Unlock()

	// Async SQLite write (non-blocking)
	if c.db != nil {
		c.db.WriteDNS(&r)
	}

	ev := Event{Type: "dns", Timestamp: r.Timestamp, DNS: &r}
	c.pushJSON(ev)
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
		}
		if r.ProcessPath != "" {
			conn.Host = r.ProcessPath
		}
		c.connMap[key] = conn
	} else {
		conn.TCPLatencyUs = r.LatencyUs
		if r.Domain != "" {
			conn.Domain = r.Domain
		}
		if r.Outbound != "" {
			conn.Outbound = r.Outbound
		}
		if r.ProcessPath != "" {
			conn.Host = r.ProcessPath
		}
	}
	c.mu.Unlock()

	// Sync latency to SQLite (per-connection INSERT, traffic_sync upserts same row by conn_id)
	if c.db != nil {
		c.db.WriteLatency(key, r.Remote, r.Domain, r.Outbound, r.LatencyUs, 0, r.ProcessPath)
	}

	ev := Event{Type: "tcp", Timestamp: r.Timestamp}
	b, _ := json.Marshal(r)
	ev.Message = string(b)
	c.pushJSON(ev)
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
		// Sync latency + host to SQLite
		if c.db != nil {
			c.db.WriteLatency(key, r.Remote, r.Domain, r.Outbound, 0, r.LatencyUs, r.ProcessPath)
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
		}
		if r.ProcessPath != "" {
			conn.Host = r.ProcessPath
		}
		c.connMap[key] = conn
		if c.db != nil {
			c.db.WriteLatency(key, r.Remote, r.Domain, r.Outbound, 0, r.LatencyUs, r.ProcessPath)
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

	c.mu.Lock()
	// Merge with existing connMap entry (from RecordTCP) if present
	if existing, ok := c.connMap[r.DestIP]; ok {
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
			c.db.UpdateConnectionMeta(r.DestIP, r.Host, r.Outbound)
		}
		ev := Event{Type: "connection", Timestamp: r.StartTime, Connection: existing}
		c.pushJSON(ev)
		return
	}
	// No TCP hook entry yet — create new
	c.connMap[r.DestIP] = &r
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

// ---- Alerts ----

func (c *Collector) EvaluateAlerts(conn ConnectionRecord) {
	c.mu.RLock()
	rules := make([]AlertRule, len(c.rules))
	copy(rules, c.rules)
	c.mu.RUnlock()

	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		var value int64
		switch rule.Metric {
		case "dns_latency":
			if conn.DNSLatencyUs != nil {
				value = *conn.DNSLatencyUs
			}
		case "tcp_latency":
			value = conn.TCPLatencyUs
		case "tls_latency":
			value = conn.TLSLatencyUs
		case "total_latency":
			value = conn.TCPLatencyUs + conn.TLSLatencyUs
		default:
			continue
		}
		if value == 0 {
			continue
		}

		triggered := false
		switch rule.Operator {
		case "gt":
			triggered = value > rule.ThresholdUs
		case "lt":
			triggered = value < rule.ThresholdUs
		case "eq":
			triggered = value == rule.ThresholdUs
		}
		if !triggered {
			continue
		}

		alert := AlertEvent{
			RuleID:       rule.ID,
			RuleName:     rule.Name,
			ConnectionID: conn.ID,
			ActualValue:  value,
			Threshold:    rule.ThresholdUs,
			Message:      rule.Name + ": " + conn.Host + " " + rule.Metric + "=" + formatUs(value) + " > " + formatUs(rule.ThresholdUs),
			Timestamp:    nowMs(),
		}
		c.mu.Lock()
		c.alertBuf.Push(alert)
		c.mu.Unlock()

		// Async SQLite write
		if c.db != nil {
			c.db.WriteAlert(&alert)
		}

		ev := Event{Type: "alert", Timestamp: alert.Timestamp, Alert: &alert}
		c.pushJSON(ev)
	}
}

func formatUs(us int64) string {
	if us < 1000 {
		return itoa(us) + "us"
	}
	return itoa(us/1000) + "ms"
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	s := ""
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	if neg {
		s = "-" + s
	}
	return s
}

// ---- Query methods ----

func (c *Collector) GetDNSHistory(limit int) []DNSRecord {
	return c.dnsBuf.Last(limit)
}

func (c *Collector) GetConnectionHistory(limit int) []ConnectionRecord {
	return c.connBuf.Last(limit)
}

func (c *Collector) GetAlertEvents(limit int) []AlertEvent {
	return c.alertBuf.Last(limit)
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

func (c *Collector) QueryAlerts(since int64, limit int) ([]AlertEvent, error) {
	if c.db == nil {
		return nil, nil
	}
	return c.db.QueryAlerts(since, limit)
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
