package template

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"kvm_console/config"
	"kvm_console/logger"
	"kvm_console/model"
	"kvm_console/utils"
)

// ValidateRootfsTarball 校验 tarball 是合法 gzip 并含 rootfs 标志路径，返回 sha256 与解压后大小估算。
func ValidateRootfsTarball(path string) (string, int64, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", 0, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return "", 0, fmt.Errorf("读取 tarball 失败: %w", err)
	}
	if st.IsDir() {
		return "", 0, fmt.Errorf("模板源必须是文件而非目录")
	}
	// 列出条目，校验存在 rootfs 标志（/bin/sh 或 /etc/os-release）
	listRes := utils.ExecCommand("tar", "-tzf", abs)
	if listRes.Error != nil {
		return "", 0, fmt.Errorf("解析 tarball 失败（非 gzip rootfs?）: %w", listRes.Error)
	}
	if !looksLikeRootfs(listRes.Stdout) {
		return "", 0, fmt.Errorf("tarball 不像 rootfs：缺少 /bin 或 /etc/os-release")
	}
	sha, err := sha256OfFile(abs)
	if err != nil {
		return "", 0, err
	}
	return sha, st.Size(), nil
}

func looksLikeRootfs(listing string) bool {
	up := strings.Split(listing, "\n")
	binRoot := false
	osRelease := false
	for _, e := range up {
		e = strings.TrimPrefix(strings.TrimSpace(e), "./")
		if strings.HasPrefix(e, "bin/") || e == "bin" || strings.HasPrefix(e, "/bin") {
			binRoot = true
		}
		if strings.Contains(e, "etc/os-release") {
			osRelease = true
		}
	}
	return binRoot || osRelease
}

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

	// 校验 tarball
	sha, size, err := ValidateRootfsTarball(params.SourcePath)
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
	ex := utils.ExecCommandLongRunning("tar", "-xzf", params.SourcePath, "-C", rootfs, "--strip-components=0")
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
		RootfsSizeBytes:   size,
		CloneVisible:      true,
		OwnerUsername:     params.OwnerUsername,
		PostCreateCommand: params.PostCreateCommand,
		SHA256:            sha,
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
