// experimental/libbox/monitor_bridge.go
package libbox

import (
	"encoding/json"

	"github.com/sagernet/sing-box/experimental/monitor"
)

// MonitorService bridges monitor.Collector to gomobile-friendly API
type MonitorService struct {
	collector *monitor.Collector
}

func NewMonitorService(dbPath string) (*MonitorService, error) {
	c, err := monitor.NewCollector(dbPath)
	if err != nil {
		return nil, err
	}
	return &MonitorService{collector: c}, nil
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
	records := m.collector.GetDNSHistory(limit)
	b, _ := json.Marshal(records)
	return string(b)
}

func (m *MonitorService) GetConnectionHistory(limit int) string {
	if m.collector == nil {
		return "[]"
	}
	records := m.collector.GetConnectionHistory(limit)
	b, _ := json.Marshal(records)
	return string(b)
}

// EventCallback is implemented by Android/Kotlin to receive event JSON
type EventCallback interface {
	SendEvent(json string) error
}
