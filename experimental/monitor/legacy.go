// experimental/monitor/legacy.go
package monitor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// legacyDatabaseFiles 是落库时代 monitor.db 的文件名，含 SQLite 的边车文件
// （回滚日志、WAL、共享内存）。
var legacyDatabaseFiles = []string{
	"monitor.db",
	"monitor.db-journal",
	"monitor.db-wal",
	"monitor.db-shm",
}

// RemoveLegacyDatabase 删掉旧版本在 dir 下留下的监控数据库，返回一句可以直接
// 打进日志的说明；没有可删的文件时返回空串。
//
// monitor 现在只在内存里保留数据，这些文件只会白占空间（历史上见过几 GB 的
// monitor.db），所以升级后第一次启动就回收掉。日志由调用方打（桌面走引擎
// 日志，Android 走 native.log），这里只负责删文件并把结果说清楚。
func RemoveLegacyDatabase(dir string) string {
	if dir == "" {
		dir = "."
	}
	var freed int64
	var removed []string
	for _, name := range legacyDatabaseFiles {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		if err = os.Remove(path); err != nil {
			continue
		}
		freed += info.Size()
		removed = append(removed, name)
	}
	if len(removed) == 0 {
		return ""
	}
	return fmt.Sprintf("removed legacy monitor database %s (%s)", strings.Join(removed, ", "), formatBytes(freed))
}

// formatBytes 把字节数格式化成人类可读的形式，只用于日志。
func formatBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(size)/float64(div), "KMGTPE"[exp])
}
