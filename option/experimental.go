package option

import "github.com/sagernet/sing/common/json/badoption"

type ExperimentalOptions struct {
	CacheFile *CacheFileOptions `json:"cache_file,omitempty"`
	ClashAPI  *ClashAPIOptions  `json:"clash_api,omitempty"`
	V2RayAPI  *V2RayAPIOptions  `json:"v2ray_api,omitempty"`
	Debug     *DebugOptions     `json:"debug,omitempty"`
	Monitor   *MonitorOptions   `json:"monitor,omitempty"`
}

// MonitorOptions configures the built-in monitoring collector.
//
// 采集数据只留在内存环形缓冲里（不写数据库）：容量决定的是"最新的记录能倒推
// 到多久以前"，进程退出即清空。0 表示用默认值。
type MonitorOptions struct {
	// MaxDNSRows 是 DNS 环形缓冲的容量（默认 2000）。
	MaxDNSRows int `json:"max_dns_rows,omitempty"`
	// MaxConnRows 是连接环形缓冲的容量（默认 1000）。
	MaxConnRows int `json:"max_conn_rows,omitempty"`
	// FailDedupeSec 是失败记录的合并窗口：窗口内对同一目的地（连接侧看
	// remote+outbound，DNS 侧看 domain+qtype+status）的重复失败只记第一条。
	// 一次上游故障会在短时间内产生成片失败记录，正是它们把有用的历史挤出环外。
	// 默认 60 秒；负值关闭去重（每条失败都记录）。
	FailDedupeSec int `json:"fail_dedupe_sec,omitempty"`

	// 以下三项目前只被接受、不生效（落库时代的键）。experimental 是严格解码，
	// 删掉 JSON 键会让带这些键的旧配置直接启动失败，所以保留字段；引擎启动时
	// 会提示一次"已忽略"。
	//
	// MaxSizeMB capped the on-disk size of monitor.db. Deprecated and ignored.
	MaxSizeMB int `json:"max_size_mb,omitempty"`
	// RetentionDays deleted records older than N days. Deprecated and ignored.
	RetentionDays int `json:"retention_days,omitempty"`
	// MaintenanceLogSec throttled prune logs. Deprecated and ignored.
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
