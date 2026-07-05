package lxc

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseSnapshotList(t *testing.T) {
	in := "Name        Comment                      Creation time\n" +
		"snap0       -                            2026-07-02 10:00:00\n" +
		"snap1       hello world                  2026-07-02 11:00:00\n"
	got := parseSnapshotList(in)
	if len(got) != 2 {
		t.Fatalf("len = %d, got %+v", len(got), got)
	}
	if got[0].Name != "snap0" || got[0].Comment != "" || got[0].CreatedAt != "2026-07-02 10:00:00" {
		t.Fatalf("got[0] = %+v", got[0])
	}
	if got[1].Name != "snap1" || got[1].Comment != "hello world" || got[1].CreatedAt != "2026-07-02 11:00:00" {
		t.Fatalf("got[1] = %+v", got[1])
	}
}

func TestSanitizeSnapshotComment(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"clean string", "clean string"},
		{"a\nb", "a b"},
		{"a\rb", "a b"},
		{"a\tb", "a b"},
		{"a\r\nb", "a b"},     // CRLF → 单个空格（两控制符的 run 合并）
		{"a\n\t\rb", "a b"},   // 多控制符 run 合并成一个空格
		{"  extra  spaces  ", "  extra  spaces  "}, // 普通空格不动
	}
	for _, c := range cases {
		got := sanitizeSnapshotComment(c.in)
		if got != c.want {
			t.Errorf("sanitizeSnapshotComment(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	// 长度上限：200 runes（与前端 maxlength 一致），多字节字符不切断。
	long := strings.Repeat("x", 250)
	got := sanitizeSnapshotComment(long)
	if utf8.RuneCountInString(got) != 200 {
		t.Errorf("250-ascii truncation: got %d runes, want 200 (len=%d)", utf8.RuneCountInString(got), len(got))
	}

	multi := strings.Repeat("世", 250) // 每字 3 bytes
	gotM := sanitizeSnapshotComment(multi)
	if utf8.RuneCountInString(gotM) != 200 {
		t.Errorf("250-multibyte truncation: got %d runes, want 200", utf8.RuneCountInString(gotM))
	}
	// 截断后剩下的字节串应仍是合法 UTF-8（未切断多字节字符）。
	if !utf8.ValidString(gotM) {
		t.Errorf("truncated multibyte string is not valid UTF-8")
	}

	// 控制符清洗发生在截断之前：250 字符里有换行，先清洗再截到 200。
	dirty := strings.Repeat("a\n", 125) // 250 chars → 清洗后 250 ('a' + ' ' 重复) → 截 200
	gotD := sanitizeSnapshotComment(dirty)
	if utf8.RuneCountInString(gotD) != 200 {
		t.Errorf("dirty truncation: got %d runes, want 200", utf8.RuneCountInString(gotD))
	}
	if strings.ContainsAny(gotD, "\n\r\t") {
		t.Errorf("dirty truncation still has control chars: %q", gotD)
	}
}
