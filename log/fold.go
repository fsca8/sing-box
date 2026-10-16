package log

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"sync"
	"time"
)

// 重复日志折叠（repeat folding）。
//
// 背景：日志调用点不做任何限流，同一形状的日志可以被刷到十几条/秒。失败本身
// 是正常的（远端不可达就该报错），问题是同一形状被逐条打印，把日志文件和
// journal 都占满，真正罕见的日志反而被冲掉。
//
// 折叠挂在日志工厂的**唯一出口**（writer）上，而不是去改各个调用点：调用点
// 散落在大量上游文件里（出站拨号失败只是其中最吵的一个），改它们既会在每次
// rebase 上游时冲突，也只能治一个来源。
//
// 规则（每种"形状"独立计数，窗口默认 5 分钟）：
//   - 窗口内前 limit 条原样输出；
//   - 同窗口内之后的同形状日志只累加计数，不再输出；
//   - 窗口滚动（或进程退出 Flush）时输出一行汇总，带条数与形状。
//
// 形状 = 抹掉易变部分（时间戳、IP、端口、连接 id、耗时、数字）后的那一行，
// 归一化只做保守替换：少合并没有代价，把不同问题并成一个形状才是丢信息。
const (
	// DefaultFoldLimit 是每种形状在每个窗口内原样输出的最大条数。
	DefaultFoldLimit = 10
	// DefaultFoldWindow 是折叠窗口的长度。
	DefaultFoldWindow = 5 * time.Minute

	// foldMaxShapes 限制同时跟踪的形状数：形状异常多时（例如日志里带随机串）
	// 宁可原样输出，也不让折叠表无限增长。
	foldMaxShapes = 4096
	// foldShapeMaxLen 是形状键的最大长度。
	foldShapeMaxLen = 256
	// foldSampleMaxLen 是参与归一化的行前缀长度，避免对超长行跑正则。
	foldSampleMaxLen = 1024
)

// FoldOptions 是重复日志折叠的配置。Limit 或 Window 非正表示关闭折叠
// （此时 newFoldingWriter 返回 nil，调用方保持原来的 writer）。
type FoldOptions struct {
	Limit  int
	Window time.Duration
}

// Enabled 报告折叠是否生效。
func (o FoldOptions) Enabled() bool {
	return o.Limit > 0 && o.Window > 0
}

type foldingWriter struct {
	access sync.Mutex
	inner  io.Writer
	limit  int
	window time.Duration
	// now 可注入，测试用它推进窗口，生产用 time.Now。
	now func() time.Time

	shapes    map[string]*foldShape
	lastPrune time.Time
}

type foldShape struct {
	start      time.Time
	emitted    int
	suppressed int
}

// newFoldingWriter 用折叠包装 inner。折叠关闭（或 inner 为 nil）时返回 nil，
// 调用方应保持原 writer，避免多一层无用的锁。
func newFoldingWriter(inner io.Writer, options FoldOptions) *foldingWriter {
	if inner == nil || !options.Enabled() {
		return nil
	}
	return &foldingWriter{
		inner:  inner,
		limit:  options.Limit,
		window: options.Window,
		now:    time.Now,
		shapes: make(map[string]*foldShape, 64),
	}
}

func (w *foldingWriter) Write(p []byte) (int, error) {
	w.access.Lock()
	defer w.access.Unlock()
	now := w.now()
	var out bytes.Buffer
	w.pruneLocked(now, &out)
	for _, line := range splitFoldLines(p) {
		w.writeLineLocked(now, line, &out)
	}
	if out.Len() == 0 {
		// 整批都被折叠：对调用方来说这次写入已经"消化"完了。
		return len(p), nil
	}
	if _, err := w.inner.Write(out.Bytes()); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Flush 把所有还在累积的折叠计数写成汇总行。Shutdown 时调用，避免最后一批
// 计数随进程退出丢掉。
func (w *foldingWriter) Flush() {
	if w == nil {
		return
	}
	w.access.Lock()
	defer w.access.Unlock()
	var out bytes.Buffer
	for key, shape := range w.shapes {
		w.writeSummaryLocked(&out, key, shape)
	}
	w.shapes = make(map[string]*foldShape, 64)
	if out.Len() == 0 {
		return
	}
	w.inner.Write(out.Bytes())
}

func (w *foldingWriter) writeLineLocked(now time.Time, line []byte, out *bytes.Buffer) {
	if len(line) == 0 {
		return
	}
	key := foldShapeOf(line)
	shape, loaded := w.shapes[key]
	if !loaded {
		if len(w.shapes) >= foldMaxShapes {
			// 折叠表满：这一行原样输出。宁可日志多，也不能吃掉日志。
			out.Write(line)
			return
		}
		shape = &foldShape{start: now}
		w.shapes[key] = shape
	} else if now.Sub(shape.start) >= w.window {
		// 窗口滚动：先把上一个窗口的折叠情况交代清楚。
		w.writeSummaryLocked(out, key, shape)
		*shape = foldShape{start: now}
	}
	if shape.emitted < w.limit {
		shape.emitted++
		out.Write(line)
		return
	}
	shape.suppressed++
}

// pruneLocked 清掉已经过期的形状：它们的汇总行不能等到"下次出现同形状日志"
// 才写（那可能是永远）。
func (w *foldingWriter) pruneLocked(now time.Time, out *bytes.Buffer) {
	if len(w.shapes) < foldMaxShapes && now.Sub(w.lastPrune) < w.pruneInterval() {
		return
	}
	w.lastPrune = now
	for key, shape := range w.shapes {
		if now.Sub(shape.start) >= w.window {
			w.writeSummaryLocked(out, key, shape)
			delete(w.shapes, key)
		}
	}
}

func (w *foldingWriter) pruneInterval() time.Duration {
	interval := w.window / 4
	if interval <= 0 {
		interval = time.Second
	}
	return interval
}

func (w *foldingWriter) writeSummaryLocked(out *bytes.Buffer, key string, shape *foldShape) {
	if shape.suppressed == 0 {
		return
	}
	fmt.Fprintf(out, "[log-fold] suppressed %d of %d similar lines in %s; shape: %s\n",
		shape.suppressed, shape.emitted+shape.suppressed, w.window, key)
	shape.suppressed = 0
}

// splitFoldLines 按行切开，保留行尾换行；末尾的空片段丢掉。
func splitFoldLines(p []byte) [][]byte {
	lines := bytes.SplitAfter(p, []byte{'\n'})
	if last := len(lines) - 1; last >= 0 && len(lines[last]) == 0 {
		lines = lines[:last]
	}
	return lines
}

var (
	// 形如 `+0800 2006-01-02 15:04:05 ` 的时间戳前缀。
	foldTimestampPattern = regexp.MustCompile(`^[+-][0-9]{4} [0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2} `)
	foldAddressPattern   = regexp.MustCompile(`\b[0-9]{1,3}(?:\.[0-9]{1,3}){3}\b`)
	foldPortPattern      = regexp.MustCompile(`:[0-9]{1,5}\b`)
	foldHexPattern       = regexp.MustCompile(`\b[0-9a-f]{8,}\b`)
	foldNumberPattern    = regexp.MustCompile(`[0-9]+`)
)

// foldShapeOf 把一行日志归一成形状键。
func foldShapeOf(line []byte) string {
	if len(line) > foldSampleMaxLen {
		line = line[:foldSampleMaxLen]
	}
	shape := foldTimestampPattern.ReplaceAllString(string(line), "")
	shape = foldAddressPattern.ReplaceAllString(shape, "<ip>")
	shape = foldPortPattern.ReplaceAllString(shape, ":<port>")
	shape = foldHexPattern.ReplaceAllString(shape, "<hex>")
	shape = foldNumberPattern.ReplaceAllString(shape, "<n>")
	if len(shape) > foldShapeMaxLen {
		shape = shape[:foldShapeMaxLen]
	}
	return shape
}
