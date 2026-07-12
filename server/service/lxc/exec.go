package lxc

const (
	execDefaultTimeoutSec = 30
	execMaxTimeoutSec     = 300
	execStreamCapBytes    = 512 * 1024
	execMaxCommandBytes   = 4 * 1024
)

// boundedWriter 是一个 io.Writer：写入累计到 cap 字节后丢弃多余内容并标记 truncated。
// 始终返回 len(p) 已写，避免管道因消费方不读而阻塞。
type boundedWriter struct {
	buf       []byte
	cap       int
	truncated bool
}

func newBoundedWriter(cap int) *boundedWriter {
	return &boundedWriter{cap: cap}
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if len(w.buf) >= w.cap {
		w.truncated = true
		return len(p), nil
	}
	remaining := w.cap - len(w.buf)
	if len(p) <= remaining {
		w.buf = append(w.buf, p...)
		return len(p), nil
	}
	w.buf = append(w.buf, p[:remaining]...)
	w.truncated = true
	return len(p), nil
}

// clampTimeout 规范化超时秒数：sec<=0 取 def；sec>max 截到 max；否则取 sec。
func clampTimeout(sec, def, max int) int {
	if sec <= 0 {
		return def
	}
	if sec > max {
		return max
	}
	return sec
}
