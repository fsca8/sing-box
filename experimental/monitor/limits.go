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
//	    "retention_days": 7      // time-based retention, applied on startup
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
}

// DefaultLimits returns the built-in caps (100 MB / 200k DNS / 100k conn / 7d).
func DefaultLimits() Limits {
	return Limits{
		MaxSizeBytes:        DefaultMaxSizeBytes,
		MaxDNSRows:          DefaultMaxDNSRows,
		MaxConnRows:         DefaultMaxConnRows,
		RetentionDays:       DefaultRetentionDays,
		MaintenanceInterval: DefaultMaintenanceInterval,
	}
}

// LimitsFrom builds Limits from configuration values (the
// `experimental.monitor` block). For every value: 0 keeps the default,
// a negative value disables that cap, a positive value overrides it.
func LimitsFrom(maxSizeMB, maxDNSRows, maxConnRows, retentionDays int) Limits {
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
	return l
}
