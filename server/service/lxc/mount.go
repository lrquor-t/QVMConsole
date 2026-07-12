package lxc

import (
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
