// experimental/monitor/api.go
package monitor

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// HTTPHandler returns an http.Handler that serves /monitor/* endpoints.
// Mount it under the Clash API router: r.Mount("/monitor", monitor.HTTPHandler())
//
// 所有数据来自内存环形缓冲：进程重启即清空，容量固定（见 Limits），写满后
// 旧记录被覆盖，覆盖条数通过 `dropped` 字段暴露。
func HTTPHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/monitor")
		path = strings.TrimPrefix(path, "/")
		if path == "" {
			handleHealth(w, r)
			return
		}
		switch path {
		case "dns":
			handleDNS(w, r)
		case "connections":
			handleConnections(w, r)
		case "stats":
			handleStats(w, r)
		case "health":
			handleHealth(w, r)
		case "shutdown":
			handleShutdown(w, r)
		default:
			w.WriteHeader(http.StatusNotFound)
			writeJSON(w, map[string]string{"error": "unknown endpoint"})
		}
	})
}

func handleDNS(w http.ResponseWriter, r *http.Request) {
	c := Get()
	if c == nil {
		writeJSON(w, map[string]string{"error": "monitor not initialized"})
		return
	}
	since, limit := parseQuery(r)
	records := c.QueryDNS(since, limit)
	if records == nil {
		records = []DNSRecord{}
	}
	writeJSON(w, map[string]interface{}{
		"records": records,
		"total":   len(records),
	})
}

func handleConnections(w http.ResponseWriter, r *http.Request) {
	c := Get()
	if c == nil {
		writeJSON(w, map[string]string{"error": "monitor not initialized"})
		return
	}
	since, limit := parseQuery(r)
	records := c.QueryConnections(since, limit)
	if records == nil {
		records = []ConnectionRecord{}
	}
	writeJSON(w, map[string]interface{}{
		"connections": records,
		"total":       len(records),
	})
}

func handleStats(w http.ResponseWriter, r *http.Request) {
	c := Get()
	if c == nil {
		writeJSON(w, map[string]string{"error": "monitor not initialized"})
		return
	}
	upload, download := c.QueryStats()
	writeJSON(w, map[string]interface{}{
		"upload_bytes":   upload,
		"download_bytes": download,
		"dropped":        c.OverwrittenRecords(),
	})
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	c := Get()
	status := "ok"
	dropped := int64(0)
	dnsRecords, connRecords, dnsCapacity, connCapacity := 0, 0, 0, 0
	if c == nil {
		status = "not_initialized"
	} else {
		dropped = c.OverwrittenRecords()
		dnsRecords, connRecords, dnsCapacity, connCapacity = c.RingStats()
	}
	writeJSON(w, map[string]interface{}{
		"status":        status,
		"dropped":       dropped,
		"dns_records":   dnsRecords,
		"connections":   connRecords,
		"dns_capacity":  dnsCapacity,
		"conn_capacity": connCapacity,
	})
}

// handleShutdown 触发引擎优雅关闭 (Windows 上 Flutter stop() 先调用此端点,
// 让引擎走完整 TUN/路由清理后正常退出, 而不是被 TerminateProcess 硬杀)。
func handleShutdown(w http.ResponseWriter, r *http.Request) {
	if !ShutdownHandlerRegistered() {
		w.WriteHeader(http.StatusServiceUnavailable)
		writeJSON(w, map[string]string{"error": "no shutdown handler registered"})
		return
	}
	writeJSON(w, map[string]string{"status": "shutting down"})
	_ = TriggerShutdown()
}

func parseQuery(r *http.Request) (since int64, limit int) {
	since, _ = strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	return
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
