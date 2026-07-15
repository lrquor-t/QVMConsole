package lxc

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	if err := writeDHCPProfiles(rootfs, name, orders, detectDistroFamily(rootfs)); err != nil {
		logger.App.Warn("写容器内网卡 DHCP 配置失败", "name", name, "error", err)
	}
}

// provisionOneRootfsNIC 给单张网卡写 DHCP profile（运行中加卡用）。best-effort，不触发 NM：
// 运行中容器靠 NM 自动 reload（RHEL）或下次启动生效（netplan/ifupdown）；已停容器下次启动生效。
func provisionOneRootfsNIC(name string, order int) {
	data, err := os.ReadFile(filepath.Join(config.GlobalConfig.LXCLxcPath, name, "config"))
	if err != nil {
		return
	}
	rootfs := rootfsDirFromConfig(string(data))
	if rootfs == "" {
		return // overlay 等只读 backing：跳过
	}
	if err := writeDHCPProfiles(rootfs, name, []int{order}, detectDistroFamily(rootfs)); err != nil {
		logger.App.Warn("运行中加卡：写容器内 DHCP 配置失败", "name", name, "order", order, "error", err)
	}
}

// detectDistroFamily 按 rootfs 的 /etc/os-release（回退目录特征）判定网络配置族：
// "rhel"（ifcfg/NM）/ "netplan"（Ubuntu 等带 netplan）/ "networkd"（systemd-networkd，Debian 云/容器版常见）
// / "ifupdown"（Debian 老版 /etc/network/interfaces）。
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
	debLike := hasAny(id, "debian", "ubuntu", "kali", "linuxmint") || hasAny(like, "debian")
	if hasAny(id, "rhel", "centos", "rocky", "almalinux", "fedora", "ol", "virtuozzo") ||
		hasAny(like, "rhel", "centos", "fedora") {
		return "rhel"
	}
	if debLike {
		return pickDebianStack(rootfs)
	}
	// 回退：按目录特征
	if dirExists(filepath.Join(rootfs, "etc", "sysconfig", "network-scripts")) {
		return "rhel"
	}
	return pickDebianStack(rootfs)
}

// pickDebianStack 在 netplan / systemd-networkd / ifupdown 间按实际配置文件判定：
// netplan—/etc/netplan 有 .yaml；networkd—/etc/systemd/network 有 .network；否则 ifupdown。
// 不能只看目录是否存在：Debian 镜像常同时残留 /etc/network/interfaces 但实际跑 networkd。
func pickDebianStack(rootfs string) string {
	switch {
	case hasFileWithExt(filepath.Join(rootfs, "etc", "netplan"), ".yaml"):
		return "netplan"
	case hasFileWithExt(filepath.Join(rootfs, "etc", "systemd", "network"), ".network"):
		return "networkd"
	default:
		return "ifupdown"
	}
}

// hasFileWithExt 目录存在且含指定后缀文件。
func hasFileWithExt(dir, ext string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ext) {
			return true
		}
	}
	return false
}

// writeDHCPProfiles 给 rootfs 的 eth<orders> 写 DHCP 配置 + 固定 MAC（按 family 选格式）。
// MAC 固定写在 profile：多网卡下 LXC 不刷 hwaddr，由容器内网络管理器在 DHCP 前固定（Plan C）。
func writeDHCPProfiles(rootfs, name string, orders []int, family string) error {
	switch family {
	case "rhel":
		dir := filepath.Join(rootfs, "etc", "sysconfig", "network-scripts")
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		for _, o := range orders {
			mac := NICMAC(name, o)
			content := fmt.Sprintf("DEVICE=eth%d\nBOOTPROTO=dhcp\nONBOOT=yes\nTYPE=Ethernet\nNAME=eth%d\nMACADDR=%s\n", o, o, mac)
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
			mac := NICMAC(name, o)
			sb.WriteString(fmt.Sprintf("    eth%d:\n      dhcp4: true\n      macaddress: %s\n", o, mac))
		}
		if err := os.WriteFile(filepath.Join(dir, "90-lxc-nics.yaml"), []byte(sb.String()), 0600); err != nil {
			return err
		}
	case "networkd":
		dir := filepath.Join(rootfs, "etc", "systemd", "network")
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
		for _, o := range orders {
			p := filepath.Join(dir, fmt.Sprintf("eth%d.network", o))
			mac := NICMAC(name, o)
			if _, err := os.Stat(p); err == nil {
				if err := ensureNetworkdLinkMAC(p, mac); err != nil {
					return err
				}
				continue
			}
			content := fmt.Sprintf("[Match]\nName=eth%d\n\n[Link]\nMACAddress=%s\n\n[Network]\nDHCP=true\n\n[DHCPv4]\nUseDomains=true\nUseMTU=true\n\n[DHCP]\nClientIdentifier=mac\n", o, mac)
			if err := os.WriteFile(p, []byte(content), 0644); err != nil {
				return err
			}
		}
	default: // ifupdown
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
			mac := NICMAC(name, o)
			if strings.Contains(string(existing), fmt.Sprintf("iface eth%d inet", o)) {
				continue
			}
			sb.WriteString(fmt.Sprintf("auto eth%d\niface eth%d inet dhcp\n\tpre-up ip link set dev eth%d address %s\n", o, o, o, mac))
		}
		if err := os.WriteFile(f, []byte(sb.String()), 0644); err != nil {
			return err
		}
	}
	return nil
}

// ensureNetworkdLinkMAC 确保 .network 文件含 [Link] MACAddress=<mac>，保留其余内容。
func ensureNetworkdLinkMAC(path, mac string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	s := string(b)
	macRe := regexp.MustCompile(`(?m)^[ \t]*MACAddress[ \t]*=.*$`)
	switch {
	case macRe.MatchString(s):
		s = macRe.ReplaceAllString(s, "MACAddress="+mac)
	case strings.Contains(s, "[Link]"):
		s = strings.Replace(s, "[Link]", "[Link]\nMACAddress="+mac, 1)
	default:
		if s != "" && !strings.HasSuffix(s, "\n") {
			s += "\n"
		}
		s += "[Link]\nMACAddress=" + mac + "\n"
	}
	return os.WriteFile(path, []byte(s), 0644)
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
