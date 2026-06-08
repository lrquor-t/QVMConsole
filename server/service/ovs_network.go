package service

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"kvm_console/config"
	"kvm_console/model"
	"kvm_console/utils"
)

const (
	ovsConfigDir     = "/etc/kvm-console/ovs"
	ovsStateDir      = "/var/lib/kvm-console/ovs"
	ovsDNSMasqUnit   = "kvm-console-ovs-dnsmasq.service"
	ovsDHCPHostsFile = "/etc/kvm-console/ovs/dhcp-hosts"
	ovsDNSMasqConf   = "/etc/kvm-console/ovs/dnsmasq.conf"
	ovsBridgePrep    = "/etc/kvm-console/ovs/prepare-bridge.sh"
	ovsLeasesFile    = "/var/lib/kvm-console/ovs/dnsmasq.leases"
)

type OVSStaticHost struct {
	VMName string
	MAC    string
	IP     string
}

type OVSDHCPLease struct {
	ExpiryTime string
	ExpiryUnix int64
	MAC        string
	IP         string
	Hostname   string
	ClientID   string
}

func networkBackend() string {
	if config.GlobalConfig == nil || strings.TrimSpace(config.GlobalConfig.NetworkBackend) == "" {
		return "ovs"
	}
	return strings.ToLower(strings.TrimSpace(config.GlobalConfig.NetworkBackend))
}

func useOVSNetwork() bool {
	return networkBackend() == "ovs"
}

func ovsBridgeName() string {
	if config.GlobalConfig != nil && strings.TrimSpace(config.GlobalConfig.OVSBridge) != "" {
		return strings.TrimSpace(config.GlobalConfig.OVSBridge)
	}
	return "br-ovs"
}

func ovsSubnetPrefix() string {
	if config.GlobalConfig != nil && strings.TrimSpace(config.GlobalConfig.SubnetPrefix) != "" {
		return strings.TrimSpace(config.GlobalConfig.SubnetPrefix)
	}
	return "192.168.122"
}

func ovsSubnetCIDR() string {
	return ovsSubnetPrefix() + ".0/24"
}

func ovsGatewayIP() string {
	return ovsSubnetPrefix() + ".1"
}

func ovsDHCPStart() string {
	if config.GlobalConfig != nil && strings.TrimSpace(config.GlobalConfig.OVSDHCPStart) != "" {
		return strings.TrimSpace(config.GlobalConfig.OVSDHCPStart)
	}
	return ovsSubnetPrefix() + ".2"
}

func ovsDHCPEnd() string {
	if config.GlobalConfig != nil && strings.TrimSpace(config.GlobalConfig.OVSDHCPEnd) != "" {
		return strings.TrimSpace(config.GlobalConfig.OVSDHCPEnd)
	}
	return ovsSubnetPrefix() + ".254"
}

func ovsUplink() string {
	if config.GlobalConfig != nil && strings.TrimSpace(config.GlobalConfig.OVSUplink) != "" {
		return strings.TrimSpace(config.GlobalConfig.OVSUplink)
	}
	result := utils.ExecShell("ip route show default 2>/dev/null | awk '{print $5}' | head -n1")
	return strings.TrimSpace(result.Stdout)
}

func BuildOVSVirtInstallNetworkArg(model string) string {
	return BuildOVSVirtInstallNetworkArgForBridge(model, ovsBridgeName())
}

func BuildOVSInterfaceXML(mac, model string) string {
	return BuildOVSInterfaceXMLForBridge(mac, model, ovsBridgeName())
}

func BuildOVSInterfaceXMLWithVLAN(mac, model string, vlanID int) string {
	xmlText := BuildOVSInterfaceXML(mac, model)
	if vlanID <= 0 {
		return xmlText
	}
	updated, changed := setFirstOVSInterfaceVLANTag(xmlText, vlanID)
	if !changed {
		return xmlText
	}
	return updated
}

func EnsureOVSNetworkReady() error {
	if !useOVSNetwork() {
		return nil
	}
	if result := utils.ExecCommand("bash", "-c", "command -v ovs-vsctl"); result.Error != nil {
		return fmt.Errorf("OVS 未安装，请先安装 openvswitch-switch")
	}
	if result := utils.ExecCommand("bash", "-c", "command -v dnsmasq"); result.Error != nil {
		return fmt.Errorf("dnsmasq 不可用，请确认已安装 dnsmasq-base")
	}

	bridge := ovsBridgeName()
	uplink := ovsUplink()
	if uplink == "" {
		return fmt.Errorf("无法检测 OVS NAT 出口网卡，请配置 KVM_OVS_UPLINK")
	}
	if err := os.MkdirAll(ovsConfigDir, 0755); err != nil {
		return fmt.Errorf("创建 OVS 配置目录失败: %w", err)
	}
	if err := os.MkdirAll(ovsStateDir, 0755); err != nil {
		return fmt.Errorf("创建 OVS 状态目录失败: %w", err)
	}
	if _, err := os.Stat(ovsDHCPHostsFile); os.IsNotExist(err) {
		if err := os.WriteFile(ovsDHCPHostsFile, []byte(""), 0644); err != nil {
			return fmt.Errorf("创建 OVS DHCP 静态绑定文件失败: %w", err)
		}
	}

	ensureSystemdUnitEnabled("openvswitch-switch")
	if !isSystemdUnitActive("openvswitch-switch") {
		utils.ExecCommand("systemctl", "start", "openvswitch-switch")
	}
	disableLibvirtDefaultNetworkIfNeeded()
	if result := utils.ExecCommand("ovs-vsctl", "--may-exist", "add-br", bridge); result.Error != nil {
		return fmt.Errorf("创建 OVS 网桥失败: %s", result.Stderr)
	}
	if result := utils.ExecCommand("ip", "link", "set", bridge, "up"); result.Error != nil {
		return fmt.Errorf("启动 OVS 网桥失败: %s", result.Stderr)
	}
	addrResult := utils.ExecShell(fmt.Sprintf("ip -4 addr show dev %s | grep -q '%s/24'", utils.ShellSingleQuote(bridge), ovsGatewayIP()))
	if addrResult.Error != nil {
		utils.ExecCommand("ip", "addr", "flush", "dev", bridge)
		if result := utils.ExecCommand("ip", "addr", "add", ovsGatewayIP()+"/24", "dev", bridge); result.Error != nil {
			return fmt.Errorf("设置 OVS 网关地址失败: %s", result.Stderr)
		}
	}
	if err := ensureLocalDNSMasqInputRules(bridge); err != nil {
		return err
	}

	dnsConfigChanged, err := writeOVSDNSMasqConfig()
	if err != nil {
		return err
	}
	prepareScriptChanged, err := writeOVSBridgePrepareScript()
	if err != nil {
		return err
	}
	unitChanged, err := writeOVSDNSMasqUnit()
	if err != nil {
		return err
	}
	if unitChanged {
		utils.ExecCommand("systemctl", "daemon-reload")
	}
	ensureSystemdUnitEnabled(ovsDNSMasqUnit)
	if isSystemdUnitFailed(ovsDNSMasqUnit) || !isSystemdUnitActive(ovsDNSMasqUnit) {
		if result := utils.ExecCommand("systemctl", "start", ovsDNSMasqUnit); result.Error != nil {
			return fmt.Errorf("启动 OVS DHCP 服务失败: %s", result.Stderr)
		}
	} else if dnsConfigChanged {
		ReloadOVSDNSMasq()
	} else if prepareScriptChanged || unitChanged {
		logNetworkRuntimeChange("OVS DHCP 服务配置已更新，将在下次服务重启时使用新的预启动脚本")
	}

	_, _ = writeFileIfChanged("/etc/sysctl.d/99-kvm-console-ovs.conf", []byte("net.ipv4.ip_forward=1\n"), 0644)
	if result := utils.ExecCommand("sysctl", "-n", "net.ipv4.ip_forward"); strings.TrimSpace(result.Stdout) != "1" {
		utils.ExecCommand("sysctl", "-w", "net.ipv4.ip_forward=1")
	}
	subnet := ovsSubnetCIDR()
	cleanupStaleManagedNATRules(subnet, bridge, uplink)
	if err := ensureIPTablesRule(
		fmt.Sprintf("iptables -t nat -C POSTROUTING -s %s -o %s -j MASQUERADE", utils.ShellSingleQuote(subnet), utils.ShellSingleQuote(uplink)),
		fmt.Sprintf("iptables -t nat -A POSTROUTING -s %s -o %s -j MASQUERADE", utils.ShellSingleQuote(subnet), utils.ShellSingleQuote(uplink)),
		"配置 OVS NAT 规则",
	); err != nil {
		return fmt.Errorf("配置 OVS NAT 规则失败: %w", err)
	}
	if err := ensureIPTablesRule(
		fmt.Sprintf("iptables -C FORWARD -i %s -o %s -j ACCEPT", utils.ShellSingleQuote(bridge), utils.ShellSingleQuote(uplink)),
		fmt.Sprintf("iptables -A FORWARD -i %s -o %s -j ACCEPT", utils.ShellSingleQuote(bridge), utils.ShellSingleQuote(uplink)),
		"配置 OVS 出站转发规则",
	); err != nil {
		return fmt.Errorf("配置 OVS 出站转发规则失败: %w", err)
	}
	if err := ensureIPTablesRule(
		fmt.Sprintf("iptables -C FORWARD -i %s -o %s -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT", utils.ShellSingleQuote(uplink), utils.ShellSingleQuote(bridge)),
		fmt.Sprintf("iptables -A FORWARD -i %s -o %s -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT", utils.ShellSingleQuote(uplink), utils.ShellSingleQuote(bridge)),
		"配置 OVS 回程转发规则",
	); err != nil {
		return fmt.Errorf("配置 OVS 回程转发规则失败: %w", err)
	}
	return nil
}

func disableLibvirtDefaultNetworkIfNeeded() {
	if result := utils.ExecShell("virsh net-info default 2>/dev/null | awk '/^Active:/ {print $2}'"); strings.TrimSpace(result.Stdout) == "yes" {
		utils.ExecCommand("virsh", "net-destroy", "default")
	}
	if result := utils.ExecShell("virsh net-info default 2>/dev/null | awk '/^Autostart:/ {print $2}'"); strings.TrimSpace(result.Stdout) == "yes" {
		utils.ExecCommand("virsh", "net-autostart", "default", "--disable")
	}
}

func ensureSystemdUnitEnabled(unit string) {
	if strings.TrimSpace(unit) == "" {
		return
	}
	if result := utils.ExecCommand("systemctl", "is-enabled", "--quiet", unit); result.Error != nil {
		utils.ExecCommand("systemctl", "enable", unit)
	}
}

func isSystemdUnitActive(unit string) bool {
	if strings.TrimSpace(unit) == "" {
		return false
	}
	return utils.ExecCommand("systemctl", "is-active", "--quiet", unit).Error == nil
}

func isSystemdUnitFailed(unit string) bool {
	if strings.TrimSpace(unit) == "" {
		return false
	}
	return utils.ExecCommand("systemctl", "is-failed", "--quiet", unit).Error == nil
}

func logNetworkRuntimeChange(message string) {
	if strings.TrimSpace(message) != "" {
		fmt.Printf("[网络] %s\n", message)
	}
}

func ensureIPTablesRule(checkCmd, addCmd, label string) error {
	if result := utils.ExecShell(checkCmd); result.Error == nil {
		return nil
	}
	if result := utils.ExecShell(addCmd); result.Error != nil {
		return fmt.Errorf("%s失败: %s", label, result.Stderr)
	}
	return nil
}

func cleanupStaleManagedNATRules(cidr, internalIF, currentUplink string) {
	cidr = strings.TrimSpace(cidr)
	internalIF = strings.TrimSpace(internalIF)
	currentUplink = strings.TrimSpace(currentUplink)
	if cidr == "" || internalIF == "" || currentUplink == "" {
		return
	}
	script := fmt.Sprintf(`CIDR=%s
INTERNAL_IF=%s
CURRENT_UPLINK=%s

delete_rule() {
  table="$1"
  rule="$2"
  delete_rule="${rule/-A /-D }"
  if [ "$table" = "nat" ]; then
    iptables -t nat $delete_rule 2>/dev/null || true
  else
    iptables $delete_rule 2>/dev/null || true
  fi
}

while IFS= read -r rule; do
  case "$rule" in
    *"-s $CIDR "*"-j MASQUERADE"*)
      set -- $rule
      out_if=""
      while [ "$#" -gt 0 ]; do
        if [ "$1" = "-o" ] && [ "$#" -ge 2 ]; then
          out_if="$2"
          break
        fi
        shift
      done
      if [ -n "$out_if" ] && [ "$out_if" != "$CURRENT_UPLINK" ]; then
        delete_rule nat "$rule"
      fi
      ;;
  esac
done <<EOF
$(iptables -t nat -S POSTROUTING 2>/dev/null)
EOF

while IFS= read -r rule; do
  case "$rule" in
    "-A FORWARD -i $INTERNAL_IF "*"-j ACCEPT")
      set -- $rule
      out_if=""
      while [ "$#" -gt 0 ]; do
        if [ "$1" = "-o" ] && [ "$#" -ge 2 ]; then
          out_if="$2"
          break
        fi
        shift
      done
      if [ -n "$out_if" ] && [ "$out_if" != "$CURRENT_UPLINK" ]; then
        delete_rule filter "$rule"
      fi
      ;;
    "-A FORWARD -i "*"-o $INTERNAL_IF "*"--ctstate RELATED,ESTABLISHED"*)
      set -- $rule
      in_if=""
      while [ "$#" -gt 0 ]; do
        if [ "$1" = "-i" ] && [ "$#" -ge 2 ]; then
          in_if="$2"
          break
        fi
        shift
      done
      if [ -n "$in_if" ] && [ "$in_if" != "$CURRENT_UPLINK" ]; then
        delete_rule filter "$rule"
      fi
      ;;
  esac
done <<EOF
$(iptables -S FORWARD 2>/dev/null)
EOF
`, utils.ShellSingleQuote(cidr), utils.ShellSingleQuote(internalIF), utils.ShellSingleQuote(currentUplink))
	utils.ExecCommand("bash", "-c", script)
}

func ensureLocalDNSMasqInputRules(iface string) error {
	iface = strings.TrimSpace(iface)
	if iface == "" {
		return nil
	}
	rules := []struct {
		proto string
		port  string
		label string
	}{
		{proto: "udp", port: "67", label: "DHCP"},
		{proto: "udp", port: "53", label: "DNS UDP"},
		{proto: "tcp", port: "53", label: "DNS TCP"},
	}
	quotedIface := utils.ShellSingleQuote(iface)
	for _, rule := range rules {
		if err := ensureIPTablesRule(
			fmt.Sprintf("iptables -C INPUT -i %s -p %s --dport %s -j ACCEPT", quotedIface, rule.proto, rule.port),
			fmt.Sprintf("iptables -I INPUT 1 -i %s -p %s --dport %s -j ACCEPT", quotedIface, rule.proto, rule.port),
			fmt.Sprintf("配置 %s %s 入站规则", iface, rule.label),
		); err != nil {
			return err
		}
	}
	return nil
}

func removeLocalDNSMasqInputRules(iface string) {
	iface = strings.TrimSpace(iface)
	if iface == "" {
		return
	}
	quotedIface := utils.ShellSingleQuote(iface)
	for _, rule := range []struct {
		proto string
		port  string
	}{
		{proto: "udp", port: "67"},
		{proto: "udp", port: "53"},
		{proto: "tcp", port: "53"},
	} {
		utils.ExecShell(fmt.Sprintf("while iptables -D INPUT -i %s -p %s --dport %s -j ACCEPT 2>/dev/null; do :; done",
			quotedIface, rule.proto, rule.port))
	}
}

func writeFileIfChanged(path string, content []byte, perm os.FileMode) (bool, error) {
	current, err := os.ReadFile(path)
	if err == nil && bytes.Equal(current, content) {
		info, statErr := os.Stat(path)
		if statErr == nil && info.Mode().Perm() != perm {
			if chmodErr := os.Chmod(path, perm); chmodErr != nil {
				return false, chmodErr
			}
			return true, nil
		}
		return false, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if err := os.WriteFile(path, content, perm); err != nil {
		return false, err
	}
	return true, nil
}

func writeOVSDNSMasqConfig() (bool, error) {
	content := fmt.Sprintf(`interface=%s
bind-interfaces
except-interface=lo
dhcp-authoritative
dhcp-range=%s,%s,255.255.255.0,12h
dhcp-option=option:router,%s
dhcp-option=option:dns-server,223.5.5.5,223.6.6.6
dhcp-hostsfile=%s
dhcp-leasefile=%s
pid-file=/run/kvm-console-ovs-dnsmasq.pid
log-dhcp
`, ovsBridgeName(), ovsDHCPStart(), ovsDHCPEnd(), ovsGatewayIP(), ovsDHCPHostsFile, ovsLeasesFile)
	changed, err := writeFileIfChanged(ovsDNSMasqConf, []byte(content), 0644)
	if err != nil {
		return false, fmt.Errorf("写入 OVS DHCP 配置失败: %w", err)
	}
	return changed, nil
}

func writeOVSBridgePrepareScript() (bool, error) {
	content := fmt.Sprintf(`#!/bin/bash
set -e
BRIDGE=%s
GATEWAY=%s

ovs-vsctl --may-exist add-br "$BRIDGE"
ip link set "$BRIDGE" up
if ! ip -4 addr show dev "$BRIDGE" | grep -q "$GATEWAY"; then
  ip addr flush dev "$BRIDGE"
  ip addr add "$GATEWAY" dev "$BRIDGE"
fi
for rule in "udp 67" "udp 53" "tcp 53"; do
  proto="${rule%% *}"
  port="${rule##* }"
  iptables -C INPUT -i "$BRIDGE" -p "$proto" --dport "$port" -j ACCEPT 2>/dev/null || \
    iptables -I INPUT 1 -i "$BRIDGE" -p "$proto" --dport "$port" -j ACCEPT
done
`, utils.ShellSingleQuote(ovsBridgeName()), utils.ShellSingleQuote(ovsGatewayIP()+"/24"))
	changed, err := writeFileIfChanged(ovsBridgePrep, []byte(content), 0755)
	if err != nil {
		return false, fmt.Errorf("写入 OVS 网桥预启动脚本失败: %w", err)
	}
	return changed, nil
}

func writeOVSDNSMasqUnit() (bool, error) {
	content := `[Unit]
Description=KVM Console OVS DHCP/DNS service
After=network-online.target openvswitch-switch.service
Wants=network-online.target openvswitch-switch.service

[Service]
Type=forking
PIDFile=/run/kvm-console-ovs-dnsmasq.pid
ExecStartPre=/bin/bash /etc/kvm-console/ovs/prepare-bridge.sh
ExecStart=/usr/sbin/dnsmasq --conf-file=/etc/kvm-console/ovs/dnsmasq.conf
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure

[Install]
WantedBy=multi-user.target
`
	path := "/etc/systemd/system/" + ovsDNSMasqUnit
	changed, err := writeFileIfChanged(path, []byte(content), 0644)
	if err != nil {
		return false, fmt.Errorf("写入 OVS DHCP systemd 服务失败: %w", err)
	}
	return changed, nil
}

func ReloadOVSDNSMasq() {
	result := utils.ExecCommand("systemctl", "reload", ovsDNSMasqUnit)
	if result.Error != nil {
		utils.ExecCommand("systemctl", "restart", ovsDNSMasqUnit)
	}
}

func ListOVSStaticHosts() ([]OVSStaticHost, error) {
	data, err := os.ReadFile(ovsDHCPHostsFile)
	if err != nil {
		if os.IsNotExist(err) {
			return []OVSStaticHost{}, nil
		}
		return nil, err
	}
	return ParseOVSStaticHostsText(string(data)), nil
}

func ParseOVSStaticHostsText(text string) []OVSStaticHost {
	var hosts []OVSStaticHost
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) < 2 {
			continue
		}
		host := OVSStaticHost{MAC: strings.ToLower(strings.TrimSpace(parts[0]))}
		for _, part := range parts[1:] {
			part = strings.TrimSpace(part)
			if net.ParseIP(part) != nil {
				host.IP = part
				continue
			}
			if !strings.HasPrefix(part, "set:") && host.VMName == "" {
				host.VMName = part
			}
		}
		if host.MAC != "" && host.IP != "" {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

func writeStaticHostsFile(path string, hosts []OVSStaticHost) error {
	sort.Slice(hosts, func(i, j int) bool {
		if hosts[i].VMName == hosts[j].VMName {
			return hosts[i].MAC < hosts[j].MAC
		}
		return hosts[i].VMName < hosts[j].VMName
	})
	var lines []string
	for _, host := range hosts {
		host.MAC = strings.ToLower(strings.TrimSpace(host.MAC))
		host.IP = strings.TrimSpace(host.IP)
		host.VMName = strings.TrimSpace(host.VMName)
		if host.MAC == "" || host.IP == "" {
			continue
		}
		if host.VMName == "" {
			lines = append(lines, fmt.Sprintf("%s,%s", host.MAC, host.IP))
		} else {
			lines = append(lines, fmt.Sprintf("%s,%s,%s", host.MAC, host.IP, host.VMName))
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644)
}

func writeOVSStaticHosts(hosts []OVSStaticHost) error {
	return writeStaticHostsFile(ovsDHCPHostsFile, hosts)
}

func ListVPCStaticHosts(switchID uint) ([]OVSStaticHost, error) {
	data, err := os.ReadFile(vpcDHCPHostsPath(switchID))
	if err != nil {
		if os.IsNotExist(err) {
			return []OVSStaticHost{}, nil
		}
		return nil, err
	}
	return ParseOVSStaticHostsText(string(data)), nil
}

func ListAllVPCStaticHosts() ([]OVSStaticHost, error) {
	files, err := filepath.Glob(filepath.Join(vpcConfigDir, "dhcp-hosts-*"))
	if err != nil {
		return nil, err
	}
	var hosts []OVSStaticHost
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		hosts = append(hosts, ParseOVSStaticHostsText(string(data))...)
	}
	return hosts, nil
}

func writeVPCStaticHosts(switchID uint, hosts []OVSStaticHost) error {
	return writeStaticHostsFile(vpcDHCPHostsPath(switchID), hosts)
}

func UpsertOVSStaticHost(vmName, mac, ipAddr string) error {
	if err := EnsureOVSNetworkReady(); err != nil {
		return err
	}
	mac = strings.ToLower(strings.TrimSpace(mac))
	vmName = strings.TrimSpace(vmName)
	ipAddr = strings.TrimSpace(ipAddr)
	hosts, err := ListOVSStaticHosts()
	if err != nil {
		return err
	}
	next, err := buildOVSStaticHostsForUpsert(hosts, OVSStaticHost{VMName: vmName, MAC: mac, IP: ipAddr})
	if err != nil {
		return err
	}
	if err := writeOVSStaticHosts(next); err != nil {
		return fmt.Errorf("写入 OVS 静态 IP 绑定失败: %w", err)
	}
	CleanOVSDHCPLease(mac, ipAddr)
	ReloadOVSDNSMasq()
	return nil
}

func buildOVSStaticHostsForUpsert(hosts []OVSStaticHost, target OVSStaticHost) ([]OVSStaticHost, error) {
	target.MAC = strings.ToLower(strings.TrimSpace(target.MAC))
	target.IP = strings.TrimSpace(target.IP)
	target.VMName = strings.TrimSpace(target.VMName)
	if target.MAC == "" {
		return nil, fmt.Errorf("MAC 地址不能为空")
	}
	if target.IP == "" {
		return nil, fmt.Errorf("IP 地址不能为空")
	}

	var next []OVSStaticHost
	for _, host := range hosts {
		host.MAC = strings.ToLower(strings.TrimSpace(host.MAC))
		host.IP = strings.TrimSpace(host.IP)
		host.VMName = strings.TrimSpace(host.VMName)

		sameVM := target.VMName != "" && host.VMName == target.VMName
		sameMAC := strings.EqualFold(host.MAC, target.MAC)
		sameIP := host.IP == target.IP

		if sameVM {
			// 同一 VM 允许在用户修改 MAC 后保留原 IP，并替换为当前 MAC。
			continue
		}
		if sameMAC {
			return nil, fmt.Errorf("MAC 地址 %s 已绑定到虚拟机 %s，不能重复绑定", target.MAC, host.VMName)
		}
		if sameIP {
			return nil, fmt.Errorf("IP 地址 %s 已绑定到虚拟机 %s（MAC: %s），不能重复绑定", target.IP, host.VMName, host.MAC)
		}
		next = append(next, host)
	}
	next = append(next, target)
	return next, nil
}

func RemoveOVSStaticHost(vmName, mac string) (string, error) {
	hosts, err := ListOVSStaticHosts()
	if err != nil {
		return "", err
	}
	var removedIP string
	var next []OVSStaticHost
	for _, host := range hosts {
		match := strings.EqualFold(host.MAC, mac) || (vmName != "" && host.VMName == vmName)
		if match {
			removedIP = host.IP
			continue
		}
		next = append(next, host)
	}
	if removedIP == "" {
		return "", fmt.Errorf("该虚拟机没有静态绑定")
	}
	if err := writeOVSStaticHosts(next); err != nil {
		return "", fmt.Errorf("删除 OVS 静态 IP 绑定失败: %w", err)
	}
	ReloadOVSDNSMasq()
	return removedIP, nil
}

func ListOVSDHCPLeases() ([]OVSDHCPLease, error) {
	data, err := os.ReadFile(ovsLeasesFile)
	if err != nil {
		if os.IsNotExist(err) {
			return []OVSDHCPLease{}, nil
		}
		return nil, err
	}
	return ParseOVSDHCPLeasesText(string(data)), nil
}

func ListVPCDHCPLeases() ([]OVSDHCPLease, error) {
	files, err := filepath.Glob(filepath.Join(vpcConfigDir, "leases-*"))
	if err != nil {
		return nil, err
	}
	var leases []OVSDHCPLease
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		leases = append(leases, ParseOVSDHCPLeasesText(string(data))...)
	}
	return leases, nil
}

func ListVPCDHCPLeasesForSwitch(switchID uint) ([]OVSDHCPLease, error) {
	data, err := os.ReadFile(filepath.Join(vpcConfigDir, fmt.Sprintf("leases-%d", switchID)))
	if err != nil {
		if os.IsNotExist(err) {
			return []OVSDHCPLease{}, nil
		}
		return nil, err
	}
	return ParseOVSDHCPLeasesText(string(data)), nil
}

func ParseOVSDHCPLeasesText(text string) []OVSDHCPLease {
	var leases []OVSDHCPLease
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		lease := OVSDHCPLease{
			ExpiryTime: formatOVSLeaseExpiry(fields[0]),
			ExpiryUnix: parseOVSLeaseExpiryUnix(fields[0]),
			MAC:        strings.ToLower(fields[1]),
			IP:         fields[2],
		}
		if len(fields) >= 4 && fields[3] != "*" {
			lease.Hostname = fields[3]
		}
		if len(fields) >= 5 && fields[4] != "*" {
			lease.ClientID = fields[4]
		}
		leases = append(leases, lease)
	}
	return leases
}

func parseOVSLeaseExpiryUnix(raw string) int64 {
	sec, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0
	}
	return sec
}

func formatOVSLeaseExpiry(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	sec, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return raw
	}
	if sec == 0 {
		return "永久"
	}
	return time.Unix(sec, 0).Local().Format("2006-01-02 15:04:05")
}

func CleanOVSDHCPLease(mac, ipAddr string) {
	data, err := os.ReadFile(ovsLeasesFile)
	if err != nil {
		return
	}
	mac = strings.ToLower(strings.TrimSpace(mac))
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 {
			if (mac != "" && strings.EqualFold(fields[1], mac)) || (ipAddr != "" && fields[2] == ipAddr) {
				continue
			}
		}
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	_ = os.WriteFile(ovsLeasesFile, []byte(strings.Join(lines, "\n")+"\n"), 0644)
}

func CleanVPCDHCPLease(switchID uint, mac, ipAddr string) {
	path := filepath.Join(vpcConfigDir, fmt.Sprintf("leases-%d", switchID))
	cleanVPCDHCPLeaseFile(path, mac, ipAddr)
}

func CleanAllVPCDHCPLeases(mac, ipAddr string) {
	entries, err := os.ReadDir(vpcConfigDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "leases-") {
			continue
		}
		cleanVPCDHCPLeaseFile(filepath.Join(vpcConfigDir, entry.Name()), mac, ipAddr)
	}
}

func cleanVPCDHCPLeaseFile(path, mac, ipAddr string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	mac = strings.ToLower(strings.TrimSpace(mac))
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 {
			if (mac != "" && strings.EqualFold(fields[1], mac)) || (ipAddr != "" && fields[2] == ipAddr) {
				continue
			}
		}
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	_ = os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644)
}

func GetOVSStaticIPByMAC(mac string) string {
	hosts, err := ListOVSStaticHosts()
	if err != nil {
		return ""
	}
	for _, host := range hosts {
		if strings.EqualFold(host.MAC, mac) {
			return host.IP
		}
	}
	return ""
}

func GetOVSStaticHostByVMName(vmName string) (OVSStaticHost, bool) {
	hosts, err := ListOVSStaticHosts()
	if err != nil {
		return OVSStaticHost{}, false
	}
	vmName = strings.TrimSpace(vmName)
	for _, host := range hosts {
		if strings.TrimSpace(host.VMName) == vmName {
			return host, true
		}
	}
	return OVSStaticHost{}, false
}

func newerOVSDHCPLease(current, candidate OVSDHCPLease) OVSDHCPLease {
	if current.IP == "" {
		return candidate
	}
	currentExpiry := current.ExpiryUnix
	candidateExpiry := candidate.ExpiryUnix
	if currentExpiry == 0 {
		currentExpiry = 1<<63 - 1
	}
	if candidateExpiry == 0 {
		candidateExpiry = 1<<63 - 1
	}
	if candidateExpiry >= currentExpiry {
		return candidate
	}
	return current
}

func GetOVSLeaseIPByMAC(mac string) string {
	leases, err := ListOVSDHCPLeases()
	if err != nil {
		leases = []OVSDHCPLease{}
	}
	if vpcLeases, vpcErr := ListVPCDHCPLeases(); vpcErr == nil {
		leases = append(leases, vpcLeases...)
	}
	var latest OVSDHCPLease
	for _, lease := range leases {
		if strings.EqualFold(lease.MAC, mac) {
			latest = newerOVSDHCPLease(latest, lease)
		}
	}
	return latest.IP
}

// GetVPCLeaseIPForVMByMAC 按 MAC 地址查找对应 VPC 交换机的 DHCP 租约 IP（多网口场景）
func GetVPCLeaseIPForVMByMAC(vmName, mac string) string {
	vmName = strings.TrimSpace(vmName)
	mac = strings.ToLower(strings.TrimSpace(mac))
	if vmName == "" || mac == "" || model.DB == nil {
		return ""
	}
	// 查找该 VM 的所有 VPC 绑定
	var bindings []model.VPCVMBinding
	if err := model.DB.Where("vm_name = ?", vmName).Order("interface_order ASC").Find(&bindings).Error; err != nil || len(bindings) == 0 {
		return ""
	}
	// 遍历每个绑定，检查对应交换机的租约和静态绑定
	for _, binding := range bindings {
		// 先查静态绑定
		if ip := GetVPCStaticIPByMAC(binding.SwitchID, mac); ip != "" {
			return ip
		}
		// 再查 DHCP 租约
		leasesPath := filepath.Join(vpcConfigDir, fmt.Sprintf("leases-%d", binding.SwitchID))
		data, err := os.ReadFile(leasesPath)
		if err != nil {
			continue
		}
		leases := ParseOVSDHCPLeasesText(string(data))
		var latest OVSDHCPLease
		for _, lease := range leases {
			if strings.EqualFold(lease.MAC, mac) {
				latest = newerOVSDHCPLease(latest, lease)
			}
		}
		if latest.IP != "" {
			return latest.IP
		}
	}
	return ""
}

func GetVPCLeaseIPForVM(vmName string) string {
	vmName = strings.TrimSpace(vmName)
	if vmName == "" || model.DB == nil {
		return ""
	}
	var binding model.VPCVMBinding
	if err := model.DB.Where("vm_name = ?", vmName).First(&binding).Error; err != nil {
		return ""
	}
	mac := getFirstVMMAC(vmName)
	if mac == "" {
		return ""
	}
	if ip := GetVPCStaticIPByMAC(binding.SwitchID, mac); ip != "" {
		return ip
	}
	data, err := os.ReadFile(filepath.Join(vpcConfigDir, fmt.Sprintf("leases-%d", binding.SwitchID)))
	if err != nil {
		return ""
	}
	leases := ParseOVSDHCPLeasesText(string(data))
	var latest OVSDHCPLease
	for _, lease := range leases {
		if strings.EqualFold(lease.MAC, mac) {
			latest = newerOVSDHCPLease(latest, lease)
		}
	}
	return latest.IP
}

func getFirstVMMAC(vmName string) string {
	macResult := utils.ExecShell(fmt.Sprintf(
		"virsh domiflist %s 2>/dev/null | grep -oP '([0-9a-f]{2}:){5}[0-9a-f]{2}' | head -1",
		utils.ShellSingleQuote(vmName)))
	if macResult.Error != nil {
		return ""
	}
	return strings.TrimSpace(macResult.Stdout)
}

func normalizeIPForOVS(ipAddr string) string {
	ipAddr = strings.TrimSpace(ipAddr)
	if matched, _ := regexp.MatchString(`^\d+$`, ipAddr); matched {
		return ovsSubnetPrefix() + "." + ipAddr
	}
	return ipAddr
}
