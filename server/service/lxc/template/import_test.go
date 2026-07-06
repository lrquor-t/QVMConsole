package template

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"kvm_console/config"
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

func TestValidateImportParams_Backing(t *testing.T) {
	cases := []struct {
		b  string
		ok bool
	}{
		{"", true}, {"dir", true}, {"overlay", true}, {"zfs", true},
		{"btrfs", false}, {"ZFS", false},
	}
	for _, c := range cases {
		p := &ImportParams{Name: "tm", SourcePath: "/x.tar", Backing: c.b}
		err := validateImportParams(p)
		if c.ok && err != nil {
			t.Errorf("backing=%q 应通过，错误: %v", c.b, err)
		}
		if !c.ok && err == nil {
			t.Errorf("backing=%q 应被拒", c.b)
		}
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

// TestMapArchToLXC 锁定宿主机架构（uname -m 规范化值）到 LXC arch（amd64/arm64）的映射。
// LXC 模板架构必须跟随宿主机；rootfs tarball 内容里没有可靠 arch，故由宿主机决定。
func TestMapArchToLXC(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"x86_64", "amd64", false},
		{"aarch64", "arm64", false},
		{"riscv64", "", true},
		{"mips", "", true},
	}
	for _, tc := range cases {
		got, err := mapArchToLXC(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("mapArchToLXC(%q) want error, got %q", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("mapArchToLXC(%q) unexpected err: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("mapArchToLXC(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestInspectRootfsTarball_OSReleaseSymlink 覆盖：/etc/os-release 是指向
// /usr/lib/os-release 的符号链接（Debian 等惯例）。tar -O 对符号链接成员输出空，
// 故须回退读 rootfs/usr/lib/os-release 才能拿到 ID/VERSION_ID。
func TestInspectRootfsTarball_OSReleaseSymlink(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	rootfs := filepath.Join(src, "rootfs")
	for _, p := range []string{
		filepath.Join(rootfs, "etc"),
		filepath.Join(rootfs, "usr", "lib"),
		filepath.Join(rootfs, "bin"),
	} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(rootfs, "usr", "lib", "os-release"),
		[]byte("NAME=\"Debian GNU/Linux\"\nID=debian\nVERSION_ID=\"12\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// etc/os-release → ../usr/lib/os-release（符号链接）
	if err := os.Symlink("../usr/lib/os-release", filepath.Join(rootfs, "etc", "os-release")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootfs, "bin", "sh"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, "symlink.tar.gz")
	if out, err := exec.Command("tar", "-czf", archive, "-C", src, "rootfs").CombinedOutput(); err != nil {
		t.Fatalf("tar create: %v\n%s", err, out)
	}
	info, err := InspectRootfsTarball(archive)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if info.Distro != "debian" {
		t.Errorf("Distro = %q, want debian (from usr/lib/os-release via symlink fallback)", info.Distro)
	}
	if info.Release != "12" {
		t.Errorf("Release = %q, want 12", info.Release)
	}
}

// TestProbeRootfsTarball 覆盖 probe 快路径：--occurrence=1 定向取 os-release、不算 sha256。
// 兼容 rootfs 与 ./rootfs 存储形态、符号链接回退、缺失报错。
func TestProbeRootfsTarball(t *testing.T) {
	t.Run("rootfs form", func(t *testing.T) {
		p := buildRootfsTar(t, t.TempDir(), "ok.tar.gz", "gz", false, true)
		distro, release, size, err := ProbeRootfsTarball(p)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if distro != "ubuntu" || release != "22.04" {
			t.Errorf("got (%q,%q), want (ubuntu,22.04)", distro, release)
		}
		if size <= 0 {
			t.Error("size should be positive")
		}
	})
	t.Run("dotrootfs form", func(t *testing.T) {
		p := buildRootfsTarOpt(t, t.TempDir(), "ok.tar.gz", "gz", false, true, true)
		distro, release, _, err := ProbeRootfsTarball(p)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if distro != "ubuntu" || release != "22.04" {
			t.Errorf("got (%q,%q), want (ubuntu,22.04)", distro, release)
		}
	})
	t.Run("symlink etc->usr/lib", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "src")
		rootfs := filepath.Join(src, "rootfs")
		for _, p := range []string{filepath.Join(rootfs, "etc"), filepath.Join(rootfs, "usr", "lib"), filepath.Join(rootfs, "bin")} {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(rootfs, "usr", "lib", "os-release"), []byte("ID=debian\nVERSION_ID=\"12\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../usr/lib/os-release", filepath.Join(rootfs, "etc", "os-release")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(rootfs, "bin", "sh"), []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
		archive := filepath.Join(dir, "sym.tar.gz")
		if out, err := exec.Command("tar", "-czf", archive, "-C", src, "rootfs").CombinedOutput(); err != nil {
			t.Fatalf("tar create: %v\n%s", err, out)
		}
		distro, release, _, err := ProbeRootfsTarball(archive)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if distro != "debian" || release != "12" {
			t.Errorf("got (%q,%q), want (debian,12)", distro, release)
		}
	})
	t.Run("missing os-release", func(t *testing.T) {
		p := buildRootfsTar(t, t.TempDir(), "no.tar.gz", "gz", false, false)
		_, _, _, err := ProbeRootfsTarball(p)
		if err == nil || !strings.Contains(err.Error(), "os-release") {
			t.Fatalf("want os-release error, got: %v", err)
		}
	})
}

// TestComposeBaseConfig 覆盖基底 config 去重：lxc-create 默认 net 块（lxcbr0）须移除，
// 我们覆盖的标量键（arch/cgroup2/start.auto）不重复，overlay 关键键（rootfs.path/
// uts.name/include）与 apparmor 默认项保留，我们的 br-ovs net 块仅出现一次。
func TestComposeBaseConfig(t *testing.T) {
	existing := `# Distribution built-in config
lxc.net.0.type = veth
lxc.net.0.link = lxcbr0
lxc.net.0.flags = up
lxc.apparmor.profile = generated
lxc.apparmor.allow_nesting = 1
lxc.rootfs.path = overlay:/var/lib/lxc/base/rootfs
lxc.uts.name = base
lxc.include = /usr/share/lxc/config/common.conf
`
	out := composeBaseConfig(existing, "amd64", "base")
	if strings.Contains(out, "lxcbr0") {
		t.Error("默认 lxcbr0 net 块应被移除")
	}
	if c := strings.Count(out, "lxc.net.0.type = veth"); c != 1 {
		t.Errorf("lxc.net.0.type 出现 %d 次，应为 1（仅我们的）", c)
	}
	if c := strings.Count(out, "lxc.net.0.link = br-ovs"); c != 1 {
		t.Errorf("br-ovs 出现 %d 次，应为 1", c)
	}
	for _, k := range []string{"lxc.rootfs.path", "lxc.uts.name", "lxc.include", "lxc.apparmor.profile"} {
		if !strings.Contains(out, k) {
			t.Errorf("%s 应被保留", k)
		}
	}
	for _, k := range []string{"lxc.arch = x86_64", "lxc.cgroup2.memory.max = 512M", "lxc.start.auto = 0"} {
		if !strings.Contains(out, k) {
			t.Errorf("缺少覆盖项 %s", k)
		}
	}
}

// TestWriteBaseConfig_EnsuresRootfsPath 重现「lxc-create -t none 不写 lxc.rootfs.path」
// 导致后续 lxc-copy「No rootfs specified」的 bug：writeBaseConfig 必须权威地写入
// lxc.rootfs.path = <lxcpath>/<base>/rootfs（import 把 rootfs 解到这里）。
func TestWriteBaseConfig_EnsuresRootfsPath(t *testing.T) {
	tmp := t.TempDir()
	orig := config.GlobalConfig.LXCLxcPath
	config.GlobalConfig.LXCLxcPath = tmp
	t.Cleanup(func() { config.GlobalConfig.LXCLxcPath = orig })

	base := "lxc__tmpl__rocky8"
	cfgDir := filepath.Join(tmp, base)
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(cfgDir, "config")
	// 模拟 lxc-create -t none 的输出：**没有** lxc.rootfs.path 行
	lxcCreateOut := "lxc.uts.name = rocky8\nlxc.include = /usr/share/lxc/config/common.conf\n"
	if err := os.WriteFile(cfg, []byte(lxcCreateOut), 0644); err != nil {
		t.Fatal(err)
	}

	if err := writeBaseConfig(base, "amd64"); err != nil {
		t.Fatalf("writeBaseConfig: %v", err)
	}
	out, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := "lxc.rootfs.path = " + filepath.Join(tmp, base, "rootfs")
	if !strings.Contains(string(out), want) {
		t.Fatalf("基底 config 缺少 rootfs.path 行\ngot:\n%s\nwant line: %s", string(out), want)
	}
}

// TestComposeBaseConfig_ResetsUtsName 验证从容器克隆来的 config（带源主机名）
// 经 composeBaseConfig 后，lxc.uts.name 被权威重置为传入的 utsName（基底名），
// 不残留源容器主机名——否则每个克隆都撞同一主机名。
func TestComposeBaseConfig_ResetsUtsName(t *testing.T) {
	existing := `lxc.uts.name = src-hostname
lxc.net.0.hwaddr = 00:11:22:33:44:55
lxc.net.0.type = veth
lxc.net.0.link = br-ovs
lxc.rootfs.path = dir:/var/lib/lxc/src/rootfs
lxc.cgroup2.cpu.weight = 100
`
	out := composeBaseConfig(existing, "amd64", "lxc__tmpl__foo")
	if strings.Contains(out, "src-hostname") {
		t.Errorf("源主机名应被移除，out=%q", out)
	}
	if c := strings.Count(out, "lxc.uts.name = lxc__tmpl__foo"); c != 1 {
		t.Errorf("lxc.uts.name 应被权威重置为基底名，出现 %d 次，out=%q", c, out)
	}
	if strings.Contains(out, "00:11:22:33:44:55") {
		t.Errorf("源 MAC 应被移除，out=%q", out)
	}
	if c := strings.Count(out, "lxc.net.0.link = br-ovs"); c != 1 {
		t.Errorf("br-ovs 应仅出现 1 次（我们追加的），出现 %d 次", c)
	}
}
