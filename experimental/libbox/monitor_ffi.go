// experimental/libbox/monitor_ffi.go
//go:build windows

package libbox

/*
#include <stdlib.h>
*/
import "C"
import (
	"encoding/json"
	"sync"
	"unsafe"

	"github.com/sagernet/sing-box/experimental/monitor"
)

var (
	ffiCollector *monitor.Collector
	ffiEvents    []monitor.Event
	ffiMu        sync.Mutex
)

//export monitor_init
func monitor_init(dbPath *C.char) C.int {
	collector, err := monitor.NewCollector(C.GoString(dbPath))
	if err != nil {
		return 0
	}
	ffiCollector = collector
	collector.SetEventCallback(func(jsonStr string) {
		var ev monitor.Event
		if err := json.Unmarshal([]byte(jsonStr), &ev); err == nil {
			ffiMu.Lock()
			ffiEvents = append(ffiEvents, ev)
			if len(ffiEvents) > 2000 {
				ffiEvents = ffiEvents[len(ffiEvents)-1000:]
			}
			ffiMu.Unlock()
		}
	})
	return 1
}

//export monitor_poll_events
func monitor_poll_events(dummy C.int) *C.char {
	ffiMu.Lock()
	events := ffiEvents
	ffiEvents = nil
	ffiMu.Unlock()

	b, _ := json.Marshal(events)
	return C.CString(string(b))
}

//export monitor_get_dns_history
func monitor_get_dns_history(limit C.int) *C.char {
	if ffiCollector == nil {
		return C.CString("[]")
	}
	records := ffiCollector.GetDNSHistory(int(limit))
	b, _ := json.Marshal(records)
	return C.CString(string(b))
}

//export monitor_get_conn_history
func monitor_get_conn_history(limit C.int) *C.char {
	if ffiCollector == nil {
		return C.CString("[]")
	}
	records := ffiCollector.GetConnectionHistory(int(limit))
	b, _ := json.Marshal(records)
	return C.CString(string(b))
}

//export monitor_free_string
func monitor_free_string(s *C.char) {
	C.free(unsafe.Pointer(s))
}
