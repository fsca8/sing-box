package option

import "github.com/sagernet/sing/common/json/badoption"

type ExperimentalOptions struct {
	CacheFile *CacheFileOptions `json:"cache_file,omitempty"`
	ClashAPI  *ClashAPIOptions  `json:"clash_api,omitempty"`
	V2RayAPI  *V2RayAPIOptions  `json:"v2ray_api,omitempty"`
	Debug     *DebugOptions     `json:"debug,omitempty"`
	Monitor   *MonitorOptions   `json:"monitor,omitempty"`
}

// MonitorOptions configures the built-in monitoring collector (monitor.db).
// Every field is optional: 0 keeps the default, a negative value disables the
// corresponding cap, a positive value overrides it.
type MonitorOptions struct {
	// MaxSizeMB caps the on-disk size of monitor.db. Defaults to 100 MB.
	// Oldest records are removed first when the cap is reached.
	MaxSizeMB int `json:"max_size_mb,omitempty"`
	// MaxDNSRows caps how many DNS records are kept (default 200000).
	MaxDNSRows int `json:"max_dns_rows,omitempty"`
	// MaxConnRows caps how many connection records are kept (default 100000).
	MaxConnRows int `json:"max_conn_rows,omitempty"`
	// RetentionDays deletes records older than N days on startup (default 7).
	RetentionDays int `json:"retention_days,omitempty"`
	// FailDedupeSec 是失败记录的合并窗口：窗口内对同一目的地（连接侧看
	// remote+outbound，DNS 侧看 domain+qtype+status）的重复失败只落库第一条。
	// 一次上游故障会在短时间内产生成片失败记录，正是它们顶满了行数上限。
	// 默认 60 秒；负值关闭去重（每条失败都落库）。
	FailDedupeSec int `json:"fail_dedupe_sec,omitempty"`
	// MaintenanceLogSec 是两行裁剪日志之间的最小间隔（默认 600 秒）；
	// 被节流掉的裁剪会累计，到下一次输出时一并交代。负值不节流，即每次裁剪都输出。
	MaintenanceLogSec int `json:"maintenance_log_sec,omitempty"`
}

type CacheFileOptions struct {
	Enabled     bool               `json:"enabled,omitempty"`
	Path        string             `json:"path,omitempty"`
	CacheID     string             `json:"cache_id,omitempty"`
	StoreFakeIP bool               `json:"store_fakeip,omitempty"`
	StoreRDRC   bool               `json:"store_rdrc,omitempty" schema:"omit"`
	RDRCTimeout badoption.Duration `json:"rdrc_timeout,omitempty"`
	StoreDNS    bool               `json:"store_dns,omitempty"`
}

type ClashAPIOptions struct {
	ExternalController               string                     `json:"external_controller,omitempty"`
	ExternalUI                       string                     `json:"external_ui,omitempty"`
	ExternalUIDownloadURL            string                     `json:"external_ui_download_url,omitempty"`
	ExternalUIDownloadDetour         string                     `json:"external_ui_download_detour,omitempty" reference:"outbound"`
	Secret                           string                     `json:"secret,omitempty"`
	DefaultMode                      string                     `json:"default_mode,omitempty"`
	AccessControlAllowOrigin         badoption.Listable[string] `json:"access_control_allow_origin,omitempty"`
	AccessControlAllowPrivateNetwork bool                       `json:"access_control_allow_private_network,omitempty"`

	// Deprecated: migrated to global cache file
	CacheFile string `json:"cache_file,omitempty" schema:"omit"`
	// Deprecated: migrated to global cache file
	CacheID string `json:"cache_id,omitempty" schema:"omit"`
	// Deprecated: migrated to global cache file
	StoreMode bool `json:"store_mode,omitempty" schema:"omit"`
	// Deprecated: migrated to global cache file
	StoreSelected bool `json:"store_selected,omitempty" schema:"omit"`
	// Deprecated: migrated to global cache file
	StoreFakeIP bool `json:"store_fakeip,omitempty" schema:"omit"`
}

type V2RayAPIOptions struct {
	Listen string                    `json:"listen,omitempty"`
	Stats  *V2RayStatsServiceOptions `json:"stats,omitempty"`
}

type V2RayStatsServiceOptions struct {
	Enabled   bool     `json:"enabled,omitempty"`
	Inbounds  []string `json:"inbounds,omitempty"`
	Outbounds []string `json:"outbounds,omitempty"`
	Users     []string `json:"users,omitempty"`
}
