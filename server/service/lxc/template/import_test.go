package template

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBaseContainerName(t *testing.T) {
	got := baseContainerName("ubuntu22")
	want := "lxc__tmpl__ubuntu22"
	if got != want {
		t.Fatalf("baseContainerName = %q, want %q", got, want)
	}
}

func TestIsBaseContainer(t *testing.T) {
	if !isBaseContainer("lxc__tmpl__ubuntu22") {
		t.Fatal("should detect base container")
	}
	if isBaseContainer("c1") {
		t.Fatal("c1 is not a base container")
	}
}

func TestValidateImportParams(t *testing.T) {
	if err := validateImportParams(&ImportParams{Name: "ubuntu22", Arch: "amd64", SourcePath: "/tmp/x.tar.gz"}); err != nil {
		t.Fatalf("valid params err: %v", err)
	}
	if err := validateImportParams(&ImportParams{Name: "", SourcePath: "/tmp/x.tar.gz"}); err == nil {
		t.Fatal("empty name should fail")
	}
}

// buildRootfsTar 用 GNU tar 在 dir 下打出含 rootfs/ 的压缩包。
// compress: "gz" | "xz" | "none"。flat=true 时不套 rootfs/ 层（造缺 rootfs 用例）。
func buildRootfsTar(t *testing.T, dir, name, compress string, flat bool, withOSRelease bool) string {
	t.Helper()
	src := filepath.Join(dir, "src")
	rootfs := src
	if !flat {
		rootfs = filepath.Join(src, "rootfs")
	}
	if err := os.MkdirAll(filepath.Join(rootfs, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootfs, "bin", "sh"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if withOSRelease {
		if err := os.MkdirAll(filepath.Join(rootfs, "etc"), 0o755); err != nil {
			t.Fatal(err)
		}
		osRelease := "NAME=\"Ubuntu\"\nID=ubuntu\nVERSION_ID=\"22.04\"\nPRETTY_NAME=\"Ubuntu 22.04\"\n"
		if err := os.WriteFile(filepath.Join(rootfs, "etc", "os-release"), []byte(osRelease), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	flag := "-czf"
	switch compress {
	case "xz":
		flag = "-cJf"
	case "none":
		flag = "-cf"
	}
	out := filepath.Join(dir, name)
	target := "."
	if !flat {
		target = "rootfs"
	}
	cmd := exec.Command("tar", flag, out, "-C", src, target)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tar create failed: %v\n%s", err, out)
	}
	return out
}

func TestInspectRootfsTarball_Gzip(t *testing.T) {
	p := buildRootfsTar(t, t.TempDir(), "ok.tar.gz", "gz", false, true)
	info, err := InspectRootfsTarball(p)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if info.Distro != "ubuntu" {
		t.Errorf("Distro = %q, want ubuntu", info.Distro)
	}
	if info.Release != "22.04" {
		t.Errorf("Release = %q, want 22.04", info.Release)
	}
	if info.SHA256 == "" || info.SizeBytes == 0 {
		t.Error("SHA256/SizeBytes should be set")
	}
}

func TestInspectRootfsTarball_Xz(t *testing.T) {
	p := buildRootfsTar(t, t.TempDir(), "ok.tar.xz", "xz", false, true)
	if _, err := InspectRootfsTarball(p); err != nil {
		t.Fatalf("xz should be auto-detected: %v", err)
	}
}

func TestInspectRootfsTarball_Plain(t *testing.T) {
	p := buildRootfsTar(t, t.TempDir(), "ok.tar", "none", false, true)
	if _, err := InspectRootfsTarball(p); err != nil {
		t.Fatalf("plain tar should be accepted: %v", err)
	}
}

func TestInspectRootfsTarball_MissingRootfsDir(t *testing.T) {
	p := buildRootfsTar(t, t.TempDir(), "flat.tar.gz", "gz", true, true)
	_, err := InspectRootfsTarball(p)
	if err == nil || !strings.Contains(err.Error(), "rootfs 目录") {
		t.Fatalf("want rootfs-dir error, got: %v", err)
	}
}

func TestInspectRootfsTarball_MissingOSRelease(t *testing.T) {
	p := buildRootfsTar(t, t.TempDir(), "noosr.tar.gz", "gz", false, false)
	_, err := InspectRootfsTarball(p)
	if err == nil || !strings.Contains(err.Error(), "os-release") {
		t.Fatalf("want os-release error, got: %v", err)
	}
}

func TestInspectRootfsTarball_NotATar(t *testing.T) {
	p := filepath.Join(t.TempDir(), "bad.tar.gz")
	if err := os.WriteFile(p, []byte("this is definitely not a tar"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := InspectRootfsTarball(p)
	if err == nil || !strings.Contains(err.Error(), "非有效 tar 包") {
		t.Fatalf("want invalid-tar error, got: %v", err)
	}
}

func TestParseOSRelease(t *testing.T) {
	d, r := parseOSRelease("NAME=\"Ubuntu\"\nID=ubuntu\nVERSION_ID=\"22.04\"\n# comment\nPRETTY_NAME=Ubuntu\n")
	if d != "ubuntu" || r != "22.04" {
		t.Errorf("got (%q,%q), want (ubuntu,22.04)", d, r)
	}
}
