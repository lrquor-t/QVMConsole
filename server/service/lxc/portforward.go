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

// resolveContainerIP 取容器当前运行 IP，无 IP 则报错。
// 端口转发的目标 IP 必须现役可用，故容器停机/IP 未拿到时直接失败。
func resolveContainerIP(name string) (string, error) {
	ip := ResolveContainerVPCIP(name) // 已存在，service/lxc/network.go:82
	if ip == "" {
		return "", errors.New("容器当前无可用 IP，无法配置端口映射（请确认容器已启动并分配到 IP）")
	}
	return ip, nil
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
	// 1. 运行 IP（lxc-info -i，最权威的"当前转发目标"）
	add(ResolveContainerVPCIP(name))
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
		return errors.New("该端口转发规则不属于此容器")
	}
	return netpkg.DeletePortForward(id) // service/network/port_forward.go:462
}
