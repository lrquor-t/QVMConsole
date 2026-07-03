package lxc

import (
	"os"
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

// TestMoveDir_RecoverFromPartialTarget 重现「cp 超时后留下半成品 to」的腐败场景：
// 旧实现见 to 存在即跳过，会把半成品当作已完成 → 重试后 lxc.conf 指向损坏容器。
// 期望：from 仍在 + to 残留时，清掉 to 重新搬，最终 to=from 内容、from 消失。
func TestMoveDir_RecoverFromPartialTarget(t *testing.T) {
	tmp := t.TempDir()
	from := filepath.Join(tmp, "c1")
	to := filepath.Join(tmp, "c1_moved")

	if err := os.MkdirAll(from, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(from, "real.txt"), []byte("real"), 0644); err != nil {
		t.Fatal(err)
	}
	// 模拟被超时杀掉的 cp 留下的半成品
	if err := os.MkdirAll(to, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(to, "partial.txt"), []byte("partial"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := moveDir(from, to); err != nil {
		t.Fatalf("moveDir 失败: %v", err)
	}
	if _, err := os.Stat(from); !os.IsNotExist(err) {
		t.Fatalf("源目录应已移走，stat err=%v", err)
	}
	if b, err := os.ReadFile(filepath.Join(to, "real.txt")); err != nil || string(b) != "real" {
		t.Fatalf("目标缺少真实内容 real.txt: %v %q", err, b)
	}
	if _, err := os.Stat(filepath.Join(to, "partial.txt")); !os.IsNotExist(err) {
		t.Fatalf("残留的 partial.txt 应被清理: stat err=%v", err)
	}
}

// TestMoveDir_SameFilesystem_Renames 同文件系统走 os.Rename（瞬时），搬完源消失。
func TestMoveDir_SameFilesystem_Renames(t *testing.T) {
	tmp := t.TempDir()
	from := filepath.Join(tmp, "src")
	to := filepath.Join(tmp, "dst")
	if err := os.MkdirAll(from, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(from, "f.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := moveDir(from, to); err != nil {
		t.Fatalf("moveDir 失败: %v", err)
	}
	if _, err := os.Stat(from); !os.IsNotExist(err) {
		t.Fatal("源目录应已移走")
	}
	b, err := os.ReadFile(filepath.Join(to, "f.txt"))
	if err != nil || string(b) != "x" {
		t.Fatalf("内容不匹配: %v %q", err, b)
	}
}

// TestMoveDir_IdempotentWhenSourceGone 源已不在 + 目标已就位 = 上次完全迁完，跳过。
func TestMoveDir_IdempotentWhenSourceGone(t *testing.T) {
	tmp := t.TempDir()
	from := filepath.Join(tmp, "gone") // 不存在
	to := filepath.Join(tmp, "present")
	if err := os.MkdirAll(to, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(to, "keep.txt"), []byte("k"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := moveDir(from, to); err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(to, "keep.txt"))
	if err != nil || string(b) != "k" {
		t.Fatalf("目标被改动: %v %q", err, b)
	}
}
