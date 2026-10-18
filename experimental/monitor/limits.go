// experimental/monitor/limits.go
package monitor

import "time"

// monitor 的数据全部留在内存里：两个定容环形缓冲，写满后覆盖最旧的记录。
// 不写数据库，也就不需要体积上限、保留期和周期裁剪这些机制——进程退出即
// 清空，磁盘上不会留下任何监控数据（旧版本留下的 monitor.db 由
// RemoveLegacyDatabase 在启动时删掉）。
//
// 可配置项（`experimental.monitor`，全部可选）：
//
//	"experimental": {
//	  "monitor": {
//	    "max_dns_rows": 2000,   // DNS 环容量（默认 2000）
//	    "max_conn_rows": 1000,  // 连接环容量（默认 1000）
//	    "fail_dedupe_sec": 60   // 失败合并窗口（默认 60 秒，负值关闭去重）
//	  }
//	}
//
// max_size_mb / retention_days / maintenance_log_sec 是落库时代的键。引擎用严格
// 解码，删掉这些键会让带它们的旧配置直接启动失败，所以保留为"接受但忽略",
// 并在启动时提示一次（见 cmd/sing-box/cmd_run.go）。
const (
	// DefaultDNSRingSize / DefaultConnRingSize 是环的默认容量。app 单次最多只
	// 翻 1000 条（monitorMaxWindow），所以容量决定的是「最新的那些记录能倒推
	// 到多久以前」，而不是"能看到多少行"。
	DefaultDNSRingSize  = 2000
	DefaultConnRingSize = 1000

	// 防御性上限：配置里多写几个 0 不至于把内存吃光（按平均每条 DNS 约
	// 350B、连接约 400B 估算，5 万条 DNS 约 17MB，2 万条连接约 8MB）。
	maxDNSRingSize  = 50000
	maxConnRingSize = 20000

	// DefaultFailDedupeWindow 是失败记录的合并窗口：上游故障/重试风暴会在
	// 短时间内对同一目标产生成片失败记录。环同样会被这种风暴冲刷（把有用的
	// 历史挤掉），所以去重依然必要。
	DefaultFailDedupeWindow = time.Minute
)

// Limits 是采集层的容量参数。零值表示"用默认值"。
type Limits struct {
	// MaxDNSRows / MaxConnRows 是 DNS / 连接环形缓冲的容量。0 或负值都用默认
	// 值——环形缓冲必须有界，不存在"关闭上限"；正数会被防御性上限截断。
	MaxDNSRows  int
	MaxConnRows int
	// FailDedupeWindow 之内的重复失败（连接侧同一 remote+outbound，DNS 侧同一
	// domain+qtype+status）只记第一条（0 用默认值，负值关闭去重）。
	FailDedupeWindow time.Duration
}

// DefaultLimits returns the built-in ring sizes (DNS 2000 / conn 1000) and the
// default failure-dedupe window (60s).
func DefaultLimits() Limits {
	return Limits{
		MaxDNSRows:       DefaultDNSRingSize,
		MaxConnRows:      DefaultConnRingSize,
		FailDedupeWindow: DefaultFailDedupeWindow,
	}
}

// LimitsFrom 把 `experimental.monitor` 的配置值映射成环参数：0 用默认，负值也
// 用默认（环不能无界），正数覆盖默认值并被防御性上限截断；failDedupeSec 为负
// 表示关闭失败去重。
func LimitsFrom(maxDNSRows, maxConnRows, failDedupeSec int) Limits {
	limits := DefaultLimits()
	if maxDNSRows > 0 {
		limits.MaxDNSRows = maxDNSRows
	}
	if maxConnRows > 0 {
		limits.MaxConnRows = maxConnRows
	}
	if failDedupeSec < 0 {
		limits.FailDedupeWindow = -1
	} else if failDedupeSec > 0 {
		limits.FailDedupeWindow = time.Duration(failDedupeSec) * time.Second
	}
	return limits
}

// withDefaults 填上零值字段并做防御性截断；构造环之前调用一次。
func (l Limits) withDefaults() Limits {
	if l.MaxDNSRows <= 0 {
		l.MaxDNSRows = DefaultDNSRingSize
	}
	if l.MaxConnRows <= 0 {
		l.MaxConnRows = DefaultConnRingSize
	}
	if l.MaxDNSRows > maxDNSRingSize {
		l.MaxDNSRows = maxDNSRingSize
	}
	if l.MaxConnRows > maxConnRingSize {
		l.MaxConnRows = maxConnRingSize
	}
	if l.FailDedupeWindow == 0 {
		l.FailDedupeWindow = DefaultFailDedupeWindow
	}
	return l
}
