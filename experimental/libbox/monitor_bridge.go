// experimental/libbox/monitor_bridge.go
package libbox

import (
	"encoding/json"

	"github.com/sagernet/sing-box/experimental/monitor"
)

// MonitorService bridges monitor.Collector to gomobile-friendly API.
//
// 采集数据只留在内存环形缓冲里（DNS 2000 / 连接 1000，见 monitor.Limits），
// 不再有数据库文件，所以构造时不需要路径，也没有需要收尾的句柄。
type MonitorService struct {
	collector *monitor.Collector
}

// NewMonitorService 创建采集器并把它装成全局实例（HTTP /monitor/* 端点读的
// 就是它）。旧版本留下的 monitor.db 由 app 侧在调用本方法之前删除。
func NewMonitorService() (*MonitorService, error) {
	return &MonitorService{collector: monitor.NewCollector()}, nil
}

// SetEventCallback sets a callback that receives JSON event strings.
// The callback is called from Go goroutines, the Android side should post to main thread.
func (m *MonitorService) SetEventCallback(cb EventCallback) {
	if m.collector == nil {
		return
	}
	m.collector.SetEventCallback(func(json string) {
		cb.SendEvent(json)
	})
}

// ---- Query methods (return JSON strings) ----

func (m *MonitorService) GetDNSHistory(limit int) string {
	if m.collector == nil {
		return "[]"
	}
	records := m.collector.QueryDNS(0, limit)
	b, _ := json.Marshal(records)
	return string(b)
}

func (m *MonitorService) GetConnectionHistory(limit int) string {
	if m.collector == nil {
		return "[]"
	}
	// 连接记录与 DNS 记录一样来自内存环；TCP/TLS/流量/关闭四条路径补全的是
	// 同一条记录，所以这里能拿到完整字段。
	records := m.collector.QueryConnections(0, limit)
	b, _ := json.Marshal(records)
	return string(b)
}

// ---- Alert rules (stub: always empty until implemented) ----

func (m *MonitorService) AddAlertRule(json string) int64 {
	if m.collector == nil {
		return 0
	}
	return 0
}

func (m *MonitorService) DeleteAlertRule(ruleID int64) {
	if m.collector == nil {
		return
	}
}

func (m *MonitorService) GetAlertRules() string {
	return "[]"
}

// EventCallback is implemented by Android/Kotlin to receive event JSON
type EventCallback interface {
	SendEvent(json string) error
}
