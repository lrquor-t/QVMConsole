package lxc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"kvm_console/config"
	"kvm_console/model"
	"kvm_console/service/bandwidth"
	"kvm_console/utils"
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

// DetachContainerFromVPC 从 OVS 删除 host veth 端口并清理 Kind=lxc 的绑定。
func DetachContainerFromVPC(name string) error {
	if strings.TrimSpace(name) == "" || model.DB == nil {
		return nil
	}
	var row model.LXCCache
	_ = model.DB.Where("name = ?", name).First(&row).Error
	if row.VethName != "" {
		bridge := config.GlobalConfig.OVSBridge
		if bridge == "" {
			bridge = "br-ovs"
		}
		utils.ExecCommandQuiet("ovs-vsctl", "--if-exists", "del-port", bridge, row.VethName)
	}
	model.DB.Where("vm_name = ? AND kind = ?", name, "lxc").Delete(&model.VPCVMBinding{})
	return nil
}

// ResolveContainerVPCIP 取容器在 VPC 内的 IPv4（lxc-info -i）。
func ResolveContainerVPCIP(name string) string {
	res := LxcInfo(name)
	if res.ExitCode != 0 {
		return ""
	}
	d, _ := ParseLxcInfo(res.Stdout)
	fields := strings.Fields(d.IP)
	if len(fields) == 0 {
		return ""
	}
	return strings.TrimSpace(fields[0])
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

// waitForVeth 从 LXCCache 读容器 MAC，再在 host 上按 MAC 解析 veth 名。
func waitForVeth(name string) string {
	if model.DB == nil {
		return ""
	}
	var row model.LXCCache
	if err := model.DB.Where("name = ?", name).First(&row).Error; err != nil {
		return ""
	}
	return findVethByMAC(row.MacAddress)
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
func applyNicRuntime(name string, order int, mac string, sw model.VPCSwitch, binding model.VPCVMBinding) error {
	veth := findVethByMAC(mac)
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
		if veth := findVethByMAC(nicMACForBinding(name, bindings[0])); veth != "" {
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
		mac := nicMACForBinding(name, b)
		if err := applyNicRuntime(name, b.InterfaceOrder, mac, sw, b); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// nicMACForBinding 取某 binding 的 MAC：优先 config 的 lxc.net.<order>.hwaddr，
// 回退 NICMAC 派生（确保 config 不可读时仍能用确定性 MAC 找到 veth）。
func nicMACForBinding(name string, b model.VPCVMBinding) string {
	cfg := configPath(name)
	if data, err := os.ReadFile(cfg); err == nil {
		if _, blocks := SplitNICBlocks(string(data)); blocks[b.InterfaceOrder]["hwaddr"] != "" {
			return blocks[b.InterfaceOrder]["hwaddr"]
		}
	}
	return NICMAC(name, b.InterfaceOrder)
}
