package lxc

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCascadeImportDir(t *testing.T) {
	old := "/var/lib/lxc"
	cases := []struct{ old, new, cur, want string }{
		{old, "/data/lxc", filepath.Join(old, "_imports"), filepath.Join("/data/lxc", "_imports")}, // 默认跟随
		{old, "/data/lxc", "/opt/imports", "/opt/imports"},                                         // 用户自定义不动
		{old, "/data/lxc", "", ""}, // 空保持空
	}
	for _, c := range cases {
		if got := CascadeImportDir(c.old, c.new, c.cur); got != c.want {
			t.Errorf("CascadeImportDir(%q,%q,%q)=%q want %q", c.old, c.new, c.cur, got, c.want)
		}
	}
}

func TestRewriteLxcConf(t *testing.T) {
	// 替换已有行，保留其它行
	in := "lxc.uts.name = x\nlxc.lxcpath = /var/lib/lxc\n# note\n"
	out := rewriteLxcConf(in, "/data/lxc")
	if !strings.Contains(out, "lxc.lxcpath = /data/lxc") ||
		strings.Contains(out, "/var/lib/lxc") ||
		!strings.Contains(out, "lxc.uts.name = x") {
		t.Fatalf("替换已有行错误: %q", out)
	}
	// 无则追加
	out2 := rewriteLxcConf("lxc.uts.name = x\n", "/data/lxc")
	if !strings.HasSuffix(strings.TrimSpace(out2), "lxc.lxcpath = /data/lxc") {
		t.Fatalf("未追加 lxc.lxcpath: %q", out2)
	}
	// 不误匹配带后缀的键（lxc.lxcpath.extra 不应被改）
	in3 := "lxc.lxcpath.extra = keep\n"
	out3 := rewriteLxcConf(in3, "/data/lxc")
	if !strings.Contains(out3, "lxc.lxcpath.extra = keep") || !strings.Contains(out3, "lxc.lxcpath = /data/lxc") {
		t.Fatalf("误匹配带后缀键: %q", out3)
	}
}

func TestRewriteContainerConfig_NoPrefixCollision(t *testing.T) {
	// 关键：旧 /var/lib/lxc 与新 /var/lib/lxc2 不能互相误伤
	old := "/var/lib/lxc"
	new := "/var/lib/lxc2"
	in := "lxc.rootfs.path = overlay:/var/lib/lxc/c1/rootfs:/var/lib/lxc/c1//rootfs\n"
	out := rewriteContainerConfig(in, old, new)
	want := "lxc.rootfs.path = overlay:/var/lib/lxc2/c1/rootfs:/var/lib/lxc2/c1//rootfs\n"
	if out != want {
		t.Fatalf("改写错误:\n got: %q\nwant: %q", out, want)
	}
	// 不应把已存在的 /var/lib/lxc2 再次叠加成 /var/lib/lxc22
	if strings.Contains(out, "/var/lib/lxc22") {
		t.Fatalf("前缀碰撞叠加: %q", out)
	}
	// 含 /var/lib/lxc-other 的内容不应被改动
	in2 := "x = /var/lib/lxc-other/y\n"
	if got := rewriteContainerConfig(in2, old, new); got != in2 {
		t.Fatalf("误伤相邻前缀: %q", got)
	}
}

func TestPlanRelocateMoves(t *testing.T) {
	steps := planRelocateMoves("/old", "/new", []string{"c1", "c2", ""})
	if len(steps) != 2 {
		t.Fatalf("应跳过空名，得到 2 步，实际 %d", len(steps))
	}
	if steps[0].From != "/old/c1" || steps[0].To != "/new/c1" {
		t.Fatalf("第 1 步错误: %+v", steps[0])
	}
	if steps[1].From != "/old/c2" || steps[1].To != "/new/c2" {
		t.Fatalf("第 2 步错误: %+v", steps[1])
	}
}
