package log

import (
	"bufio"
	"bytes"
	"io"
	"regexp"
	"time"
)

// 统一格式转换 writer（fork 新增，不入上游）。
//
// 把 sing-box 原生文件日志行
//
//	+0800 2026-08-18 16:19:58 INFO network: updated default interface WLAN
//
// 转换为统一线格式（与 Dart lib/logkit、Kotlin FileLogger v2 一致）：
//
//	2026-08-18T16:19:58.000+08:00 |I| engine|network|updated default interface WLAN
//
// 转换失败的行原样透传（查看器降级 raw 显示，不丢行）。
// 仅在显式启用文件轮转（log.max_file_size_mb）时挂载——普通 CLI 输出
// 格式不受影响。
type unifiedWriter struct {
	w io.Writer
}

var unifiedLineRe = regexp.MustCompile(
	`^([+-]\d{4}) (\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}) ` +
		`(TRACE|DEBUG|INFO|WARN|ERROR|FATAL|PANIC) ([^\s:]+): (.*)$`,
)
var unifiedNoTagRe = regexp.MustCompile(
	`^([+-]\d{4}) (\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}) ` +
		`(TRACE|DEBUG|INFO|WARN|ERROR|FATAL|PANIC) (.*)$`,
)

func unifiedLevel(s string) string {
	switch s {
	case "TRACE", "DEBUG":
		return "D"
	case "INFO":
		return "I"
	case "WARN":
		return "W"
	case "ERROR":
		return "E"
	default: // FATAL, PANIC
		return "F"
	}
}

func (u *unifiedWriter) Write(p []byte) (int, error) {
	// 逐行转换（保留行尾）。缓冲按行处理，避免跨 Write 的半行问题。
	var out bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(p))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if converted, ok := convertLine(line); ok {
			out.WriteString(converted)
			out.WriteByte('\n')
		} else {
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	_, err := u.w.Write(out.Bytes())
	return len(p), err
}

func convertLine(line string) (string, bool) {
	var m []string
	var tag, msg string
	if mm := unifiedLineRe.FindStringSubmatch(line); mm != nil {
		m = mm
		tag = m[4]
		msg = m[5]
	} else if mm := unifiedNoTagRe.FindStringSubmatch(line); mm != nil {
		m = mm
		msg = m[4]
	}
	if m == nil {
		return "", false
	}
	ts, err := time.Parse("Z0700 2006-01-02 15:04:05", m[1]+" "+m[2])
	if err != nil {
		return "", false
	}
	iso := ts.Format("2006-01-02T15:04:05.000Z07:00")
	if tag == "" {
		return iso + " |" + unifiedLevel(m[3]) + "| engine||" + msg, true
	}
	return iso + " |" + unifiedLevel(m[3]) + "| engine|" + tag + "|" + msg, true
}
