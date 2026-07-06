package template

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBackingFromRootfsPath(t *testing.T) {
	cases := map[string]string{
		"dir:/var/lib/lxc/c/rootfs":     "dir",
		"overlayfs:/a:/b":               "overlay",
		"zfs:zp01/lxc/c":                "zfs",
		"/var/lib/lxc/c/rootfs":         "dir",
		"":                              "dir",
		"btrfs:/var/lib/lxc/c":          "dir",
	}
	for in, want := range cases {
		if got := backingFromRootfsPath(in); got != want {
			t.Errorf("backingFromRootfsPath(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestExtractRootfsPathFromConfig(t *testing.T) {
	cfg := `# comment
lxc.uts.name = c
lxc.rootfs.path = overlayfs:/var/lib/lxc/c/rootfs:/var/lib/lxc/c/delta
lxc.net.0.type = veth
`
	if got := extractRootfsPathFromConfig(cfg); got != "overlayfs:/var/lib/lxc/c/rootfs:/var/lib/lxc/c/delta" {
		t.Errorf("extractRootfsPathFromConfig=%q", got)
	}
	if extractRootfsPathFromConfig("no rootfs here") != "" {
		t.Error("缺失时应返回空串")
	}
}

func TestParseLxcInfoState(t *testing.T) {
	out := `Name: c1
State: RUNNING
IP: 10.0.0.1
`
	if got := parseLxcInfoState(out); got != "RUNNING" {
		t.Errorf("parseLxcInfoState=%q, want RUNNING", got)
	}
	if parseLxcInfoState("nothing") != "" {
		t.Error("缺失时应返回空串")
	}
}

func TestReadOSReleaseFromDir(t *testing.T) {
	tmp := t.TempDir()
	etc := filepath.Join(tmp, "etc")
	if err := os.MkdirAll(etc, 0755); err != nil {
		t.Fatal(err)
	}
	content := `ID=ubuntu
VERSION_ID="22.04"
`
	if err := os.WriteFile(filepath.Join(etc, "os-release"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	d, r := readOSReleaseFromDir(tmp)
	if d != "ubuntu" || r != "22.04" {
		t.Errorf("readOSReleaseFromDir=(%q,%q), want (ubuntu,22.04)", d, r)
	}
	// 目录无 os-release → 空串（best-effort，不报错）
	d2, r2 := readOSReleaseFromDir(t.TempDir())
	if d2 != "" || r2 != "" {
		t.Errorf("无 os-release 应返回空串，got (%q,%q)", d2, r2)
	}
}
