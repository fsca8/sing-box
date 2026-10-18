// experimental/monitor/ringbuf.go
package monitor

// recordRing 是定容环形缓冲：写满之后每来一条就覆盖最旧的一条。
//
// 不加锁：调用方（Collector）统一用 c.mu 串行化。连接记录还要能被后续的
// TLS/流量/关闭事件就地更新，索引表和环必须看到同一份数据，分成两把锁
// 只会制造竞态。
type recordRing[T any] struct {
	buf  []T
	head int // 最旧一条的下标
	size int
}

func newRecordRing[T any](capacity int) *recordRing[T] {
	if capacity < 1 {
		capacity = 1
	}
	return &recordRing[T]{buf: make([]T, capacity)}
}

// push 追加一条记录，返回是否覆盖了最旧的一条。
func (r *recordRing[T]) push(v T) bool {
	evicted := r.size == len(r.buf)
	r.buf[(r.head+r.size)%len(r.buf)] = v
	if evicted {
		r.head = (r.head + 1) % len(r.buf)
	} else {
		r.size++
	}
	return evicted
}

func (r *recordRing[T]) len() int { return r.size }

func (r *recordRing[T]) capacity() int { return len(r.buf) }

// newest 按「新 → 旧」返回最多 n 条记录。
func (r *recordRing[T]) newest(n int) []T {
	if n > r.size {
		n = r.size
	}
	out := make([]T, 0, n)
	for i := 0; i < n; i++ {
		// head 很可能已经前移过，取模才是"倒数第 i+1 条"。
		out = append(out, r.buf[(r.head+r.size-1-i)%len(r.buf)])
	}
	return out
}

// oldest 按「旧 → 新」返回最多 n 条记录（即最后 n 条，与落库时代
// `Last(n)` 的顺序一致）。
func (r *recordRing[T]) oldest(n int) []T {
	out := r.newest(n)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}
