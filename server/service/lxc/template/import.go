package template

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"kvm_console/config"
	"kvm_console/logger"
	"kvm_console/model"
	"kvm_console/utils"
)

func sha256OfFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// FinalizeImport 由已落地的 tarball 创建金基底容器 + DB 行，并删除临时 tarball。
func FinalizeImport(params *ImportParams) error {
	if err := validateImportParams(params); err != nil {
		return err
	}
	if params.OwnerUsername == "" {
		params.OwnerUsername = "admin"
	}
	backing := config.GlobalConfig.LXCDefaultBacking
	base := baseContainerName(params.Name)

	// 校验 tarball（结构 + os-release；sha/size 由其返回）
	info, err := InspectRootfsTarball(params.SourcePath)
	if err != nil {
		return err
	}

	// 名称占用检查
	var cnt int64
	model.DB.Model(&model.LXCTemplate{}).Where("name = ?", params.Name).Count(&cnt)
	if cnt > 0 {
		return fmt.Errorf("模板 %s 已存在", params.Name)
	}
	// 基底容器是否已存在（lxc-create 会失败）
	if existsContainer(base) {
		return fmt.Errorf("基底容器 %s 已存在", base)
	}

	// 创建金基底容器：lxc-create -n <base> -t none -B <backing>
	cre := utils.ExecCommandLongRunning("lxc-create", "-n", base, "-t", "none", "-B", backing)
	if cre.Error != nil {
		return fmt.Errorf("创建基底容器失败: %w", cre.Error)
	}
	// 解包 tarball 到 rootfs（清空后解包）
	rootfs := filepath.Join(config.GlobalConfig.LXCLxcPath, base, "rootfs")
	if err := os.MkdirAll(rootfs, 0755); err != nil {
		return fmt.Errorf("创建 rootfs 目录失败: %w", err)
	}
	// 只取 rootfs/ 子树，去 rootfs/ 前缀，落入 <base>/rootfs/；-xf auto-detect 压缩格式。
	// 成员名按归档原始形态传入（rootfs 或 ./rootfs）。GNU tar 的 --strip-components 按文件系统
	// 路径段计数：对 "./rootfs/bin/sh"，strip=1 会留下 "rootfs/bin/sh"（双重前缀 bug），故须按
	// 成员名段数取 strip：rootfs→1，./rootfs→2，使两种存储形态最终都落在 <rootfs>/bin/sh。
	strip := strings.Count(info.RootfsMember, "/") + 1
	ex := utils.ExecCommandLongRunning("tar", "-xf", params.SourcePath, "-C", rootfs, "--strip-components", strconv.Itoa(strip), info.RootfsMember)
	if ex.Error != nil {
		_ = destroyContainerQuiet(base)
		return fmt.Errorf("解包 rootfs 失败: %w", ex.Error)
	}
	// 写基底 config 默认值（lxc-copy 继承）
	if err := writeBaseConfig(base, params.Arch); err != nil {
		_ = destroyContainerQuiet(base)
		return err
	}

	// 写 DB 行
	tpl := model.LXCTemplate{
		Name:              params.Name,
		DisplayName:       orDefault(params.DisplayName, params.Name),
		Distro:            params.Distro,
		Release:           params.Release,
		Arch:              orDefault(params.Arch, "amd64"),
		Description:       params.Description,
		BaseContainerName: base,
		Backing:           backing,
		RootfsSizeBytes:   info.SizeBytes,
		CloneVisible:      true,
		OwnerUsername:     params.OwnerUsername,
		PostCreateCommand: params.PostCreateCommand,
		SHA256:            info.SHA256,
	}
	if err := model.DB.Create(&tpl).Error; err != nil {
		_ = destroyContainerQuiet(base)
		return fmt.Errorf("保存模板记录失败: %w", err)
	}

	// 删除临时 tarball（基底 rootfs 即唯一源）
	if params.SourcePath != "" && isInDir(params.SourcePath, config.GlobalConfig.LXCTemplateImportDir) {
		if err := os.Remove(params.SourcePath); err != nil {
			logger.App.Warn("删除临时 tarball 失败", "path", params.SourcePath, "error", err)
		}
	}
	logger.App.Info("LXC 模板导入完成", "name", params.Name, "base", base)
	return nil
}

func writeBaseConfig(base, arch string) error {
	if arch == "" {
		arch = "amd64"
	}
	lxcArch := "x86_64"
	if arch == "arm64" {
		lxcArch = "aarch64"
	}
	cfg := filepath.Join(config.GlobalConfig.LXCLxcPath, base, "config")
	// 追加覆盖项到 lxc-create 生成的 config（lxc 配置后值覆盖前值）。
	// 不覆盖 lxc.rootfs.path / lxc.uts.name —— lxc-create 已按所选 backing（overlay）
	// 正确设置这两项；用 dir: 路径覆盖 rootfs.path 会破坏 overlay 后端，导致克隆无法启动。
	lines := []string{
		"lxc.arch = " + lxcArch,
		"lxc.cgroup2.cpu.weight = 256",
		"lxc.cgroup2.memory.max = 512M",
		"lxc.start.auto = 0",
		"lxc.net.0.type = veth",
		"lxc.net.0.flags = up",
		"lxc.net.0.link = br-ovs",
	}
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	f, err := openForAppend(cfg)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return err
}

// ---- helpers ----

// openForAppend 以追加写方式打开文件（lxc 配置后值覆盖前值，故追加而非截断）。
func openForAppend(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func existsContainer(name string) bool {
	res := utils.ExecCommandQuiet("lxc-info", "-n", name)
	return res.ExitCode == 0
}

func destroyContainerQuiet(name string) error {
	res := utils.ExecCommandQuiet("lxc-destroy", "-n", name)
	return res.Error
}

func isInDir(path, dir string) bool {
	abs, _ := filepath.Abs(path)
	d, _ := filepath.Abs(dir)
	return strings.HasPrefix(abs+string(filepath.Separator), d+string(filepath.Separator))
}

// RootfsInfo 是对 rootfs tarball 校验/探测的结果。
type RootfsInfo struct {
	SHA256      string
	SizeBytes   int64
	Distro      string // 来自 os-release 的 ID（best-effort）
	Release     string // 来自 os-release 的 VERSION_ID（best-effort）
	RootfsMember string // 顶层 rootfs 目录在归档里的【原始】成员名（rootfs 或 ./rootfs），FinalizeImport 据此解包
}

// InspectRootfsTarball 校验 tarball（按内容 auto-detect 格式）顶层含 rootfs/ 目录、
// 含 rootfs/etc/os-release，并解析 os-release 的 ID/VERSION_ID，返回 sha256 与大小。
func InspectRootfsTarball(path string) (*RootfsInfo, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("读取 tarball 失败: %w", err)
	}
	if st.IsDir() {
		return nil, fmt.Errorf("模板源必须是文件而非目录")
	}
	// 列条目（-t 不带压缩标志，GNU tar 按内容自动识别 gzip/xz/bzip2/zstd）
	listRes := utils.ExecCommand("tar", "-tf", abs)
	if listRes.Error != nil {
		return nil, fmt.Errorf("非有效 tar 包或格式不支持: %w", listRes.Error)
	}
	rawListing := listRes.Stdout
	if !listingHasTopLevelRootfs(rawListing) {
		return nil, fmt.Errorf("压缩包顶层未找到 rootfs 目录")
	}
	// 顶层 rootfs 校验已通过，findMember 必然命中；保留原始成员名供 finalize 解包用。
	// 去掉目录条目的尾随 '/'（tar -t 常把目录列为 "rootfs/"），使后续 strip 推导与解包选择器稳定。
	rootfsMember, ok := findMember(rawListing, "rootfs")
	if !ok {
		return nil, fmt.Errorf("压缩包顶层未找到 rootfs 目录")
	}
	rootfsMember = strings.TrimSuffix(rootfsMember, "/")
	osrMember, ok := findMember(rawListing, "rootfs/etc/os-release")
	if !ok {
		return nil, fmt.Errorf("rootfs 下缺少 etc/os-release，无法判定为合法 rootfs")
	}
	// 单成员解到 stdout（auto-detect）
	osr := utils.ExecCommand("tar", "-xf", abs, "-O", osrMember)
	if osr.Error != nil {
		return nil, fmt.Errorf("读取 os-release 失败: %w", osr.Error)
	}
	distro, release := parseOSRelease(osr.Stdout)
	sha, err := sha256OfFile(abs)
	if err != nil {
		return nil, err
	}
	return &RootfsInfo{SHA256: sha, SizeBytes: st.Size(), Distro: distro, Release: release, RootfsMember: rootfsMember}, nil
}

// listingHasTopLevelRootfs 判断 tar -t 输出里是否存在顶层 rootfs 目录
// （原始条目为 rootfs、rootfs/、./rootfs、./rootfs/ ，或以其为前缀的子路径）。
func listingHasTopLevelRootfs(listing string) bool {
	for _, line := range strings.Split(listing, "\n") {
		e := strings.TrimSpace(line)
		if e == "" {
			continue
		}
		e = strings.TrimPrefix(e, "./")
		e = strings.TrimSuffix(e, "/")
		if e == "rootfs" || strings.HasPrefix(e, "rootfs/") {
			return true
		}
	}
	return false
}

// findMember 在 tar -t 原始输出里找规范化后等于 target 的成员，返回其原始行。
// 用原始行（而非规范化值）传给 tar -O，以兼容 ./rootfs/... 形式的存储名。
func findMember(listing, target string) (string, bool) {
	for _, line := range strings.Split(listing, "\n") {
		raw := strings.TrimSpace(line)
		if raw == "" {
			continue
		}
		norm := strings.TrimSuffix(strings.TrimPrefix(raw, "./"), "/")
		if norm == target {
			return raw, true
		}
	}
	return "", false
}

// parseOSRelease 解析 os-release 文本，取 ID 与 VERSION_ID（去引号与首尾空白）。缺失返回空串。
func parseOSRelease(content string) (distro, release string) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.Trim(strings.TrimSpace(line[eq+1:]), `"'`)
		switch key {
		case "ID":
			distro = val
		case "VERSION_ID":
			release = val
		}
	}
	return distro, release
}
