package lxc

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"kvm_console/config"
)

func TestMain(m *testing.M) {
	// Initialize config for tests
	config.Init()
	m.Run()
}

func TestRenderMountEntry(t *testing.T) {
	cases := []struct {
		name string
		in   LXCMount
		want string
	}{
		{"rw", LXCMount{HostPath: "/data/share", Target: "/mnt/data", ReadOnly: false},
			"lxc.mount.entry = /data/share mnt/data none bind,create=dir 0 0"},
		{"ro", LXCMount{HostPath: "/iso", Target: "/mnt/iso", ReadOnly: true},
			"lxc.mount.entry = /iso mnt/iso none bind,create=dir,ro 0 0"},
		{"target without leading slash preserved", LXCMount{HostPath: "/a/b", Target: "/x/y", ReadOnly: false},
			"lxc.mount.entry = /a/b x/y none bind,create=dir 0 0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := renderMountEntry(c.in); got != c.want {
				t.Fatalf("renderMountEntry = %q, want %q", got, c.want)
			}
		})
	}
}

func TestParseMountEntry(t *testing.T) {
	m, ok := parseMountEntry("lxc.mount.entry = /data/share mnt/data none bind,create=dir 0 0")
	if !ok || !reflect.DeepEqual(m, LXCMount{HostPath: "/data/share", Target: "/mnt/data", ReadOnly: false}) {
		t.Fatalf("rw parse got (%+v, %v)", m, ok)
	}
	m, ok = parseMountEntry("lxc.mount.entry = /iso mnt/iso none bind,create=dir,ro 0 0")
	if !ok || !reflect.DeepEqual(m, LXCMount{HostPath: "/iso", Target: "/mnt/iso", ReadOnly: true}) {
		t.Fatalf("ro parse got (%+v, %v)", m, ok)
	}
	// 非 bind（overlay）→ 不属于我们管理
	if _, ok := parseMountEntry("lxc.mount.entry = /a b overlay ro 0 0"); ok {
		t.Fatalf("overlay line should not be recognized as ours")
	}
	// 非 mount.entry 行
	if _, ok := parseMountEntry("lxc.uts.name = x"); ok {
		t.Fatalf("non mount.entry line should not parse")
	}
	// 注释行
	if _, ok := parseMountEntry("# lxc.mount.entry = x"); ok {
		t.Fatalf("comment line should not parse")
	}
	// target 带前导斜杠也能正确还原
	m, ok = parseMountEntry("lxc.mount.entry = /a /b none bind,create=dir 0 0")
	if !ok || m.Target != "/b" {
		t.Fatalf("leading-slash target got (%+v, %v)", m, ok)
	}
}

func TestParseOurMounts(t *testing.T) {
	text := "lxc.uts.name = c1\n" +
		"lxc.mount.entry = /data/share mnt/data none bind,create=dir 0 0\n" +
		"lxc.mount.entry = /a b overlay ro 0 0\n" +
		"lxc.mount.entry = /iso mnt/iso none bind,create=dir,ro 0 0\n"
	got := parseOurMounts(text)
	if len(got) != 2 || got[0].Target != "/mnt/data" || got[1].ReadOnly != true {
		t.Fatalf("parseOurMounts got %+v", got)
	}
}

func TestContainerConfigPath(t *testing.T) {
	// 仅校验拼接形态，不读 config 常量值
	got := containerConfigPath("c1")
	if !strings.HasSuffix(got, filepath.Join("c1", "config")) {
		t.Fatalf("containerConfigPath = %q", got)
	}
}

func TestValidateHostPath(t *testing.T) {
	// 黑名单
	for _, p := range []string{"/", "/proc", "/sys", "/dev"} {
		if err := validateHostPath(p); err == nil {
			t.Fatalf("validateHostPath(%q) should reject blacklist", p)
		}
	}
	// 相对路径
	if err := validateHostPath("relative/path"); err == nil {
		t.Fatalf("relative path should be rejected")
	}
	// 含空格（会破坏 mount.entry 字段切分）
	if err := validateHostPath("/a b"); err == nil {
		t.Fatalf("path with space should be rejected")
	}
	// 不存在
	if err := validateHostPath("/definitely/not/here/12345"); err == nil {
		t.Fatalf("non-existent path should be rejected")
	}
	// 是文件不是目录
	file := filepath.Join(t.TempDir(), "f")
	os.WriteFile(file, []byte("x"), 0644)
	if err := validateHostPath(file); err == nil {
		t.Fatalf("file should be rejected (not a dir)")
	}
	// 合法：临时目录
	dir := t.TempDir()
	if err := validateHostPath(dir); err != nil {
		t.Fatalf("valid dir rejected: %v", err)
	}
}

func TestValidateTarget(t *testing.T) {
	if err := validateTarget("/"); err == nil {
		t.Fatalf("root target should be rejected")
	}
	if err := validateTarget("mnt/data"); err == nil {
		t.Fatalf("relative target should be rejected")
	}
	if err := validateTarget("/a b"); err == nil {
		t.Fatalf("target with space should be rejected")
	}
	if err := validateTarget("/mnt/data"); err != nil {
		t.Fatalf("valid target rejected: %v", err)
	}
}

func TestContainerIsUnprivileged(t *testing.T) {
	if containerIsUnprivileged("lxc.uts.name = c1\n") {
		t.Fatalf("privileged config misdetected as unprivileged")
	}
	if !containerIsUnprivileged("lxc.idmap = u:0 100000 65536\n") {
		t.Fatalf("idmap config not detected")
	}
	if !containerIsUnprivileged("lxc.uidmap = 0 100000 65536\n") {
		t.Fatalf("uidmap config not detected")
	}
	// 注释里的 idmap 不算
	if containerIsUnprivileged("# lxc.idmap = u:0 100000 65536\n") {
		t.Fatalf("commented idmap should not count")
	}
}

func TestRewriteMountEntries(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	original := "lxc.uts.name = c1\n" +
		"lxc.mount.entry = /old mnt/old none bind,create=dir 0 0\n" + // 我们的旧行（应被替换）
		"lxc.mount.entry = /a b overlay ro 0 0\n" + // 非我们的行（必须保留）
		"lxc.start.auto = 0\n"
	os.WriteFile(cfg, []byte(original), 0644)

	want := []LXCMount{
		{HostPath: "/data/share", Target: "/mnt/data", ReadOnly: false},
		{HostPath: "/iso", Target: "/mnt/iso", ReadOnly: true},
	}
	if err := rewriteMountEntries(cfg, original, want); err != nil {
		t.Fatalf("rewriteMountEntries err: %v", err)
	}
	got, _ := os.ReadFile(cfg)
	s := string(got)
	// 新行存在
	if !strings.Contains(s, "lxc.mount.entry = /data/share mnt/data none bind,create=dir 0 0") {
		t.Fatalf("new rw line missing: %s", s)
	}
	if !strings.Contains(s, "lxc.mount.entry = /iso mnt/iso none bind,create=dir,ro 0 0") {
		t.Fatalf("new ro line missing: %s", s)
	}
	// 旧我们的行已删
	if strings.Contains(s, "/old mnt/old") {
		t.Fatalf("old managed line should be dropped: %s", s)
	}
	// 非我们的 overlay 行保留
	if !strings.Contains(s, "/a b overlay ro 0 0") {
		t.Fatalf("foreign mount.entry must be preserved: %s", s)
	}
	// 其它 key 保留
	if !strings.Contains(s, "lxc.start.auto = 0") {
		t.Fatalf("other config keys must be preserved: %s", s)
	}
}
