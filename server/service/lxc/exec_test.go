package lxc

import "testing"

func TestBoundedWriterUnderCap(t *testing.T) {
	w := newBoundedWriter(16)
	w.Write([]byte("hello"))
	if string(w.buf) != "hello" || w.truncated {
		t.Fatalf("buf=%q truncated=%v", w.buf, w.truncated)
	}
}

func TestBoundedWriterTruncates(t *testing.T) {
	w := newBoundedWriter(8)
	w.Write([]byte("1234"))
	w.Write([]byte("5678"))
	w.Write([]byte("90AB")) // 超出
	if string(w.buf) != "12345678" {
		t.Fatalf("buf=%q want 12345678", w.buf)
	}
	if !w.truncated {
		t.Fatalf("should be truncated")
	}
}

func TestClampTimeout(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, 30}, {-1, 30}, {5, 5}, {300, 300}, {301, 300}, {400, 300},
	}
	for _, c := range cases {
		if got := clampTimeout(c.in, 30, 300); got != c.want {
			t.Fatalf("clampTimeout(%d)=%d want %d", c.in, got, c.want)
		}
	}
}
