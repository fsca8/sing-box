// experimental/monitor/types.go
package monitor

import "time"

type DNSRecord struct {
	Timestamp  int64    `json:"timestamp"`
	Domain     string   `json:"domain"`
	QType      string   `json:"qtype"`
	Transport  string   `json:"transport"`
	LatencyUs  int64    `json:"latency_us"`
	Status     string   `json:"status"`
	Answers    []string `json:"answers"`
	TTL        uint32   `json:"ttl"`
	IsRejected bool     `json:"is_rejected"`
}

type TCPRecord struct {
	Timestamp int64  `json:"timestamp"`
	Remote    string `json:"remote"`
	ConnID    string `json:"conn_id,omitempty"`
	Domain    string `json:"domain,omitempty"`
	Outbound  string `json:"outbound,omitempty"`
	LatencyUs int64  `json:"latency_us"`
	Error     string `json:"error,omitempty"`
}

type TLSRecord struct {
	Timestamp   int64  `json:"timestamp"`
	Remote      string `json:"remote"`
	ConnID      string `json:"conn_id,omitempty"`
	Domain      string `json:"domain,omitempty"`
	Outbound    string `json:"outbound,omitempty"`
	ServerName  string `json:"server_name"`
	LatencyUs   int64  `json:"latency_us"`
	Version     string `json:"version,omitempty"`
	CipherSuite string `json:"cipher_suite,omitempty"`
	Error       string `json:"error,omitempty"`
}

type ConnectionRecord struct {
	ID            string `json:"id"`
	DNSQueryID    string `json:"dns_query_id,omitempty"`
	Host          string `json:"host,omitempty"`
	Domain        string `json:"domain,omitempty"`
	DestIP        string `json:"dest_ip"`
	DestPort      int    `json:"dest_port"`
	SourceIP      string `json:"source_ip,omitempty"`
	Rule          string `json:"rule"`
	Outbound      string `json:"outbound"`
	Chain         string `json:"chain,omitempty"`
	TCPLatencyUs  int64  `json:"tcp_latency_us"`
	TLSLatencyUs  int64  `json:"tls_latency_us"`
	DNSLatencyUs  int64  `json:"dns_latency_us,omitempty"`
	TLSVersion    string `json:"tls_version,omitempty"`
	CipherSuite   string `json:"cipher_suite,omitempty"`
	UploadBytes   int64  `json:"upload_bytes"`
	DownloadBytes int64  `json:"download_bytes"`
	StartTime     int64  `json:"start_time"`
	EndTime       int64  `json:"end_time,omitempty"`
	DurationMs    int64  `json:"duration_ms,omitempty"`
	Error         string `json:"error,omitempty"`
	Closed        bool   `json:"closed"`
}

type AlertRule struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Metric        string `json:"metric"`
	Operator      string `json:"operator"` // gt, lt, eq
	ThresholdUs   int64  `json:"threshold_us"`
	TargetPattern string `json:"target_pattern,omitempty"`
	Enabled       bool   `json:"enabled"`
}

type AlertEvent struct {
	RuleID       int64  `json:"rule_id"`
	RuleName     string `json:"rule_name"`
	ConnectionID string `json:"connection_id,omitempty"`
	ActualValue  int64  `json:"actual_value"`
	Threshold    int64  `json:"threshold"`
	Message      string `json:"message"`
	Timestamp    int64  `json:"timestamp"`
}

// Event is the unified JSON event sent to Flutter
type Event struct {
	Type      string `json:"type"`
	Timestamp int64  `json:"timestamp"`
	// Only one of these is set, depending on Type
	DNS        *DNSRecord        `json:"dns,omitempty"`
	Connection *ConnectionRecord `json:"connection,omitempty"`
	Alert      *AlertEvent       `json:"alert,omitempty"`
	Status     string            `json:"status,omitempty"`
	Message    string            `json:"message,omitempty"`
}

func nowMs() int64 {
	return time.Now().UnixMilli()
}
