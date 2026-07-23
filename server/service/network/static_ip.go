package network

import (
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"kvm_console/logger"
	"kvm_console/model"
	"kvm_console/service/ip_resolver"
	"kvm_console/service/libvirt_rpc"
	"kvm_console/utils"
)

// ListStaticIPs 列出静态 IP 绑定
func ListStaticIPs() (*IPListInfo, error) {
	info := &IPListInfo{}

	staticHosts, err := HookListOVSStaticHosts()
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
	if vpcStaticHosts, vpcErr := HookListAllVPCStaticHosts(); vpcErr == nil {
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
	domains, err := libvirt_rpc.ListAllDomainsRPC()
	if err == nil {
		for _, dom := range domains {
			vmName := dom.Name
			if vmName == "" {
				continue
			}
			domXML, xmlErr := libvirt_rpc.GetDomainXMLRPC(vmName, 0)
			if xmlErr != nil {
				continue
			}
			ifaces := libvirt_rpc.ParseInterfacesFromDomainXML(domXML)
			for _, iface := range ifaces {
				if iface.MAC != "" {
					macToVMName[strings.ToLower(iface.MAC)] = vmName
				}
			}
		}
	}

	leases, err := HookListOVSDHCPLeases()
	if err != nil {
		leases = []OVSDHCPLease{}
	}
	if vpcLeases, vpcErr := HookListVPCDHCPLeases(); vpcErr == nil {
		leases = append(leases, vpcLeases...)
	}
	leaseMap := make(map[string]OVSDHCPLease)
	for _, lease := range leases {
		mac := strings.ToLower(lease.MAC)
		leaseMap[mac] = HookNewerOVSDHCPLease(leaseMap[mac], lease)
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
	subnet := HookOvsSubnetPrefix()

	// 收集所有已占用的 IP（静态绑定 + DHCP 租约）
	usedIPs := make(map[int]bool)
	// .1 是网关，始终标记为已占用
	usedIPs[1] = true

	staticHosts, _ := HookListOVSStaticHosts()
	for _, host := range staticHosts {
		parts := strings.Split(host.IP, ".")
		if len(parts) == 4 {
			if lastOctet, err := strconv.Atoi(parts[3]); err == nil {
				usedIPs[lastOctet] = true
			}
		}
	}

	leases, _ := HookListOVSDHCPLeases()
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
	staticHosts, _ := HookListVPCStaticHosts(sw.ID)
	for _, host := range staticHosts {
		used[host.IP] = true
	}
	leases, _ := HookListVPCDHCPLeasesForSwitch(sw.ID)
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
	if !ipInCIDR(ipAddr, sw.CIDR) {
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

// UpsertVPCStaticHost 插入或更新 VPC 静态绑定
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
	hosts, err := HookListVPCStaticHosts(sw.ID)
	if err != nil {
		return err
	}
	next, err := HookBuildOVSStaticHostsForUpsert(hosts, OVSStaticHost{VMName: vmName, MAC: mac, IP: ipAddr})
	if err != nil {
		return err
	}
	if err := HookWriteVPCStaticHosts(sw.ID, next); err != nil {
		return fmt.Errorf("写入 VPC 静态 IP 绑定失败: %w", err)
	}
	HookCleanVPCDHCPLease(sw.ID, mac, ipAddr)
	HookCleanOVSDHCPLease(mac, "")
	HookReloadVPCDNSMasq(sw.ID)
	return nil
}

// RemoveVPCStaticHost 删除 VPC 静态绑定
func RemoveVPCStaticHost(switchID uint, vmName, mac string) (string, error) {
	hosts, err := HookListVPCStaticHosts(switchID)
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
	if err := HookWriteVPCStaticHosts(switchID, next); err != nil {
		return "", fmt.Errorf("删除 VPC 静态 IP 绑定失败: %w", err)
	}
	HookReloadVPCDNSMasq(switchID)
	return removedIP, nil
}

// GetVPCStaticIPByMAC 通过 MAC 查找 VPC 静态绑定的 IP
func GetVPCStaticIPByMAC(switchID uint, mac string) string {
	hosts, err := HookListVPCStaticHosts(switchID)
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

// GetVPCStaticHostByVMName 通过 VM 名称查找 VPC 静态绑定
func GetVPCStaticHostByVMName(switchID uint, vmName string) (OVSStaticHost, bool) {
	hosts, err := HookListVPCStaticHosts(switchID)
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
	mac := firstNICMAC(vmName)
	if mac == "" {
		return "", fmt.Errorf("无法获取虚拟机 %s 的 MAC 地址", vmName)
	}
	if sw, ok := HookGetVPCSwitchForVM(vmName); ok {
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
		if ip := HookGetVPCLeaseIPForVM(vmName); ip != "" {
			if err := UpsertVPCStaticHost(*sw, vmName, mac, ip); err != nil {
				return "", fmt.Errorf("固定当前 VPC DHCP 地址失败: %w", err)
			}
			return ip, nil
		}
		if ip := ip_resolver.GetHostNeighborIPByMAC(mac, sw.CIDR, true); ip != "" {
			if err := UpsertVPCStaticHost(*sw, vmName, mac, ip); err != nil {
				return "", fmt.Errorf("固定当前 VPC 邻居表地址失败: %w", err)
			}
			return ip, nil
		}
		return BindStaticIP(vmName, "")
	}

	// 如果同一 VM 曾绑定过静态 IP，但用户修改了 MAC，则保留原 IP 并迁移到当前 MAC。
	if host, ok := HookGetOVSStaticHostByVMName(vmName); ok {
		if !strings.EqualFold(host.MAC, mac) {
			if err := HookUpsertOVSStaticHost(vmName, mac, host.IP); err != nil {
				return "", fmt.Errorf("同步静态 IP 绑定到当前 MAC 失败: %w", err)
			}
			refreshNIC(vmName, mac, "")
		}
		return host.IP, nil
	}

	// 检查当前 MAC 是否已有静态绑定
	if ip := HookGetOVSStaticIPByMAC(mac); ip != "" {
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
	if sw, ok := HookGetVPCSwitchForVM(vmName); ok {
		mac := firstNICMAC(vmName)
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
		if ip := HookGetVPCLeaseIPForVM(vmName); ip != "" {
			if err := UpsertVPCStaticHost(*sw, vmName, mac, ip); err != nil {
				return "", fmt.Errorf("固定当前 VPC DHCP 地址失败: %w", err)
			}
			return ip, nil
		}
		if ip := ip_resolver.GetHostNeighborIPByMAC(mac, sw.CIDR, true); ip != "" {
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

// firstNICMACFromSources 是分派纯逻辑：kind=lxc 用容器 MAC，否则用 VM(libvirt) MAC。
func firstNICMACFromSources(kind, lxcMAC, vmMAC string) string {
	if strings.TrimSpace(kind) == "lxc" {
		return strings.ToLower(strings.TrimSpace(lxcMAC))
	}
	return vmMAC
}

// firstNICMAC 解析 vm_name 对应首网卡 MAC，按 VPCVMBinding.Kind 分派。
// LXC：读 LXCCache.MacAddress（即 lxc.net.0.hwaddr）；VM：走 libvirt（原 GetFirstVMMAC）。
func firstNICMAC(vmName string) string {
	vmName = strings.TrimSpace(vmName)
	var b model.VPCVMBinding
	if err := model.DB.Where("vm_name = ?", vmName).First(&b).Error; err == nil {
		if strings.TrimSpace(b.Kind) == "lxc" {
			var row model.LXCCache
			if err := model.DB.Where("name = ?", vmName).First(&row).Error; err == nil {
				return firstNICMACFromSources("lxc", row.MacAddress, "")
			}
			return ""
		}
	}
	return firstNICMACFromSources("vm", "", ip_resolver.GetFirstVMMAC(vmName))
}

// BindStaticIP 绑定静态 IP，ipAddr 为空时自动分配空闲 IP
// 返回实际绑定的 IP 地址
func BindStaticIP(vmName, ipAddr string) (string, error) {
	// 获取 MAC 地址
	mac := firstNICMAC(vmName)
	if mac == "" {
		return "", fmt.Errorf("无法获取虚拟机 %s 的 MAC 地址", vmName)
	}
	if sw, ok := HookGetVPCSwitchForVM(vmName); ok {
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
		_, _ = HookRemoveOVSStaticHost(vmName, mac)
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
		ipAddr = HookNormalizeIPForOVS(ipAddr)
	}

	// 执行绑定
	if err := HookUpsertOVSStaticHost(vmName, mac, ipAddr); err != nil {
		return "", fmt.Errorf("绑定失败: %w", err)
	}

	// 如果虚拟机正在运行，拔插网卡以强制刷新 DHCP，确保使用新 IP
	refreshNIC(vmName, mac, "")

	return ipAddr, nil
}

// refreshNIC 拔插网卡以强制刷新 DHCP（仅运行中的虚拟机）
func refreshNIC(vmName, mac, network string) {
	// LXC 容器：在容器内刷新 DHCP（无 libvirt 域）
	var b model.VPCVMBinding
	if err := model.DB.Where("vm_name = ?", vmName).First(&b).Error; err == nil && strings.TrimSpace(b.Kind) == "lxc" {
		refreshLXCContainerDHCP(vmName)
		return
	}

	state, err := libvirt_rpc.GetDomainStateRPC(vmName)
	if err != nil || state != "running" {
		return
	}

	// 获取网卡模型
	nicModel := libvirt_rpc.GetFirstVMInterfaceModelFromXML(vmName)

	if HookUseOVSNetwork() {
		var ifaceXML string
		if sw, ok := HookGetVPCSwitchForVM(vmName); ok {
			ifaceXML = HookBuildOVSInterfaceXMLWithVLAN(mac, nicModel, sw.VLANID)
			if err := libvirt_rpc.DetachDeviceFlagsRPC(vmName, ifaceXML, 1); err == nil { // VIR_DOMAIN_DEVICE_MODIFY_LIVE
				time.Sleep(1 * time.Second)
				if err := libvirt_rpc.AttachDeviceFlagsRPC(vmName, ifaceXML, 1); err == nil {
					_ = applyVPCSwitchRuntime(vmName, *sw)
				}
			}
			return
		}
		ifaceXML = HookBuildOVSInterfaceXML(mac, nicModel)
		if err := libvirt_rpc.DetachDeviceFlagsRPC(vmName, ifaceXML, 1); err == nil { // VIR_DOMAIN_DEVICE_MODIFY_LIVE
			time.Sleep(1 * time.Second)
			libvirt_rpc.AttachDeviceFlagsRPC(vmName, ifaceXML, 1)
		}
		return
	}

	// 非 OVS 环境：通过 detach/attach XML 方式拔插网卡
	detachXML := fmt.Sprintf("<interface type='network'>\n"+
		"  <mac address='%s'/>\n"+
		"  <source network='%s'/>\n"+
		"  <model type='%s'/>\n"+
		"</interface>", mac, network, nicModel)
	if err := libvirt_rpc.DetachDeviceFlagsRPC(vmName, detachXML, 1); err == nil { // VIR_DOMAIN_DEVICE_MODIFY_LIVE
		time.Sleep(1 * time.Second)
		libvirt_rpc.AttachDeviceFlagsRPC(vmName, detachXML, 1)
	}
}

// refreshLXCContainerDHCP 在运行中的 LXC 容器内释放并重新获取 DHCP（best-effort）。
func refreshLXCContainerDHCP(name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	// 仅运行中容器才有意义
	res := utils.ExecCommand("lxc-info", "-n", name, "-s")
	if res.Error != nil || !strings.Contains(res.Stdout, "RUNNING") {
		return
	}
	// best-effort：容器内未必有 dhclient，忽略错误
	utils.ExecShell(fmt.Sprintf(
		"lxc-attach -n %s -- sh -c 'dhclient -r 2>/dev/null; dhclient 2>/dev/null; true' 2>/dev/null",
		utils.ShellSingleQuote(name)))
}

// UnbindStaticIP 解绑静态 IP
func UnbindStaticIP(vmName string) error {
	// 获取 MAC
	mac := firstNICMAC(vmName)
	if mac == "" {
		return fmt.Errorf("无法获取 MAC 地址")
	}
	if sw, ok := HookGetVPCSwitchForVM(vmName); ok {
		boundIP, err := RemoveVPCStaticHost(sw.ID, vmName, mac)
		if err != nil {
			return err
		}
		if boundIP != "" {
			RemovePortForwardsForIP(boundIP)
		}
		go refreshNIC(vmName, mac, "")
		return nil
	}

	boundIP, err := HookRemoveOVSStaticHost(vmName, mac)
	if err != nil {
		return err
	}

	// 删除所有指向该 IP 的端口转发规则（倒序删除避免行号偏移）
	if boundIP != "" {
		RemovePortForwardsForIP(boundIP)
	}

	// 如果虚拟机正在运行，拔插网卡以强制刷新 DHCP，确保切换回动态 IP
	refreshNIC(vmName, mac, "")

	return nil
}

// RemovePortForwardsForIP 删除所有指向指定 IP 的端口转发规则
func RemovePortForwardsForIP(targetIP string) {
	// 获取所有 DNAT 规则及行号
	result := utils.ExecShellQuiet("iptables -t nat -L PREROUTING -n --line-numbers 2>/dev/null | grep DNAT")
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

		// 删除 NAT 规则 (PREROUTING)
		utils.ExecShell(fmt.Sprintf("iptables -t nat -D PREROUTING %d", id))

		// 删除 NAT 规则 (OUTPUT - 本地流量 DNAT)
		if hostPort != "" {
			utils.ExecShell(fmt.Sprintf(
				"iptables -t nat -D OUTPUT -d %s -p %s --dport %s -j DNAT --to-destination %s:%s 2>/dev/null",
				utils.ShellSingleQuote(getHostIP()), utils.ShellSingleQuote(proto), utils.ShellSingleQuote(hostPort), utils.ShellSingleQuote(targetIP), utils.ShellSingleQuote(destPort)))
		}

		// 删除 FORWARD 规则
		if destPort != "" {
			utils.ExecShell(fmt.Sprintf(
				"iptables -D FORWARD -d %s -p %s --dport %s -j ACCEPT 2>/dev/null",
				utils.ShellSingleQuote(targetIP), utils.ShellSingleQuote(proto), utils.ShellSingleQuote(destPort)))
		}

		// 删除 UFW 规则
		if hostPort != "" {
			_ = HookDeleteHostFirewallPortForwardRule(hostPort, proto)
		}
	}

	// 如果有删除规则，自动持久化
	if len(ruleIDs) > 0 {
		go SavePortForwardRules()
	}
}

// switchSupportsFixedIP 判断交换机是否支持固定 IP 绑定：NAT 模式且有 DHCP 池。
// 同时覆盖 VPC 每交换机 dnsmasq（VLANID!=0）与系统基础网络的全局 OVS dnsmasq（VLANID==0）。
// 直通/桥接模式（BridgeMode!="nat"）或无 DHCP 池的交换机返回 false。
func switchSupportsFixedIP(sw model.VPCSwitch) bool {
	return sw.BridgeMode == "nat" &&
		strings.TrimSpace(sw.DHCPStart) != "" &&
		strings.TrimSpace(sw.DHCPEnd) != ""
}

// switchIsSystemBase 判断是否为系统基础网络（NAT + VLANID==0，走全局 OVS dnsmasq）。
func switchIsSystemBase(sw model.VPCSwitch) bool {
	return sw.BridgeMode == "nat" && sw.VLANID == 0
}

// NICFixedIP 单张网卡的固定 IP 绑定计划。IP 为空表示该网卡维持动态 DHCP。
type NICFixedIP struct {
	Order int    `json:"order"`
	IP    string `json:"ip"`
}

// nicMAC 解析 vm_name 第 order 张网卡的 MAC，按 VPCVMBinding.Kind 分派：
// LXC 走确定性派生 lxc.NICMAC（经 hook），VM 走 virsh domiflist（经 hook）。
func nicMAC(vmName string, order int) string {
	vmName = strings.TrimSpace(vmName)
	var b model.VPCVMBinding
	if err := model.DB.Where("vm_name = ? AND interface_order = ?", vmName, order).First(&b).Error; err == nil {
		if strings.TrimSpace(b.Kind) == "lxc" {
			if HookGetLXCNICMAC != nil {
				return strings.ToLower(strings.TrimSpace(HookGetLXCNICMAC(vmName, order)))
			}
			return ""
		}
		if HookGetVMMACByOrder != nil {
			return strings.ToLower(strings.TrimSpace(HookGetVMMACByOrder(vmName, order)))
		}
	}
	return ""
}

// switchOccupiedIPs 收集交换机子网内已占用 IP：网关 + 静态绑定 + dnsmasq 租约 + ARP 邻居。
// 基础网络（system-base）走全局 OVS 源；其余走 VPC 每交换机源。
func switchOccupiedIPs(sw model.VPCSwitch) map[string]bool {
	used := map[string]bool{sw.GatewayIP: true}
	if switchIsSystemBase(sw) {
		if hosts, err := HookListOVSStaticHosts(); err == nil {
			for _, h := range hosts {
				used[h.IP] = true
			}
		}
		if leases, err := HookListOVSDHCPLeases(); err == nil {
			for _, l := range leases {
				used[l.IP] = true
			}
		}
		// 兼容旧版面板绑定：BindStaticIP 对基础网络 VM 走 VPC 路径，静态绑定会写进
		// vpc/dhcp-hosts-<baseID>（该文件无独立 dnsmasq 读取，但记录确实存在）。
		// 选择器需一并计入，否则会把已绑定 IP 当作可分配（与 bind 去重来源对齐）。
		if hosts, err := HookListVPCStaticHosts(sw.ID); err == nil {
			for _, h := range hosts {
				used[h.IP] = true
			}
		}
	} else {
		if hosts, err := HookListVPCStaticHosts(sw.ID); err == nil {
			for _, h := range hosts {
				used[h.IP] = true
			}
		}
		if leases, err := HookListVPCDHCPLeasesForSwitch(sw.ID); err == nil {
			for _, l := range leases {
				used[l.IP] = true
			}
		}
	}
	// ARP 增强：邻居表里存活的 IP（租约过期但仍占用 / 客户机内手设静态 IP）
	for _, ip := range ip_resolver.ListNeighborIPsInCIDR(sw.CIDR) {
		used[ip] = true
	}
	return used
}

// AvailableVPCIPs 返回交换机 DHCP 池内当前可分配的 IP（按序）。非受管交换机返回空切片。
func AvailableVPCIPs(switchID uint) ([]string, error) {
	var sw model.VPCSwitch
	if err := model.DB.First(&sw, switchID).Error; err != nil {
		return nil, fmt.Errorf("交换机不存在")
	}
	if !switchSupportsFixedIP(sw) {
		return []string{}, nil
	}
	start := net.ParseIP(sw.DHCPStart).To4()
	end := net.ParseIP(sw.DHCPEnd).To4()
	if start == nil || end == nil {
		return nil, fmt.Errorf("交换机 DHCP 地址池无效")
	}
	used := switchOccupiedIPs(sw)
	var out []string
	for ip := append(net.IP(nil), start...); compareIPv4(ip, end) <= 0; incrementIPv4(ip) {
		if !used[ip.String()] {
			out = append(out, ip.String())
		}
	}
	return out, nil
}

// IsVPCIPFree 判断指定 IP 在该交换机子网内是否空闲（含 ARP 邻居）。
func IsVPCIPFree(switchID uint, ip string) bool {
	var sw model.VPCSwitch
	if err := model.DB.First(&sw, switchID).Error; err != nil {
		return false
	}
	if !switchSupportsFixedIP(sw) {
		return false
	}
	return !switchOccupiedIPs(sw)[strings.TrimSpace(ip)]
}

// ValidateFixedIPForSwitch 创建/克隆前预检：交换机存在、IP 合法且空闲。空 IP 放行。
func ValidateFixedIPForSwitch(switchID uint, ip string) error {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return nil
	}
	var sw model.VPCSwitch
	if err := model.DB.First(&sw, switchID).Error; err != nil {
		return fmt.Errorf("交换机不存在")
	}
	if _, err := normalizeIPForVPC(ip, sw); err != nil {
		return err
	}
	if !IsVPCIPFree(switchID, ip) {
		return fmt.Errorf("IP %s 已被占用", ip)
	}
	return nil
}

// bindOneNICStaticIP 为单张网卡写 dnsmasq dhcp-host 预约。
// 基础网络（system-base）走全局 OVS dnsmasq（switchID 返 0 标记）；VPC 走每交换机 dnsmasq。
// 直通/桥接或无 DHCP 池：静默跳过（mac/switchID 返 0，无错）。
func bindOneNICStaticIP(vmName string, order int, ip string) (mac string, switchID uint, err error) {
	var b model.VPCVMBinding
	if e := model.DB.Where("vm_name = ? AND interface_order = ?", vmName, order).First(&b).Error; e != nil {
		return "", 0, fmt.Errorf("网卡绑定记录不存在(vm=%s,order=%d)", vmName, order)
	}
	var sw model.VPCSwitch
	if e := model.DB.First(&sw, b.SwitchID).Error; e != nil {
		return "", 0, fmt.Errorf("交换机不存在")
	}
	if !switchSupportsFixedIP(sw) {
		return "", 0, nil // 直通/桥接或无 DHCP 池：静默跳过
	}
	mac = nicMAC(vmName, order)
	if mac == "" {
		return "", 0, fmt.Errorf("无法解析网卡 MAC(vm=%s,order=%d)", vmName, order)
	}
	normalized, err := normalizeIPForVPC(ip, sw)
	if err != nil {
		return "", 0, err
	}
	if switchIsSystemBase(sw) {
		// 基础网络：全局 OVS dnsmasq 读 /etc/kvm-console/ovs/dhcp-hosts。
		// 切勿走 UpsertVPCStaticHost——会写 dhcp-hosts-<baseID> 死文件（VLANID==0 无独立 dnsmasq）。
		if err := HookUpsertOVSStaticHost(vmName, mac, normalized); err != nil {
			return "", 0, err
		}
		go refreshNIC(vmName, mac, "")
		return mac, 0, nil // switchID=0 标记 OVS 路径
	}
	if err := UpsertVPCStaticHost(sw, vmName, mac, normalized); err != nil {
		return "", 0, err
	}
	go refreshNIC(vmName, mac, "")
	return mac, sw.ID, nil
}

// BindStaticIPForNICs 为多张网卡依次绑定固定 IP。任一失败则回滚本次已写条目（best-effort）。
// IP 为空的网卡跳过（维持动态）。非受管交换机的网卡静默跳过。
func BindStaticIPForNICs(vmName string, plans []NICFixedIP) error {
	vmName = strings.TrimSpace(vmName)
	type done struct {
		switchID uint
		mac      string
	}
	var doneList []done
	for _, p := range plans {
		ip := strings.TrimSpace(p.IP)
		if ip == "" {
			continue
		}
		mac, swID, err := bindOneNICStaticIP(vmName, p.Order, ip)
		if err != nil {
			for _, d := range doneList {
				if d.switchID == 0 {
					if _, rmErr := HookRemoveOVSStaticHost(vmName, d.mac); rmErr != nil {
						logger.App.Warn("回滚固定 IP 绑定失败(OVS)", "vm", vmName, "mac", d.mac, "error", rmErr)
					}
				} else {
					if _, rmErr := RemoveVPCStaticHost(d.switchID, vmName, d.mac); rmErr != nil {
						logger.App.Warn("回滚固定 IP 绑定失败", "vm", vmName, "switch", d.switchID, "mac", d.mac, "error", rmErr)
					}
				}
			}
			return fmt.Errorf("网卡(order=%d)绑定固定 IP %s 失败: %w", p.Order, ip, err)
		}
		if mac != "" {
			doneList = append(doneList, done{switchID: swID, mac: mac})
		}
	}
	return nil
}

