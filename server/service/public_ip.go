package service

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"kvm_console/model"
	"kvm_console/utils"
)

const (
	PublicIPModeNAT           = "nat"
	PublicIPModeClassicRoute  = "classic_route"
	PublicIPModeClassicBridge = "classic_bridge"

	PublicIPStatusFree  = "free"
	PublicIPStatusBound = "bound"

	publicIPConfigDir   = "/etc/kvm-console/public-ip"
	publicIPRulesPath   = "/etc/kvm-console/public-ip/rules.sh"
	publicIPRuleComment = "kvm-console:public-ip"
	publicIPFlowPrefix  = "0x9a"
	publicIPFlowMask    = "0xff00000000000000"
)

type PublicIPRequest struct {
	IP             string `json:"ip"`
	CIDR           string `json:"cidr"`
	Gateway        string `json:"gateway"`
	UplinkIF       string `json:"uplink_if"`
	SupportedModes string `json:"supported_modes"`
	Status         string `json:"status"`
	Remark         string `json:"remark"`
}

type PublicIPBindRequest struct {
	Username    string `json:"username"`
	VMName      string `json:"vm_name"`
	VMPrivateIP string `json:"vm_private_ip"`
	Mode        string `json:"mode"`
}

type PublicIPOperationParams struct {
	Action      string              `json:"action"`
	PublicIPID  uint                `json:"public_ip_id"`
	TargetVM    string              `json:"target_vm"`
	TargetUser  string              `json:"target_user"`
	BindRequest PublicIPBindRequest `json:"bind_request"`
}

type PublicIPInfo struct {
	model.PublicIP
	Modes        []string               `json:"modes"`
	ModeLabels   []string               `json:"mode_labels"`
	Binding      *model.PublicIPBinding `json:"binding,omitempty"`
	RuntimeRules []string               `json:"runtime_rules,omitempty"`
	Issues       []string               `json:"issues,omitempty"`
}

type PublicIPPreview struct {
	PublicIP   model.PublicIP      `json:"public_ip"`
	Binding    PublicIPBindRequest `json:"binding"`
	Commands   []string            `json:"commands"`
	ConfigHint string              `json:"config_hint"`
	Warnings   []string            `json:"warnings"`
}

type PublicIPAttachment struct {
	PublicIP      string `json:"public_ip"`
	Mode          string `json:"mode"`
	ModeLabel     string `json:"mode_label"`
	VMPrivateIP   string `json:"vm_private_ip"`
	RuntimeStatus string `json:"runtime_status"`
}

func NormalizePublicIPMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "1:1 nat", "nat":
		return PublicIPModeNAT
	case "classic", "classic_route", "经典网络-路由":
		return PublicIPModeClassicRoute
	case "classic_bridge", "经典网络-桥接":
		return PublicIPModeClassicBridge
	default:
		return strings.ToLower(strings.TrimSpace(mode))
	}
}

func PublicIPModeLabel(mode string) string {
	switch NormalizePublicIPMode(mode) {
	case PublicIPModeNAT:
		return "1:1 NAT"
	case PublicIPModeClassicRoute:
		return "经典网络-路由"
	case PublicIPModeClassicBridge:
		return "经典网络-桥接"
	default:
		return mode
	}
}

func ListPublicIPs() ([]PublicIPInfo, error) {
	var rows []model.PublicIP
	if err := model.DB.Order("ip ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	var bindings []model.PublicIPBinding
	if err := model.DB.Find(&bindings).Error; err != nil {
		return nil, err
	}
	byIPID := map[uint]model.PublicIPBinding{}
	for _, binding := range bindings {
		byIPID[binding.PublicIPID] = binding
	}

	out := make([]PublicIPInfo, 0, len(rows))
	for _, row := range rows {
		modes := parsePublicIPModes(row.SupportedModes)
		info := PublicIPInfo{
			PublicIP:   row,
			Modes:      modes,
			ModeLabels: publicIPModeLabels(modes),
		}
		if binding, ok := byIPID[row.ID]; ok {
			copyBinding := binding
			info.Binding = &copyBinding
			info.RuntimeRules = publicIPRuntimeRuleSummary(row, binding)
		}
		out = append(out, info)
	}
	return out, nil
}

func CreatePublicIP(req PublicIPRequest) (*model.PublicIP, error) {
	row, err := normalizePublicIPRequest(req, nil)
	if err != nil {
		return nil, err
	}
	if row.Status == PublicIPStatusBound {
		return nil, fmt.Errorf("新增公网 IP 不能直接设置为已绑定")
	}
	if row.Status == "" {
		row.Status = PublicIPStatusFree
	}
	if err := model.DB.Create(row).Error; err != nil {
		return nil, fmt.Errorf("创建公网 IP 失败: %w", err)
	}
	return row, nil
}

func UpdatePublicIP(id uint, req PublicIPRequest) (*model.PublicIP, error) {
	var current model.PublicIP
	if err := model.DB.First(&current, id).Error; err != nil {
		return nil, fmt.Errorf("公网 IP 不存在")
	}
	row, err := normalizePublicIPRequest(req, &current)
	if err != nil {
		return nil, err
	}
	row.ID = current.ID
	var bindingCount int64
	model.DB.Model(&model.PublicIPBinding{}).Where("public_ip_id = ?", current.ID).Count(&bindingCount)
	if bindingCount > 0 && row.IP != current.IP {
		return nil, fmt.Errorf("公网 IP 已绑定，不能修改 IP 地址")
	}
	if bindingCount > 0 {
		row.Status = PublicIPStatusBound
	}
	if err := model.DB.Model(&current).Updates(map[string]interface{}{
		"ip":              row.IP,
		"cidr":            row.CIDR,
		"gateway":         row.Gateway,
		"uplink_if":       row.UplinkIF,
		"supported_modes": row.SupportedModes,
		"status":          row.Status,
		"remark":          row.Remark,
	}).Error; err != nil {
		return nil, fmt.Errorf("更新公网 IP 失败: %w", err)
	}
	if row.IP != current.IP {
		model.DB.Model(&model.PublicIPBinding{}).Where("public_ip_id = ?", current.ID).Update("public_ip", row.IP)
	}
	if err := model.DB.First(&current, id).Error; err != nil {
		return nil, err
	}
	return &current, nil
}

func DeletePublicIP(id uint) error {
	var count int64
	model.DB.Model(&model.PublicIPBinding{}).Where("public_ip_id = ?", id).Count(&count)
	if count > 0 {
		return fmt.Errorf("公网 IP 已绑定，请先解绑后再删除")
	}
	if err := model.DB.Delete(&model.PublicIP{}, id).Error; err != nil {
		return fmt.Errorf("删除公网 IP 失败: %w", err)
	}
	return nil
}

func PreviewPublicIPBinding(id uint, req PublicIPBindRequest) (*PublicIPPreview, error) {
	ipRow, err := getPublicIP(id)
	if err != nil {
		return nil, err
	}
	bindReq, warnings, err := normalizePublicIPBindRequest(*ipRow, req, false)
	if err != nil {
		return nil, err
	}
	commands, err := buildPublicIPCommands(*ipRow, bindReq)
	if err != nil {
		return nil, err
	}
	return &PublicIPPreview{
		PublicIP:   *ipRow,
		Binding:    bindReq,
		Commands:   commands,
		ConfigHint: buildPublicIPConfigHint(*ipRow, bindReq),
		Warnings:   warnings,
	}, nil
}

func ExecutePublicIPOperation(ctx context.Context, params PublicIPOperationParams, progress func(int, string)) (string, error) {
	if progress == nil {
		progress = func(int, string) {}
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

	action := strings.ToLower(strings.TrimSpace(params.Action))
	progress(10, "正在校验公网 IP 操作...")

	var result interface{}
	var err error
	switch action {
	case "bind":
		result, err = bindPublicIP(params.PublicIPID, params.BindRequest)
	case "unbind":
		result, err = unbindPublicIP(params.PublicIPID)
	case "migrate":
		req := params.BindRequest
		if strings.TrimSpace(params.TargetVM) != "" {
			req.VMName = params.TargetVM
		}
		if strings.TrimSpace(params.TargetUser) != "" {
			req.Username = params.TargetUser
		}
		result, err = migratePublicIP(params.PublicIPID, req)
	case "apply_all":
		result = map[string]string{"action": "apply_all"}
	default:
		err = fmt.Errorf("不支持的公网 IP 操作: %s", action)
	}
	if err != nil {
		return "", err
	}

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}
	if publicIPHasVPCBindings() {
		progress(45, "正在同步 VPC 安全组规则...")
		if err := ApplyVPCACLRules(); err != nil {
			markPublicIPBindingsRuntimeFailed(err.Error())
			return "", err
		}
	}
	progress(55, "正在写入并应用公网 IP 运行规则...")
	if err := ApplyPublicIPRules(); err != nil {
		markPublicIPBindingsRuntimeFailed(err.Error())
		return "", err
	}
	markPublicIPBindingsApplied()
	progress(100, "公网 IP 规则已应用")
	data, _ := json.Marshal(result)
	return string(data), nil
}

func ApplyPublicIPRules() error {
	if err := os.MkdirAll(filepath.Join(publicIPConfigDir, "backups"), 0755); err != nil {
		return fmt.Errorf("创建公网 IP 配置目录失败: %w", err)
	}
	if _, err := os.Stat(publicIPRulesPath); err == nil {
		backup := filepath.Join(publicIPConfigDir, "backups", "rules.sh."+time.Now().Format("20060102_150405"))
		_ = copyFile(publicIPRulesPath, backup, 0755)
	}
	script, err := BuildPublicIPRulesScript()
	if err != nil {
		return err
	}
	if _, err := writeFileIfChanged(publicIPRulesPath, []byte(script), 0755); err != nil {
		return fmt.Errorf("写入公网 IP 规则失败: %w", err)
	}
	result := utils.ExecCommand("bash", publicIPRulesPath)
	if result.Error != nil {
		msg := strings.TrimSpace(result.Stderr)
		if msg == "" {
			msg = result.Error.Error()
		}
		return fmt.Errorf("应用公网 IP 规则失败: %s", msg)
	}
	return nil
}

func RestorePublicIPRules() error {
	if _, err := os.Stat(publicIPRulesPath); err == nil {
		result := utils.ExecCommand("bash", publicIPRulesPath)
		if result.Error != nil {
			return fmt.Errorf("恢复公网 IP 规则失败: %s", strings.TrimSpace(result.Stderr))
		}
	}
	return ApplyPublicIPRules()
}

func BuildPublicIPRulesScript() (string, error) {
	var bindings []model.PublicIPBinding
	if err := model.DB.Order("public_ip ASC").Find(&bindings).Error; err != nil {
		return "", err
	}
	ipRows := map[uint]model.PublicIP{}
	var ips []model.PublicIP
	if err := model.DB.Find(&ips).Error; err != nil {
		return "", err
	}
	for _, ipRow := range ips {
		ipRows[ipRow.ID] = ipRow
	}

	var b strings.Builder
	b.WriteString("#!/bin/bash\n")
	b.WriteString("set -e\n")
	b.WriteString("# KVM 公网 IP / 浮动 IP 规则 - 自动生成\n\n")
	b.WriteString(cleanupPublicIPRulesShell())
	b.WriteString("\n")
	b.WriteString(cleanupPublicIPHostAddressesShell(ips))
	b.WriteString("\n")
	b.WriteString("sysctl -w net.ipv4.ip_forward=1 >/dev/null 2>&1 || true\n\n")

	for _, binding := range bindings {
		ipRow, ok := ipRows[binding.PublicIPID]
		if !ok {
			continue
		}
		commands, err := buildPublicIPCommands(ipRow, PublicIPBindRequest{
			Username:    binding.Username,
			VMName:      binding.VMName,
			VMPrivateIP: binding.VMPrivateIP,
			Mode:        binding.Mode,
		})
		if err != nil {
			return "", err
		}
		for _, cmd := range commands {
			b.WriteString(cmd)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("exit 0\n")
	return b.String(), nil
}

func buildPublicIPCommands(ipRow model.PublicIP, req PublicIPBindRequest) ([]string, error) {
	mode := NormalizePublicIPMode(req.Mode)
	switch mode {
	case PublicIPModeNAT:
		if strings.TrimSpace(req.VMPrivateIP) == "" {
			return nil, fmt.Errorf("1:1 NAT 模式需要 VM 私网 IP")
		}
		return buildPublicIPNATCommands(ipRow, req), nil
	case PublicIPModeClassicRoute:
		return buildPublicIPClassicRouteCommands(ipRow, req), nil
	case PublicIPModeClassicBridge:
		return buildPublicIPClassicBridgeCommands(ipRow, req), nil
	default:
		return nil, fmt.Errorf("不支持的公网 IP 模式: %s", req.Mode)
	}
}

func buildPublicIPNATCommands(ipRow model.PublicIP, req PublicIPBindRequest) []string {
	publicIP := strings.TrimSpace(ipRow.IP)
	privateIP := strings.TrimSpace(req.VMPrivateIP)
	uplink := strings.TrimSpace(ipRow.UplinkIF)
	if uplink == "" {
		uplink = ovsUplink()
	}
	comment := publicIPRuleComment + ":" + publicIP
	var cmds []string
	if addr := publicIPAddrForHost(ipRow); addr != "" && uplink != "" {
		cmds = append(cmds, fmt.Sprintf("ip addr replace %s dev %s || true", utils.ShellSingleQuote(addr), utils.ShellSingleQuote(uplink)))
	}
	cmds = append(cmds,
		fmt.Sprintf("iptables -t nat -A PREROUTING -d %s/32 -m comment --comment %s -j DNAT --to-destination %s",
			utils.ShellSingleQuote(publicIP), utils.ShellSingleQuote(comment+":dnat"), utils.ShellSingleQuote(privateIP)),
	)
	if uplink != "" {
		cmds = append(cmds, fmt.Sprintf("iptables -t nat -I POSTROUTING 1 -s %s/32 -o %s -m comment --comment %s -j SNAT --to-source %s",
			utils.ShellSingleQuote(privateIP), utils.ShellSingleQuote(uplink), utils.ShellSingleQuote(comment+":snat"), utils.ShellSingleQuote(publicIP)))
	} else {
		cmds = append(cmds, fmt.Sprintf("iptables -t nat -I POSTROUTING 1 -s %s/32 -m comment --comment %s -j SNAT --to-source %s",
			utils.ShellSingleQuote(privateIP), utils.ShellSingleQuote(comment+":snat"), utils.ShellSingleQuote(publicIP)))
	}
	if !IsVPCManagedIP(privateIP) {
		cmds = append(cmds,
			fmt.Sprintf("iptables -A FORWARD -d %s/32 -m comment --comment %s -j ACCEPT", utils.ShellSingleQuote(privateIP), utils.ShellSingleQuote(comment+":forward-in")),
			fmt.Sprintf("iptables -A FORWARD -s %s/32 -m comment --comment %s -j ACCEPT", utils.ShellSingleQuote(privateIP), utils.ShellSingleQuote(comment+":forward-out")),
		)
	}
	return cmds
}

func buildPublicIPClassicRouteCommands(ipRow model.PublicIP, req PublicIPBindRequest) []string {
	bridge := publicIPVMBridge(req.VMName)
	if bridge == "" {
		bridge = ovsBridgeName()
	}
	var cmds []string
	if strings.TrimSpace(req.VMPrivateIP) != "" {
		cmds = append(cmds, fmt.Sprintf("ip route replace %s/32 via %s dev %s || true",
			utils.ShellSingleQuote(ipRow.IP), utils.ShellSingleQuote(req.VMPrivateIP), utils.ShellSingleQuote(bridge)))
	} else {
		cmds = append(cmds, fmt.Sprintf("ip route replace %s/32 dev %s || true", utils.ShellSingleQuote(ipRow.IP), utils.ShellSingleQuote(bridge)))
	}
	cmds = append(cmds, buildPublicIPAntiSpoofCommands(ipRow, req)...)
	return cmds
}

func buildPublicIPClassicBridgeCommands(ipRow model.PublicIP, req PublicIPBindRequest) []string {
	return buildPublicIPAntiSpoofCommands(ipRow, req)
}

func buildPublicIPAntiSpoofCommands(ipRow model.PublicIP, req PublicIPBindRequest) []string {
	if strings.TrimSpace(req.VMName) == "" {
		return nil
	}
	iface, bridge := publicIPVMInterface(req.VMName)
	if iface == "" || bridge == "" {
		return []string{fmt.Sprintf("# 未找到 VM %s 的运行态 OVS 端口，跳过经典网络防伪造流表", req.VMName)}
	}
	ofport := getOVSInterfaceOfPort(iface)
	if ofport == "" {
		return []string{fmt.Sprintf("# VM %s 的 OVS ofport 无效，跳过经典网络防伪造流表", req.VMName)}
	}
	cookie := publicIPFlowCookie(ipRow.IP)
	return []string{
		fmt.Sprintf("ovs-ofctl -O OpenFlow13 del-flows %s %s || true", utils.ShellSingleQuote(bridge), utils.ShellSingleQuote("cookie="+cookie+"/-1")),
		fmt.Sprintf("ovs-ofctl -O OpenFlow13 add-flow %s %s", utils.ShellSingleQuote(bridge), utils.ShellSingleQuote(fmt.Sprintf("cookie=%s,priority=240,in_port=%s,ip,nw_src=%s,actions=NORMAL", cookie, ofport, ipRow.IP))),
		fmt.Sprintf("ovs-ofctl -O OpenFlow13 add-flow %s %s", utils.ShellSingleQuote(bridge), utils.ShellSingleQuote(fmt.Sprintf("cookie=%s,priority=240,in_port=%s,arp,arp_spa=%s,actions=NORMAL", cookie, ofport, ipRow.IP))),
		fmt.Sprintf("ovs-ofctl -O OpenFlow13 add-flow %s %s", utils.ShellSingleQuote(bridge), utils.ShellSingleQuote(fmt.Sprintf("cookie=%s,priority=230,in_port=%s,ip,actions=drop", cookie, ofport))),
		fmt.Sprintf("ovs-ofctl -O OpenFlow13 add-flow %s %s", utils.ShellSingleQuote(bridge), utils.ShellSingleQuote(fmt.Sprintf("cookie=%s,priority=230,in_port=%s,arp,actions=drop", cookie, ofport))),
	}
}

func bindPublicIP(id uint, req PublicIPBindRequest) (*model.PublicIPBinding, error) {
	ipRow, err := getPublicIP(id)
	if err != nil {
		return nil, err
	}
	if ipRow.Status == PublicIPStatusBound {
		return nil, fmt.Errorf("公网 IP 已绑定，请使用迁移操作")
	}
	if ipRow.Status == "reserved" {
		return nil, fmt.Errorf("公网 IP 当前为保留状态，不能绑定")
	}
	req, _, err = normalizePublicIPBindRequest(*ipRow, req, true)
	if err != nil {
		return nil, err
	}
	if err := checkPublicIPQuota(req.Username, 1); err != nil {
		return nil, err
	}
	now := time.Now()
	binding := &model.PublicIPBinding{
		PublicIPID:    ipRow.ID,
		PublicIP:      ipRow.IP,
		Username:      req.Username,
		VMName:        req.VMName,
		VMPrivateIP:   req.VMPrivateIP,
		Mode:          NormalizePublicIPMode(req.Mode),
		RuntimeStatus: "pending",
		ConfigHint:    buildPublicIPConfigHint(*ipRow, req),
		LastAppliedAt: &now,
	}
	if err := model.DB.Create(binding).Error; err != nil {
		return nil, fmt.Errorf("保存公网 IP 绑定失败: %w", err)
	}
	model.DB.Model(ipRow).Updates(map[string]interface{}{"status": PublicIPStatusBound})
	return binding, nil
}

func unbindPublicIP(id uint) (map[string]string, error) {
	ipRow, err := getPublicIP(id)
	if err != nil {
		return nil, err
	}
	if err := model.DB.Where("public_ip_id = ?", id).Delete(&model.PublicIPBinding{}).Error; err != nil {
		return nil, fmt.Errorf("删除公网 IP 绑定失败: %w", err)
	}
	model.DB.Model(ipRow).Updates(map[string]interface{}{"status": PublicIPStatusFree})
	cleanupConntrackForPublicIP(ipRow.IP)
	return map[string]string{"public_ip": ipRow.IP, "action": "unbind"}, nil
}

func migratePublicIP(id uint, req PublicIPBindRequest) (*model.PublicIPBinding, error) {
	ipRow, err := getPublicIP(id)
	if err != nil {
		return nil, err
	}
	var binding model.PublicIPBinding
	if err := model.DB.Where("public_ip_id = ?", id).First(&binding).Error; err != nil {
		return nil, fmt.Errorf("公网 IP 尚未绑定，不能迁移")
	}
	if strings.TrimSpace(req.Mode) == "" {
		req.Mode = binding.Mode
	}
	req, _, err = normalizePublicIPBindRequest(*ipRow, req, true)
	if err != nil {
		return nil, err
	}
	if req.Username != binding.Username {
		if err := checkPublicIPQuota(req.Username, 1); err != nil {
			return nil, err
		}
	}
	now := time.Now()
	if err := model.DB.Model(&binding).Updates(map[string]interface{}{
		"username":        req.Username,
		"vm_name":         req.VMName,
		"vm_private_ip":   req.VMPrivateIP,
		"mode":            NormalizePublicIPMode(req.Mode),
		"runtime_status":  "pending",
		"config_hint":     buildPublicIPConfigHint(*ipRow, req),
		"last_applied_at": &now,
	}).Error; err != nil {
		return nil, fmt.Errorf("迁移公网 IP 失败: %w", err)
	}
	if err := model.DB.First(&binding, binding.ID).Error; err != nil {
		return nil, err
	}
	cleanupConntrackForPublicIP(ipRow.IP)
	return &binding, nil
}

func normalizePublicIPRequest(req PublicIPRequest, current *model.PublicIP) (*model.PublicIP, error) {
	ipText := strings.TrimSpace(req.IP)
	if current != nil && ipText == "" {
		ipText = current.IP
	}
	if net.ParseIP(ipText) == nil {
		return nil, fmt.Errorf("公网 IP 格式无效")
	}
	if strings.Contains(ipText, ":") {
		return nil, fmt.Errorf("当前仅支持 IPv4 公网 IP")
	}
	cidr := strings.TrimSpace(req.CIDR)
	if cidr != "" {
		if err := validatePublicIPCidr(ipText, cidr); err != nil {
			return nil, err
		}
	}
	gateway := strings.TrimSpace(req.Gateway)
	if gateway != "" && net.ParseIP(gateway) == nil {
		return nil, fmt.Errorf("网关 IP 格式无效")
	}
	modes := normalizeSupportedPublicIPModes(req.SupportedModes)
	status := strings.TrimSpace(req.Status)
	if status == "" {
		if current != nil {
			status = current.Status
		} else {
			status = PublicIPStatusFree
		}
	}
	if status != PublicIPStatusFree && status != PublicIPStatusBound && status != "reserved" {
		return nil, fmt.Errorf("公网 IP 状态无效")
	}
	return &model.PublicIP{
		IP:             ipText,
		CIDR:           cidr,
		Gateway:        gateway,
		UplinkIF:       strings.TrimSpace(req.UplinkIF),
		SupportedModes: modes,
		Status:         status,
		Remark:         strings.TrimSpace(req.Remark),
	}, nil
}

func normalizePublicIPBindRequest(ipRow model.PublicIP, req PublicIPBindRequest, allowMutate bool) (PublicIPBindRequest, []string, error) {
	req.Username = strings.TrimSpace(req.Username)
	req.VMName = strings.TrimSpace(req.VMName)
	req.VMPrivateIP = strings.TrimSpace(req.VMPrivateIP)
	req.Mode = NormalizePublicIPMode(req.Mode)
	if req.VMName == "" {
		return req, nil, fmt.Errorf("请选择虚拟机")
	}
	if req.Username == "" {
		req.Username = FindVMOwner(req.VMName)
	}
	if req.Username == "" {
		return req, nil, fmt.Errorf("无法识别虚拟机归属用户，请手动选择用户")
	}
	if !publicIPModeAllowed(ipRow, req.Mode) {
		return req, nil, fmt.Errorf("公网 IP 不支持 %s 模式", PublicIPModeLabel(req.Mode))
	}
	var warnings []string
	if req.Mode == PublicIPModeNAT {
		if req.VMPrivateIP == "" {
			if allowMutate {
				ip, err := EnsureStaticIP(req.VMName)
				if err != nil {
					return req, nil, err
				}
				req.VMPrivateIP = ip
			} else if ip := ResolvePublicIPVMPrivateIP(req.VMName); ip != "" {
				req.VMPrivateIP = ip
			}
		}
		if req.VMPrivateIP == "" {
			return req, nil, fmt.Errorf("1:1 NAT 模式需要 VM 私网 IP")
		}
		if net.ParseIP(req.VMPrivateIP) == nil {
			return req, nil, fmt.Errorf("VM 私网 IP 格式无效")
		}
	} else {
		if req.VMPrivateIP == "" {
			req.VMPrivateIP = ResolvePublicIPVMPrivateIP(req.VMName)
		}
		warnings = append(warnings, "经典网络需要上游网络支持，并由用户在 VM 内手动配置公网 IP")
	}
	return req, warnings, nil
}

func ResolvePublicIPVMPrivateIP(vmName string) string {
	if ip := GetVPCLeaseIPForVM(vmName); ip != "" {
		return ip
	}
	if host, ok := GetOVSStaticHostByVMName(vmName); ok {
		return host.IP
	}
	status, err := GetVMNetworkRuntimeStatus(vmName)
	if err == nil && status != nil {
		for _, iface := range status.Interfaces {
			if iface.IP != "" && net.ParseIP(iface.IP) != nil {
				return iface.IP
			}
		}
	}
	return ""
}

func checkPublicIPQuota(username string, delta int) error {
	var user model.User
	if err := model.DB.Where("username = ?", username).First(&user).Error; err != nil {
		return fmt.Errorf("用户不存在")
	}
	if user.Role == "admin" || user.MaxPublicIPs <= 0 {
		return nil
	}
	var count int64
	model.DB.Model(&model.PublicIPBinding{}).Where("username = ?", username).Count(&count)
	if int(count)+delta > user.MaxPublicIPs {
		return fmt.Errorf("公网 IP 配额不足（已用 %d / 上限 %d）", count, user.MaxPublicIPs)
	}
	return nil
}

func GetUserPublicIPUsage(username string) int {
	var count int64
	if model.DB != nil {
		model.DB.Model(&model.PublicIPBinding{}).Where("username = ?", strings.TrimSpace(username)).Count(&count)
	}
	return int(count)
}

func buildPublicIPConfigHint(ipRow model.PublicIP, req PublicIPBindRequest) string {
	mode := NormalizePublicIPMode(req.Mode)
	prefix := publicIPPrefix(ipRow)
	gateway := strings.TrimSpace(ipRow.Gateway)
	switch mode {
	case PublicIPModeNAT:
		return fmt.Sprintf("VM 内保持私网 IP %s，无需配置公网 IP。公网 %s 会通过 1:1 NAT 映射到该 VM。", req.VMPrivateIP, ipRow.IP)
	case PublicIPModeClassicRoute:
		if gateway == "" {
			gateway = ovsGatewayIP()
		}
		return fmt.Sprintf("经典网络-路由：请在 VM 内配置 IP %s/%d，默认网关 %s。上游需要把该公网 IP 或公网段路由到宿主机。", ipRow.IP, prefix, gateway)
	case PublicIPModeClassicBridge:
		return fmt.Sprintf("经典网络-桥接：请在 VM 内配置 IP %s/%d，默认网关 %s。上游交换机需要允许 VM MAC 使用该公网 IP。", ipRow.IP, prefix, gateway)
	default:
		return ""
	}
}

func cleanupPublicIPRulesShell() string {
	var b strings.Builder
	b.WriteString(`cleanup_iptables_comments() {
  table="$1"
  chain="$2"
  iptables_cmd=(iptables)
  [ "$table" = "nat" ] && iptables_cmd=(iptables -t nat)
  while true; do
    line="$("${iptables_cmd[@]}" -L "$chain" --line-numbers 2>/dev/null | awk '/kvm-console:public-ip/ {print $1}' | sort -rn | head -n1)"
    [ -n "$line" ] || break
    "${iptables_cmd[@]}" -D "$chain" "$line" 2>/dev/null || break
  done
}

cleanup_iptables_comments nat PREROUTING
cleanup_iptables_comments nat POSTROUTING
cleanup_iptables_comments filter FORWARD
`)
	for _, bridge := range publicIPManagedBridges() {
		b.WriteString(fmt.Sprintf("ovs-ofctl -O OpenFlow13 del-flows %s %s 2>/dev/null || true\n",
			utils.ShellSingleQuote(bridge), utils.ShellSingleQuote("cookie="+publicIPFlowPrefix+"00000000000000/"+publicIPFlowMask)))
	}
	return b.String()
}

func cleanupPublicIPHostAddressesShell(ipRows []model.PublicIP) string {
	type addrKey struct {
		addr string
		dev  string
	}
	seen := map[addrKey]bool{}
	var items []addrKey
	for _, ipRow := range ipRows {
		if strings.TrimSpace(ipRow.IP) == "" || net.ParseIP(strings.TrimSpace(ipRow.IP)) == nil {
			continue
		}
		uplink := strings.TrimSpace(ipRow.UplinkIF)
		if uplink == "" {
			uplink = ovsUplink()
		}
		if uplink == "" {
			continue
		}
		item := addrKey{addr: publicIPAddrForHost(ipRow), dev: uplink}
		if item.addr == "" || seen[item] {
			continue
		}
		seen[item] = true
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].dev == items[j].dev {
			return items[i].addr < items[j].addr
		}
		return items[i].dev < items[j].dev
	})

	var b strings.Builder
	b.WriteString("# 清理面板托管的宿主机公网地址，后续会按当前绑定重新添加\n")
	for _, item := range items {
		b.WriteString(fmt.Sprintf("ip addr del %s dev %s 2>/dev/null || true\n",
			utils.ShellSingleQuote(item.addr), utils.ShellSingleQuote(item.dev)))
	}
	return b.String()
}

func publicIPManagedBridges() []string {
	seen := map[string]bool{ovsBridgeName(): true}
	bridges := []string{ovsBridgeName()}
	if model.DB != nil {
		var rows []model.NetworkBridge
		model.DB.Find(&rows)
		for _, row := range rows {
			name := strings.TrimSpace(row.Name)
			if name != "" && !seen[name] {
				seen[name] = true
				bridges = append(bridges, name)
			}
		}
	}
	sort.Strings(bridges)
	return bridges
}

func publicIPRuntimeRuleSummary(ipRow model.PublicIP, binding model.PublicIPBinding) []string {
	req := PublicIPBindRequest{
		Username:    binding.Username,
		VMName:      binding.VMName,
		VMPrivateIP: binding.VMPrivateIP,
		Mode:        binding.Mode,
	}
	commands, err := buildPublicIPCommands(ipRow, req)
	if err != nil {
		return []string{err.Error()}
	}
	return commands
}

func markPublicIPBindingsApplied() {
	now := time.Now()
	model.DB.Model(&model.PublicIPBinding{}).Where("1 = 1").Updates(map[string]interface{}{
		"runtime_status":  "applied",
		"last_applied_at": &now,
	})
}

func markPublicIPBindingsRuntimeFailed(message string) {
	model.DB.Model(&model.PublicIPBinding{}).Where("1 = 1").Update("runtime_status", "failed: "+message)
}

func ListPublicIPAttachmentsForVM(vmName string) []PublicIPAttachment {
	vmName = strings.TrimSpace(vmName)
	if vmName == "" || model.DB == nil {
		return []PublicIPAttachment{}
	}
	var bindings []model.PublicIPBinding
	if err := model.DB.Where("vm_name = ?", vmName).Order("public_ip ASC").Find(&bindings).Error; err != nil {
		return []PublicIPAttachment{}
	}
	out := make([]PublicIPAttachment, 0, len(bindings))
	for _, binding := range bindings {
		mode := NormalizePublicIPMode(binding.Mode)
		out = append(out, PublicIPAttachment{
			PublicIP:      binding.PublicIP,
			Mode:          mode,
			ModeLabel:     PublicIPModeLabel(mode),
			VMPrivateIP:   binding.VMPrivateIP,
			RuntimeStatus: binding.RuntimeStatus,
		})
	}
	return out
}

func PublicIPNATPrivateIPsForVM(vmName string) []string {
	vmName = strings.TrimSpace(vmName)
	if vmName == "" || model.DB == nil {
		return []string{}
	}
	var bindings []model.PublicIPBinding
	if err := model.DB.Where("vm_name = ? AND mode = ?", vmName, PublicIPModeNAT).Find(&bindings).Error; err != nil {
		return []string{}
	}
	seen := map[string]bool{}
	var ips []string
	for _, binding := range bindings {
		ip := strings.TrimSpace(binding.VMPrivateIP)
		if ip == "" || net.ParseIP(ip) == nil || seen[ip] {
			continue
		}
		seen[ip] = true
		ips = append(ips, ip)
	}
	sort.Strings(ips)
	return ips
}

func publicIPHasVPCBindings() bool {
	if model.DB == nil {
		return false
	}
	var count int64
	model.DB.Model(&model.VPCVMBinding{}).Count(&count)
	return count > 0
}

func parsePublicIPModes(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = PublicIPModeNAT + "," + PublicIPModeClassicRoute + "," + PublicIPModeClassicBridge
	}
	seen := map[string]bool{}
	var modes []string
	for _, part := range strings.Split(raw, ",") {
		mode := NormalizePublicIPMode(part)
		if mode == "" || seen[mode] {
			continue
		}
		seen[mode] = true
		modes = append(modes, mode)
	}
	return modes
}

func publicIPModeLabels(modes []string) []string {
	labels := make([]string, 0, len(modes))
	for _, mode := range modes {
		labels = append(labels, PublicIPModeLabel(mode))
	}
	return labels
}

func normalizeSupportedPublicIPModes(raw string) string {
	modes := parsePublicIPModes(raw)
	if len(modes) == 0 {
		modes = []string{PublicIPModeNAT}
	}
	return strings.Join(modes, ",")
}

func publicIPModeAllowed(ipRow model.PublicIP, mode string) bool {
	mode = NormalizePublicIPMode(mode)
	for _, item := range parsePublicIPModes(ipRow.SupportedModes) {
		if item == mode {
			return true
		}
	}
	return false
}

func getPublicIP(id uint) (*model.PublicIP, error) {
	var row model.PublicIP
	if err := model.DB.First(&row, id).Error; err != nil {
		return nil, fmt.Errorf("公网 IP 不存在")
	}
	return &row, nil
}

func validatePublicIPCidr(ipText, cidr string) error {
	if strings.Contains(cidr, "/") {
		ip, network, err := net.ParseCIDR(cidr)
		if err != nil {
			return fmt.Errorf("CIDR/掩码格式无效")
		}
		publicIP := net.ParseIP(ipText)
		if publicIP == nil || !network.Contains(publicIP) {
			return fmt.Errorf("公网 IP 不在填写的 CIDR 范围内")
		}
		if ip.String() == ipText {
			return nil
		}
		return nil
	}
	if net.ParseIP(cidr) == nil {
		return fmt.Errorf("CIDR/掩码格式无效")
	}
	return nil
}

func publicIPPrefix(ipRow model.PublicIP) int {
	if strings.Contains(ipRow.CIDR, "/") {
		_, network, err := net.ParseCIDR(ipRow.CIDR)
		if err == nil {
			ones, _ := network.Mask.Size()
			if ones > 0 {
				return ones
			}
		}
	}
	if maskIP := net.ParseIP(strings.TrimSpace(ipRow.CIDR)); maskIP != nil {
		mask := net.IPMask(maskIP.To4())
		if len(mask) == net.IPv4len {
			if ones, bits := mask.Size(); bits == 32 && ones >= 0 {
				return ones
			}
		}
	}
	return 32
}

func publicIPAddrForHost(ipRow model.PublicIP) string {
	prefix := publicIPPrefix(ipRow)
	if prefix <= 0 || prefix > 32 {
		prefix = 32
	}
	return fmt.Sprintf("%s/%d", strings.TrimSpace(ipRow.IP), prefix)
}

func publicIPVMInterface(vmName string) (string, string) {
	for _, iface := range parseVirshDomiflistOutput(utils.ExecCommand("virsh", "domiflist", strings.TrimSpace(vmName)).Stdout) {
		if strings.TrimSpace(iface.Name) != "" && iface.Name != "-" && strings.TrimSpace(iface.Source) != "" {
			return iface.Name, iface.Source
		}
	}
	return "", ""
}

func publicIPVMBridge(vmName string) string {
	_, bridge := publicIPVMInterface(vmName)
	return bridge
}

func publicIPFlowCookie(ipText string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(strings.TrimSpace(ipText)))
	value := h.Sum64() & 0x00ffffffffffffff
	return publicIPFlowPrefix + fmt.Sprintf("%014x", value)
}

func cleanupConntrackForPublicIP(publicIP string) {
	publicIP = strings.TrimSpace(publicIP)
	if publicIP == "" {
		return
	}
	utils.ExecCommand("conntrack", "-D", "-d", publicIP)
	utils.ExecCommand("conntrack", "-D", "-s", publicIP)
}

func copyFile(src, dst string, perm os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, perm)
}

func ParsePublicIPOperationParams(raw string) (PublicIPOperationParams, error) {
	var params PublicIPOperationParams
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		return params, err
	}
	return params, nil
}

func ParsePublicIPID(raw string) (uint, error) {
	id, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil || id == 0 {
		return 0, fmt.Errorf("公网 IP ID 无效")
	}
	return uint(id), nil
}
