package lxc

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"kvm_console/config"
	"kvm_console/logger"
	"kvm_console/model"
	"kvm_console/service/bandwidth"
	"kvm_console/utils"

	"gorm.io/gorm"
)

// AttachContainerToVPC 建立 VPCVMBinding（Kind=lxc）并在容器启动后把 host veth
// 接入 OVS 桥、打 VLAN tag。VLAN/ACL/带宽策略复用既有 VPC 运行时工具（见 Task 7 Step 1
// 探查）：此处采用与 VM 路径一致的 `ovs-vsctl set Port <veth> tag=<vlan>` 直接表达。
func AttachContainerToVPC(name string, switchID, sgID uint) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("容器名不能为空")
	}
	if switchID == 0 {
		return nil // 未指定交换机：用默认桥，不打 VLAN
	}
	binding := model.VPCVMBinding{
		VMName:          name,
		Username:        ownerOf(name),
		SwitchID:        switchID,
		SecurityGroupID: sgID,
		InterfaceOrder:  0,
		Kind:            "lxc",
	}
	if err := model.DB.Where("vm_name = ? AND interface_order = ?", name, 0).
		Assign(binding).FirstOrCreate(&binding).Error; err != nil {
		return fmt.Errorf("写入 VPC 绑定失败: %w", err)
	}
	var sw model.VPCSwitch
	if err := model.DB.First(&sw, switchID).Error; err != nil {
		return fmt.Errorf("交换机不存在: %w", err)
	}
	veth := waitForVeth(name)
	if veth == "" {
		return fmt.Errorf("无法解析容器 %s 的 host veth", name)
	}
	bridge := config.GlobalConfig.OVSBridge
	if bridge == "" {
		bridge = "br-ovs"
	}
	// 接入 OVS 网关桥（端口可能已存在，--may-exist 保证幂等）。
	utils.ExecCommandQuiet("ovs-vsctl", "--may-exist", "add-port", bridge, veth)
	if sw.VLANID > 0 {
		if r := utils.ExecCommand("ovs-vsctl", "set", "Port", veth, fmt.Sprintf("tag=%d", sw.VLANID)); r.Error != nil {
			return fmt.Errorf("设置 VLAN tag 失败: %s", r.Stderr)
		}
	}
	// 回填 host veth 到缓存行。
	model.DB.Model(&model.LXCCache{}).Where("name = ?", name).Update("veth_name", veth)
	return nil
}

// DetachContainerFromVPC 从 OVS 删除全部 host veth 端口（多卡逐个按 MAC 解析）并清理 Kind=lxc 的绑定。
func DetachContainerFromVPC(name string) error {
	if strings.TrimSpace(name) == "" || model.DB == nil {
		return nil
	}
	var bindings []model.VPCVMBinding
	model.DB.Where("vm_name = ? AND kind = ?", name, "lxc").Find(&bindings)
	bridge := defaultBridge()
	seen := map[string]bool{}
	for _, b := range bindings {
		veth := findContainerHostVeth(name, b.InterfaceOrder)
		if veth != "" && !seen[veth] {
			utils.ExecCommandQuiet("ovs-vsctl", "--if-exists", "del-port", bridge, veth)
			seen[veth] = true
		}
	}
	// 兼容：旧版只回填单值 VethName 的容器
	var row model.LXCCache
	if err := model.DB.Where("name = ?", name).First(&row).Error; err == nil && row.VethName != "" && !seen[row.VethName] {
		utils.ExecCommandQuiet("ovs-vsctl", "--if-exists", "del-port", bridge, row.VethName)
	}
	// 多网卡预建端口按稳定名清（停机态 veth 已无，上面的 findContainerHostVeth 可能漏）
	if data, err := os.ReadFile(configPath(name)); err == nil {
		if _, blocks := SplitNICBlocks(string(data)); len(blocks) > 0 {
			cleanStablePorts(name, blocks)
		}
	}
	model.DB.Where("vm_name = ? AND kind = ?", name, "lxc").Delete(&model.VPCVMBinding{})
	return nil
}

// ResolveContainerVPCIP 取容器在 VPC 内的 IPv4（lxc-info -i，多 IP 取首个）。
func ResolveContainerVPCIP(name string) string {
	res := LxcInfo(name)
	if res.ExitCode != 0 {
		return ""
	}
	d, _ := ParseLxcInfo(res.Stdout)
	return firstIP(d.IP)
}

// ---- helpers ----

func ownerOf(name string) string {
	if model.DB == nil {
		return "admin"
	}
	var row model.LXCCache
	if err := model.DB.Where("name = ?", name).First(&row).Error; err == nil {
		return row.OwnerUsername
	}
	return "admin"
}

// waitForVeth 解析容器 order0 网卡在 host 侧的 veth 名（按网络命名空间，非 MAC）。
func waitForVeth(name string) string {
	return findContainerHostVeth(name, 0)
}

// ReadVethCounters 读取 host veth 的累计 rx/tx 字节数（来自 sysfs）。
// 用于流量采集的 lxc 分支：取代 VM 的 libvirt 接口统计。
func ReadVethCounters(veth string) (int64, int64) {
	if strings.TrimSpace(veth) == "" {
		return 0, 0
	}
	return readSysCounter(veth, "rx_bytes"), readSysCounter(veth, "tx_bytes")
}

func readSysCounter(veth, name string) int64 {
	b, err := os.ReadFile(fmt.Sprintf("/sys/class/net/%s/statistics/%s", veth, name))
	if err != nil {
		return 0
	}
	var v int64
	fmt.Sscanf(strings.TrimSpace(string(b)), "%d", &v)
	return v
}

// configPath 返回容器 config 文件路径（lxc.lxcpath/<name>/config）。
func configPath(name string) string {
	return filepath.Join(config.GlobalConfig.LXCLxcPath, name, "config")
}

// applyNicRuntime 对单个 host veth 幂等施加 OVS 端口 + VLAN tag + 下行限速。
// veth 为空（容器未运行 / 暂无 veth）时跳过，不报错；order 0 总会回填 LXCCache.VethName
// 以兼容现有流量采集/Detach 路径。
func applyNicRuntime(name string, order int, sw model.VPCSwitch, binding model.VPCVMBinding) error {
	veth := findContainerHostVeth(name, order)
	if order == 0 {
		// 兼容现有流量采集/Detach 读 LXCCache.VethName；即使容器未运行也清空旧值。
		model.DB.Model(&model.LXCCache{}).Where("name = ?", name).Update("veth_name", veth)
	}
	if veth == "" {
		return nil // 容器未运行，无 veth 可施加
	}
	bridge := strings.TrimSpace(sw.BridgeName)
	if bridge == "" {
		bridge = config.GlobalConfig.OVSBridge
		if bridge == "" {
			bridge = "br-ovs"
		}
	}
	utils.ExecCommandQuiet("ovs-vsctl", "--may-exist", "add-port", bridge, veth)
	if sw.VLANID > 0 {
		if r := utils.ExecCommand("ovs-vsctl", "set", "Port", veth, fmt.Sprintf("tag=%d", sw.VLANID)); r.Error != nil {
			return fmt.Errorf("设置 VLAN tag 失败: %s", r.Stderr)
		}
	}
	// 下行限速（按端口名，libvirt 无关）；0 = 不限
	if binding.BandwidthInboundAvg > 0 {
		applyNicRateLimit(veth, binding.BandwidthInboundAvg)
	}
	return nil
}

// applyNicRateLimit 对 host veth 打 tc 下行限速（Mbps）。best-effort，失败仅告警不中断。
func applyNicRateLimit(veth string, downMbps int) {
	if veth == "" || downMbps <= 0 {
		return
	}
	bandwidth.ApplyTCVPCSwitchDownlinkLimit(veth, downMbps)
}

// ReconcileContainerNICs 在容器启动后对其全部 VPCVMBinding(kind=lxc) 施加 OVS/VLAN/限速。
// 修复「重启丢 OVS」缺口（host veth 每次启动换名 → 旧 Stop 路径删过端口、Start 路径不重接），
// 并使停机态新增的卡在下次启动生效。幂等：缺失 veth / 无绑定 / 已存在的 OVS 端口都不报错。
func ReconcileContainerNICs(name string) error {
	var bindings []model.VPCVMBinding
	if err := model.DB.Where("vm_name = ? AND kind = ?", name, "lxc").
		Order("interface_order ASC").Find(&bindings).Error; err != nil {
		return err
	}
	if len(bindings) == 0 {
		return nil
	}
	// 等 order 0 的 veth 出现（最长 ~5s）：lxc-start 返回后内核创建 veth 有延迟。
	deadline := 5 * time.Second
	start := time.Now()
	for time.Since(start) < deadline {
		if veth := findContainerHostVeth(name, bindings[0].InterfaceOrder); veth != "" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	var lastErr error
	for _, b := range bindings {
		var sw model.VPCSwitch
		if err := model.DB.First(&sw, b.SwitchID).Error; err != nil {
			lastErr = err
			continue
		}
		if err := applyNicRuntime(name, b.InterfaceOrder, sw, b); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// lxcBindingsByOrder 取容器 <name> 的全部 kind=lxc VPC 绑定，按 interface_order 索引。
func lxcBindingsByOrder(name string) map[int]model.VPCVMBinding {
	out := map[int]model.VPCVMBinding{}
	if model.DB == nil {
		return out
	}
	var bs []model.VPCVMBinding
	model.DB.Where("vm_name = ? AND kind = ?", name, "lxc").Find(&bs)
	for _, b := range bs {
		out[b.InterfaceOrder] = b
	}
	return out
}

// stablePortBridge 取某网卡稳定端口所在的 OVS 桥：bound→switch.BridgeName；unbound→默认 br-ovs。
func stablePortBridge(order int, bindings map[int]model.VPCVMBinding) string {
	if b, ok := bindings[order]; ok {
		var sw model.VPCSwitch
		if err := model.DB.First(&sw, b.SwitchID).Error; err == nil {
			if strings.TrimSpace(sw.BridgeName) != "" {
				return sw.BridgeName
			}
		}
	}
	return defaultBridge()
}

// ensureContainerNetConfig 在 lxc-start 前就绪容器网络（每次启动幂等调，单/多网卡统一）：
//  1. 规范 config（去 link、补 veth.pair——存量容器懒迁移）
//  2. rootfs profile 补 MAC（Plan C）
//  3. OVS 预建端口（稳定名 + VLAN），veth 一出生即入桥、零 DHCP 竞态
func ensureContainerNetConfig(name string) {
	cfgPath := configPath(name)
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return
	}
	other, blocks := SplitNICBlocks(string(data))
	if len(blocks) == 0 {
		return
	}
	// 1. 规范 config（ensureNicConfig 去 link + 写 name/veth.pair）；有变化才回写
	before := RenderNICBlocks(blocks)
	ensureNicConfig(name, blocks)
	if RenderNICBlocks(blocks) != before {
		if err := os.WriteFile(cfgPath, []byte(other+RenderNICBlocks(blocks)), 0644); err != nil {
			logger.App.Warn("规范容器网络 config 写回失败", "name", name, "error", err)
		}
	}
	// 2. rootfs profile MAC
	provisionRootfsNICs(name)
	// 3. 预建 OVS 端口
	preCreateContainerOVSPorts(name, blocks)
}

// preCreateContainerOVSPorts 按稳定名在每网卡所属桥预建端口（带绑定 VLAN）。netdev 此刻不存在，
// ovs-vsctl 报 "could not open network device"——非致命（端口已入 OVSDB，veth 出现即自动接管，已验证），用 Quiet 吞错。
func preCreateContainerOVSPorts(name string, blocks map[int]map[string]string) {
	bindings := lxcBindingsByOrder(name)
	for o := range blocks {
		pair := vethPairName(name, o)
		br := stablePortBridge(o, bindings)
		utils.ExecCommandQuiet("ovs-vsctl", "--may-exist", "add-port", br, pair)
		if b, ok := bindings[o]; ok {
			var sw model.VPCSwitch
			if err := model.DB.First(&sw, b.SwitchID).Error; err == nil && sw.VLANID > 0 {
				utils.ExecCommandQuiet("ovs-vsctl", "set", "Port", pair, fmt.Sprintf("tag=%d", sw.VLANID))
			}
		}
	}
}

// cleanStablePorts 按稳定名删容器各网卡所在桥的预建端口（销毁/删网卡用，停机态也能清）。
func cleanStablePorts(name string, blocks map[int]map[string]string) {
	bindings := lxcBindingsByOrder(name)
	for o := range blocks {
		utils.ExecCommandQuiet("ovs-vsctl", "--if-exists", "del-port", stablePortBridge(o, bindings), vethPairName(name, o))
	}
}

const maxLXCInterfaces = 8

// ListContainerInterfaces 列出容器全部网卡（config + VPCVMBinding + 运行态）。
func ListContainerInterfaces(name string) ([]LXCInterfaceInfo, error) {
	data, err := os.ReadFile(configPath(name))
	if err != nil {
		return nil, fmt.Errorf("读取容器 config 失败: %w", err)
	}
	_, blocks := SplitNICBlocks(string(data))
	var bindings []model.VPCVMBinding
	model.DB.Where("vm_name = ? AND kind = ?", name, "lxc").Order("interface_order ASC").Find(&bindings)
	bindingByOrder := map[int]model.VPCVMBinding{}
	for _, b := range bindings {
		bindingByOrder[b.InterfaceOrder] = b
	}
	// order 集合 = config 块 ∪ binding
	orderSet := map[int]bool{}
	for o := range blocks {
		orderSet[o] = true
	}
	for o := range bindingByOrder {
		orderSet[o] = true
	}
	orders := make([]int, 0, len(orderSet))
	for o := range orderSet {
		orders = append(orders, o)
	}
	sort.Ints(orders)
	ip := ResolveContainerVPCIP(name)
	out := make([]LXCInterfaceInfo, 0, len(orders))
	for _, o := range orders {
		blk := blocks[o]
		mac := blk["hwaddr"]
		if mac == "" {
			mac = NICMAC(name, o)
		}
		b := bindingByOrder[o]
		info := LXCInterfaceInfo{
			Order: o, IsPrimary: o == 0, MAC: mac, Link: defaultBridge(),
			SwitchID: b.SwitchID, SecurityGroupID: b.SecurityGroupID,
			BandwidthInboundAvg: b.BandwidthInboundAvg, BandwidthOutboundAvg: b.BandwidthOutboundAvg,
		}
		if varSw, err := lookupSwitch(b.SwitchID); err == nil {
			info.SwitchName = varSw.Name
			info.BridgeMode = varSw.BridgeMode
			info.CIDR = varSw.CIDR
			info.VLANID = varSw.VLANID
			info.Link = varSw.BridgeName
		}
		info.SecurityGroupName = lookupSGName(b.SecurityGroupID)
		if veth := findContainerHostVeth(name, o); veth != "" {
			info.Veth = veth
			rx, tx := ReadVethCounters(veth)
			info.RXBytes, info.TXBytes = rx, tx
		}
		if o == 0 {
			info.IP = ip
		}
		out = append(out, info)
	}
	return out, nil
}

// addInterfaceConfig 写一块附加网卡的 config 块 + VPCVMBinding（停机/运行通用前置），不做热插拔。
// 返回新网卡 order 与其交换机（运行中热插拔需 sw）。写 config 失败时回滚刚写的绑定，避免悬挂。
func addInterfaceConfig(name string, req AddLXCInterfaceRequest) (int, model.VPCSwitch, error) {
	if req.SwitchID == 0 {
		return 0, model.VPCSwitch{}, fmt.Errorf("必须选择 VPC 交换机")
	}
	sw, err := lookupSwitch(req.SwitchID)
	if err != nil {
		return 0, model.VPCSwitch{}, err
	}
	data, err := os.ReadFile(configPath(name))
	if err != nil {
		return 0, sw, fmt.Errorf("读取容器 config 失败: %w", err)
	}
	other, blocks := SplitNICBlocks(string(data))
	if len(blocks) >= maxLXCInterfaces {
		return 0, sw, fmt.Errorf("网卡数量已达上限 %d", maxLXCInterfaces)
	}
	next := nextOrder(blocks)
	blocks[next] = map[string]string{
		"type":   "veth",
		"hwaddr": NICMAC(name, next),
		"flags":  "up",
	}
	binding := model.VPCVMBinding{
		VMName: name, Username: ownerOf(name), SwitchID: req.SwitchID,
		SecurityGroupID: req.SecurityGroupID, InterfaceOrder: next, Kind: "lxc",
		BandwidthInboundAvg: req.BandwidthInboundAvg, BandwidthOutboundAvg: req.BandwidthOutboundAvg,
	}
	if err := model.DB.Where("vm_name = ? AND interface_order = ? AND kind = ?", name, next, "lxc").
		Assign(binding).FirstOrCreate(&binding).Error; err != nil {
		return next, sw, fmt.Errorf("写入 VPC 绑定失败: %w", err)
	}
	if err := writeConfig(name, other, blocks); err != nil {
		// 回滚刚写入的绑定，避免悬挂的 config 缺失
		model.DB.Where("vm_name = ? AND interface_order = ? AND kind = ?", name, next, "lxc").
			Delete(&model.VPCVMBinding{})
		return next, sw, err
	}
	return next, sw, nil
}

// AddContainerInterface 给容器追加一块网卡（已停：仅写 config+绑定；运行中：热插拔）。
func AddContainerInterface(name string, req AddLXCInterfaceRequest) error {
	order, sw, err := addInterfaceConfig(name, req)
	if err != nil {
		return err
	}
	// 写容器内 eth<order> 的 DHCP profile（best-effort，不触发 NM：运行中靠 NM 自动 reload 或
	// 下次启动生效；已停容器下次启动生效）。
	provisionOneRootfsNIC(name, order)
	if !containerRunning(name) {
		return nil // 已停：下次启动由 ReconcileContainerNICs 施加
	}
	return hotplugNic(name, order, sw) // 运行中：热插拔
}

// PrepareContainerNICs 在容器启动前把全部网卡就绪（停机态，不热插拔）：
//   - 主网卡（order 0）：switchID != 0 时建绑定（switchID=0 表示用默认桥、不打 VLAN，跳过）；
//   - 附加网卡：逐张写 config 块 + 绑定。
//
// 之后 StartContainer → lxc-start 按 config 一次性建出全部网卡，ReconcileContainerNICs（在
// StartContainer 内）按绑定统一接 OVS/VLAN/限速（含主卡，替代旧的启动后 AttachContainerToVPC）。
// MAC 由 NICMAC(name,order) 确定性派生，不依赖运行态。任一附加网卡准备失败即返回错误（中止创建）。
func PrepareContainerNICs(name string, switchID, sgID uint, extraNics []AddLXCInterfaceRequest) error {
	if switchID != 0 {
		binding := model.VPCVMBinding{
			VMName: name, Username: ownerOf(name), SwitchID: switchID,
			SecurityGroupID: sgID, InterfaceOrder: 0, Kind: "lxc",
		}
		if err := model.DB.Where("vm_name = ? AND interface_order = ? AND kind = ?", name, 0, "lxc").
			Assign(binding).FirstOrCreate(&binding).Error; err != nil {
			return fmt.Errorf("写入主网卡绑定失败: %w", err)
		}
	}
	for _, nic := range extraNics {
		if _, _, err := addInterfaceConfig(name, nic); err != nil {
			return fmt.Errorf("附加网卡准备失败: %w", err)
		}
	}
	return nil
}

// UpdateContainerInterface 编辑某网卡（换交换机/限速/安全组）。MAC 不变。
func UpdateContainerInterface(name string, order int, req AddLXCInterfaceRequest) error {
	sw, err := lookupSwitch(req.SwitchID)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(configPath(name))
	if err != nil {
		return err
	}
	other, blocks := SplitNICBlocks(string(data))
	if blocks[order] == nil {
		return fmt.Errorf("网卡 order=%d 不存在", order)
	}
	// Fix A: order 0 主网卡换交换机时，若旧交换机已按 MAC 绑定静态 IP，阻止切换 ——
	// 旧 dhcp-hosts-<oldSwitchID> 条目不会随切换迁移/清理，会留下孤立条目导致容器丢静态 IP。
	if order == 0 {
		var cur model.VPCVMBinding
		model.DB.Where("vm_name = ? AND interface_order = ? AND kind = ?", name, 0, "lxc").First(&cur)
		if cur.SwitchID != req.SwitchID {
			mac := blocks[order]["hwaddr"]
			if mac == "" {
				mac = NICMAC(name, order)
			}
			if GetVPCStaticIPByMACExported(cur.SwitchID, mac) != "" {
				return fmt.Errorf("主网卡已绑定静态 IP，请先在网络 tab 解绑后再更换交换机")
			}
		}
	}
	bridge := sw.BridgeName
	if strings.TrimSpace(bridge) == "" {
		bridge = defaultBridge()
	}
	blocks[order]["link"] = bridge // 仅换 link，hwaddr/type/flags 保留
	// Fix B: 先更新 DB，再写 config；config 写失败时按 prior 回滚 binding 字段，
	// 避免出现「config 已写新桥 + binding 仍是旧交换机」的错位（下次 Reconcile 会用旧 VLAN/限速施加到新桥 veth）。
	var prior model.VPCVMBinding
	model.DB.Where("vm_name = ? AND interface_order = ? AND kind = ?", name, order, "lxc").First(&prior)
	updates := map[string]interface{}{
		"switch_id": req.SwitchID, "security_group_id": req.SecurityGroupID,
		"bandwidth_inbound_avg": req.BandwidthInboundAvg, "bandwidth_outbound_avg": req.BandwidthOutboundAvg,
	}
	if err := model.DB.Model(&model.VPCVMBinding{}).
		Where("vm_name = ? AND interface_order = ? AND kind = ?", name, order, "lxc").Updates(updates).Error; err != nil {
		return err
	}
	if err := writeConfig(name, other, blocks); err != nil {
		// 配置写失败：best-effort 把 binding 字段回滚到 prior，与未变更的 config 保持一致
		model.DB.Model(&model.VPCVMBinding{}).
			Where("vm_name = ? AND interface_order = ? AND kind = ?", name, order, "lxc").
			Updates(map[string]interface{}{
				"switch_id": prior.SwitchID, "security_group_id": prior.SecurityGroupID,
				"bandwidth_inbound_avg": prior.BandwidthInboundAvg, "bandwidth_outbound_avg": prior.BandwidthOutboundAvg,
			})
		return err
	}
	// 重新施加运行态（VLAN/限速随交换机变化）
	var b model.VPCVMBinding
	model.DB.Where("vm_name = ? AND interface_order = ? AND kind = ?", name, order, "lxc").First(&b)
	if containerRunning(name) {
		return applyNicRuntime(name, order, sw, b)
	}
	return nil
}

// RemoveContainerInterface 删除某网卡并重排索引。order==0 主卡需 force=true。
func RemoveContainerInterface(name string, order int, force bool) error {
	data, err := os.ReadFile(configPath(name))
	if err != nil {
		return err
	}
	other, blocks := SplitNICBlocks(string(data))
	if blocks[order] == nil {
		return fmt.Errorf("网卡 order=%d 不存在", order)
	}
	mac := blocks[order]["hwaddr"]
	if mac == "" {
		mac = NICMAC(name, order)
	}
	// Fix C: 在 delete/compact 前深拷贝 blocks，作为事务失败时 best-effort 回滚 config 的 pre-compaction 快照。
	// （other 不会被改动，复用同一变量即可。）
	origBlocks := make(map[int]map[string]string, len(blocks))
	for o, blk := range blocks {
		cp := make(map[string]string, len(blk))
		for k, v := range blk {
			cp[k] = v
		}
		origBlocks[o] = cp
	}
	// 已绑静态 IP 的卡：必须先解绑（直接按 binding.SwitchID 查，避免 switch 行缺失时绕过校验）
	var b model.VPCVMBinding
	model.DB.Where("vm_name = ? AND interface_order = ? AND kind = ?", name, order, "lxc").First(&b)
	if b.SwitchID != 0 && mac != "" {
		if GetVPCStaticIPByMACExported(b.SwitchID, mac) != "" {
			return fmt.Errorf("该网卡已绑定静态 IP，请先在网络 tab 解绑后再删除")
		}
	}
	if order == 0 && !force {
		return fmt.Errorf("主网卡需二次确认（force=true）方可删除")
	}
	// 运行中：热拔
	if containerRunning(name) {
		hotunplugNic(name, order)
	} else {
		// 停机态：删网卡会 CompactNICBlocks 重排 order → 剩余网卡稳定名（含 hash）变化，
		// 清全部旧稳定端口防孤儿（下次 start 由 preCreateContainerOVSPorts 重建）。
		// 运行态不清（会 del-port 在跑的其余网卡致断网），仅上面 hotunplugNic 清被删卡。
		cleanStablePorts(name, blocks)
	}
	delete(blocks, order)
	blocks = CompactNICBlocks(blocks)
	if err := writeConfig(name, other, blocks); err != nil {
		return err
	}
	// 删旧绑定 + 重排其余绑定 interface_order（事务内原子完成）
	var all []model.VPCVMBinding
	model.DB.Where("vm_name = ? AND kind = ?", name, "lxc").Order("interface_order ASC").Find(&all)
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("vm_name = ? AND interface_order = ? AND kind = ?", name, order, "lxc").
			Delete(&model.VPCVMBinding{}).Error; err != nil {
			return err
		}
		newIdx := 0
		for _, bb := range all {
			if bb.InterfaceOrder == order {
				continue
			}
			if bb.InterfaceOrder != newIdx {
				if err := tx.Model(&model.VPCVMBinding{}).Where("id = ?", bb.ID).
					Update("interface_order", newIdx).Error; err != nil {
					return err
				}
			}
			newIdx++
		}
		return nil
	}); err != nil {
		// 事务失败：binding 未变更，best-effort 把 config 回滚到 pre-compaction 状态以保持一致
		writeConfig(name, other, origBlocks)
		return fmt.Errorf("重排网卡绑定失败: %w", err)
	}
	return nil
}

func lookupSwitch(id uint) (model.VPCSwitch, error) {
	var sw model.VPCSwitch
	if err := model.DB.First(&sw, id).Error; err != nil {
		return sw, fmt.Errorf("交换机不存在: %w", err)
	}
	return sw, nil
}
func lookupSGName(id uint) string {
	if id == 0 {
		return ""
	}
	var sg model.VPCSecurityGroup
	if err := model.DB.First(&sg, id).Error; err != nil {
		return ""
	}
	return sg.Name
}
func defaultBridge() string {
	if b := config.GlobalConfig.OVSBridge; b != "" {
		return b
	}
	return "br-ovs"
}
func nextOrder(blocks map[int]map[string]string) int {
	max := -1
	for o := range blocks {
		if o > max {
			max = o
		}
	}
	return max + 1
}
func writeConfig(name, other string, blocks map[int]map[string]string) error {
	ensureNicConfig(name, blocks) // 去 link + name=eth<order> + veth.pair（统一，单/多网卡一致）
	return os.WriteFile(configPath(name), []byte(other+RenderNICBlocks(blocks)), 0644)
}
func containerRunning(name string) bool {
	r := LxcInfo(name)
	if r.ExitCode != 0 {
		return false
	}
	d, _ := ParseLxcInfo(r.Stdout)
	return strings.Contains(strings.ToUpper(d.Status), "RUNNING")
}

// hotplugNic / hotunplugNic：运行中容器的 veth 热加/热拔（best-effort，失败返回 needs_restart 语义见 handler）。
func hotplugNic(name string, order int, sw model.VPCSwitch) error {
	pid := containerPID(name)
	if pid == "" {
		return fmt.Errorf("无法获取容器 PID，请重启容器使配置生效")
	}
	// veth 名须 ≤15 字符（Linux IFNAMSIZ 上限）。容器名写进 veth 名会让默认名 lxc-XXXXXX（10 字符）
	// 撑爆长度；findContainerHostVeth 按 peer ifindex 回查，不依赖 veth 名编码容器信息，故改用
	// name+order 的短哈希。a=host 侧（接 OVS），c=容器侧 peer（随即改名 eth{order}，与 config 的
	// lxc.net.<order>.name 及 findContainerHostVeth(name,order) 对齐）。
	h := shortVethHash(name, order)
	a := fmt.Sprintf("vxa%d%s", order, h)
	c := fmt.Sprintf("vxb%d%s", order, h)
	if r := utils.ExecCommand("ip", "link", "add", a, "type", "veth", "peer", "name", c); r.Error != nil {
		return fmt.Errorf("热插拔建 veth 失败，请重启容器: %s", r.Stderr)
	}
	utils.ExecCommandQuiet("ip", "link", "set", c, "netns", pid)
	// a/c derive from validated container name → injection-safe by construction
	// 容器内改名 eth{order}：order 即 config 网卡序号（net.1→eth1），须与 lxc.net.<order>.name、
	// findContainerHostVeth(name,order) 一致。旧写 eth{order+1} 是 off-by-one（首张附加卡被命名成 eth2）。
	utils.ExecShell(fmt.Sprintf("lxc-attach -n %s -- sh -c 'ip link set %s name eth%d; ip link set eth%d up' 2>/dev/null",
		utils.ShellSingleQuote(name), c, order, order))
	bridge := sw.BridgeName
	if strings.TrimSpace(bridge) == "" {
		bridge = defaultBridge()
	}
	utils.ExecCommandQuiet("ovs-vsctl", "--may-exist", "add-port", bridge, a)
	if sw.VLANID > 0 {
		utils.ExecCommandQuiet("ovs-vsctl", "set", "Port", a, fmt.Sprintf("tag=%d", sw.VLANID))
	}
	// 注：热插拔的 veth host 侧 MAC 为随机值，与 lxc.net.N.hwaddr 不一致；
	//     后续 ReconcileContainerNICs 仍按 config MAC 找不到该 veth，故热插拔为「临时生效」，
	//     持久态依赖下次重启由 lxc-start 按 config 重建。引导用户重启以获一致状态。
	return nil
}

// shortVethHash 取 name+order 的短哈希（8 hex）用于构造 veth 名，保证 ≤15 字符且按容器/order 唯一。
// findContainerHostVeth 按 peer ifindex 回查 host veth，故 veth 名无需编码容器名。
func shortVethHash(name string, order int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d", name, order)))
	return hex.EncodeToString(sum[:4]) // 前 4 字节 = 8 hex
}

// vethPairName 返回容器 <name> 第 <order> 网卡的稳定 host veth 名（lxc.net.<order>.veth.pair 用）。
// app 在 lxc-start 前按此名预建 OVS 端口；去 link 后 LXC 不原生 attach，veth 出生即绑预建端口。
// 复用 shortVethHash 保证 ≤15 字符(IFNAMSIZ)、跨容器/order 唯一。例：vp0a1b2c3d4。
func vethPairName(name string, order int) string {
	return fmt.Sprintf("vp%d%s", order, shortVethHash(name, order))
}

func hotunplugNic(name string, order int) {
	br := stablePortBridge(order, lxcBindingsByOrder(name))
	pair := vethPairName(name, order)
	// 清 OVS 端口：稳定名（新容器 port 名即 veth.pair）+ live veth 名（旧容器二者不同）。
	// 历史用 defaultBridge()，对非 br-ovs 网卡（如 br0）会 del-port 到错的桥（--if-exists 吞错成空操作）→ 端口孤儿。
	utils.ExecCommandQuiet("ovs-vsctl", "--if-exists", "del-port", br, pair)
	if veth := findContainerHostVeth(name, order); veth != "" {
		if veth != pair {
			utils.ExecCommandQuiet("ovs-vsctl", "--if-exists", "del-port", br, veth)
		}
		utils.ExecCommandQuiet("ip", "link", "del", veth)
	}
}
func containerPID(name string) string {
	r := utils.ExecCommand("lxc-info", "-n", name, "-p")
	if r.Error != nil {
		return ""
	}
	for _, line := range strings.Split(r.Stdout, "\n") {
		if strings.Contains(line, "PID:") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				return f[len(f)-1]
			}
		}
	}
	return ""
}

// GetVPCStaticIPByMACExported 读交换机 dhcp-hosts 文件，返回 MAC 对应已绑 IP（空=未绑）。
func GetVPCStaticIPByMACExported(switchID uint, mac string) string {
	mac = strings.ToLower(strings.TrimSpace(mac))
	if mac == "" {
		return ""
	}
	b, err := os.ReadFile(fmt.Sprintf("/etc/kvm-console/vpc/dhcp-hosts-%d", switchID))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ",")
		if len(fields) >= 2 && strings.EqualFold(strings.TrimSpace(fields[0]), mac) {
			return strings.TrimSpace(fields[1])
		}
	}
	return ""
}
