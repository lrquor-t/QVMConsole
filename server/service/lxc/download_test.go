package lxc

import "testing"

func TestParseDownloadList(t *testing.T) {
	// 真实 --list 片段：表头前有 "Downloading..." + ---；行空白分隔；含 cloud 变体（过滤）+ 重复行（去重）。
	in := "Downloading the image index\n\n---\n" +
		"DIST\tRELEASE\tARCH\tVARIANT\tBUILD\n" +
		"---\n" +
		"almalinux\t10\tamd64\tdefault\t20260703_23:08\n" +
		"almalinux\t10\tarm64\tdefault\t20260703_23:08\n" +
		"alpine\t3.21\tamd64\tdefault\t20260703_13:00\n" +
		"alpine\t3.21\tarmhf\tdefault\t20260703_13:03\n" +
		"debian\tbookworm\tamd64\tcloud\t20260703_05:24\n" +
		"almalinux\t10\tamd64\tdefault\t20260703_23:08\n" +
		"lxc-create: ... Failed to create container\n"
	got := parseDownloadList(in)
	want := []DownloadImageEntry{
		{"almalinux", "10", "amd64"},
		{"almalinux", "10", "arm64"},
		{"alpine", "3.21", "amd64"},
		{"alpine", "3.21", "armhf"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i, e := range want {
		if got[i] != e {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], e)
		}
	}
}

func TestParseDownloadList_Empty(t *testing.T) {
	// 无外网：输出无表头 → 0 条
	if got := parseDownloadList("Downloading the image index\nfailed to download\n"); len(got) != 0 {
		t.Fatalf("expected 0 entries, got %d: %+v", len(got), got)
	}
}
