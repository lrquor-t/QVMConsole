package lxc

import (
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
