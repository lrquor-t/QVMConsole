package lxc

import (
	"errors"
	"strconv"
	"strings"

	"kvm_console/model"
	netpkg "kvm_console/service/network"
)

// PortForwardRequest LXC 端口映射请求（前端表单）。与 VM 的 AddPortForwardRequest 对齐，
// 但无需 vm_name/vm_ip（容器名来自 URL，容器 IP 由后端自动解析）。
type PortForwardRequest struct {
	HostPort string `json:"host_port"` // 留空自动分配
	VMPort   string `json:"vm_port"`
	Protocol string `json:"protocol"` // tcp/udp/both
	Comment  string `json:"comment"`
}

// ErrPortForwardNotOwned 表示端口转发规则不属于该容器（删除时归属校验失败）。
// 哨兵错误，供 handler 层 errors.Is 识别后映射为 404（与 VM DeletePortForward 语义对齐）。
var ErrPortForwardNotOwned = errors.New("该端口转发规则不属于此容器")

// resolveContainerIP 解析端口转发目标 IP（与 VM 对齐：尽量固化为静态绑定）。
// 优先 netpkg.ResolvePortForwardTargetIP——对绑定了 VPC 交换机的主网卡，取当前静态绑定/DHCP
// 租约并写 dhcp-hosts 固化，重启后 IP 不变、转发规则持久生效（修复旧实现仅取 eth0 运行 IP、
// 容器重启 DHCP 换 IP 致转发失效）。
// 无 VPC 绑定（switchID=0 默认桥等，firstNICMAC 取不到 MAC）或固化失败时，回退进容器取 eth0
// 当前运行 IP——保证不回归（这类容器本就无 VPC 静态 IP 体系可固化）。
func resolveContainerIP(name string) (string, error) {
	if ip, err := netpkg.ResolvePortForwardTargetIP(name, ""); err == nil && strings.TrimSpace(ip) != "" {
		return ip, nil
	}
	if ip := ResolveContainerNICIP(name, 0); ip != "" {
		return ip, nil
	}
	return "", errors.New("容器主网卡当前无可用 IP，无法配置端口映射（请确认容器已启动并分配到 IP）")
}

// collectContainerIPs 聚合容器可能关联的全部目标 IP：运行 IP + VPC 静态绑定 IP。
// 用于 ListContainerPortForwards 过滤属于该容器的端口转发规则——
// 容器重启后 IP 可能变化（DHCP 续约/重新分配），历史规则仍指向旧 IP 时也要能列出。
func collectContainerIPs(name string) []string {
	seen := map[string]bool{}
	var ips []string
	add := func(ip string) {
		ip = strings.TrimSpace(ip)
		if ip != "" && !seen[ip] {
			seen[ip] = true
			ips = append(ips, ip)
		}
	}
	// 1. 主网卡 eth0 的运行 IP（端口转发一律针对主网卡，最权威的"当前转发目标"）
	add(ResolveContainerNICIP(name, 0))
	// 2. VPC 静态绑定 IP：按 binding 的 switchID + 网卡 MAC 查 dhcp-hosts-<switchID>
	if model.DB != nil {
		var bindings []model.VPCVMBinding
		model.DB.Where("vm_name = ? AND kind = ?", name, "lxc").Find(&bindings)
		for _, b := range bindings {
			mac := NICMAC(name, b.InterfaceOrder)
			if ip := GetVPCStaticIPByMACExported(b.SwitchID, mac); ip != "" {
				add(ip)
			}
		}
	}
	return ips
}

// ListContainerPortForwards 列出容器的端口转发（按容器所有可能 IP 过滤全局规则）。
func ListContainerPortForwards(name string) ([]netpkg.PortForwardRule, error) {
	all, err := netpkg.ListPortForwards() // service/network/port_forward.go:166
	if err != nil {
		return nil, err
	}
	ips := collectContainerIPs(name)
	want := make(map[string]bool, len(ips))
	for _, ip := range ips {
		want[ip] = true
	}
	var out []netpkg.PortForwardRule
	for _, r := range all {
		if want[r.DestIP] {
			out = append(out, r)
		}
	}
	return out, nil
}

// AddContainerPortForward 新增端口转发，自动填容器 IP；HostPort 留空时自动分配宿主机端口。
// 复用 VM 的 iptables DNAT 链路（service/network.AddPortForward）——后端只关心目标 IP，
// 不区分虚拟化类型，故容器与 VM 共用同一套 iptables 规则与持久化机制。
func AddContainerPortForward(name string, req PortForwardRequest, createdBy string, isAdmin bool) error {
	ip, err := resolveContainerIP(name)
	if err != nil {
		return err
	}
	hostPort := strings.TrimSpace(req.HostPort)
	if hostPort == "" {
		port, err := netpkg.AutoAllocatePort()
		if err != nil {
			return err
		}
		hostPort = strconv.Itoa(port)
	}
	params := netpkg.PortForwardAddParams{
		VMIP:           ip,
		HostPort:       hostPort,
		VMPort:         req.VMPort,
		Protocol:       req.Protocol,
		Comment:        req.Comment,
		CreatedBy:      createdBy,
		CreatedByAdmin: isAdmin,
	}
	return netpkg.AddPortForward(&params) // service/network/port_forward.go:204
}

// DeleteContainerPortForward 按 iptables 行号 id 删除一条端口转发。
// 先校验该 id 的 DestIP 属于本容器，避免用户通过猜测 id 删除其他容器/VM 的规则。
func DeleteContainerPortForward(name string, id int) error {
	rule, err := netpkg.GetPortForwardRuleByID(id)
	if err != nil {
		return err
	}
	ips := collectContainerIPs(name)
	owned := false
	for _, ip := range ips {
		if ip == rule.DestIP {
			owned = true
			break
		}
	}
	if !owned {
		return ErrPortForwardNotOwned
	}
	return netpkg.DeletePortForward(id) // service/network/port_forward.go:462
}

// BuildLXCOwnerByIP 构建 LXC 容器 "IP -> (容器名 + 属主)" 映射，
// 供 service/network.buildPortForwardTargetInfoMap 回填 LXC 规则的 vm_name/owner_username。
//
// 数据源（与 collectContainerIPs 对齐，保证单容器视角与全局列表视角一致）：
//  1. LXCCache.CachedIP —— 运行态缓存 IP（最贴近当前转发目标，优先）
//  2. VPCVMBinding{Kind:"lxc"} —— 按 SwitchID + 网卡 MAC 反查 dhcp-hosts 绑定的静态 IP
//     （兼容容器重启后 IP 变化，历史规则仍指向旧 IP 时也能正确归属）
//
// 同一 IP 已被前序源填则跳过；最终合并进 targetMap 时也遵循 "IP 已存在则跳过" 的语义，
// 因此 VM/静态/手动绑定优先于 LXC。
func BuildLXCOwnerByIP() map[string]netpkg.PortForwardOwnerInfo {
	out := make(map[string]netpkg.PortForwardOwnerInfo)
	if model.DB == nil {
		return out
	}
	set := func(ip, name, owner string) {
		ip = strings.TrimSpace(ip)
		name = strings.TrimSpace(name)
		if ip == "" || name == "" {
			return
		}
		if _, exists := out[ip]; exists {
			return
		}
		out[ip] = netpkg.PortForwardOwnerInfo{VMName: name, OwnerUsername: strings.TrimSpace(owner)}
	}

	// 1. LXCCache.CachedIP（运行 IP）。多网卡时 CachedIP 形如 "ip1, ip2"（lxc-info/lxc-ls 全量），
	//    逐个登记以匹配单条规则的 DestIP（整串当 key 会匹配失败）；set 内部已 trim/去空/去重。
	var caches []model.LXCCache
	if err := model.DB.Find(&caches).Error; err == nil {
		for _, c := range caches {
			for _, ip := range strings.Split(c.CachedIP, ",") {
				set(ip, c.Name, c.OwnerUsername)
			}
		}
	}

	// 2. VPCVMBinding{Kind:"lxc"} -> 静态绑定 IP
	var bindings []model.VPCVMBinding
	if err := model.DB.Where("kind = ?", "lxc").Find(&bindings).Error; err == nil {
		for _, b := range bindings {
			mac := NICMAC(b.VMName, b.InterfaceOrder)
			set(GetVPCStaticIPByMACExported(b.SwitchID, mac), b.VMName, b.Username)
		}
	}

	return out
}
