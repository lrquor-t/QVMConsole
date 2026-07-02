package lxc

import (
	"fmt"
	"os"
	"strings"

	"kvm_console/config"
	"kvm_console/model"
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
