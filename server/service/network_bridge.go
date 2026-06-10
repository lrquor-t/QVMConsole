package service

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"kvm_console/logger"
	"kvm_console/model"
	"kvm_console/utils"
)

const (
	BridgeModeNAT    = "nat"
	BridgeModeDirect = "bridge"
	bridgeConfigDir  = "/etc/kvm-console/bridges"
)

type HostInterfaceInfo struct {
	Name          string   `json:"name"`
	MAC           string   `json:"mac"`
	State         string   `json:"state"`
	MTU           int      `json:"mtu"`
	Addresses     []string `json:"addresses"`
	DefaultRoute  bool     `json:"default_route"`
	OVSBridge     string   `json:"ovs_bridge"`
	OVSPort       bool     `json:"ovs_port"`
	Physical      bool     `json:"physical"`
	ManagedBridge string   `json:"managed_bridge"`
	Risk          string   `json:"risk,omitempty"`
}

type NetworkBridgeInfo struct {
	ID            uint   `json:"id"`
	Name          string `json:"name"`
	Mode          string `json:"mode"`
	UplinkIF      string `json:"uplink_if"`
	MigrateHostIP bool   `json:"migrate_host_ip"`
	IsDefault     bool   `json:"is_default"`
	Exists        bool   `json:"exists"`
	Active        bool   `json:"active"`
	SwitchCount   int64  `json:"switch_count"`
}

type NetworkBridgeRequest struct {
	Name          string `json:"name"`
	Mode          string `json:"mode"`
	UplinkIF      string `json:"uplink_if"`
	MigrateHostIP bool   `json:"migrate_host_ip"`
}

type ipAddrJSON struct {
	IfName    string `json:"ifname"`
	Address   string `json:"address"`
	OperState string `json:"operstate"`
	MTU       int    `json:"mtu"`
	AddrInfo  []struct {
		Local     string `json:"local"`
		PrefixLen int    `json:"prefixlen"`
		Family    string `json:"family"`
	} `json:"addr_info"`
}

type ipRouteJSON struct {
	Dst     string `json:"dst"`
	Dev     string `json:"dev"`
	Gateway string `json:"gateway"`
}

func ListHostPhysicalInterfaces() ([]HostInterfaceInfo, error) {
	items, err := readIPAddrJSON()
	if err != nil {
		return nil, err
	}
	defaults := readDefaultRouteIfaces()
	ovsPorts := readOVSPortBridgeMap()
	managed := readManagedBridgeByUplink()
	var result []HostInterfaceInfo
	for _, item := range items {
		if item.IfName == "" {
			continue
		}
		info := HostInterfaceInfo{
			Name:         item.IfName,
			MAC:          item.Address,
			State:        item.OperState,
			MTU:          item.MTU,
			DefaultRoute: defaults[item.IfName],
			Physical:     isPhysicalInterface(item.IfName),
		}
		for _, addr := range item.AddrInfo {
			if addr.Local != "" {
				info.Addresses = append(info.Addresses, fmt.Sprintf("%s/%d", addr.Local, addr.PrefixLen))
			}
		}
		if bridge := ovsPorts[item.IfName]; bridge != "" {
			info.OVSPort = true
			info.OVSBridge = bridge
		}
		info.ManagedBridge = managed[item.IfName]
		if info.DefaultRoute {
			info.Risk = "承载默认路由，桥接时可能短暂中断宿主机网络"
		}
		if info.Physical {
			result = append(result, info)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func ListNetworkBridges() ([]NetworkBridgeInfo, error) {
	bridges := defaultNetworkBridgeRecords()
	if model.DB != nil {
		var rows []model.NetworkBridge
		if err := model.DB.Order("is_default DESC, name ASC").Find(&rows).Error; err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, item := range bridges {
			seen[item.Name] = true
		}
		for _, row := range rows {
			if seen[row.Name] {
				continue
			}
			bridges = append(bridges, NetworkBridgeInfo{
				ID:            row.ID,
				Name:          row.Name,
				Mode:          normalizeBridgeMode(row.Mode),
				UplinkIF:      row.UplinkIF,
				MigrateHostIP: row.MigrateHostIP,
				IsDefault:     row.IsDefault,
			})
		}
	}
	for i := range bridges {
		bridges[i].Exists = ovsBridgeExists(bridges[i].Name)
		bridges[i].Active = linkIsUp(bridges[i].Name)
		if model.DB != nil {
			if bridges[i].IsDefault {
				model.DB.Model(&model.VPCSwitch{}).Where("bridge_name = ? OR bridge_name = '' OR bridge_name IS NULL", bridges[i].Name).Count(&bridges[i].SwitchCount)
			} else {
				model.DB.Model(&model.VPCSwitch{}).Where("bridge_name = ?", bridges[i].Name).Count(&bridges[i].SwitchCount)
			}
		}
	}
	return bridges, nil
}

func CreateNetworkBridge(req NetworkBridgeRequest) (*model.NetworkBridge, error) {
	req.Name = strings.TrimSpace(req.Name)
	req.Mode = normalizeBridgeMode(req.Mode)
	req.UplinkIF = strings.TrimSpace(req.UplinkIF)
	if req.Mode != BridgeModeDirect {
		return nil, fmt.Errorf("当前仅允许创建桥接直通网桥")
	}
	if err := validateBridgeName(req.Name); err != nil {
		return nil, err
	}
	if req.Name == ovsBridgeName() {
		return nil, fmt.Errorf("默认 OVS 内网网桥已存在，不能重复创建")
	}
	if req.UplinkIF == "" {
		return nil, fmt.Errorf("请选择物理网卡")
	}
	if err := validateBridgeUplink(req.UplinkIF, req.Name); err != nil {
		return nil, err
	}
	if model.DB != nil {
		var count int64
		model.DB.Model(&model.NetworkBridge{}).Where("name = ?", req.Name).Count(&count)
		if count > 0 {
			return nil, fmt.Errorf("网桥名称已存在")
		}
	}
	if err := EnsureOVSBridgeDirect(req.Name, req.UplinkIF, req.MigrateHostIP); err != nil {
		return nil, err
	}
	row := &model.NetworkBridge{Name: req.Name, Mode: BridgeModeDirect, UplinkIF: req.UplinkIF, MigrateHostIP: req.MigrateHostIP}
	if model.DB != nil {
		if err := model.DB.Create(row).Error; err != nil {
			return nil, fmt.Errorf("保存网桥配置失败: %w", err)
		}
		if err := EnsureOVSNetworkReady(); err != nil {
			return nil, fmt.Errorf("网桥已创建，但恢复默认 OVS 网络失败: %w", err)
		}
		if err := EnsureAllVPCSwitchRuntime(); err != nil {
			return nil, fmt.Errorf("网桥已创建，但恢复 VPC 交换机网络失败: %w", err)
		}
	}
	return row, nil
}

func DeleteNetworkBridge(id uint) error {
	if id == 0 {
		return fmt.Errorf("默认网桥不能删除")
	}
	var row model.NetworkBridge
	if err := model.DB.First(&row, id).Error; err != nil {
		return fmt.Errorf("网桥不存在")
	}
	var count int64
	model.DB.Model(&model.VPCSwitch{}).Where("bridge_name = ?", row.Name).Count(&count)
	if count > 0 {
		return fmt.Errorf("该网桥仍有交换机使用，不能删除")
	}
	_ = os.Remove(bridgeRestoreScriptPath(row.Name))
	// 仅当 OVS 桥实际存在时执行物理清理；桥已不存在则跳过直接清理记录
	if ovsBridgeExists(row.Name) {
		if row.MigrateHostIP {
			migrateBridgeIPv4ToInterface(row.Name, row.UplinkIF)
		}
		utils.ExecCommand("ovs-vsctl", "--if-exists", "del-port", row.Name, row.UplinkIF)
		utils.ExecCommand("ovs-vsctl", "--if-exists", "del-br", row.Name)
	}
	disableBridgeRestoreUnitIfEmpty()
	// 先恢复 OVS 默认网络（此时 IP/路由已迁回物理口，可正常检测 uplink）
	if err := EnsureOVSNetworkReady(); err != nil {
		return fmt.Errorf("网桥已删除，但恢复默认 OVS 网络失败: %w", err)
	}
	if err := EnsureAllVPCSwitchRuntime(); err != nil {
		return fmt.Errorf("网桥已删除，但恢复 VPC 交换机网络失败: %w", err)
	}
	// 最后恢复 networkd DHCP（networkctl reload 可能短暂影响路由，必须在 EnsureOVSNetworkReady 之后）
	removeNetworkdDHCPOverrideForPort(row.UplinkIF)
	if err := model.DB.Delete(&row).Error; err != nil {
		return err
	}
	return nil
}

func EnsureAllNetworkBridgesRuntime() error {
	if model.DB == nil {
		return nil
	}
	var rows []model.NetworkBridge
	if err := model.DB.Where("mode = ?", BridgeModeDirect).Find(&rows).Error; err != nil {
		return err
	}
	var lastErr error
	for _, row := range rows {
		if err := EnsureOVSBridgeDirect(row.Name, row.UplinkIF, row.MigrateHostIP); err != nil {
			lastErr = err
			logger.App.Warn("恢复桥接网桥失败", "bridge", row.Name, "error", err)
		}
	}
	return lastErr
}

func EnsureOVSBridgeDirect(bridge, uplink string, migrateHostIP bool) error {
	if result := utils.ExecCommand("bash", "-c", "command -v ovs-vsctl"); result.Error != nil {
		return fmt.Errorf("OVS 未安装，请先安装 openvswitch-switch")
	}
	bridge = strings.TrimSpace(bridge)
	uplink = strings.TrimSpace(uplink)
	if err := os.MkdirAll(bridgeConfigDir, 0755); err != nil {
		return fmt.Errorf("创建网桥配置目录失败: %w", err)
	}
	if result := utils.ExecCommand("ovs-vsctl", "--may-exist", "add-br", bridge); result.Error != nil {
		return fmt.Errorf("创建桥接网桥失败: %s", result.Stderr)
	}
	utils.ExecCommand("ip", "link", "set", bridge, "up")
	if result := utils.ExecCommand("ovs-vsctl", "--may-exist", "add-port", bridge, uplink); result.Error != nil {
		return fmt.Errorf("添加物理网卡到桥接网桥失败: %s", result.Stderr)
	}
	utils.ExecCommand("ip", "link", "set", uplink, "up")
	// 先完成 IP 迁移（必须在禁用 DHCP 之前，否则 networkctl reload 会立即移除 DHCP 地址）
	if migrateHostIP {
		migrateInterfaceIPv4ToBridge(uplink, bridge)
		ensureBridgeResolvedDNS(uplink, bridge)
	}
	// IP 已迁移完成后再禁用 networkd DHCP，避免周期性 DHCP Discover 干扰 OVS 数据通道
	disableNetworkdDHCPForPort(uplink)
	if err := writeBridgeRestoreScript(bridge, uplink, migrateHostIP); err != nil {
		return err
	}
	if err := writeBridgeRestoreUnit(); err != nil {
		return err
	}
	return nil
}

func BridgeModeForSwitch(sw model.VPCSwitch) string {
	mode := normalizeBridgeMode(sw.BridgeMode)
	if mode == "" {
		mode = BridgeModeNAT
	}
	return mode
}

func BridgeNameForSwitch(sw model.VPCSwitch) string {
	if strings.TrimSpace(sw.BridgeName) != "" {
		return strings.TrimSpace(sw.BridgeName)
	}
	return ovsBridgeName()
}

func SwitchUsesDirectBridge(sw model.VPCSwitch) bool {
	return BridgeModeForSwitch(sw) == BridgeModeDirect
}

func BuildOVSInterfaceXMLForBridge(mac, modelName, bridge string) string {
	if strings.TrimSpace(modelName) == "" {
		modelName = "virtio"
	}
	if strings.TrimSpace(bridge) == "" {
		bridge = ovsBridgeName()
	}
	var b strings.Builder
	b.WriteString("    <interface type='bridge'>\n")
	if strings.TrimSpace(mac) != "" {
		b.WriteString(fmt.Sprintf("      <mac address='%s'/>\n", strings.TrimSpace(mac)))
	}
	b.WriteString(fmt.Sprintf("      <source bridge='%s'/>\n", strings.TrimSpace(bridge)))
	b.WriteString("      <virtualport type='openvswitch'/>\n")
	b.WriteString(fmt.Sprintf("      <model type='%s'/>\n", strings.TrimSpace(modelName)))
	b.WriteString("    </interface>")
	return b.String()
}

func BuildOVSVirtInstallNetworkArgForBridge(modelName, bridge string) string {
	if strings.TrimSpace(modelName) == "" {
		modelName = "virtio"
	}
	if strings.TrimSpace(bridge) == "" {
		bridge = ovsBridgeName()
	}
	value := fmt.Sprintf("bridge=%s,virtualport.type=openvswitch,model=%s", strings.TrimSpace(bridge), strings.TrimSpace(modelName))
	return "--network " + utils.ShellSingleQuote(value)
}

func readIPAddrJSON() ([]ipAddrJSON, error) {
	result := utils.ExecCommand("ip", "-j", "addr")
	if result.Error != nil {
		return nil, fmt.Errorf("读取宿主机网卡失败: %s", result.Stderr)
	}
	var items []ipAddrJSON
	if err := json.Unmarshal([]byte(result.Stdout), &items); err != nil {
		return nil, fmt.Errorf("解析宿主机网卡失败: %w", err)
	}
	return items, nil
}

func readDefaultRouteIfaces() map[string]bool {
	result := utils.ExecCommand("ip", "-j", "route", "show", "default")
	var routes []ipRouteJSON
	_ = json.Unmarshal([]byte(result.Stdout), &routes)
	out := map[string]bool{}
	for _, route := range routes {
		if route.Dev != "" {
			out[route.Dev] = true
		}
	}
	return out
}

func readOVSPortBridgeMap() map[string]string {
	result := utils.ExecCommand("ovs-vsctl", "--format=json", "--columns=name,ports", "list", "Bridge")
	if result.Error != nil {
		return map[string]string{}
	}
	bridges := strings.Fields(strings.TrimSpace(utils.ExecCommand("ovs-vsctl", "list-br").Stdout))
	out := map[string]string{}
	for _, bridge := range bridges {
		ports := strings.Fields(strings.TrimSpace(utils.ExecCommand("ovs-vsctl", "list-ports", bridge).Stdout))
		for _, port := range ports {
			out[port] = bridge
		}
	}
	return out
}

func readManagedBridgeByUplink() map[string]string {
	out := map[string]string{}
	if model.DB == nil {
		return out
	}
	var rows []model.NetworkBridge
	model.DB.Find(&rows)
	for _, row := range rows {
		if row.UplinkIF != "" {
			out[row.UplinkIF] = row.Name
		}
	}
	return out
}

func isPhysicalInterface(name string) bool {
	if name == "lo" || strings.HasPrefix(name, "vnet") || strings.HasPrefix(name, "tap") || strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "br-") || strings.HasPrefix(name, "ovs") {
		return false
	}
	if _, err := os.Stat(filepath.Join("/sys/class/net", name, "device")); err == nil {
		return true
	}
	return false
}

func validateBridgeName(name string) error {
	if name == "" {
		return fmt.Errorf("网桥名称不能为空")
	}
	if len(name) > 15 {
		return fmt.Errorf("网桥名称不能超过 15 个字符")
	}
	if ok, _ := regexp.MatchString(`^[A-Za-z0-9_.-]+$`, name); !ok {
		return fmt.Errorf("网桥名称只能包含字母、数字、点、下划线和短横线")
	}
	return nil
}

func validateBridgeUplink(uplink, targetBridge string) error {
	if !isPhysicalInterface(uplink) {
		return fmt.Errorf("请选择真实物理网卡")
	}
	ports := readOVSPortBridgeMap()
	if bridge := ports[uplink]; bridge != "" && bridge != targetBridge {
		return fmt.Errorf("物理网卡 %s 已接入 OVS 网桥 %s", uplink, bridge)
	}
	if model.DB != nil {
		var count int64
		model.DB.Model(&model.NetworkBridge{}).Where("uplink_if = ?", uplink).Count(&count)
		if count > 0 {
			return fmt.Errorf("物理网卡 %s 已被其它桥接网桥使用", uplink)
		}
	}
	return nil
}

func defaultNetworkBridgeRecords() []NetworkBridgeInfo {
	return []NetworkBridgeInfo{{
		Name:      ovsBridgeName(),
		Mode:      BridgeModeNAT,
		IsDefault: true,
	}}
}

func normalizeBridgeMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return BridgeModeNAT
	}
	return mode
}

func ovsBridgeExists(name string) bool {
	return utils.ExecCommand("ovs-vsctl", "br-exists", strings.TrimSpace(name)).Error == nil
}

func linkIsUp(name string) bool {
	result := utils.ExecCommand("ip", "-j", "link", "show", "dev", strings.TrimSpace(name))
	return result.Error == nil && strings.Contains(strings.ToUpper(result.Stdout), "UP")
}

func migrateInterfaceIPv4ToBridge(uplink, bridge string) {
	script := fmt.Sprintf(`set -e
UPLINK=%s
BRIDGE=%s
%s
`, utils.ShellSingleQuote(uplink), utils.ShellSingleQuote(bridge), bridgeHostIPMigrationShell())
	utils.ExecCommand("bash", "-c", script)
}

func migrateBridgeIPv4ToInterface(bridge, uplink string) {
	script := fmt.Sprintf(`set -e
BRIDGE=%s
UPLINK=%s
%s
`, utils.ShellSingleQuote(bridge), utils.ShellSingleQuote(uplink), bridgeHostIPRollbackShell())
	utils.ExecCommand("bash", "-c", script)
}

func ensureBridgeResolvedDNS(uplink, bridge string) {
	uplink = strings.TrimSpace(uplink)
	bridge = strings.TrimSpace(bridge)
	if uplink == "" || bridge == "" {
		return
	}
	if result := utils.ExecCommand("bash", "-c", "command -v resolvectl"); result.Error != nil {
		return
	}
	servers := resolvectlDNSServers(uplink)
	if len(servers) == 0 {
		servers = resolvectlDNSServers("")
	}
	if len(servers) > 0 {
		args := append([]string{"dns", bridge}, servers...)
		utils.ExecCommand("resolvectl", args...)
	}
	utils.ExecCommand("resolvectl", "default-route", bridge, "yes")
	utils.ExecCommand("resolvectl", "domain", bridge, "~.")
}

func resolvectlDNSServers(link string) []string {
	args := []string{"dns"}
	if strings.TrimSpace(link) != "" {
		args = append(args, strings.TrimSpace(link))
	}
	result := utils.ExecCommand("resolvectl", args...)
	if result.Error != nil {
		return nil
	}
	return parseResolvectlDNSServers(result.Stdout)
}

func parseResolvectlDNSServers(text string) []string {
	seen := map[string]bool{}
	var servers []string
	for _, field := range strings.Fields(text) {
		value := strings.Trim(field, ",;")
		if strings.HasPrefix(value, "[") || strings.HasSuffix(value, "]") || strings.Contains(value, "(") || strings.Contains(value, ")") {
			continue
		}
		host, _, splitErr := net.SplitHostPort(value)
		if splitErr == nil {
			value = host
		}
		ip := net.ParseIP(value)
		if ip == nil || seen[value] {
			continue
		}
		seen[value] = true
		servers = append(servers, value)
	}
	return servers
}

func writeBridgeRestoreScript(bridge, uplink string, migrateHostIP bool) error {
	content := buildBridgeRestoreScriptContent(bridge, uplink, migrateHostIP)
	_, err := writeFileIfChanged(bridgeRestoreScriptPath(bridge), []byte(content), 0755)
	return err
}

func buildBridgeRestoreScriptContent(bridge, uplink string, migrateHostIP bool) string {
	content := fmt.Sprintf(`#!/bin/bash
set -e
BRIDGE=%s
UPLINK=%s
`, utils.ShellSingleQuote(bridge), utils.ShellSingleQuote(uplink))
	if migrateHostIP {
		content += `# 先记录物理口当前 DHCP/静态地址，加入 OVS 后再迁移到 bridge。
`
		content += bridgeHostIPCaptureShell()
	}
	content += `ovs-vsctl --may-exist add-br "$BRIDGE"
ip link set "$BRIDGE" up
ovs-vsctl --may-exist add-port "$BRIDGE" "$UPLINK"
ip link set "$UPLINK" up
`
	if migrateHostIP {
		content += bridgeHostIPApplyShell()
		content += `# DNS 迁移到 bridge，避免默认路由切换后解析仍绑定在物理口。
`
		content += bridgeResolvedDNSShell()
	}
	return content
}

func bridgeHostIPMigrationShell() string {
	return bridgeHostIPCaptureShell() + bridgeHostIPApplyShell()
}

func bridgeHostIPRollbackShell() string {
	return bridgeHostIPCaptureFromBridgeShell() + bridgeHostIPApplyToUplinkShell()
}

func bridgeHostIPCaptureShell() string {
	return `HOST_ADDRS="$(ip -4 -o addr show dev "$UPLINK" scope global 2>/dev/null | awk '{print $4}')"
HOST_GW="$(ip -4 route show default dev "$UPLINK" 2>/dev/null | awk '{print $3; exit}')"
HOST_METRIC="$(ip -4 route show default dev "$UPLINK" 2>/dev/null | awk '{for (i=1;i<=NF;i++) if ($i=="metric") {print $(i+1); exit}}')"
`
}

func bridgeHostIPCaptureFromBridgeShell() string {
	return `HOST_ADDRS="$(ip -4 -o addr show dev "$BRIDGE" scope global 2>/dev/null | awk '{print $4}')"
HOST_GW="$(ip -4 route show default dev "$BRIDGE" 2>/dev/null | awk '{print $3; exit}')"
HOST_METRIC="$(ip -4 route show default dev "$BRIDGE" 2>/dev/null | awk '{for (i=1;i<=NF;i++) if ($i=="metric") {print $(i+1); exit}}')"
`
}

func bridgeHostIPApplyShell() string {
	return `if [ -n "$HOST_ADDRS" ]; then
  ip addr flush dev "$UPLINK"
  while IFS= read -r addr; do
    [ -n "$addr" ] || continue
    ip addr replace "$addr" dev "$BRIDGE"
  done <<< "$HOST_ADDRS"
fi
if [ -n "$HOST_GW" ]; then
  ip route del "$HOST_GW" dev "$UPLINK" 2>/dev/null || true
  ip route replace "$HOST_GW" dev "$BRIDGE" scope link
  if [ -n "$HOST_METRIC" ]; then
    ip route replace default via "$HOST_GW" dev "$BRIDGE" metric "$HOST_METRIC"
  else
    ip route replace default via "$HOST_GW" dev "$BRIDGE"
  fi
fi
`
}

func bridgeHostIPApplyToUplinkShell() string {
	return `ip link set "$UPLINK" up
if [ -n "$HOST_ADDRS" ]; then
  ip addr flush dev "$BRIDGE"
  while IFS= read -r addr; do
    [ -n "$addr" ] || continue
    ip addr replace "$addr" dev "$UPLINK"
  done <<< "$HOST_ADDRS"
fi
if [ -n "$HOST_GW" ]; then
  ip route del "$HOST_GW" dev "$BRIDGE" 2>/dev/null || true
  ip route replace "$HOST_GW" dev "$UPLINK" scope link
  if [ -n "$HOST_METRIC" ]; then
    ip route replace default via "$HOST_GW" dev "$UPLINK" metric "$HOST_METRIC"
  else
    ip route replace default via "$HOST_GW" dev "$UPLINK"
  fi
fi
`
}

func bridgeResolvedDNSShell() string {
	return `if command -v resolvectl >/dev/null 2>&1; then
  DNS_SERVERS="$(resolvectl dns "$UPLINK" 2>/dev/null | sed 's/.*://' | xargs)"
  if [ -z "$DNS_SERVERS" ]; then
    DNS_SERVERS="$(resolvectl dns 2>/dev/null | sed 's/.*://' | xargs)"
  fi
  if [ -n "$DNS_SERVERS" ]; then
    resolvectl dns "$BRIDGE" $DNS_SERVERS || true
  fi
  resolvectl default-route "$BRIDGE" yes || true
  resolvectl domain "$BRIDGE" '~.' || true
fi
`
}

func bridgeRestoreScriptPath(bridge string) string {
	return filepath.Join(bridgeConfigDir, strings.TrimSpace(bridge)+".sh")
}

func writeBridgeRestoreUnit() error {
	content := `[Unit]
Description=KVM Console managed OVS bridge restore
After=network-online.target openvswitch-switch.service
Wants=network-online.target openvswitch-switch.service

[Service]
Type=oneshot
ExecStart=/bin/bash -c 'for f in /etc/kvm-console/bridges/*.sh; do [ -e "$f" ] && /bin/bash "$f"; done'
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
`
	changed, err := writeFileIfChanged("/etc/systemd/system/kvm-console-bridges.service", []byte(content), 0644)
	if err != nil {
		return fmt.Errorf("写入桥接网桥 systemd 服务失败: %w", err)
	}
	if changed {
		utils.ExecCommand("systemctl", "daemon-reload")
	}
	ensureSystemdUnitEnabled("kvm-console-bridges.service")
	return nil
}

func disableBridgeRestoreUnitIfEmpty() {
	matches, err := filepath.Glob(filepath.Join(bridgeConfigDir, "*.sh"))
	if err != nil || len(matches) > 0 {
		return
	}
	utils.ExecCommand("systemctl", "disable", "--now", "kvm-console-bridges.service")
	utils.ExecCommand("systemctl", "reset-failed", "kvm-console-bridges.service")
}

// networkdOverridePath 返回 networkd override 文件路径
func networkdOverridePath(iface string) string {
	return fmt.Sprintf("/etc/systemd/network/01-kvm-console-%s.network", iface)
}

// disableNetworkdDHCPForPort 为已加入 OVS bridge 的物理网卡写入 networkd 覆盖配置，
// 禁用 DHCP 以防止 systemd-networkd 周期性发送 DHCP Discover 干扰 OVS 数据通道导致丢包。
func disableNetworkdDHCPForPort(iface string) {
	iface = strings.TrimSpace(iface)
	if iface == "" {
		return
	}
	// 仅当 systemd-networkd 在运行时处理
	if utils.ExecCommand("systemctl", "is-active", "--quiet", "systemd-networkd").Error != nil {
		return
	}
	content := fmt.Sprintf(`[Match]
Name=%s

[Link]
Unmanaged=yes

[Network]
DHCP=no
LinkLocalAddressing=no
`, iface)
	path := networkdOverridePath(iface)
	changed, err := writeFileIfChanged(path, []byte(content), 0644)
	if err != nil {
		logger.App.Warn("写入 networkd 覆盖配置失败", "iface", iface, "error", err)
		return
	}
	if changed {
		utils.ExecCommand("networkctl", "reload")
		logger.App.Info("已禁用 networkd 对 OVS 端口的 DHCP 管理", "iface", iface)
	}
}

// removeNetworkdDHCPOverrideForPort 删除物理网卡从 OVS bridge 移除后不再需要的 networkd 覆盖配置。
func removeNetworkdDHCPOverrideForPort(iface string) {
	iface = strings.TrimSpace(iface)
	if iface == "" {
		return
	}
	path := networkdOverridePath(iface)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return
	}
	if err := os.Remove(path); err != nil {
		logger.App.Warn("删除 networkd 覆盖配置失败", "iface", iface, "error", err)
		return
	}
	if utils.ExecCommand("systemctl", "is-active", "--quiet", "systemd-networkd").Error == nil {
		utils.ExecCommand("networkctl", "reload")
		logger.App.Info("已恢复 networkd 对端口的管理", "iface", iface)
	}
}

func ensureSystemdUnitEnabled(unit string) {
	if result := utils.ExecCommand("systemctl", "is-enabled", "--quiet", unit); result.Error != nil {
		utils.ExecCommand("systemctl", "enable", unit)
	}
}
