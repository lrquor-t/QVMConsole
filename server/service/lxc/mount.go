package lxc

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"kvm_console/config"
)

// LXCMount 一个宿主机目录到容器的 bind 挂载。
type LXCMount struct {
	HostPath string `json:"host_path"` // 宿主机绝对路径
	Target   string `json:"target"`    // 容器内挂载点（绝对路径，如 /mnt/data）
	ReadOnly bool   `json:"read_only"` // true => options 含 ro
}

// LXCMountListResult 列表接口返回：附容器状态，供前端判断是否提示重启。
type LXCMountListResult struct {
	Status          string   `json:"status"`           // 容器状态，如 RUNNING
	RestartRequired bool     `json:"restart_required"` // status == RUNNING 时为 true
	Mounts          []LXCMount `json:"mounts"`
}

// containerConfigPath 返回容器 config 文件路径。
func containerConfigPath(name string) string {
	return filepath.Join(config.GlobalConfig.LXCLxcPath, name, "config")
}

// renderMountEntry 生成一行 lxc.mount.entry 配置。
// target 去掉前导 /（LXC 的 dst 相对 rootfs，不带前导斜杠兼容性最好）。
func renderMountEntry(m LXCMount) string {
	target := strings.TrimPrefix(m.Target, "/")
	options := "bind,create=dir"
	if m.ReadOnly {
		options = "bind,create=dir,ro"
	}
	return "lxc.mount.entry = " + m.HostPath + " " + target + " none " + options + " 0 0"
}

// parseMountEntry 解析一行 config；仅识别我们格式（key=lxc.mount.entry 且 fstype=none 且 options 含 bind）。
// 非 bind 的 mount.entry（如 overlay）返回 false，由 rewrite 逻辑原样保留。
func parseMountEntry(line string) (LXCMount, bool) {
	t := strings.TrimSpace(line)
	const key = "lxc.mount.entry"
	if !strings.HasPrefix(t, key) {
		return LXCMount{}, false
	}
	rest := strings.TrimSpace(strings.TrimLeft(t[len(key):], " ="))
	fields := strings.Fields(rest)
	if len(fields) < 4 {
		return LXCMount{}, false
	}
	source, target, fstype, options := fields[0], fields[1], fields[2], fields[3]
	if fstype != "none" || !strings.Contains(options, "bind") {
		return LXCMount{}, false
	}
	m := LXCMount{HostPath: source, Target: target}
	if !strings.HasPrefix(target, "/") {
		m.Target = "/" + target
	}
	if strings.Contains(options, "ro") {
		m.ReadOnly = true
	}
	return m, true
}

// parseOurMounts 扫描整段 config 文本，返回所有我们管理的挂载。
func parseOurMounts(text string) []LXCMount {
	var out []LXCMount
	for _, line := range strings.Split(text, "\n") {
		if m, ok := parseMountEntry(line); ok {
			out = append(out, m)
		}
	}
	return out
}

// 路径黑名单：bind 进去会内核异常或被需求禁止。
var forbiddenHostPaths = map[string]bool{
	"/":     true,
	"/proc": true,
	"/sys":  true,
	"/dev":  true,
}

// containsUnsafeChar 拒绝空白与控制字符（会破坏 mount.entry 字段切分或注入配置行）。
func containsUnsafeChar(p string) bool {
	for _, r := range p {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r < 0x20 {
			return true
		}
	}
	return false
}

// validatePathShape 共享校验：绝对路径、标准化、无空白/控制字符。
func validatePathShape(p string) (string, error) {
	if !filepath.IsAbs(p) {
		return "", errors.New("路径必须是绝对路径")
	}
	if containsUnsafeChar(p) {
		return "", errors.New("路径不能包含空格或控制字符")
	}
	return filepath.Clean(p), nil
}

// validateHostPath 校验宿主机路径：绝对、无非法字符、不在黑名单、存在且为目录。
func validateHostPath(p string) error {
	clean, err := validatePathShape(p)
	if err != nil {
		return err
	}
	if forbiddenHostPaths[clean] {
		return errors.New("不允许挂载该系统目录")
	}
	info, err := os.Stat(clean)
	if err != nil {
		return errors.New("宿主机路径不存在")
	}
	if !info.IsDir() {
		return errors.New("宿主机路径不是目录")
	}
	return nil
}

// validateTarget 校验容器内挂载点：绝对、无非法字符、非根。
func validateTarget(p string) error {
	clean, err := validatePathShape(p)
	if err != nil {
		return err
	}
	if clean == "/" {
		return errors.New("容器挂载点不能是根目录")
	}
	return nil
}

// containerIsUnprivileged 检测 config 是否含 idmap（非特权容器）。
func containerIsUnprivileged(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") {
			continue
		}
		if strings.HasPrefix(t, "lxc.idmap") || strings.HasPrefix(t, "lxc.uidmap") || strings.HasPrefix(t, "lxc.gidmap") {
			return true
		}
	}
	return false
}

// rewriteMountEntries 在 config 文本上删除我们管理的 mount.entry 行，
// 追加新的完整集合后单次写回。非我们的 mount.entry 行（如 overlay）原样保留。
func rewriteMountEntries(cfgPath, text string, mounts []LXCMount) error {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if _, ok := parseMountEntry(line); ok {
			continue // 我们管理的行：丢弃，下方重新生成
		}
		out = append(out, line)
	}
	for _, m := range mounts {
		out = append(out, renderMountEntry(m))
	}
	content := strings.Join(out, "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return os.WriteFile(cfgPath, []byte(content), 0644)
}
