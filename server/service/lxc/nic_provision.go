package lxc

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"kvm_console/config"
	"kvm_console/logger"
)

// provisionRootfsNICs 往容器 rootfs 写每张网卡的 DHCP 配置，使 lxc-start 建出的 eth0..eth<N>
// 在容器内被网络管理器自动 DHCP 拿到 IP。
//
// 动机：LXC 不像 VM 跑 cloud-init，guest 默认只有 eth0 的网络配置；附加网卡 eth1+ 在容器内
// 无 connection profile → 不发 DHCP → 拿不到 IP，表现即「第二张网卡不会自动启动」。VM 靠
// cloud-init + cloud 镜像默认对所有网卡 DHCP，故无此问题。
//
// 容器内网卡名恒为 eth<order>（veth 无 PCI 拓扑，systemd predictable naming 不生效；且
// lxc.net.<order>.name 已显式固定）。按 rootfs 发行版写对应格式（RHEL ifcfg / netplan / ifupdown）。
// 仅 dir:/zfs backing 可写（overlay 只读 lower 跳过）。best-effort：失败仅告警不阻断创建。
func provisionRootfsNICs(name string) {
	cfgPath := filepath.Join(config.GlobalConfig.LXCLxcPath, name, "config")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		logger.App.Warn("读容器 config 失败，跳过网卡 DHCP 配置置备", "name", name, "error", err)
		return
	}
	rootfs := rootfsDirFromConfig(string(data))
	if rootfs == "" {
		return // overlay 等只读 backing：不写
	}
	_, blocks := SplitNICBlocks(string(data))
	if len(blocks) == 0 {
		return
	}
	orders := make([]int, 0, len(blocks))
	for o := range blocks {
		orders = append(orders, o)
	}
	sort.Ints(orders)
	if err := writeDHCPProfiles(rootfs, orders, detectDistroFamily(rootfs)); err != nil {
		logger.App.Warn("写容器内网卡 DHCP 配置失败", "name", name, "error", err)
	}
}

// detectDistroFamily 按 rootfs 的 /etc/os-release（回退目录特征）判定网络配置族：
// "rhel"（ifcfg）/ "netplan"（Debian/Ubuntu 现代版）/ "ifupdown"（Debian 老版）。
func detectDistroFamily(rootfs string) string {
	id, like := "", ""
	if b, err := os.ReadFile(filepath.Join(rootfs, "etc", "os-release")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "ID=") {
				id = trimQuote(strings.TrimPrefix(line, "ID="))
			} else if strings.HasPrefix(line, "ID_LIKE=") {
				like = trimQuote(strings.TrimPrefix(line, "ID_LIKE="))
			}
		}
	}
	if hasAny(id, "rhel", "centos", "rocky", "almalinux", "fedora", "ol", "virtuozzo") ||
		hasAny(like, "rhel", "centos", "fedora") {
		return "rhel"
	}
	if hasAny(id, "debian", "ubuntu", "kali", "linuxmint") || hasAny(like, "debian") {
		if dirExists(filepath.Join(rootfs, "etc", "netplan")) {
			return "netplan"
		}
		return "ifupdown"
	}
	// 回退：按目录特征
	switch {
	case dirExists(filepath.Join(rootfs, "etc", "sysconfig", "network-scripts")):
		return "rhel"
	case dirExists(filepath.Join(rootfs, "etc", "netplan")):
		return "netplan"
	default:
		return "ifupdown"
	}
}

// writeDHCPProfiles 给 rootfs 的 eth<orders> 写 DHCP 配置（按 family 选格式）。
func writeDHCPProfiles(rootfs string, orders []int, family string) error {
	switch family {
	case "rhel":
		dir := filepath.Join(rootfs, "etc", "sysconfig", "network-scripts")
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		for _, o := range orders {
			content := fmt.Sprintf("DEVICE=eth%d\nBOOTPROTO=dhcp\nONBOOT=yes\nTYPE=Ethernet\nNAME=eth%d\n", o, o)
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("ifcfg-eth%d", o)), []byte(content), 0644); err != nil {
				return err
			}
		}
	case "netplan":
		dir := filepath.Join(rootfs, "etc", "netplan")
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		var sb strings.Builder
		sb.WriteString("network:\n  version: 2\n  renderer: networkd\n  ethernets:\n")
		for _, o := range orders {
			sb.WriteString(fmt.Sprintf("    eth%d:\n      dhcp4: true\n", o))
		}
		// 90- 前缀确保覆盖镜像自带（如 50-cloud-init.yaml）的同名配置
		if err := os.WriteFile(filepath.Join(dir, "90-lxc-nics.yaml"), []byte(sb.String()), 0600); err != nil {
			return err
		}
	default: // ifupdown（Debian 老版 /etc/network/interfaces）
		f := filepath.Join(rootfs, "etc", "network", "interfaces")
		if err := os.MkdirAll(filepath.Dir(f), 0755); err != nil {
			return err
		}
		existing, _ := os.ReadFile(f)
		var sb strings.Builder
		sb.Write(existing)
		if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
			sb.WriteString("\n")
		}
		for _, o := range orders {
			if strings.Contains(string(existing), fmt.Sprintf("iface eth%d inet", o)) {
				continue // 已有该网卡配置，幂等
			}
			sb.WriteString(fmt.Sprintf("auto eth%d\niface eth%d inet dhcp\n", o, o))
		}
		if err := os.WriteFile(f, []byte(sb.String()), 0644); err != nil {
			return err
		}
	}
	return nil
}

func trimQuote(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'`)
	return s
}

func hasAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if s == sub {
			return true
		}
		if strings.Contains(" "+s+" ", " "+sub+" ") { // ID_LIKE 可能空格分隔多值
			return true
		}
	}
	return false
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
