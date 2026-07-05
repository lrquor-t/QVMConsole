package zfsbacking

import "testing"

func TestNormalizeZfsCreation(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Sun Jul  5 13:26 2026", "2026-07-05 13:26:00"},  // 单日，zfs 留双空格
		{"Mon Jul 15 16:23 2026", "2026-07-15 16:23:00"},  // 双日
		{"Wed Jan  1 00:00 2025", "2025-01-01 00:00:00"},  // 年初
		{"Sat Dec 31 23:59 2025", "2025-12-31 23:59:00"},  // 年末
	}
	for _, c := range cases {
		got := normalizeZfsCreation(c.in)
		if got != c.want {
			t.Errorf("normalizeZfsCreation(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// 解析失败保留原值（去首尾空白）
	if got := normalizeZfsCreation("  garbage not a date  "); got != "garbage not a date" {
		t.Errorf("normalizeZfsCreation(garbage) = %q, want original trimmed", got)
	}
	// 空串
	if got := normalizeZfsCreation("   "); got != "" {
		t.Errorf("normalizeZfsCreation(spaces) = %q, want empty", got)
	}
}
