// Package logkit 定义 singbird 统一日志约定的常量与目录助手
// （fork 新增，不入上游）。
//
// 约定（与 Flutter 侧 lib/logkit/ 及 Android FileLogger v2 同源，见
// singbird/.hermes/plans/2026-08-18-log-system-redesign.md）：
//
//	<数据目录>/logs/
//	├── engine.log      sing-box 引擎日志（config log.output 规范化指向此处）
//	├── engine.log.1~3  引擎日志轮转备份
//	├── netbird.log     netbird 引擎日志（嵌入引擎 LogOutput 指向此处）
//	└── netbird.log.1~2 netbird 日志轮转备份
//
// Flutter 侧另有 app.log（Dart）与 native.log（Kotlin，Android），
// 一并落在 logs/ 下。
package logkit

import "path/filepath"

const (
	// EngineLogName 是 sing-box 引擎日志文件名（config log.output 规范化目标）。
	EngineLogName = "engine.log"
	// NetbirdLogName 是 netbird 引擎日志文件名。
	NetbirdLogName = "netbird.log"

	// MaxLogBytes 单文件上限（5MB）。与 Flutter/Android 端轮转阈值一致。
	MaxLogBytes = int64(5 << 20)
	// EngineKeepFiles 引擎日志保留的滚动备份数。
	EngineKeepFiles = 3
	// NetbirdKeepFiles netbird 日志保留的滚动备份数。
	NetbirdKeepFiles = 2
)

// LogsDir 返回数据目录下的统一日志目录。
func LogsDir(dataDir string) string {
	return filepath.Join(dataDir, "logs")
}
