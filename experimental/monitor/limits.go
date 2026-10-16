// experimental/monitor/limits.go
package monitor

import "time"

// Defaults for the growth caps applied to monitor.db.
//
// The database used to grow without bound (a 6.5 GB monitor.db was observed in
// production). The caps are configurable through the sing-box config file:
//
//	"experimental": {
//	  "monitor": {
//	    "max_size_mb": 100,      // cap on the on-disk size (default 100)
//	    "max_dns_rows": 200000,  // row caps, size-independent guard
//	    "max_conn_rows": 100000,
//	    "retention_days": 7,     // time-based retention, applied on startup
//	    "fail_dedupe_sec": 60,   // failure dedup window (default 60)
//	    "maintenance_log_sec": 600 // prune log throttle (default 600)
//	  }
//	}
const (
	// DefaultMaxSizeBytes caps the on-disk database size (main file + WAL).
	DefaultMaxSizeBytes int64 = 100 << 20
	// DefaultMaxDNSRows / DefaultMaxConnRows cap the rows kept per table so a
	// single burst of tiny (or huge) records cannot blow past the size cap
	// between two maintenance passes.
	DefaultMaxDNSRows  = 200000
	DefaultMaxConnRows = 100000
	// DefaultRetentionDays deletes records older than this on startup.
	DefaultRetentionDays = 7
	// DefaultMaintenanceInterval is how often the caps are enforced at runtime.
	DefaultMaintenanceInterval = time.Minute
	// DefaultFailDedupeWindow 是失败记录的合并窗口：上游故障/重试风暴会在
	// 短时间对同一目标产生成片失败记录，一次故障就能顶满行数上限。
	DefaultFailDedupeWindow = time.Minute
	// DefaultMaintenanceLogInterval 是两行裁剪日志之间的最小间隔：维护每分钟
	// 跑一次，但"又裁了多少行"这件事一小时看一次就够了。
	DefaultMaintenanceLogInterval = 10 * time.Minute
)

// Limits caps the growth of the monitor database. All fields are optional:
// the zero value means "use the default", and a negative value disables that
// particular cap (see LimitsFrom for the config-mapping rules).
type Limits struct {
	// MaxSizeBytes caps the on-disk size of the database (0 disables).
	MaxSizeBytes int64
	// MaxDNSRows / MaxConnRows cap the number of rows kept per table
	// (0 disables). Oldest rows are dropped first.
	MaxDNSRows  int
	MaxConnRows int
	// RetentionDays deletes records older than N days (0 disables). Applied on
	// startup only.
	RetentionDays int
	// MaintenanceInterval overrides DefaultMaintenanceInterval.
	MaintenanceInterval time.Duration
	// FailDedupeWindow 之内的重复失败（同一 remote+outbound，或同一
	// domain+qtype+status）只落库第一条（0 用默认值，负值关闭去重）。
	FailDedupeWindow time.Duration
	// MaintenanceLogInterval 节流裁剪日志：窗口内的裁剪只累计不输出
	// （0 用默认值，负值不节流，即每次裁剪都输出）。
	MaintenanceLogInterval time.Duration
}

// DefaultLimits returns the built-in caps (100 MB / 200k DNS / 100k conn / 7d).
func DefaultLimits() Limits {
	return Limits{
		MaxSizeBytes:           DefaultMaxSizeBytes,
		MaxDNSRows:             DefaultMaxDNSRows,
		MaxConnRows:            DefaultMaxConnRows,
		RetentionDays:          DefaultRetentionDays,
		MaintenanceInterval:    DefaultMaintenanceInterval,
		FailDedupeWindow:       DefaultFailDedupeWindow,
		MaintenanceLogInterval: DefaultMaintenanceLogInterval,
	}
}

// LimitsFrom builds Limits from configuration values (the
// `experimental.monitor` block). For every value: 0 keeps the default,
// a negative value disables that cap, a positive value overrides it.
// failDedupeSec / maintenanceLogSec are given in seconds.
func LimitsFrom(maxSizeMB, maxDNSRows, maxConnRows, retentionDays, failDedupeSec, maintenanceLogSec int) Limits {
	limits := DefaultLimits()
	if maxSizeMB != 0 {
		if maxSizeMB < 0 {
			limits.MaxSizeBytes = -1
		} else {
			limits.MaxSizeBytes = int64(maxSizeMB) << 20
		}
	}
	if maxDNSRows != 0 {
		if maxDNSRows < 0 {
			limits.MaxDNSRows = -1
		} else {
			limits.MaxDNSRows = maxDNSRows
		}
	}
	if maxConnRows != 0 {
		if maxConnRows < 0 {
			limits.MaxConnRows = -1
		} else {
			limits.MaxConnRows = maxConnRows
		}
	}
	if retentionDays != 0 {
		if retentionDays < 0 {
			limits.RetentionDays = -1
		} else {
			limits.RetentionDays = retentionDays
		}
	}
	if failDedupeSec != 0 {
		if failDedupeSec < 0 {
			limits.FailDedupeWindow = -1
		} else {
			limits.FailDedupeWindow = time.Duration(failDedupeSec) * time.Second
		}
	}
	if maintenanceLogSec != 0 {
		if maintenanceLogSec < 0 {
			limits.MaintenanceLogInterval = -1
		} else {
			limits.MaintenanceLogInterval = time.Duration(maintenanceLogSec) * time.Second
		}
	}
	return limits
}

// withDefaults fills the zero-valued fields with their defaults.
func (l Limits) withDefaults() Limits {
	if l.MaxSizeBytes == 0 {
		l.MaxSizeBytes = DefaultMaxSizeBytes
	}
	if l.MaxDNSRows == 0 {
		l.MaxDNSRows = DefaultMaxDNSRows
	}
	if l.MaxConnRows == 0 {
		l.MaxConnRows = DefaultMaxConnRows
	}
	if l.RetentionDays == 0 {
		l.RetentionDays = DefaultRetentionDays
	}
	if l.MaintenanceInterval <= 0 {
		l.MaintenanceInterval = DefaultMaintenanceInterval
	}
	if l.FailDedupeWindow == 0 {
		l.FailDedupeWindow = DefaultFailDedupeWindow
	}
	if l.MaintenanceLogInterval == 0 {
		l.MaintenanceLogInterval = DefaultMaintenanceLogInterval
	}
	return l
}
