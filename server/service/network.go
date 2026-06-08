package service

import (
	"fmt"
	"net"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"kvm_console/config"
	"kvm_console/model"
	"kvm_console/utils"
)

// StaticIPInfo 静态 IP 绑定信息
type StaticIPInfo struct {
	VMName string `json:"vm_name"`
	IP     string `json:"ip"`
	MAC    string `json:"mac"`
}

// IPListInfo 完整 IP 信息
type IPListInfo struct {
	StaticBindings []StaticIPInfo  `json:"static_bindings"`
	DHCPLeases     []DHCPLeaseInfo `json:"dhcp_leases"`
}

// DHCPLeaseInfo DHCP 租约信息
type DHCPLeaseInfo struct {
	ExpiryTime string `json:"expiry_time"`
	MAC        string `json:"mac"`
	IP         string `json:"ip"`
	Hostname   string `json:"hostname"`
	VMName     string `json:"vm_name"` // 通过 MAC 地址关联的虚拟机名称
}

// PortForwardRule 端口转发规则
type PortForwardRule struct {
	ID                    int    `json:"id"`                      // 规则编号
	Protocol              string `json:"protocol"`                // tcp/udp
	HostPort              string `json:"host_port"`               // 宿主机端口
	AccessIP              string `json:"access_ip"`               // 对外访问 IP
	AccessAddress         string `json:"access_address"`          // 对外完整访问地址
	DestIP                string `json:"dest_ip"`                 // 目标 IP
	DestPort              string `json:"dest_port"`               // 目标端口
	VMName                string `json:"vm_name"`                 // 关联虚拟机
	OwnerUsername         string `json:"owner_username"`          // 归属用户
	FirewallKey           string `json:"firewall_key"`            // 防火墙豁免使用的稳定标识
	RegionFilterEnabled   bool   `json:"region_filter_enabled"`   // 是否继承入站区域限制
	RegionFilterInherited bool   `json:"region_filter_inherited"` // 是否继承全局入站策略
	RuleKey               string `json:"rule_key"`                // 稳定规则标识
	Live                  bool   `json:"live"`                    // 当前是否仍存在于 iptables
	ProbeStatus           string `json:"probe_status"`            // 探测状态
	ProbeReason           string `json:"probe_reason"`            // 探测或封禁原因
	ProbeLastCheckedAt    string `json:"probe_last_checked_at"`   // 最近探测时间
	ProbeHTTPStatusCode   int    `json:"probe_http_status_code"`  // 最近 HTTP 状态码
	ProbeWhitelistScope   string `json:"probe_whitelist_scope"`   // 命中的白名单范围
	Banned                bool   `json:"banned"`                  // 是否已自动封禁
}

// StableKey 返回端口转发规则的稳定标识，避免依赖 iptables 行号。
func (r PortForwardRule) StableKey() string {
	return strings.ToLower(strings.TrimSpace(r.Protocol)) + "|" +
		strings.TrimSpace(r.HostPort) + "|" +
		strings.TrimSpace(r.DestIP) + "|" +
		strings.TrimSpace(r.DestPort)
}

// PortForwardAddParams 添加端口转发参数
type PortForwardAddParams struct {
	VMIP           string `json:"vm_ip"`
	HostPort       string `json:"host_port"`
	VMPort         string `json:"vm_port"`
	Protocol       string `json:"protocol"` // tcp/udp/both
	Comment        string `json:"comment"`
	CreatedBy      string `json:"created_by"`
	CreatedByAdmin bool   `json:"created_by_admin"`
}

// PortForwardAutoAddParams 自动分配端口参数
type PortForwardAutoAddParams struct {
	VMIP     string `json:"vm_ip" binding:"required"`
	VMPort   string `json:"vm_port" binding:"required"`
	Protocol string `json:"protocol"`
	Comment  string `json:"comment"`
}

// PortForwardUpdateParams 编辑端口转发参数
type PortForwardUpdateParams struct {
	VMIP           string `json:"vm_ip"`
	HostPort       string `json:"host_port"`
	VMPort         string `json:"vm_port"`
	Protocol       string `json:"protocol"`
	Comment        string `json:"comment"`
	CreatedBy      string `json:"created_by"`
	CreatedByAdmin bool   `json:"created_by_admin"`
}

type portForwardTargetInfo struct {
	VMName        string
	OwnerUsername string
}

var listPortForwardRulesForAvailability = listLivePortForwardsFromIPTables
var canListenOnHostPort = canBindHostPort

// ListStaticIPs 列出静态 IP 绑定
func ListStaticIPs() (*IPListInfo, error) {
	info := &IPListInfo{}

	staticHosts, err := ListOVSStaticHosts()
	if err != nil {
		return info, fmt.Errorf("读取 OVS 静态绑定失败: %w", err)
	}
	for _, host := range staticHosts {
		info.StaticBindings = append(info.StaticBindings, StaticIPInfo{
			MAC:    host.MAC,
			VMName: host.VMName,
			IP:     host.IP,
		})
	}
	if vpcStaticHosts, vpcErr := ListAllVPCStaticHosts(); vpcErr == nil {
		for _, host := range vpcStaticHosts {
			info.StaticBindings = append(info.StaticBindings, StaticIPInfo{
				MAC:    host.MAC,
				VMName: host.VMName,
				IP:     host.IP,
			})
		}
	}

	// 构建 MAC -> VM名称 的映射（通过遍历所有虚拟机的网卡）
	macToVMName := make(map[string]string)
	listResult := utils.ExecCommand("virsh", "list", "--all", "--name")
	if listResult.Error == nil {
		for _, vmName := range strings.Split(listResult.Stdout, "\n") {
			vmName = strings.TrimSpace(vmName)
			if vmName == "" {
				continue
			}
			ifResult := utils.ExecCommand("virsh", "domiflist", vmName)
			if ifResult.Error == nil {
				for _, ifLine := range strings.Split(ifResult.Stdout, "\n") {
					ifFields := strings.Fields(ifLine)
					if len(ifFields) >= 5 && ifFields[0] != "Interface" && !strings.HasPrefix(ifLine, "-") {
						mac := strings.ToLower(ifFields[4])
						macToVMName[mac] = vmName
					}
				}
			}
		}
	}

	leases, err := ListOVSDHCPLeases()
	if err != nil {
		leases = []OVSDHCPLease{}
	}
	if vpcLeases, vpcErr := ListVPCDHCPLeases(); vpcErr == nil {
		leases = append(leases, vpcLeases...)
	}
	leaseMap := make(map[string]OVSDHCPLease)
	for _, lease := range leases {
		mac := strings.ToLower(lease.MAC)
		leaseMap[mac] = newerOVSDHCPLease(leaseMap[mac], lease)
	}
	for mac, lease := range leaseMap {
		info.DHCPLeases = append(info.DHCPLeases, DHCPLeaseInfo{
			ExpiryTime: lease.ExpiryTime,
			MAC:        lease.MAC,
			IP:         lease.IP,
			Hostname:   lease.Hostname,
			VMName:     macToVMName[mac],
		})
	}

	return info, nil
}

// findFreeIP 自动查找空闲 IP，从 .2 到 .254 按顺序分配
func findFreeIP() (string, error) {
	subnet := ovsSubnetPrefix()

	// 收集所有已占用的 IP（静态绑定 + DHCP 租约）
	usedIPs := make(map[int]bool)
	// .1 是网关，始终标记为已占用
	usedIPs[1] = true

	staticHosts, _ := ListOVSStaticHosts()
	for _, host := range staticHosts {
		parts := strings.Split(host.IP, ".")
		if len(parts) == 4 {
			if lastOctet, err := strconv.Atoi(parts[3]); err == nil {
				usedIPs[lastOctet] = true
			}
		}
	}

	leases, _ := ListOVSDHCPLeases()
	for _, lease := range leases {
		parts := strings.Split(lease.IP, ".")
		if len(parts) == 4 {
			if lastOctet, err := strconv.Atoi(parts[3]); err == nil {
				usedIPs[lastOctet] = true
			}
		}
	}

	// 从 .2 开始按顺序查找空闲 IP
	for i := 2; i <= 254; i++ {
		if !usedIPs[i] {
			return fmt.Sprintf("%s.%d", subnet, i), nil
		}
	}

	return "", fmt.Errorf("网段 %s.0/24 内没有可用的空闲 IP（2-254 均已占用）", subnet)
}

func findVPCFreeIP(sw model.VPCSwitch) (string, error) {
	start := net.ParseIP(sw.DHCPStart).To4()
	end := net.ParseIP(sw.DHCPEnd).To4()
	if start == nil || end == nil {
		return "", fmt.Errorf("交换机 DHCP 地址池无效")
	}
	used := map[string]bool{sw.GatewayIP: true}
	staticHosts, _ := ListVPCStaticHosts(sw.ID)
	for _, host := range staticHosts {
		used[host.IP] = true
	}
	leases, _ := ListVPCDHCPLeasesForSwitch(sw.ID)
	for _, lease := range leases {
		used[lease.IP] = true
	}
	for ip := append(net.IP(nil), start...); compareIPv4(ip, end) <= 0; incrementIPv4(ip) {
		ipText := ip.String()
		if !used[ipText] {
			return ipText, nil
		}
	}
	return "", fmt.Errorf("交换机 %s 的 DHCP 地址池没有可用 IP", sw.Name)
}

func compareIPv4(a, b net.IP) int {
	for i := 0; i < 4; i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

func incrementIPv4(ip net.IP) {
	for i := 3; i >= 0; i-- {
		ip[i]++
		if ip[i] != 0 {
			return
		}
	}
}

func normalizeIPForVPC(ipAddr string, sw model.VPCSwitch) (string, error) {
	ipAddr = strings.TrimSpace(ipAddr)
	if matched, _ := regexp.MatchString(`^\d+$`, ipAddr); matched {
		parts := strings.Split(sw.GatewayIP, ".")
		if len(parts) == 4 {
			ipAddr = strings.Join(parts[:3], ".") + "." + ipAddr
		}
	}
	ip := net.ParseIP(ipAddr)
	if ip == nil || ip.To4() == nil {
		return "", fmt.Errorf("IP 地址格式无效")
	}
	if !IPInCIDR(ipAddr, sw.CIDR) {
		return "", fmt.Errorf("IP 地址 %s 不在交换机子网 %s 内", ipAddr, sw.CIDR)
	}
	if ipAddr == sw.GatewayIP {
		return "", fmt.Errorf("IP 地址 %s 是交换机网关，不能绑定", ipAddr)
	}
	parts := strings.Split(ipAddr, ".")
	if len(parts) == 4 && (parts[3] == "0" || parts[3] == "255") {
		return "", fmt.Errorf("IP 地址 %s 不能作为虚拟机地址", ipAddr)
	}
	return ipAddr, nil
}

func UpsertVPCStaticHost(sw model.VPCSwitch, vmName, mac, ipAddr string) error {
	if err := os.MkdirAll(vpcConfigDir, 0755); err != nil {
		return err
	}
	if _, err := os.Stat(vpcDHCPHostsPath(sw.ID)); os.IsNotExist(err) {
		if err := os.WriteFile(vpcDHCPHostsPath(sw.ID), []byte(""), 0644); err != nil {
			return fmt.Errorf("创建 VPC 静态 DHCP 绑定文件失败: %w", err)
		}
	}
	mac = strings.ToLower(strings.TrimSpace(mac))
	vmName = strings.TrimSpace(vmName)
	ipAddr = strings.TrimSpace(ipAddr)
	hosts, err := ListVPCStaticHosts(sw.ID)
	if err != nil {
		return err
	}
	next, err := buildOVSStaticHostsForUpsert(hosts, OVSStaticHost{VMName: vmName, MAC: mac, IP: ipAddr})
	if err != nil {
		return err
	}
	if err := writeVPCStaticHosts(sw.ID, next); err != nil {
		return fmt.Errorf("写入 VPC 静态 IP 绑定失败: %w", err)
	}
	CleanVPCDHCPLease(sw.ID, mac, ipAddr)
	CleanOVSDHCPLease(mac, "")
	reloadVPCDNSMasq(sw.ID)
	return nil
}

func RemoveVPCStaticHost(switchID uint, vmName, mac string) (string, error) {
	hosts, err := ListVPCStaticHosts(switchID)
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
	if err := writeVPCStaticHosts(switchID, next); err != nil {
		return "", fmt.Errorf("删除 VPC 静态 IP 绑定失败: %w", err)
	}
	reloadVPCDNSMasq(switchID)
	return removedIP, nil
}

func GetVPCStaticIPByMAC(switchID uint, mac string) string {
	hosts, err := ListVPCStaticHosts(switchID)
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

func GetVPCStaticHostByVMName(switchID uint, vmName string) (OVSStaticHost, bool) {
	hosts, err := ListVPCStaticHosts(switchID)
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

// EnsureStaticIP 确保虚拟机有静态 IP 绑定，如果没有则自动绑定
// 返回实际的静态 IP 地址
func EnsureStaticIP(vmName string) (string, error) {
	// 获取 MAC 地址
	mac := getFirstVMMAC(vmName)
	if mac == "" {
		return "", fmt.Errorf("无法获取虚拟机 %s 的 MAC 地址", vmName)
	}
	if sw, ok := getVPCSwitchForVM(vmName); ok {
		if host, ok := GetVPCStaticHostByVMName(sw.ID, vmName); ok {
			if !strings.EqualFold(host.MAC, mac) {
				if err := UpsertVPCStaticHost(*sw, vmName, mac, host.IP); err != nil {
					return "", fmt.Errorf("同步 VPC 静态 IP 绑定到当前 MAC 失败: %w", err)
				}
			}
			return host.IP, nil
		}
		if ip := GetVPCStaticIPByMAC(sw.ID, mac); ip != "" {
			return ip, nil
		}
		if ip := GetVPCLeaseIPForVM(vmName); ip != "" {
			if err := UpsertVPCStaticHost(*sw, vmName, mac, ip); err != nil {
				return "", fmt.Errorf("固定当前 VPC DHCP 地址失败: %w", err)
			}
			return ip, nil
		}
		if ip := getHostNeighborIPByMAC(mac, sw.CIDR, true); ip != "" {
			if err := UpsertVPCStaticHost(*sw, vmName, mac, ip); err != nil {
				return "", fmt.Errorf("固定当前 VPC 邻居表地址失败: %w", err)
			}
			return ip, nil
		}
		return BindStaticIP(vmName, "")
	}

	// 如果同一 VM 曾绑定过静态 IP，但用户修改了 MAC，则保留原 IP 并迁移到当前 MAC。
	if host, ok := GetOVSStaticHostByVMName(vmName); ok {
		if !strings.EqualFold(host.MAC, mac) {
			if err := UpsertOVSStaticHost(vmName, mac, host.IP); err != nil {
				return "", fmt.Errorf("同步静态 IP 绑定到当前 MAC 失败: %w", err)
			}
			refreshNIC(vmName, mac, "")
		}
		return host.IP, nil
	}

	// 检查当前 MAC 是否已有静态绑定
	if ip := GetOVSStaticIPByMAC(mac); ip != "" {
		return ip, nil
	}

	// 没有静态绑定，自动绑定（IP 留空表示自动分配）
	return BindStaticIP(vmName, "")
}

// ResolvePortForwardTargetIP 解析端口转发目标 IP。
// VPC VM 始终以后端当前静态绑定或最新 DHCP 租约为准，避免前端缓存旧 IP 导致 DNAT 指向失效地址。
func ResolvePortForwardTargetIP(vmName, requestedIP string) (string, error) {
	vmName = strings.TrimSpace(vmName)
	requestedIP = strings.TrimSpace(requestedIP)
	if vmName == "" {
		if requestedIP == "" {
			return "", fmt.Errorf("虚拟机名称或目标 IP 不能为空")
		}
		return requestedIP, nil
	}
	if sw, ok := getVPCSwitchForVM(vmName); ok {
		mac := getFirstVMMAC(vmName)
		if mac == "" {
			return "", fmt.Errorf("无法获取虚拟机 %s 的 MAC 地址", vmName)
		}
		if host, ok := GetVPCStaticHostByVMName(sw.ID, vmName); ok {
			if !strings.EqualFold(host.MAC, mac) {
				if err := UpsertVPCStaticHost(*sw, vmName, mac, host.IP); err != nil {
					return "", fmt.Errorf("同步 VPC 静态 IP 绑定到当前 MAC 失败: %w", err)
				}
			}
			return host.IP, nil
		}
		if ip := GetVPCLeaseIPForVM(vmName); ip != "" {
			if err := UpsertVPCStaticHost(*sw, vmName, mac, ip); err != nil {
				return "", fmt.Errorf("固定当前 VPC DHCP 地址失败: %w", err)
			}
			return ip, nil
		}
		if ip := getHostNeighborIPByMAC(mac, sw.CIDR, true); ip != "" {
			if err := UpsertVPCStaticHost(*sw, vmName, mac, ip); err != nil {
				return "", fmt.Errorf("固定当前 VPC 邻居表地址失败: %w", err)
			}
			return ip, nil
		}
		if requestedIP != "" {
			normalized, err := normalizeIPForVPC(requestedIP, *sw)
			if err != nil {
				return "", err
			}
			if err := UpsertVPCStaticHost(*sw, vmName, mac, normalized); err != nil {
				return "", fmt.Errorf("绑定 VPC 静态 IP 失败: %w", err)
			}
			return normalized, nil
		}
		return BindStaticIP(vmName, "")
	}
	if requestedIP != "" {
		return requestedIP, nil
	}
	return EnsureStaticIP(vmName)
}

// BindStaticIP 绑定静态 IP，ipAddr 为空时自动分配空闲 IP
// 返回实际绑定的 IP 地址
func BindStaticIP(vmName, ipAddr string) (string, error) {
	// 获取 MAC 地址
	mac := getFirstVMMAC(vmName)
	if mac == "" {
		return "", fmt.Errorf("无法获取虚拟机 %s 的 MAC 地址", vmName)
	}
	if sw, ok := getVPCSwitchForVM(vmName); ok {
		if ipAddr == "" {
			freeIP, err := findVPCFreeIP(*sw)
			if err != nil {
				return "", err
			}
			ipAddr = freeIP
		} else {
			normalized, err := normalizeIPForVPC(ipAddr, *sw)
			if err != nil {
				return "", err
			}
			ipAddr = normalized
		}
		if err := UpsertVPCStaticHost(*sw, vmName, mac, ipAddr); err != nil {
			return "", fmt.Errorf("绑定 VPC 静态 IP 失败: %w", err)
		}
		_, _ = RemoveOVSStaticHost(vmName, mac)
		go refreshNIC(vmName, mac, "")
		return ipAddr, nil
	}

	// IP 为空时自动分配
	if ipAddr == "" {
		freeIP, err := findFreeIP()
		if err != nil {
			return "", err
		}
		ipAddr = freeIP
	} else {
		ipAddr = normalizeIPForOVS(ipAddr)
	}

	// 执行绑定
	if err := UpsertOVSStaticHost(vmName, mac, ipAddr); err != nil {
		return "", fmt.Errorf("绑定失败: %w", err)
	}

	// 如果虚拟机正在运行，拔插网卡以强制刷新 DHCP，确保使用新 IP
	refreshNIC(vmName, mac, "")

	return ipAddr, nil
}

// refreshNIC 拔插网卡以强制刷新 DHCP（仅运行中的虚拟机）
func refreshNIC(vmName, mac, network string) {
	stateResult := utils.ExecCommand("virsh", "domstate", vmName)
	if stateResult.Error != nil || strings.TrimSpace(stateResult.Stdout) != "running" {
		return
	}

	// 获取网卡模型
	nicModel := "virtio"
	ifListResult := utils.ExecCommand("virsh", "domiflist", vmName)
	if ifListResult.Error == nil {
		for _, line := range strings.Split(ifListResult.Stdout, "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 5 && fields[0] != "Interface" && !strings.HasPrefix(line, "-") {
				nicModel = fields[3]
				break
			}
		}
	}

	if useOVSNetwork() {
		xmlPath := fmt.Sprintf("/tmp/_ovs-if-%s.xml", vmName)
		if sw, ok := getVPCSwitchForVM(vmName); ok {
			_ = os.WriteFile(xmlPath, []byte(BuildOVSInterfaceXMLWithVLAN(mac, nicModel, sw.VLANID)), 0644)
			detachResult := utils.ExecCommand("virsh", "detach-device", vmName, xmlPath, "--live")
			if detachResult.Error == nil {
				time.Sleep(1 * time.Second)
				if attachResult := utils.ExecCommand("virsh", "attach-device", vmName, xmlPath, "--live"); attachResult.Error == nil {
					_ = ApplyVPCSwitchRuntime(vmName, *sw)
				}
			}
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(xmlPath)))
			return
		}
		_ = os.WriteFile(xmlPath, []byte(BuildOVSInterfaceXML(mac, nicModel)), 0644)
		detachResult := utils.ExecCommand("virsh", "detach-device", vmName, xmlPath, "--live")
		if detachResult.Error == nil {
			time.Sleep(1 * time.Second)
			utils.ExecCommand("virsh", "attach-device", vmName, xmlPath, "--live")
		}
		utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(xmlPath)))
		return
	}

	detachResult := utils.ExecCommand("virsh", "detach-interface", vmName, "network", "--mac", mac, "--live")
	if detachResult.Error == nil {
		time.Sleep(1 * time.Second)
		utils.ExecCommand("virsh", "attach-interface", vmName, "network", network, "--model", nicModel, "--mac", mac, "--live")
	}
}

// UnbindStaticIP 解绑静态 IP
func UnbindStaticIP(vmName string) error {
	// 获取 MAC
	mac := getFirstVMMAC(vmName)
	if mac == "" {
		return fmt.Errorf("无法获取 MAC 地址")
	}
	if sw, ok := getVPCSwitchForVM(vmName); ok {
		boundIP, err := RemoveVPCStaticHost(sw.ID, vmName, mac)
		if err != nil {
			return err
		}
		if boundIP != "" {
			removePortForwardsForIP(boundIP)
		}
		go refreshNIC(vmName, mac, "")
		return nil
	}

	boundIP, err := RemoveOVSStaticHost(vmName, mac)
	if err != nil {
		return err
	}

	// 删除所有指向该 IP 的端口转发规则（倒序删除避免行号偏移）
	if boundIP != "" {
		removePortForwardsForIP(boundIP)
	}

	// 如果虚拟机正在运行，拔插网卡以强制刷新 DHCP，确保切换回动态 IP
	refreshNIC(vmName, mac, "")

	return nil
}

// removePortForwardsForIP 删除所有指向指定 IP 的端口转发规则
func removePortForwardsForIP(targetIP string) {
	// 获取所有 DNAT 规则及行号
	result := utils.ExecShell("iptables -t nat -L PREROUTING -n --line-numbers 2>/dev/null | grep DNAT")
	if result.Error != nil || result.Stdout == "" {
		return
	}

	// 收集需要删除的规则行号（倒序删除避免偏移）
	var ruleIDs []int
	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, targetIP) {
			continue
		}
		// 检查 to:targetIP: 格式确保精确匹配
		if !strings.Contains(line, "to:"+targetIP+":") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 {
			var id int
			fmt.Sscanf(fields[0], "%d", &id)
			if id > 0 {
				ruleIDs = append(ruleIDs, id)
			}
		}
	}

	// 倒序删除（从大到小，避免行号偏移）
	for i := len(ruleIDs) - 1; i >= 0; i-- {
		id := ruleIDs[i]
		// 获取规则信息用于清理 FORWARD 和 UFW
		ruleInfo := utils.ExecShell(fmt.Sprintf("iptables -t nat -L PREROUTING %d -n 2>/dev/null", id))

		// 解析协议
		protoRe := regexp.MustCompile(`\s+(tcp|udp|6|17)\s+`)
		proto := "tcp"
		if m := protoRe.FindStringSubmatch(ruleInfo.Stdout); len(m) > 1 {
			switch m[1] {
			case "6":
				proto = "tcp"
			case "17":
				proto = "udp"
			default:
				proto = m[1]
			}
		}

		// 解析宿主机端口
		dportRe := regexp.MustCompile(`dpts?:(\S+)`)
		hostPort := ""
		if m := dportRe.FindStringSubmatch(ruleInfo.Stdout); len(m) > 1 {
			hostPort = m[1]
		}

		// 解析目标端口
		destRe := regexp.MustCompile(`to:(\S+)`)
		destPort := ""
		if m := destRe.FindStringSubmatch(ruleInfo.Stdout); len(m) > 1 {
			parts := strings.SplitN(m[1], ":", 2)
			if len(parts) > 1 {
				destPort = parts[1]
			}
		}

		// 删除 NAT 规则
		utils.ExecShell(fmt.Sprintf("iptables -t nat -D PREROUTING %d", id))

		// 删除 FORWARD 规则
		if destPort != "" {
			utils.ExecShell(fmt.Sprintf(
				"iptables -D FORWARD -d %s -p %s --dport %s -j ACCEPT 2>/dev/null",
				utils.ShellSingleQuote(targetIP), utils.ShellSingleQuote(proto), utils.ShellSingleQuote(destPort)))
		}

		// 删除 UFW 规则
		if hostPort != "" {
			_ = DeleteHostFirewallPortForwardRule(hostPort, proto)
		}
	}

	// 如果有删除规则，自动持久化
	if len(ruleIDs) > 0 {
		go SavePortForwardRules()
	}
}

func buildVMOwnerMap() map[string]string {
	owners := make(map[string]string)
	vmAccessDir := config.GlobalConfig.VMAccessDir
	lsResult := utils.ExecShell(fmt.Sprintf("ls %s 2>/dev/null", utils.ShellSingleQuote(vmAccessDir)))
	if lsResult.Error != nil || strings.TrimSpace(lsResult.Stdout) == "" {
		return owners
	}

	for _, username := range strings.Split(lsResult.Stdout, "\n") {
		username = strings.TrimSpace(username)
		if username == "" {
			continue
		}
		for _, vmName := range GetUserVMList(username) {
			vmName = strings.TrimSpace(vmName)
			if vmName != "" {
				owners[vmName] = username
			}
		}
	}

	return owners
}

func buildPortForwardTargetInfoMap() map[string]portForwardTargetInfo {
	targetMap := make(map[string]portForwardTargetInfo)
	ownerMap := buildVMOwnerMap()

	setTarget := func(ipAddr, vmName string) {
		ipAddr = strings.TrimSpace(ipAddr)
		vmName = strings.TrimSpace(vmName)
		if ipAddr == "" || vmName == "" {
			return
		}
		targetMap[ipAddr] = portForwardTargetInfo{
			VMName:        vmName,
			OwnerUsername: ownerMap[vmName],
		}
	}

	staticHosts, _ := ListStaticIPs()
	if staticHosts != nil {
		for _, item := range staticHosts.StaticBindings {
			setTarget(item.IP, item.VMName)
		}
		for _, item := range staticHosts.DHCPLeases {
			setTarget(item.IP, item.VMName)
		}
	}

	var manualIPs []model.PortForwardIP
	if err := model.DB.Find(&manualIPs).Error; err == nil {
		for _, item := range manualIPs {
			setTarget(item.IP, item.VMName)
		}
	}

	return targetMap
}

func populatePortForwardRuleMetadata(rule *PortForwardRule, targetMap map[string]portForwardTargetInfo) {
	if rule == nil {
		return
	}
	info, ok := targetMap[strings.TrimSpace(rule.DestIP)]
	if !ok {
		return
	}
	rule.VMName = info.VMName
	rule.OwnerUsername = info.OwnerUsername
}

func listLivePortForwardsFromIPTables() ([]PortForwardRule, error) {
	result := utils.ExecShell("iptables -t nat -L PREROUTING -n --line-numbers 2>/dev/null | grep DNAT")
	if result.Error != nil || result.Stdout == "" {
		return []PortForwardRule{}, nil
	}
	policy, _ := GetFirewallPolicy()
	hostIP := getHostIP()
	targetMap := buildPortForwardTargetInfoMap()

	var rules []PortForwardRule
	lines := strings.Split(result.Stdout, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}

		rule := PortForwardRule{}

		// 编号
		fmt.Sscanf(fields[0], "%d", &rule.ID)

		// 协议
		proto := fields[2]
		switch proto {
		case "6":
			rule.Protocol = "TCP"
		case "17":
			rule.Protocol = "UDP"
		default:
			rule.Protocol = strings.ToUpper(proto)
		}

		// 宿主机端口
		dportRe := regexp.MustCompile(`dpts?:(\S+)`)
		if m := dportRe.FindStringSubmatch(line); len(m) > 1 {
			rule.HostPort = m[1]
		}
		rule.AccessIP = hostIP
		rule.AccessAddress = buildPortForwardAccessAddress(hostIP, rule.HostPort)

		// 目标
		destRe := regexp.MustCompile(`to:(\S+)`)
		if m := destRe.FindStringSubmatch(line); len(m) > 1 {
			dest := m[1]
			parts := strings.SplitN(dest, ":", 2)
			rule.DestIP = parts[0]
			if len(parts) > 1 {
				rule.DestPort = parts[1]
			}
		}
		rule.FirewallKey = rule.StableKey()
		rule.RuleKey = rule.StableKey()
		rule.Live = true
		rule.RegionFilterInherited = true
		rule.RegionFilterEnabled = true
		if policy != nil && policy.PortForwardExemptions != nil && policy.PortForwardExemptions[rule.FirewallKey] {
			rule.RegionFilterEnabled = false
			rule.RegionFilterInherited = false
		}
		populatePortForwardRuleMetadata(&rule, targetMap)

		rules = append(rules, rule)
	}

	return rules, nil
}

// ListPortForwards 列出端口转发规则
func ListPortForwards() ([]PortForwardRule, error) {
	rules, err := listLivePortForwardsFromIPTables()
	if err != nil {
		return nil, err
	}
	return MergePortForwardProbeState(rules), nil
}

// GetPortForwardRuleByID 根据当前 iptables 行号获取端口转发规则。
func GetPortForwardRuleByID(ruleID int) (*PortForwardRule, error) {
	rules, err := listLivePortForwardsFromIPTables()
	if err != nil {
		return nil, err
	}
	for i := range rules {
		if rules[i].ID == ruleID {
			rule := rules[i]
			return &rule, nil
		}
	}
	return nil, fmt.Errorf("规则编号 %d 不存在", ruleID)
}

func findLivePortForwardByStableKey(ruleKey string) (*PortForwardRule, error) {
	rules, err := listLivePortForwardsFromIPTables()
	if err != nil {
		return nil, err
	}
	for i := range rules {
		if rules[i].StableKey() == strings.TrimSpace(ruleKey) {
			rule := rules[i]
			return &rule, nil
		}
	}
	return nil, nil
}

// GetUserPortForwardUsage 获取用户当前已使用的端口转发数量。
func GetUserPortForwardUsage(username string) int {
	rules, err := listLivePortForwardsFromIPTables()
	if err != nil {
		return 0
	}
	count := 0
	for _, rule := range rules {
		if strings.TrimSpace(rule.OwnerUsername) == strings.TrimSpace(username) {
			count++
		}
	}
	return count
}

// CheckUserPortForwardFeatureEnabled 检查用户是否允许使用端口转发功能。
func CheckUserPortForwardFeatureEnabled(username string) error {
	var user model.User
	if err := model.DB.Where("username = ?", username).First(&user).Error; err != nil {
		return fmt.Errorf("用户不存在: %w", err)
	}
	if user.Role == "admin" {
		return nil
	}
	if !user.EnablePortForward {
		return fmt.Errorf("当前用户未开通端口转发功能")
	}
	return nil
}

// CheckUserPortForwardQuota 检查用户端口转发数量配额。
func CheckUserPortForwardQuota(username string, delta int) error {
	if delta <= 0 {
		return nil
	}

	var user model.User
	if err := model.DB.Where("username = ?", username).First(&user).Error; err != nil {
		return fmt.Errorf("用户不存在: %w", err)
	}
	if user.Role == "admin" || user.MaxPortForwards <= 0 {
		return nil
	}

	used := GetUserPortForwardUsage(username)
	if used+delta > user.MaxPortForwards {
		return fmt.Errorf("端口转发数量超出配额限制（已用 %d / 上限 %d）", used, user.MaxPortForwards)
	}
	return nil
}

func buildPortForwardAccessAddress(hostIP, hostPort string) string {
	hostIP = strings.TrimSpace(hostIP)
	hostPort = strings.TrimSpace(hostPort)
	if hostIP == "" && hostPort == "" {
		return ""
	}
	if hostIP == "" {
		return hostPort
	}
	if hostPort == "" {
		return hostIP
	}
	return hostIP + ":" + hostPort
}

// GetConfiguredPortForwardHostIP 返回端口转发对外展示使用的 IP。
func GetConfiguredPortForwardHostIP() string {
	return getHostIP()
}

// BuildPortForwardAccessAddressForMessage 生成端口转发对外访问地址文本。
func BuildPortForwardAccessAddressForMessage(hostPort string) string {
	return buildPortForwardAccessAddress(getHostIP(), hostPort)
}

// AddPortForward 添加端口转发（内置端口冲突检测）
func AddPortForward(params *PortForwardAddParams) error {
	if err := EnsureOVSNetworkReady(); err != nil {
		return err
	}
	if params.VMPort == "" {
		params.VMPort = params.HostPort
	}
	if err := CheckRequestedPortForwardHostPortAvailable(params.HostPort, params.Protocol, nil); err != nil {
		return err
	}
	if params.Protocol == "" {
		params.Protocol = "tcp"
	}

	protocols := []string{params.Protocol}
	if params.Protocol == "both" {
		protocols = []string{"tcp", "udp"}
	}

	// 端口冲突检测：无论自动分配还是手动指定都要检查
	for _, proto := range protocols {
		available, reason := IsPortAvailable(params.HostPort, proto)
		if !available {
			return fmt.Errorf("宿主机端口 %s/%s 已被占用: %s", params.HostPort, proto, reason)
		}
	}

	hostIP := getHostIP()

	for _, proto := range protocols {
		// 目标端口格式转换
		destPort := strings.Replace(params.VMPort, ":", "-", 1)

		// DNAT 规则
		cmd := fmt.Sprintf("iptables -t nat -A PREROUTING -d %s -p %s --dport %s -j DNAT --to-destination %s:%s",
			utils.ShellSingleQuote(hostIP), utils.ShellSingleQuote(proto), utils.ShellSingleQuote(params.HostPort), utils.ShellSingleQuote(params.VMIP), destPort)
		result := utils.ExecShell(cmd)
		if result.Error != nil {
			return fmt.Errorf("添加 %s NAT 规则失败: %s", proto, result.Stderr)
		}

		// 非 VPC 转发继续使用传统 FORWARD 放行；VPC 转发必须经过安全组 ACL。
		if !IsVPCManagedIP(params.VMIP) {
			fwdCmd := fmt.Sprintf("iptables -I FORWARD -d %s -p %s --dport %s -j ACCEPT",
				utils.ShellSingleQuote(params.VMIP), utils.ShellSingleQuote(proto), destPort)
			utils.ExecShell(fwdCmd)
		}

		// 无论宿主机防火墙当前是否启用，都写入 UFW 持久规则，避免下次开启后拦截已有端口转发。
		if err := EnsureHostFirewallPortForwardRule(params.HostPort, proto, params.Comment); err != nil {
			return err
		}
		SyncPortForwardProbeStateOnAdd(params, proto, FindVMOwner(strings.TrimSpace(params.Comment)))
	}

	// 自动持久化规则
	go SavePortForwardRules()

	return nil
}

func stripIPTablesCIDR(value string) string {
	value = strings.TrimSpace(value)
	if idx := strings.Index(value, "/"); idx >= 0 {
		return value[:idx]
	}
	return value
}

func iptablesArgValue(args []string, key string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == key {
			return args[i+1]
		}
	}
	return ""
}

func RemoveVPCPortForwardAcceptRules() {
	result := utils.ExecShell("iptables -S FORWARD 2>/dev/null | grep -- '-j ACCEPT' | grep -- '-d ' | grep -- '--dport '")
	if result.Error != nil || strings.TrimSpace(result.Stdout) == "" {
		return
	}
	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		args := strings.Fields(line)
		if len(args) < 3 || args[0] != "-A" || args[1] != "FORWARD" {
			continue
		}
		destIP := stripIPTablesCIDR(iptablesArgValue(args, "-d"))
		if !IsVPCManagedIP(destIP) {
			continue
		}
		args[0] = "-D"
		utils.ExecCommand("iptables", args...)
	}
}

func removePortForwardsForCIDR(cidr string) {
	rules, err := listLivePortForwardsFromIPTables()
	if err != nil || len(rules) == 0 {
		return
	}
	var ids []int
	for _, rule := range rules {
		if IPInCIDR(rule.DestIP, cidr) {
			ids = append(ids, rule.ID)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(ids)))
	for _, id := range ids {
		if err := DeletePortForward(id); err != nil {
			fmt.Printf("[警告] 删除 VPC CIDR %s 的端口转发规则 %d 失败: %v\n", cidr, id, err)
		}
	}
}

func cleanupOVSStaticHostsForVMs(vmNames []string) {
	if len(vmNames) == 0 {
		return
	}
	vmSet := make(map[string]bool, len(vmNames))
	for _, vmName := range vmNames {
		vmName = strings.TrimSpace(vmName)
		if vmName != "" {
			vmSet[vmName] = true
		}
	}
	if len(vmSet) == 0 {
		return
	}
	hosts, err := ListOVSStaticHosts()
	if err != nil || len(hosts) == 0 {
		return
	}
	next := make([]OVSStaticHost, 0, len(hosts))
	changed := false
	for _, host := range hosts {
		if vmSet[strings.TrimSpace(host.VMName)] {
			removePortForwardsForIP(host.IP)
			changed = true
			continue
		}
		next = append(next, host)
	}
	if !changed {
		return
	}
	if err := writeOVSStaticHosts(next); err != nil {
		fmt.Printf("[警告] 清理 OVS 静态 IP 绑定失败: %v\n", err)
		return
	}
	ReloadOVSDNSMasq()
}

// getOccupiedPorts 获取所有被占用的端口集合（TCP/UDP监听 + iptables DNAT）
func getOccupiedPorts() map[int]bool {
	usedPorts := make(map[int]bool)

	// TCP 监听端口
	tcpResult := utils.ExecShell(`ss -tlnH 2>/dev/null | awk '{print $4}' | grep -oP '\d+$' | sort -un`)
	for _, line := range strings.Split(tcpResult.Stdout, "\n") {
		if p := strings.TrimSpace(line); p != "" {
			var port int
			fmt.Sscanf(p, "%d", &port)
			usedPorts[port] = true
		}
	}

	// UDP 监听端口
	udpResult := utils.ExecShell(`ss -ulnH 2>/dev/null | awk '{print $4}' | grep -oP '\d+$' | sort -un`)
	for _, line := range strings.Split(udpResult.Stdout, "\n") {
		if p := strings.TrimSpace(line); p != "" {
			var port int
			fmt.Sscanf(p, "%d", &port)
			usedPorts[port] = true
		}
	}

	// iptables DNAT 已用端口
	iptResult := utils.ExecShell(`iptables -t nat -L PREROUTING -n 2>/dev/null | grep DNAT | grep -oP 'dpts?:\K\S+'`)
	for _, line := range strings.Split(iptResult.Stdout, "\n") {
		if p := strings.TrimSpace(line); p != "" {
			var port int
			fmt.Sscanf(p, "%d", &port)
			usedPorts[port] = true
		}
	}

	return usedPorts
}

func normalizePortForwardProtocols(protocol string) ([]string, error) {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	if protocol == "" {
		return []string{"tcp"}, nil
	}
	switch protocol {
	case "tcp", "udp":
		return []string{protocol}, nil
	case "both":
		return []string{"tcp", "udp"}, nil
	default:
		return nil, fmt.Errorf("不支持的端口转发协议: %s", protocol)
	}
}

func buildPortForwardAvailabilityExcludeSet(hostPort string, protocols []string, currentRule *PortForwardRule) map[string]struct{} {
	if currentRule == nil {
		return nil
	}
	if strings.TrimSpace(hostPort) != strings.TrimSpace(currentRule.HostPort) {
		return nil
	}

	currentProtocol := strings.ToLower(strings.TrimSpace(currentRule.Protocol))
	for _, protocol := range protocols {
		if protocol == currentProtocol {
			return map[string]struct{}{
				currentRule.StableKey(): {},
			}
		}
	}
	return nil
}

func canBindHostPort(protocol string, port int) bool {
	address := ":" + strconv.Itoa(port)
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "udp":
		conn, err := net.ListenPacket("udp", address)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	default:
		listener, err := net.Listen("tcp", address)
		if err != nil {
			return false
		}
		_ = listener.Close()
		return true
	}
}

func detectHostPortListener(protocol string, port int) (bool, string) {
	protocol = strings.ToLower(strings.TrimSpace(protocol))

	ssFlag := "-tlnH"
	ssProcessFlag := "-tlpnH"
	if protocol == "udp" {
		ssFlag = "-ulnH"
		ssProcessFlag = "-ulpnH"
	}

	ssResult := utils.ExecShell(fmt.Sprintf(
		"ss %s 2>/dev/null | awk '{print $4}' | grep -P ':%d$'", ssFlag, port))
	if strings.TrimSpace(ssResult.Stdout) != "" {
		procResult := utils.ExecShell(fmt.Sprintf(
			"ss %s 2>/dev/null | grep ':%d ' | head -1",
			ssProcessFlag, port))
		procInfo := strings.TrimSpace(procResult.Stdout)
		if procInfo != "" {
			procRe := regexp.MustCompile(`users:\(\("([^"]+)"`)
			if matches := procRe.FindStringSubmatch(procInfo); len(matches) > 1 {
				return true, fmt.Sprintf("端口被宿主机服务 \"%s\" 占用（%s/%d）", matches[1], protocol, port)
			}
		}
		return true, fmt.Sprintf("端口已被宿主机监听占用（%s/%d）", protocol, port)
	}

	if canListenOnHostPort != nil && !canListenOnHostPort(protocol, port) {
		return true, fmt.Sprintf("端口已被宿主机监听占用（%s/%d）", protocol, port)
	}

	return false, ""
}

func isPortAvailableWithExclusions(portStr string, protocol string, excludeRuleKeys map[string]struct{}) (bool, string) {
	port, err := strconv.Atoi(strings.TrimSpace(portStr))
	if err != nil || port <= 0 || port > 65535 {
		return false, "端口格式无效"
	}

	protocol = strings.ToLower(strings.TrimSpace(protocol))
	if protocol == "" {
		protocol = "tcp"
	}

	if listPortForwardRulesForAvailability != nil {
		if rules, err := listPortForwardRulesForAvailability(); err == nil {
			for _, rule := range rules {
				if strings.ToLower(strings.TrimSpace(rule.Protocol)) != protocol {
					continue
				}
				if strings.TrimSpace(rule.HostPort) != strconv.Itoa(port) {
					continue
				}
				if _, excluded := excludeRuleKeys[rule.StableKey()]; excluded {
					continue
				}
				return false, fmt.Sprintf("已存在端口转发规则（%s/%s -> %s:%s）", rule.Protocol, rule.HostPort, rule.DestIP, rule.DestPort)
			}
		}
	}

	if occupied, reason := detectHostPortListener(protocol, port); occupied {
		return false, reason
	}

	return true, ""
}

// IsPortAvailable 检查指定端口是否可用（未被系统服务、其他进程或 iptables 占用）
// 返回 (是否可用, 占用原因)
func IsPortAvailable(portStr string, protocol string) (bool, string) {
	return isPortAvailableWithExclusions(portStr, protocol, nil)
}

// CheckRequestedPortForwardHostPortAvailable 检查用户手动指定的宿主机端口是否可用。
// currentRule 仅用于编辑场景忽略当前规则自身，避免同端口更新时误判冲突。
func CheckRequestedPortForwardHostPortAvailable(hostPort, protocol string, currentRule *PortForwardRule) error {
	hostPort = strings.TrimSpace(hostPort)
	if hostPort == "" {
		return nil
	}
	if strings.TrimSpace(protocol) == "" && currentRule != nil {
		protocol = currentRule.Protocol
	}

	protocols, err := normalizePortForwardProtocols(protocol)
	if err != nil {
		return err
	}
	excludeRuleKeys := buildPortForwardAvailabilityExcludeSet(hostPort, protocols, currentRule)
	for _, proto := range protocols {
		available, reason := isPortAvailableWithExclusions(hostPort, proto, excludeRuleKeys)
		if !available {
			return fmt.Errorf("宿主机端口 %s/%s 已被占用: %s", hostPort, proto, reason)
		}
	}
	return nil
}

// AutoAllocatePort 自动分配端口（包含 TCP+UDP 全面检测）
func AutoAllocatePort() (int, error) {
	start := config.GlobalConfig.AutoPortStart
	end := config.GlobalConfig.AutoPortEnd

	// 获取所有被占用的端口
	usedPorts := getOccupiedPorts()

	for port := start; port <= end; port++ {
		if !usedPorts[port] {
			return port, nil
		}
	}

	return 0, fmt.Errorf("范围 %d-%d 内没有可用端口", start, end)
}

// DeletePortForward 按编号删除端口转发规则
func deletePortForwardWithOptions(ruleID int, preserveProbeState bool) error {
	// 直接用 iptables 行号获取规则信息（不过滤 grep，避免行号错位）
	ruleInfo := utils.ExecShell(fmt.Sprintf(
		"iptables -t nat -L PREROUTING %d -n 2>/dev/null", ruleID))
	if ruleInfo.Error != nil || ruleInfo.Stdout == "" {
		return fmt.Errorf("规则编号 %d 不存在", ruleID)
	}
	if !strings.Contains(ruleInfo.Stdout, "DNAT") {
		return fmt.Errorf("规则编号 %d 不是端口转发规则", ruleID)
	}

	// 解析目标信息用于删除 FORWARD 规则
	destRe := regexp.MustCompile(`to:(\S+)`)
	var destIP, destPort string
	if m := destRe.FindStringSubmatch(ruleInfo.Stdout); len(m) > 1 {
		parts := strings.SplitN(m[1], ":", 2)
		destIP = parts[0]
		if len(parts) > 1 {
			destPort = parts[1]
		}
	}

	dportRe := regexp.MustCompile(`dpts?:(\S+)`)
	var hostPort string
	if m := dportRe.FindStringSubmatch(ruleInfo.Stdout); len(m) > 1 {
		hostPort = m[1]
	}

	protoRe := regexp.MustCompile(`\s+(tcp|udp|6|17)\s+`)
	proto := "tcp"
	if m := protoRe.FindStringSubmatch(ruleInfo.Stdout); len(m) > 1 {
		switch m[1] {
		case "6":
			proto = "tcp"
		case "17":
			proto = "udp"
		default:
			proto = m[1]
		}
	}
	stableKey := PortForwardRule{
		Protocol: strings.ToLower(proto),
		HostPort: hostPort,
		DestIP:   destIP,
		DestPort: destPort,
	}.StableKey()

	// 删除 NAT 规则
	utils.ExecShell(fmt.Sprintf("iptables -t nat -D PREROUTING %d", ruleID))

	// 删除 FORWARD 规则
	if destIP != "" && destPort != "" {
		utils.ExecShell(fmt.Sprintf(
			"iptables -D FORWARD -d %s -p %s --dport %s -j ACCEPT 2>/dev/null",
			utils.ShellSingleQuote(destIP), utils.ShellSingleQuote(proto), utils.ShellSingleQuote(destPort)))
	}

	// 删除 UFW 规则
	if hostPort != "" {
		_ = DeleteHostFirewallPortForwardRule(hostPort, proto)
	}
	_ = ClearPortForwardFirewallExemption(stableKey)
	SyncPortForwardProbeStateOnDelete(stableKey, preserveProbeState)

	cleanupErr := RemoveSecurityGroupAllowsPortForwardIfUnused(destIP, proto, destPort)

	// 自动持久化规则
	go SavePortForwardRules()

	return cleanupErr
}

// DeletePortForward 按编号删除端口转发规则
func DeletePortForward(ruleID int) error {
	return deletePortForwardWithOptions(ruleID, false)
}

func deleteLivePortForwardByStableKey(ruleKey string, preserveProbeState bool) error {
	rule, err := findLivePortForwardByStableKey(ruleKey)
	if err != nil {
		return err
	}
	if rule == nil {
		return fmt.Errorf("规则 %s 不存在", ruleKey)
	}
	return deletePortForwardWithOptions(rule.ID, preserveProbeState)
}

// DeletePortForwards 按批量删除端口转发规则。
func DeletePortForwards(ruleIDs []int) error {
	if len(ruleIDs) == 0 {
		return nil
	}

	unique := make(map[int]struct{})
	var ids []int
	for _, id := range ruleIDs {
		if id <= 0 {
			continue
		}
		if _, exists := unique[id]; exists {
			continue
		}
		unique[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(ids)))

	for _, id := range ids {
		if err := DeletePortForward(id); err != nil {
			return err
		}
	}
	return nil
}

func normalizeEditablePortForwardProtocol(protocol string) (string, error) {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	if protocol == "" {
		return "tcp", nil
	}
	switch protocol {
	case "tcp", "udp":
		return protocol, nil
	default:
		return "", fmt.Errorf("编辑端口转发仅支持单协议 tcp 或 udp")
	}
}

// UpdatePortForward 编辑单条端口转发规则。
func UpdatePortForward(ruleID int, params *PortForwardUpdateParams) error {
	if params == nil {
		return fmt.Errorf("更新参数不能为空")
	}

	oldRule, err := GetPortForwardRuleByID(ruleID)
	if err != nil {
		return err
	}
	oldState, _ := getPortForwardProbeStateByRuleKey(oldRule.StableKey())

	oldProtocol := strings.ToLower(strings.TrimSpace(oldRule.Protocol))
	newProtocol := params.Protocol
	if strings.TrimSpace(newProtocol) == "" {
		newProtocol = oldProtocol
	}
	newProtocol, err = normalizeEditablePortForwardProtocol(newProtocol)
	if err != nil {
		return err
	}

	hostPort := strings.TrimSpace(params.HostPort)
	if hostPort == "" {
		hostPort = strings.TrimSpace(oldRule.HostPort)
	}
	vmIP := strings.TrimSpace(params.VMIP)
	if vmIP == "" {
		vmIP = strings.TrimSpace(oldRule.DestIP)
	}
	vmPort := strings.TrimSpace(params.VMPort)
	if vmPort == "" {
		vmPort = strings.TrimSpace(oldRule.DestPort)
	}
	comment := strings.TrimSpace(params.Comment)
	if comment == "" {
		comment = strings.TrimSpace(oldRule.VMName)
	}
	if comment == "" {
		comment = "port-forward"
	}

	oldPolicy, _ := GetFirewallPolicy()
	oldExempt := oldPolicy != nil && oldPolicy.PortForwardExemptions[oldRule.FirewallKey]
	rollbackParams := &PortForwardAddParams{
		VMIP:           oldRule.DestIP,
		HostPort:       oldRule.HostPort,
		VMPort:         oldRule.DestPort,
		Protocol:       oldProtocol,
		Comment:        comment,
		CreatedBy:      strings.TrimSpace(params.CreatedBy),
		CreatedByAdmin: params.CreatedByAdmin,
	}
	if oldState != nil {
		if strings.TrimSpace(rollbackParams.CreatedBy) == "" {
			rollbackParams.CreatedBy = strings.TrimSpace(oldState.CreatedBy)
		}
		if !rollbackParams.CreatedByAdmin {
			rollbackParams.CreatedByAdmin = oldState.CreatedByAdmin
		}
	}

	if err := DeletePortForward(ruleID); err != nil {
		return err
	}

	addParams := &PortForwardAddParams{
		VMIP:           vmIP,
		HostPort:       hostPort,
		VMPort:         vmPort,
		Protocol:       newProtocol,
		Comment:        comment,
		CreatedBy:      strings.TrimSpace(params.CreatedBy),
		CreatedByAdmin: params.CreatedByAdmin,
	}
	if oldState != nil {
		if strings.TrimSpace(addParams.CreatedBy) == "" {
			addParams.CreatedBy = strings.TrimSpace(oldState.CreatedBy)
		}
		if !addParams.CreatedByAdmin {
			addParams.CreatedByAdmin = oldState.CreatedByAdmin
		}
	}
	if err := AddPortForward(addParams); err != nil {
		restoreErr := AddPortForward(rollbackParams)
		if restoreErr == nil && oldExempt {
			_, _ = SetPortForwardFirewallExemption(oldRule.FirewallKey, true)
		}
		if restoreErr != nil {
			return fmt.Errorf("更新端口转发失败，且恢复原规则失败: %v；原始错误: %w", restoreErr, err)
		}
		return fmt.Errorf("更新端口转发失败，已恢复原规则: %w", err)
	}

	if oldExempt {
		newRule := PortForwardRule{
			Protocol: newProtocol,
			HostPort: hostPort,
			DestIP:   vmIP,
			DestPort: vmPort,
		}
		if _, err := SetPortForwardFirewallExemption(newRule.StableKey(), true); err != nil {
			return fmt.Errorf("端口转发已更新，但恢复入站区域限制豁免失败: %w", err)
		}
	}

	return nil
}

func iptablesCheckLineForAddLine(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "iptables ") {
		return "", false
	}
	idx := strings.Index(line, " -A ")
	if idx < 0 {
		return "", false
	}
	return line[:idx] + " -C " + line[idx+4:], true
}

func idempotentIPTablesAddLine(line string) string {
	line = normalizePortForwardIPTablesLine(strings.TrimSpace(line))
	checkLine, ok := iptablesCheckLineForAddLine(line)
	if !ok {
		return line
	}
	return checkLine + " 2>/dev/null || " + line
}

func normalizePortForwardIPTablesLine(line string) string {
	line = strings.TrimSpace(line)
	if !strings.Contains(line, " PREROUTING") || !strings.Contains(line, " DNAT") || strings.Contains(line, " -t nat ") {
		return line
	}
	replacer := strings.NewReplacer(
		"iptables -A PREROUTING", "iptables -t nat -A PREROUTING",
		"iptables -C PREROUTING", "iptables -t nat -C PREROUTING",
		"iptables -D PREROUTING", "iptables -t nat -D PREROUTING",
	)
	return replacer.Replace(line)
}

func restorePortForwardCommand(line, hostIP string) error {
	line = normalizePortForwardIPTablesLine(strings.TrimSpace(line))
	if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "HOST_IP=") || strings.HasPrefix(line, "#!") {
		return nil
	}
	if strings.Contains(line, "||") {
		result := utils.ExecShell("HOST_IP=" + utils.ShellSingleQuote(hostIP) + "; " + line)
		if result.Error != nil {
			return fmt.Errorf("%s: %s", line, result.Stderr)
		}
		return nil
	}
	if !strings.HasPrefix(line, "iptables ") {
		return nil
	}
	checkLine, ok := iptablesCheckLineForAddLine(line)
	if !ok {
		return nil
	}
	prefix := "HOST_IP=" + utils.ShellSingleQuote(hostIP) + "; "
	if result := utils.ExecShell(prefix + checkLine); result.Error == nil {
		return nil
	}
	result := utils.ExecShell(prefix + line)
	if result.Error != nil {
		return fmt.Errorf("%s: %s", line, result.Stderr)
	}
	return nil
}

// RestorePortForwardRules 从持久化脚本恢复端口转发规则。
func RestorePortForwardRules() error {
	if err := EnsureOVSNetworkReady(); err != nil {
		return err
	}
	portfwdDir := config.GlobalConfig.PortForwardDir
	rulesPath := portfwdDir + "/rules.sh"
	data, err := os.ReadFile(rulesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("读取端口转发持久化规则失败: %w", err)
	}
	hostIP := getHostIP()
	var lastErr error
	restored := 0
	for _, line := range strings.Split(string(data), "\n") {
		if err := restorePortForwardCommand(line, hostIP); err != nil {
			lastErr = err
			fmt.Printf("[警告] 恢复端口转发规则失败: %v\n", err)
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "iptables ") {
			restored++
		}
	}
	if restored > 0 && lastErr == nil {
		if err := SavePortForwardRules(); err != nil {
			fmt.Printf("[警告] 重写端口转发持久化规则失败: %v\n", err)
		}
	}
	return lastErr
}

// SavePortForwardRules 持久化端口转发规则
func SavePortForwardRules() error {
	portfwdDir := config.GlobalConfig.PortForwardDir
	utils.ExecShell(fmt.Sprintf("mkdir -p '%s/backups'", portfwdDir))

	hostIP := getHostIP()

	// 备份
	utils.ExecShell(fmt.Sprintf(
		"[ -f %s/rules.sh ] && cp %s/rules.sh %s/backups/rules.sh.$(date +%%Y%%m%%d_%%H%%M%%S)",
		utils.ShellSingleQuote(portfwdDir), utils.ShellSingleQuote(portfwdDir), utils.ShellSingleQuote(portfwdDir)))

	// 只保留最近 10 个备份
	utils.ExecShell(fmt.Sprintf(
		"ls -t %s/backups/rules.sh.* 2>/dev/null | tail -n +11 | xargs rm -f 2>/dev/null",
		utils.ShellSingleQuote(portfwdDir)))

	// 导出规则
	script := fmt.Sprintf("#!/bin/bash\n# KVM 端口转发规则 - 自动生成\nHOST_IP=\"%s\"\n\n", hostIP)

	// DNAT 规则
	script += "# === DNAT 转发规则 ===\n"
	dnatResult := utils.ExecShell("iptables -t nat -S PREROUTING 2>/dev/null | grep DNAT")
	if dnatResult.Stdout != "" {
		for _, line := range strings.Split(dnatResult.Stdout, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				script += idempotentIPTablesAddLine("iptables -t nat "+line) + "\n"
			}
		}
	}

	// FORWARD 规则
	script += "\n# === FORWARD 放行规则 ===\n"
	fwdResult := utils.ExecShell("iptables -S FORWARD 2>/dev/null | grep -- '-j ACCEPT' | grep -- '-d ' | grep -- '--dport '")
	if fwdResult.Stdout != "" {
		for _, line := range strings.Split(fwdResult.Stdout, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				args := strings.Fields(line)
				destIP := stripIPTablesCIDR(iptablesArgValue(args, "-d"))
				if IsVPCManagedIP(destIP) {
					continue
				}
				script += idempotentIPTablesAddLine("iptables "+line) + "\n"
			}
		}
	}

	rulesPath := portfwdDir + "/rules.sh"
	writeResult := utils.ExecShell(fmt.Sprintf("cat > %s << 'RULESEOF'\n%s\nRULESEOF\nchmod +x %s",
		utils.ShellSingleQuote(rulesPath), script, utils.ShellSingleQuote(rulesPath)))
	if writeResult.Error != nil {
		return fmt.Errorf("保存规则失败: %s", writeResult.Stderr)
	}

	return nil
}

// GetUFWStatus 获取 UFW 状态
func GetUFWStatus() (string, error) {
	result := utils.ExecCommand("ufw", "status", "numbered")
	if result.Error != nil {
		return "", fmt.Errorf("获取 UFW 状态失败: %s", result.Stderr)
	}
	return result.Stdout, nil
}

// ManageUFWRule 管理 UFW 规则
func ManageUFWRule(action, rule string) error {
	var cmd string
	switch action {
	case "allow":
		cmd = fmt.Sprintf("ufw allow %s", rule)
	case "deny":
		cmd = fmt.Sprintf("ufw deny %s", rule)
	case "delete":
		cmd = fmt.Sprintf("ufw delete %s", rule)
	default:
		return fmt.Errorf("不支持的操作: %s", action)
	}

	result := utils.ExecShell(cmd)
	if result.Error != nil {
		return fmt.Errorf("UFW 操作失败: %s", result.Stderr)
	}
	return nil
}

// getHostIP 获取宿主机外网 IP
func getHostIP() string {
	// 优先使用配置的固定 IP
	if config.GlobalConfig.HostIP != "" {
		return config.GlobalConfig.HostIP
	}

	// 使用配置的外网网卡名称
	nic := config.GlobalConfig.ExternalNIC
	if nic != "" {
		result := utils.ExecShell(fmt.Sprintf(
			"ip -4 addr show %s 2>/dev/null | grep -oP '(?<=inet\\s)\\d+\\.\\d+\\.\\d+\\.\\d+'", utils.ShellSingleQuote(nic)))
		if result.Error == nil && result.Stdout != "" {
			return strings.TrimSpace(result.Stdout)
		}
	}

	// 自动检测：通过默认路由获取外网网卡 IP
	result := utils.ExecShell(
		"ip -4 route get 8.8.8.8 2>/dev/null | grep -oP 'src \\K\\S+'")
	if result.Error == nil && result.Stdout != "" {
		return strings.TrimSpace(result.Stdout)
	}

	return "0.0.0.0"
}
