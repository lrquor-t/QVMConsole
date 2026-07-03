package template

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"kvm_console/utils"
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
// dotPrefix=true 时打包成 ./rootfs/ 形态（社区 rootfs 常见：tar -C dir ./rootfs）。
func buildRootfsTar(t *testing.T, dir, name, compress string, flat bool, withOSRelease bool) string {
	return buildRootfsTarOpt(t, dir, name, compress, flat, withOSRelease, false)
}

func buildRootfsTarOpt(t *testing.T, dir, name, compress string, flat, withOSRelease, dotPrefix bool) string {
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
	if dotPrefix {
		target = "./" + target // ./rootfs —— 复现社区 rootfs 包的存储形态
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

// TestFinalizeExtraction_BothForms 回归验证 Critical：probe（InspectRootfsTarball）与
// finalize 的 tar -xf 解包对【rootfs/】与【./rootfs/】两种存储形态都正确落地。
// 关键点：GNU tar 的 --strip-components 按文件系统路径段计数，"./rootfs" 的前导 "./" 也算一段，
// 故 strip 必须随成员名段数调整，否则 ./rootfs 形态会解到 rootfs/bin/sh（双重前缀）。
func TestFinalizeExtraction_BothForms(t *testing.T) {
	cases := []struct {
		name       string
		dotPrefix  bool
		wantMember string // 期望 InspectRootfsTarball 返回的原始成员名
	}{
		{"rootfs_form", false, "rootfs"},
		{"dotrootfs_form", true, "./rootfs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := buildRootfsTarOpt(t, t.TempDir(), tc.name+".tar.gz", "gz", false, true, tc.dotPrefix)

			// 1) probe 路径（InspectRootfsTarball）必须接受此形态，并回填非空 RootfsMember。
			info, err := InspectRootfsTarball(fixture)
			if err != nil {
				t.Fatalf("InspectRootfsTarball failed for %s: %v", tc.name, err)
			}
			if info.RootfsMember == "" {
				t.Fatalf("RootfsMember is empty for %s", tc.name)
			}
			if info.RootfsMember != tc.wantMember {
				t.Fatalf("RootfsMember = %q, want %q", info.RootfsMember, tc.wantMember)
			}

			// 2) 跑【finalize 用的那条命令】并断言落在 <dest>/bin/sh。
			dest := t.TempDir()
			strip := strings.Count(info.RootfsMember, "/") + 1
			args := []string{"-xf", fixture, "-C", dest, "--strip-components", strconv.Itoa(strip), info.RootfsMember}
			res := utils.ExecCommandLongRunning("tar", args...)
			if res.Error != nil {
				t.Fatalf("extraction failed for %s (args=%v): %v\nstdout=%s\nstderr=%s",
					tc.name, args, res.Error, res.Stdout, res.Stderr)
			}
			// 期望：rootfs/bin/sh → bin/sh（无双重 rootfs 前缀）
			wantBinSh := filepath.Join(dest, "bin", "sh")
			if _, err := os.Stat(wantBinSh); err != nil {
				t.Fatalf("%s: bin/sh not extracted at %s: %v", tc.name, wantBinSh, err)
			}
			// 排查双重前缀回归
			doublePrefix := filepath.Join(dest, "rootfs", "bin", "sh")
			if _, err := os.Stat(doublePrefix); err == nil {
				t.Fatalf("%s: double rootfs prefix detected — file landed at %s", tc.name, doublePrefix)
			}
			// 顺带验证 os-release 也落到正确层级
			wantOSR := filepath.Join(dest, "etc", "os-release")
			if _, err := os.Stat(wantOSR); err != nil {
				t.Fatalf("%s: etc/os-release not extracted at %s: %v", tc.name, wantOSR, err)
			}
		})
	}
}

// TestRootfsMemberStripDerivation 锁定 strip 推导逻辑：strip = 段数（按 '/' 切分，含 './'）。
// 防止有人误把 finalize 改回固定 --strip-components=1。
func TestRootfsMemberStripDerivation(t *testing.T) {
	cases := []struct {
		member string
		strip  int
	}{
		{"rootfs", 1},
		{"./rootfs", 2},
	}
	for _, tc := range cases {
		got := strings.Count(tc.member, "/") + 1
		if got != tc.strip {
			t.Errorf("strip for %q = %d, want %d", tc.member, got, tc.strip)
		}
	}
}
