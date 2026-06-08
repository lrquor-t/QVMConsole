package service

import (
	"fmt"
	"hash/fnv"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"kvm_console/config"
	"kvm_console/model"
	"kvm_console/utils"
)

// ==================== 网卡速率限制管理 ====================
// 运行中 VM 在 OVS 上按外网流量限速，配置值通过 virsh domiftune 持久化。

// BandwidthInfo 带宽信息（KB/s 或 KB）
type BandwidthInfo struct {
	Average int `json:"average"` // 平峰速率 KB/s
	Peak    int `json:"peak"`    // 峰值速率 KB/s
	Burst   int `json:"burst"`   // 突发量 KB
}

// BandwidthDetail VM 带宽详情（用户可读的 Mbps）
type BandwidthDetail struct {
	InboundAvg    int `json:"inbound_avg"`    // 下行平峰 Mbps
	InboundPeak   int `json:"inbound_peak"`   // 下行峰值 Mbps
	InboundBurst  int `json:"inbound_burst"`  // 下行突发量 KB
	OutboundAvg   int `json:"outbound_avg"`   // 上行平峰 Mbps
	OutboundPeak  int `json:"outbound_peak"`  // 上行峰值 Mbps
	OutboundBurst int `json:"outbound_burst"` // 上行突发量 KB
}

type vmBandwidthConfigRaw struct {
	InboundAvg    int
	InboundPeak   int
	InboundBurst  int
	OutboundAvg   int
	OutboundPeak  int
	OutboundBurst int
}

// MbpsToKBps 将 Mbps 转换为 KB/s（1 Mbps = 1000/8 KB/s = 125 KB/s）
func MbpsToKBps(mbps int) int {
	return mbps * 125
}

// KBpsToMbps 将 KB/s 转换为 Mbps（向下取整）
func KBpsToMbps(kbps int) int {
	if kbps <= 0 {
		return 0
	}
	return kbps / 125
}

// getVMMAC 获取 VM 的第一个网卡 MAC 地址
func getVMMAC(vmName string) string {
	result := utils.ExecShell(fmt.Sprintf(
		"virsh domiflist %s 2>/dev/null | grep -oP '([0-9a-f]{2}:){5}[0-9a-f]{2}' | head -1", utils.ShellSingleQuote(vmName)))
	if result.Error != nil {
		return ""
	}
	return strings.TrimSpace(result.Stdout)
}

// getVMVnetIF 获取运行中 VM 的 vnet 接口名称
func getVMVnetIF(vmName string) string {
	result := utils.ExecShell(fmt.Sprintf(
		"virsh domiflist %s 2>/dev/null | awk 'NR>2 && $1 ~ /^vnet/ {print $1; exit}'", utils.ShellSingleQuote(vmName)))
	if result.Error != nil || strings.TrimSpace(result.Stdout) == "" || strings.TrimSpace(result.Stdout) == "-" {
		return ""
	}
	return strings.TrimSpace(result.Stdout)
}

func clearTCBandwidthLimit(vnetIF string) {
	if vnetIF == "" {
		return
	}
	utils.ExecShell(fmt.Sprintf("tc qdisc del dev %s root 2>/dev/null", utils.ShellSingleQuote(vnetIF)))
	clearTCUploadLimit(vnetIF)
}

func tcRateKbit(avgKBps int) int {
	if avgKBps <= 0 {
		return 0
	}
	return avgKBps * 8
}

func tcBurstBytes(avgKBps int) int {
	burstBytes := avgKBps * 1024 / 10
	if burstBytes < 15360 {
		return 15360
	}
	return burstBytes
}

func tcIFBTxQueueLen() int {
	return 100
}

func applyTCVPCSwitchDownlinkLimit(gwPort string, downMbps int) {
	if gwPort == "" {
		return
	}
	utils.ExecShell(fmt.Sprintf("tc qdisc del dev %s root 2>/dev/null", utils.ShellSingleQuote(gwPort)))
	if downMbps <= 0 {
		return
	}
	rateKbit := downMbps * 1000
	burstBytes := downMbps * 1000 * 1024 / 10
	if burstBytes < 15360 {
		burstBytes = 15360
	}
	result := utils.ExecShell(fmt.Sprintf(
		"tc qdisc add dev %s root handle 1: htb default 1", utils.ShellSingleQuote(gwPort)))
	if result.Error != nil {
		fmt.Printf("[警告] 添加 VPC 网关 tc qdisc 失败 (%s): %s\n", gwPort, result.Stderr)
		return
	}
	result = utils.ExecShell(fmt.Sprintf(
		"tc class add dev %s parent 1: classid 1:1 htb rate %dkbit ceil %dkbit burst %d",
		utils.ShellSingleQuote(gwPort), rateKbit, rateKbit, burstBytes))
	if result.Error != nil {
		fmt.Printf("[警告] 添加 VPC 网关 tc class 失败 (%s): %s\n", gwPort, result.Stderr)
	}
}

func clearTCVPCSwitchDownlinkLimit(gwPort string) {
	if gwPort == "" {
		return
	}
	utils.ExecShell(fmt.Sprintf("tc qdisc del dev %s root 2>/dev/null", utils.ShellSingleQuote(gwPort)))
}

// applyTCDownloadLimit 使用 tc 命令在 vnet 接口的 egress 方向设置下行限速
// 解决 virsh domiftune inbound 限速不生效的 libvirt 已知问题
// 从宿主机 vnet 接口角度：egress(发出) = 数据发往VM = VM的下行
// 注意：tc HTB 的 burst 是 token bucket 大小，不等于 virsh domiftune 的 burst（突发时长）
// 这里用 rate=ceil 做硬限制，突发行为由 TC 自己控制
func applyTCDownloadLimit(vnetIF string, avgKBps, peakKBps, burstKB int) {
	if vnetIF == "" {
		return
	}

	// 先清除已有的 tc qdisc
	utils.ExecShell(fmt.Sprintf("tc qdisc del dev %s root 2>/dev/null", utils.ShellSingleQuote(vnetIF)))

	if avgKBps <= 0 {
		return // 不限制，清除即可
	}

	// rate = average（硬限制速率）
	rateKbit := tcRateKbit(avgKBps)
	burstBytes := tcBurstBytes(avgKBps)

	// 创建根 qdisc
	result := utils.ExecShell(fmt.Sprintf(
		"tc qdisc add dev %s root handle 1: htb default 1", utils.ShellSingleQuote(vnetIF)))
	if result.Error != nil {
		fmt.Printf("[警告] 添加 tc qdisc 失败 (%s): %s\n", vnetIF, result.Stderr)
		return
	}

	// 创建限速 class，rate=ceil 做硬限制
	result = utils.ExecShell(fmt.Sprintf(
		"tc class add dev %s parent 1: classid 1:1 htb rate %dkbit ceil %dkbit burst %d",
		utils.ShellSingleQuote(vnetIF), rateKbit, rateKbit, burstBytes))
	if result.Error != nil {
		fmt.Printf("[警告] 添加 tc class 失败 (%s): %s\n", vnetIF, result.Stderr)
	}
}

func tcUploadIFBName(vnetIF string) string {
	cleaned := strings.TrimSpace(vnetIF)
	if cleaned == "" {
		return ""
	}
	cleaned = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, cleaned)
	name := "ifb-" + cleaned
	if len(name) <= 15 {
		return name
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(cleaned))
	return fmt.Sprintf("ifb%x", h.Sum32())
}

func clearTCUploadLimit(vnetIF string) {
	if vnetIF == "" {
		return
	}
	ifbIF := tcUploadIFBName(vnetIF)
	utils.ExecShell(fmt.Sprintf("tc qdisc del dev %s ingress 2>/dev/null", utils.ShellSingleQuote(vnetIF)))
	if ifbIF != "" {
		utils.ExecShell(fmt.Sprintf("tc qdisc del dev %s root 2>/dev/null", utils.ShellSingleQuote(ifbIF)))
		utils.ExecShell(fmt.Sprintf("ip link set %s down 2>/dev/null || true", utils.ShellSingleQuote(ifbIF)))
		utils.ExecShell(fmt.Sprintf("ip link del %s 2>/dev/null || true", utils.ShellSingleQuote(ifbIF)))
	}
}

// applyTCUploadLimit 使用 IFB + HTB 在 vnet 入口方向设置上行整形。
// 从宿主机 vnet 接口角度：ingress(进入) = VM 发出的包 = VM 的上行。
// 相比 ingress police 直接丢包，IFB 队列整形能显著降低单 TCP 流的重传。
func applyTCUploadLimit(vnetIF string, avgKBps int) {
	if vnetIF == "" {
		return
	}

	clearTCUploadLimit(vnetIF)

	if avgKBps <= 0 {
		return
	}

	rateKbit := tcRateKbit(avgKBps)
	burstBytes := tcBurstBytes(avgKBps)
	ifbIF := tcUploadIFBName(vnetIF)
	if ifbIF == "" {
		return
	}

	utils.ExecShell("modprobe ifb 2>/dev/null || true")
	result := utils.ExecShell(fmt.Sprintf("ip link show %s >/dev/null 2>&1 || ip link add %s type ifb",
		utils.ShellSingleQuote(ifbIF), utils.ShellSingleQuote(ifbIF)))
	if result.Error != nil {
		fmt.Printf("[警告] 创建 IFB 上行整形接口失败 (%s): %s\n", ifbIF, result.Stderr)
		return
	}
	result = utils.ExecShell(fmt.Sprintf("ip link set %s up", utils.ShellSingleQuote(ifbIF)))
	if result.Error != nil {
		fmt.Printf("[警告] 启用 IFB 上行整形接口失败 (%s): %s\n", ifbIF, result.Stderr)
		return
	}
	result = utils.ExecShell(fmt.Sprintf("ip link set dev %s txqueuelen %d", utils.ShellSingleQuote(ifbIF), tcIFBTxQueueLen()))
	if result.Error != nil {
		fmt.Printf("[警告] 调整 IFB 上行队列长度失败 (%s): %s\n", ifbIF, result.Stderr)
	}
	result = utils.ExecShell(fmt.Sprintf(
		"tc qdisc add dev %s root handle 1: htb default 1", utils.ShellSingleQuote(ifbIF)))
	if result.Error != nil {
		fmt.Printf("[警告] 添加 IFB 上行 qdisc 失败 (%s): %s\n", ifbIF, result.Stderr)
		return
	}
	result = utils.ExecShell(fmt.Sprintf(
		"tc class add dev %s parent 1: classid 1:1 htb rate %dkbit ceil %dkbit burst %d",
		utils.ShellSingleQuote(ifbIF), rateKbit, rateKbit, burstBytes))
	if result.Error != nil {
		fmt.Printf("[警告] 添加 IFB 上行 class 失败 (%s): %s\n", ifbIF, result.Stderr)
		return
	}
	result = utils.ExecShell(fmt.Sprintf(
		"tc qdisc add dev %s parent 1:1 handle 10: fq_codel limit 100 target 20ms interval 100ms",
		utils.ShellSingleQuote(ifbIF)))
	if result.Error != nil {
		fmt.Printf("[警告] 添加 IFB 上行 fq_codel 队列失败 (%s): %s\n", ifbIF, result.Stderr)
	}

	result = utils.ExecShell(fmt.Sprintf(
		"tc qdisc add dev %s ingress", utils.ShellSingleQuote(vnetIF)))
	if result.Error != nil {
		fmt.Printf("[警告] 添加 tc ingress qdisc 失败 (%s): %s\n", vnetIF, result.Stderr)
		return
	}

	result = utils.ExecShell(fmt.Sprintf(
		"tc filter add dev %s parent ffff: protocol all prio 1 matchall action mirred egress redirect dev %s",
		utils.ShellSingleQuote(vnetIF), utils.ShellSingleQuote(ifbIF)))
	if result.Error != nil {
		result = utils.ExecShell(fmt.Sprintf(
			"tc filter add dev %s parent ffff: protocol all prio 1 u32 match u32 0 0 action mirred egress redirect dev %s",
			utils.ShellSingleQuote(vnetIF), utils.ShellSingleQuote(ifbIF)))
		if result.Error != nil {
			fmt.Printf("[警告] 添加 tc 上行 IFB 重定向规则失败 (%s -> %s): %s\n", vnetIF, ifbIF, result.Stderr)
		}
	}
}

func getOVSInterfaceOfPort(vnetIF string) string {
	if strings.TrimSpace(vnetIF) == "" {
		return ""
	}
	result := utils.ExecCommand("ovs-vsctl", "get", "Interface", strings.TrimSpace(vnetIF), "ofport")
	if result.Error != nil {
		return ""
	}
	ofport := strings.TrimSpace(result.Stdout)
	if ofport == "" || ofport == "-1" || ofport == "[]" {
		return ""
	}
	return ofport
}

func getVMBandwidthIP(vmName, mac string) string {
	if host, ok := GetOVSStaticHostByVMName(vmName); ok {
		return host.IP
	}
	// 补充查询所有 VPC 交换机的静态主机绑定（VM 名称匹配）
	if allVpcHosts, err := ListAllVPCStaticHosts(); err == nil {
		for _, host := range allVpcHosts {
			if strings.TrimSpace(host.VMName) == strings.TrimSpace(vmName) {
				return host.IP
			}
		}
	}
	if strings.TrimSpace(mac) != "" {
		if ip := GetOVSStaticIPByMAC(mac); ip != "" {
			return ip
		}
		// 补充查询所有 VPC 交换机的静态 MAC 绑定
		if allVpcHosts, err := ListAllVPCStaticHosts(); err == nil {
			for _, host := range allVpcHosts {
				if strings.EqualFold(host.MAC, mac) {
					return host.IP
				}
			}
		}
		if ip := GetOVSLeaseIPByMAC(mac); ip != "" {
			return ip
		}
	}
	return ""
}

func ovsBandwidthCookie(vmName string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte("kvm-console-bandwidth:" + vmName))
	return fmt.Sprintf("0x%x", h.Sum64())
}

func ovsBandwidthQueueID(vmName, direction string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte("kvm-console-bandwidth:" + vmName + ":" + direction))
	// OVS 队列 ID 使用较小正整数，便于 set_queue 和人工排查。
	return 1000 + h.Sum32()%60000
}

func ovsBandwidthMeterID(vmName, direction string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte("kvm-console-bandwidth:" + vmName + ":" + direction))
	// meter ID 使用较大正整数，避免和人工维护的小编号冲突。
	return 100000 + h.Sum32()%900000000
}

func ovsBandwidthMaxRateBps(avgKBps int) int {
	if avgKBps <= 0 {
		return 0
	}
	return avgKBps * 8000
}

func ovsBandwidthQueueKey(queueID uint32) string {
	return strconv.FormatUint(uint64(queueID), 10)
}

func ovsBandwidthMeterArg(meterID uint32) string {
	return "meter=" + strconv.FormatUint(uint64(meterID), 10)
}

func ovsBandwidthRateKbit(avgKBps int) int {
	if avgKBps <= 0 {
		return 0
	}
	return avgKBps * 8
}

func domiftuneBandwidthArg(avg, peak, burst int) string {
	if avg <= 0 && peak <= 0 && burst <= 0 {
		return "0,0,0"
	}
	return fmt.Sprintf("%d,%d,%d", avg, peak, burst)
}

func parseVMBandwidthConfigRaw(output string) vmBandwidthConfigRaw {
	config := vmBandwidthConfigRaw{}
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val, _ := strconv.Atoi(strings.TrimSpace(parts[1]))

		switch key {
		case "inbound.average":
			config.InboundAvg = val
		case "inbound.peak":
			config.InboundPeak = val
		case "inbound.burst":
			config.InboundBurst = val
		case "outbound.average":
			config.OutboundAvg = val
		case "outbound.peak":
			config.OutboundPeak = val
		case "outbound.burst":
			config.OutboundBurst = val
		}
	}
	return config
}

func getVMBandwidthConfigRaw(vmName string) (vmBandwidthConfigRaw, error) {
	mac := getVMMAC(vmName)
	if mac == "" {
		return vmBandwidthConfigRaw{}, fmt.Errorf("无法获取虚拟机 %s 的网卡 MAC 地址", vmName)
	}

	result := utils.ExecCommand("virsh", "domiftune", vmName, mac, "--config")
	if result.Error != nil {
		return vmBandwidthConfigRaw{}, fmt.Errorf("获取速率限制失败: %s", result.Stderr)
	}
	return parseVMBandwidthConfigRaw(result.Stdout), nil
}

// ReapplyConfiguredVMBandwidth 按持久化配置重刷运行态带宽规则。
// 这一步会在 VM 重新获得新的 vnet/ofport 后清理旧流表，再按当前端口重新下发。
func ReapplyConfiguredVMBandwidth(vmName string) error {
	config, err := getVMBandwidthConfigRaw(vmName)
	if err != nil {
		return err
	}
	return ApplyVMBandwidth(vmName,
		config.InboundAvg, config.InboundPeak, config.InboundBurst,
		config.OutboundAvg, config.OutboundPeak, config.OutboundBurst,
	)
}

func buildOVSBandwidthFlows(cookie, ofport, vmIP, subnetCIDR string, downQueueID, upMeterID uint32, downRateBps, upRateKbit int) []string {
	var flows []string
	if upRateKbit > 0 {
		flows = append(flows,
			fmt.Sprintf("cookie=%s,priority=220,in_port=%s,ip,nw_src=%s,nw_dst=%s,actions=NORMAL", cookie, ofport, vmIP, subnetCIDR),
			fmt.Sprintf("cookie=%s,priority=120,in_port=%s,ip,nw_src=%s,actions=meter:%d,output:LOCAL", cookie, ofport, vmIP, upMeterID),
		)
	}
	if downRateBps > 0 {
		flows = append(flows,
			fmt.Sprintf("cookie=%s,priority=220,in_port=LOCAL,ip,nw_src=%s,nw_dst=%s,actions=NORMAL", cookie, subnetCIDR, vmIP),
			fmt.Sprintf("cookie=%s,priority=120,in_port=LOCAL,ip,nw_dst=%s,actions=set_queue:%d,output:%s,pop_queue", cookie, vmIP, downQueueID, ofport),
		)
	}
	return flows
}

func buildOVSVPCBandwidthFlows(cookie, vmOfport, gatewayOfport, vmIP, switchCIDR string, downQueueID, upMeterID uint32, downRateBps, upRateKbit int) []string {
	var flows []string
	if upRateKbit > 0 || downRateBps > 0 {
		flows = append(flows,
			fmt.Sprintf("cookie=%s,priority=220,in_port=%s,ip,nw_src=%s,nw_dst=%s,actions=NORMAL", cookie, vmOfport, switchCIDR, switchCIDR),
			fmt.Sprintf("cookie=%s,priority=220,in_port=%s,ip,nw_src=%s,nw_dst=%s,actions=NORMAL", cookie, gatewayOfport, switchCIDR, switchCIDR),
		)
	}
	if upRateKbit > 0 {
		flows = append(flows,
			fmt.Sprintf("cookie=%s,priority=120,in_port=%s,ip,nw_src=%s,actions=meter:%d,output:%s", cookie, vmOfport, vmIP, upMeterID, gatewayOfport),
		)
	}
	if downRateBps > 0 {
		flows = append(flows,
			fmt.Sprintf("cookie=%s,priority=120,in_port=%s,ip,nw_dst=%s,actions=set_queue:%d,output:%s,pop_queue", cookie, gatewayOfport, vmIP, downQueueID, vmOfport),
		)
	}
	return flows
}

func getVPCSwitchForVM(vmName string) (*model.VPCSwitch, bool) {
	vmName = strings.TrimSpace(vmName)
	if vmName == "" || model.DB == nil {
		return nil, false
	}
	var binding model.VPCVMBinding
	if err := model.DB.Where("vm_name = ?", vmName).First(&binding).Error; err != nil {
		return inferVPCSwitchForVM(vmName)
	}
	var sw model.VPCSwitch
	if err := model.DB.First(&sw, binding.SwitchID).Error; err != nil {
		return inferVPCSwitchForVM(vmName)
	}
	return &sw, true
}

func findOVSUUIDs(table, vmName, direction string) []string {
	result := utils.ExecCommand("ovs-vsctl", "--bare", "--columns=_uuid", "find", table,
		"external-ids:kvm-console-vm="+vmName,
		"external-ids:kvm-console-direction="+direction)
	if result.Error != nil {
		return nil
	}
	return strings.Fields(result.Stdout)
}

func destroyOVSRecords(table string, uuids []string) {
	for _, uuid := range uuids {
		utils.ExecCommand("ovs-vsctl", "--if-exists", "destroy", table, uuid)
	}
}

func clearOVSBandwidthLimit(vmName, vnetIF string) {
	bridge := ovsBridgeName()
	cookie := ovsBandwidthCookie(vmName)
	utils.ExecCommand("ovs-ofctl", "-O", "OpenFlow13", "del-flows", bridge, "cookie="+cookie+"/0xffffffffffffffff")
	if strings.TrimSpace(vnetIF) != "" {
		utils.ExecCommand("ovs-vsctl", "clear", "Port", strings.TrimSpace(vnetIF), "qos")
	}
	bridgeQoS := strings.TrimSpace(utils.ExecCommand("ovs-vsctl", "get", "Port", bridge, "qos").Stdout)
	if bridgeQoS != "" && bridgeQoS != "[]" {
		utils.ExecCommand("ovs-vsctl", "--if-exists", "remove", "QoS", bridgeQoS, "queues", ovsBandwidthQueueKey(ovsBandwidthQueueID(vmName, "up")))
	}
	utils.ExecCommand("ovs-ofctl", "-O", "OpenFlow13", "del-meter", bridge, ovsBandwidthMeterArg(ovsBandwidthMeterID(vmName, "down")))
	utils.ExecCommand("ovs-ofctl", "-O", "OpenFlow13", "del-meter", bridge, ovsBandwidthMeterArg(ovsBandwidthMeterID(vmName, "up")))
	destroyOVSRecords("QoS", findOVSUUIDs("QoS", vmName, "down"))
	destroyOVSRecords("Queue", findOVSUUIDs("Queue", vmName, "down"))
	destroyOVSRecords("Queue", findOVSUUIDs("Queue", vmName, "up"))
}

func setOVSPortQueue(port, vmName, direction string, queueID uint32, maxRateBps int) error {
	if strings.TrimSpace(port) == "" || maxRateBps <= 0 {
		return nil
	}
	queueKey := ovsBandwidthQueueKey(queueID)
	rateArg := fmt.Sprintf("other-config:max-rate=%d", maxRateBps)
	result := utils.ExecCommand("ovs-vsctl",
		"--", "--id=@q", "create", "Queue", rateArg,
		"external-ids:kvm-console-vm="+vmName,
		"external-ids:kvm-console-direction="+direction,
		"--", "--id=@qos", "create", "QoS", "type=linux-htb", "queues:"+queueKey+"=@q",
		"external-ids:kvm-console-vm="+vmName,
		"external-ids:kvm-console-direction="+direction,
		"--", "set", "Port", strings.TrimSpace(port), "qos=@qos")
	if result.Error != nil {
		return fmt.Errorf("配置 OVS 下行队列失败: %s", result.Stderr)
	}
	return nil
}

func addOVSBandwidthMeter(bridge string, meterID uint32, rateKbit int) error {
	if rateKbit <= 0 {
		return nil
	}
	arg := fmt.Sprintf("meter=%d,kbps,band=type=drop,rate=%d", meterID, rateKbit)
	result := utils.ExecCommand("ovs-ofctl", "-O", "OpenFlow13", "add-meter", bridge, arg)
	if result.Error != nil {
		if strings.Contains(result.Stderr, "METER_EXISTS") {
			utils.ExecCommand("ovs-ofctl", "-O", "OpenFlow13", "del-meter", bridge, fmt.Sprintf("meter=%d", meterID))
			result = utils.ExecCommand("ovs-ofctl", "-O", "OpenFlow13", "add-meter", bridge, arg)
			if result.Error != nil {
				return fmt.Errorf("配置 OVS 上行限速失败: %s", result.Stderr)
			}
			return nil
		}
		return fmt.Errorf("配置 OVS 上行限速失败: %s", result.Stderr)
	}
	return nil
}

func applyOVSBandwidthLimit(vmName, mac, vnetIF string, downAvg, upAvg int) error {
	clearOVSBandwidthLimit(vmName, vnetIF)
	clearTCBandwidthLimit(vnetIF)

	downRateBps := ovsBandwidthMaxRateBps(downAvg)
	upRateKbit := ovsBandwidthRateKbit(upAvg)
	if downRateBps <= 0 && upRateKbit <= 0 {
		return nil
	}

	vmIP := getVMBandwidthIP(vmName, mac)
	if vmIP == "" {
		return fmt.Errorf("无法获取虚拟机 %s 的 OVS 内网 IP，暂不能应用外网限速", vmName)
	}
	vmOfport := getOVSInterfaceOfPort(vnetIF)
	if vmOfport == "" {
		return fmt.Errorf("无法获取虚拟机 %s 的 OVS 端口号", vmName)
	}

	bridge := ovsBridgeName()
	downQueueID := ovsBandwidthQueueID(vmName, "down")
	upMeterID := ovsBandwidthMeterID(vmName, "up")
	if err := setOVSPortQueue(vnetIF, vmName, "down", downQueueID, downRateBps); err != nil {
		return err
	}
	if err := addOVSBandwidthMeter(bridge, upMeterID, upRateKbit); err != nil {
		return err
	}

	var flows []string
	if sw, ok := getVPCSwitchForVM(vmName); ok {
		gatewayOfport := getOVSInterfaceOfPort(vpcGatewayPortName(sw.ID))
		if gatewayOfport == "" {
			return fmt.Errorf("无法获取虚拟机 %s 的 VPC 网关端口号", vmName)
		}
		flows = buildOVSVPCBandwidthFlows(ovsBandwidthCookie(vmName), vmOfport, gatewayOfport, vmIP, sw.CIDR, downQueueID, upMeterID, downRateBps, upRateKbit)
	} else {
		flows = buildOVSBandwidthFlows(ovsBandwidthCookie(vmName), vmOfport, vmIP, ovsSubnetCIDR(), downQueueID, upMeterID, downRateBps, upRateKbit)
	}
	for _, flow := range flows {
		result := utils.ExecCommand("ovs-ofctl", "-O", "OpenFlow13", "add-flow", bridge, flow)
		if result.Error != nil {
			return fmt.Errorf("添加 OVS 外网限速流表失败: %s", result.Stderr)
		}
	}
	return nil
}

// ApplyVMBandwidth 设置单台 VM 的网卡速率限制
// virsh domiftune 方向（从域/VM 视角）：
//
//	inbound  = 流量进入 VM = VM 的下行
//	outbound = 流量离开 VM = VM 的上行
//
// 参数说明：
//
//	downAvg/downPeak/downBurst → 对应 inbound（VM 下行）
//	upAvg/upPeak/upBurst     → 对应 outbound（VM 上行）
//
// 单位：average/peak 为 KB/s，burst 为 KB
// 所有值为 0 时清除限制
func ApplyVMBandwidth(vmName string, downAvg, downPeak, downBurst, upAvg, upPeak, upBurst int) error {
	mac := getVMMAC(vmName)
	if mac == "" {
		return fmt.Errorf("无法获取虚拟机 %s 的网卡 MAC 地址", vmName)
	}

	inboundArg := domiftuneBandwidthArg(downAvg, downPeak, downBurst)
	outboundArg := domiftuneBandwidthArg(upAvg, upPeak, upBurst)

	// 应用到 config（持久化）
	result := utils.ExecCommand("virsh", "domiftune", vmName, mac,
		"--inbound", inboundArg, "--outbound", outboundArg, "--config")
	if result.Error != nil {
		return fmt.Errorf("设置速率限制失败(config): %s", result.Stderr)
	}

	// 如果 VM 正在运行，同时应用到 live
	stateResult := utils.ExecCommand("virsh", "domstate", vmName)
	if stateResult.Error == nil && strings.TrimSpace(stateResult.Stdout) == "running" {
		zeroArg := domiftuneBandwidthArg(0, 0, 0)
		vnetIF := getVMVnetIF(vmName)
		if useOVSNetwork() {
			// OVS 运行态由 queue/meter 接管；保留 live domiftune 会把上传变成端口 policing，导致低速率时抖动明显。
			liveResult := utils.ExecCommand("virsh", "domiftune", vmName, mac,
				"--inbound", zeroArg, "--outbound", zeroArg, "--live")
			if liveResult.Error != nil {
				fmt.Printf("[警告] VM %s 清理实时 domiftune 速率限制失败: %s\n", vmName, liveResult.Stderr)
			}
			if vnetIF != "" {
				if err := applyOVSBandwidthLimit(vmName, mac, vnetIF, downAvg, upAvg); err != nil {
					return err
				}
			}
		} else {
			liveResult := utils.ExecCommand("virsh", "domiftune", vmName, mac,
				"--inbound", inboundArg, "--outbound", outboundArg, "--live")
			if liveResult.Error != nil {
				fmt.Printf("[警告] VM %s 实时应用速率限制失败: %s\n", vmName, liveResult.Stderr)
			}
			if vnetIF != "" {
				// 非 OVS 环境保留旧的下行 tc 兜底。
				applyTCDownloadLimit(vnetIF, downAvg, downPeak, downBurst)
			}
		}
	}

	return nil
}

// ApplyVMNICBandwidth 设置单台 VM 的网卡口径速率限制。
// 该路径用于轻量云：不依赖 VPC 网关、IP 租约或 OVS 流表命中，直接在 VM 的 vnet 口限制上下行。
// domiftune 只保存 config，运行态使用 TC/IFB；低速惩罚时叠加 live domiftune 容易因为大 burst 产生卡顿。
func ApplyVMNICBandwidth(vmName string, downAvg, downPeak, downBurst, upAvg, upPeak, upBurst int) error {
	mac := getVMMAC(vmName)
	if mac == "" {
		return fmt.Errorf("无法获取虚拟机 %s 的网卡 MAC 地址", vmName)
	}

	inboundArg := domiftuneBandwidthArg(downAvg, downPeak, downBurst)
	outboundArg := domiftuneBandwidthArg(upAvg, upPeak, upBurst)

	result := utils.ExecCommand("virsh", "domiftune", vmName, mac,
		"--inbound", inboundArg, "--outbound", outboundArg, "--config")
	if result.Error != nil {
		return fmt.Errorf("设置速率限制失败(config): %s", result.Stderr)
	}

	stateResult := utils.ExecCommand("virsh", "domstate", vmName)
	if stateResult.Error == nil && strings.TrimSpace(stateResult.Stdout) == "running" {
		zeroArg := domiftuneBandwidthArg(0, 0, 0)
		liveResult := utils.ExecCommand("virsh", "domiftune", vmName, mac,
			"--inbound", zeroArg, "--outbound", zeroArg, "--live")
		if liveResult.Error != nil {
			fmt.Printf("[警告] VM %s 清理实时 domiftune 速率限制失败: %s\n", vmName, liveResult.Stderr)
		}

		vnetIF := getVMVnetIF(vmName)
		if vnetIF != "" {
			clearOVSBandwidthLimit(vmName, vnetIF)
			applyTCDownloadLimit(vnetIF, downAvg, downPeak, downBurst)
			applyTCUploadLimit(vnetIF, upAvg)
		}
	}

	return nil
}

// ClearVMBandwidth 清除 VM 的速率限制
func ClearVMBandwidth(vmName string) error {
	vnetIF := getVMVnetIF(vmName)
	clearOVSBandwidthLimit(vmName, vnetIF)
	if vnetIF != "" {
		clearTCBandwidthLimit(vnetIF)
	}
	return ApplyVMBandwidth(vmName, 0, 0, 0, 0, 0, 0)
}

// GetVMBandwidth 获取 VM 当前的速率限制配置
func GetVMBandwidth(vmName string) (*BandwidthDetail, error) {
	config, err := getVMBandwidthConfigRaw(vmName)
	if err != nil {
		return nil, err
	}
	detail := &BandwidthDetail{
		InboundAvg:    KBpsToMbps(config.InboundAvg),
		InboundPeak:   KBpsToMbps(config.InboundPeak),
		InboundBurst:  config.InboundBurst,
		OutboundAvg:   KBpsToMbps(config.OutboundAvg),
		OutboundPeak:  KBpsToMbps(config.OutboundPeak),
		OutboundBurst: config.OutboundBurst,
	}
	return detail, nil
}

func IsVPCBoundVM(vmName string) bool {
	vmName = strings.TrimSpace(vmName)
	if vmName == "" || model.DB == nil {
		return false
	}
	var count int64
	model.DB.Model(&model.VPCVMBinding{}).Where("vm_name = ?", vmName).Count(&count)
	return count > 0
}

func listBandwidthConfigurableVMs(vms []string) []string {
	var configurable []string
	for _, vmName := range vms {
		if strings.TrimSpace(vmName) == "" {
			continue
		}
		if IsVPCBoundVM(vmName) {
			fmt.Printf("[信息] 跳过 VM %s 的用户带宽重分配：VPC 交换机负责聚合限速\n", vmName)
			continue
		}
		if getVMMAC(vmName) == "" {
			fmt.Printf("[警告] 跳过 VM %s 的速率重分配：无法获取网卡 MAC 地址\n", vmName)
			continue
		}
		configurable = append(configurable, vmName)
	}
	return configurable
}

// RebalanceUserBandwidth 重新分配用户所有 VM 的带宽
// 规则：average = 用户配额 / VM数量（均分），peak = 用户配额，burst = 系统最大速率 × 30秒
func RebalanceUserBandwidth(username string) error {
	// 获取用户信息
	var user model.User
	if err := model.DB.Where("username = ?", username).First(&user).Error; err != nil {
		return fmt.Errorf("用户不存在: %w", err)
	}

	vms := GetUserVMList(username)
	if len(vms) == 0 {
		return nil
	}
	configurableVMs := listBandwidthConfigurableVMs(vms)
	if len(configurableVMs) == 0 {
		fmt.Printf("[警告] 用户 %s 没有可配置速率限制的 VM，跳过带宽重分配\n", username)
		return nil
	}

	cfg := config.GlobalConfig

	// 如果带宽配额为 0（不限制），清除所有 VM 限制
	if user.MaxBandwidthDown <= 0 && user.MaxBandwidthUp <= 0 {
		for _, vmName := range configurableVMs {
			if err := ClearVMBandwidth(vmName); err != nil {
				fmt.Printf("[警告] 清除 VM %s 速率限制失败: %v\n", vmName, err)
			}
		}
		return nil
	}

	// 计算下行参数（对应 virsh domiftune 的 inbound）
	var downAvg, downPeak, downBurst int
	if user.MaxBandwidthDown > 0 {
		downPeak = int(user.MaxBandwidthDown * 125)                     // peak = 用户配额
		downAvg = int(user.MaxBandwidthDown*125) / len(configurableVMs) // average = 配额均分
		if downAvg < 1 {
			downAvg = 1
		}
		if cfg.MaxBurstInbound > 0 {
			downBurst = MbpsToKBps(cfg.MaxBurstInbound) * 30
		} else {
			downBurst = downPeak * 30
		}
	}

	// 计算上行参数（对应 virsh domiftune 的 outbound）
	var upAvg, upPeak, upBurst int
	if user.MaxBandwidthUp > 0 {
		upPeak = int(user.MaxBandwidthUp * 125)
		upAvg = int(user.MaxBandwidthUp*125) / len(configurableVMs)
		if upAvg < 1 {
			upAvg = 1
		}
		if cfg.MaxBurstOutbound > 0 {
			upBurst = MbpsToKBps(cfg.MaxBurstOutbound) * 30
		} else {
			upBurst = upPeak * 30
		}
	}

	// 应用到所有 VM：参数顺序 (downAvg, downPeak, downBurst, upAvg, upPeak, upBurst)
	// 检查用户是否处于流量超限状态，如果是则覆盖对应方向为惩罚速率
	downTrafficLimited, upTrafficLimited := IsUserTrafficLimited(username)

	var lastErr error
	for _, vmName := range configurableVMs {
		finalDownAvg, finalDownPeak, finalDownBurst := downAvg, downPeak, downBurst
		finalUpAvg, finalUpPeak, finalUpBurst := upAvg, upPeak, upBurst

		// 流量超限时覆盖为惩罚速率
		if downTrafficLimited {
			penaltyDown := MbpsToKBps(10) // 下行惩罚 10Mbps
			finalDownAvg = penaltyDown
			finalDownPeak = penaltyDown
			finalDownBurst = penaltyDown * 30
		}
		if upTrafficLimited {
			penaltyUp := MbpsToKBps(1) // 上行惩罚 1Mbps
			finalUpAvg = penaltyUp
			finalUpPeak = penaltyUp
			finalUpBurst = penaltyUp * 30
		}

		if err := ApplyVMBandwidth(vmName, finalDownAvg, finalDownPeak, finalDownBurst, finalUpAvg, finalUpPeak, finalUpBurst); err != nil {
			fmt.Printf("[警告] 为 VM %s 设置速率限制失败: %v\n", vmName, err)
			lastErr = err
		}
	}

	return lastErr
}

// SetVMCustomAverage 用户自定义某台 VM 的平峰速率
// 校验：修改后该 VM 的 average 不超过用户配额，且所有 VM 的 average 总和不超配额
func SetVMCustomAverage(username, vmName string, inAvgMbps, outAvgMbps int) error {
	if IsLightweightCloudVM(vmName) {
		return fmt.Errorf("轻量云服务器的带宽由管理员在单机配额中设置，不能在 VM 详情页修改")
	}
	if IsVPCBoundVM(vmName) {
		if inAvgMbps < 0 || outAvgMbps < 0 {
			return fmt.Errorf("VM 级带宽不能小于 0")
		}
		if inAvgMbps == 0 && outAvgMbps == 0 {
			return ClearVMBandwidth(vmName)
		}
		inAvgKB := MbpsToKBps(inAvgMbps)
		outAvgKB := MbpsToKBps(outAvgMbps)
		return ApplyVMBandwidth(vmName, inAvgKB, inAvgKB, inAvgKB*30, outAvgKB, outAvgKB, outAvgKB*30)
	}

	// 获取用户信息
	var user model.User
	if err := model.DB.Where("username = ?", username).First(&user).Error; err != nil {
		return fmt.Errorf("用户不存在: %w", err)
	}

	// 获取用户所有 VM
	vms := GetUserVMList(username)
	if len(vms) == 0 {
		return fmt.Errorf("用户没有虚拟机")
	}

	// 校验单台 VM 不超过用户配额
	if user.MaxBandwidthDown > 0 && inAvgMbps <= 0 {
		return fmt.Errorf("下行平峰速率必须大于 0Mbps，当前用户下行配额为 %.2fMbps", user.MaxBandwidthDown)
	}
	if user.MaxBandwidthUp > 0 && outAvgMbps <= 0 {
		return fmt.Errorf("上行平峰速率必须大于 0Mbps，当前用户上行配额为 %.2fMbps", user.MaxBandwidthUp)
	}
	if user.MaxBandwidthDown > 0 && float64(inAvgMbps) > user.MaxBandwidthDown {
		return fmt.Errorf("下行平峰速率 %dMbps 超过配额上限 %.2fMbps", inAvgMbps, user.MaxBandwidthDown)
	}
	if user.MaxBandwidthUp > 0 && float64(outAvgMbps) > user.MaxBandwidthUp {
		return fmt.Errorf("上行平峰速率 %dMbps 超过配额上限 %.2fMbps", outAvgMbps, user.MaxBandwidthUp)
	}

	// 统计其他 VM 的 average 总和
	var otherInTotal, otherOutTotal int
	for _, vm := range vms {
		if vm == vmName {
			continue
		}
		bw, err := GetVMBandwidth(vm)
		if err != nil {
			continue
		}
		otherInTotal += bw.InboundAvg
		otherOutTotal += bw.OutboundAvg
	}

	// 校验总和不超配额
	if user.MaxBandwidthDown > 0 && float64(otherInTotal+inAvgMbps) > user.MaxBandwidthDown {
		return fmt.Errorf("下行总带宽超出配额（其他VM已用 %dMbps + 本机 %dMbps > 上限 %.2fMbps）",
			otherInTotal, inAvgMbps, user.MaxBandwidthDown)
	}
	if user.MaxBandwidthUp > 0 && float64(otherOutTotal+outAvgMbps) > user.MaxBandwidthUp {
		return fmt.Errorf("上行总带宽超出配额（其他VM已用 %dMbps + 本机 %dMbps > 上限 %.2fMbps）",
			otherOutTotal, outAvgMbps, user.MaxBandwidthUp)
	}

	cfg := config.GlobalConfig

	// 计算 peak 和 burst（固定，用户不可修改）
	downPeak := int(user.MaxBandwidthDown * 125)
	upPeak := int(user.MaxBandwidthUp * 125)

	var downBurst, upBurst int
	if user.MaxBandwidthDown > 0 {
		if cfg.MaxBurstInbound > 0 {
			downBurst = MbpsToKBps(cfg.MaxBurstInbound) * 30
		} else {
			downBurst = downPeak * 30
		}
	}
	if user.MaxBandwidthUp > 0 {
		if cfg.MaxBurstOutbound > 0 {
			upBurst = MbpsToKBps(cfg.MaxBurstOutbound) * 30
		} else {
			upBurst = upPeak * 30
		}
	}

	downTrafficLimited, upTrafficLimited := IsUserTrafficLimited(username)
	if downTrafficLimited {
		penaltyDown := MbpsToKBps(10)
		inAvgMbps = 10
		downPeak = penaltyDown
		downBurst = penaltyDown * 30
	}
	if upTrafficLimited {
		penaltyUp := MbpsToKBps(1)
		outAvgMbps = 1
		upPeak = penaltyUp
		upBurst = penaltyUp * 30
	}

	// 参数顺序：(downAvg, downPeak, downBurst, upAvg, upPeak, upBurst)
	if err := ApplyVMBandwidth(vmName, MbpsToKBps(inAvgMbps), downPeak, downBurst, MbpsToKBps(outAvgMbps), upPeak, upBurst); err != nil {
		return err
	}
	RefreshVMCacheByNameAsync(vmName)
	return nil
}

// GetVMBandwidthMbps 获取 VM 带宽的简要信息（Mbps）
func GetVMBandwidthMbps(vmName string) (inAvg, outAvg int) {
	mac := getVMMAC(vmName)
	if mac == "" {
		return 0, 0
	}

	result := utils.ExecCommand("virsh", "domiftune", vmName, mac, "--config")
	if result.Error != nil {
		return 0, 0
	}

	re := regexp.MustCompile(`(\w+\.average)\s*:\s*(\d+)`)
	matches := re.FindAllStringSubmatch(result.Stdout, -1)
	for _, m := range matches {
		if len(m) >= 3 {
			val, _ := strconv.Atoi(m[2])
			switch m[1] {
			case "inbound.average":
				inAvg = KBpsToMbps(val) // inbound = VM 下行
			case "outbound.average":
				outAvg = KBpsToMbps(val) // outbound = VM 上行
			}
		}
	}
	return inAvg, outAvg
}

// ==================== 全局带宽限制（管理员系统设置） ====================

// getRunningVMNames 获取宿主机所有运行中的VM名称列表
func getRunningVMNames() []string {
	result := utils.ExecCommand("virsh", "list", "--state-running", "--name")
	if result.Error != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Split(result.Stdout, "\n") {
		name := strings.TrimSpace(line)
		if name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// getGlobalEffectiveBandwidth 根据系统设置计算全局有效带宽
// 规则：有效带宽 = max(1, 配置值 - 5) Mbps，未配置(0)则返回 0
// 返回 (下行Mbps, 上行Mbps)
func getGlobalEffectiveBandwidth() (downMbps, upMbps int) {
	cfg := config.GlobalConfig
	if cfg.MaxBurstInbound > 0 {
		downMbps = cfg.MaxBurstInbound - 5
		if downMbps < 1 {
			downMbps = 1
		}
	}
	if cfg.MaxBurstOutbound > 0 {
		upMbps = cfg.MaxBurstOutbound - 5
		if upMbps < 1 {
			upMbps = 1
		}
	}
	return downMbps, upMbps
}

// listNonLightweightRunningVMs 获取所有运行中的非轻量云VM名称（用于全局带宽分配）
func listNonLightweightRunningVMs() []string {
	var names []string
	for _, name := range getRunningVMNames() {
		if strings.TrimSpace(name) == "" {
			continue
		}
		// 跳过轻量云VM（其带宽由单机配额管理）
		if IsLightweightCloudVM(name) {
			continue
		}
		names = append(names, name)
	}
	return names
}

// ApplyGlobalBandwidthLimit 根据系统设置中的全局带宽配置，为所有运行中的VM和VPC交换机设置外网带宽上限
//
// 规则：
//   - 当 max_burst_inbound / max_burst_outbound > 0 时启用全局限制
//   - 有效带宽 = 配置值 - 5Mbps（最少 1Mbps）
//   - 每台 VM 设置全量有效带宽作为上限（不除以 VM 数量），多台 VM 同时跑满时由 TCP 拥塞控制自然分享
//   - 非 VPC VM 直接在 VM 级应用限速
//   - VPC VM 由交换机层面聚合限速
//   - 轻量云 VM 不受全局带宽限制（由单机配额管理）
func ApplyGlobalBandwidthLimit() error {
	cfg := config.GlobalConfig
	// 未配置全局带宽限制
	if cfg.MaxBurstInbound <= 0 && cfg.MaxBurstOutbound <= 0 {
		return nil
	}

	runningVMs := listNonLightweightRunningVMs()
	vmCount := len(runningVMs)

	if vmCount == 0 {
		fmt.Println("[全局带宽] 没有运行中的非轻量云虚拟机，跳过VM级带宽分配")
		return nil
	}

	totalDown, totalUp := getGlobalEffectiveBandwidth()
	fmt.Printf("[全局带宽] 有效带宽: 下行 %dMbps / 上行 %dMbps，运行中VM数: %d，每台VM限速为全量带宽，多VM运行时由TCP自然分享\n",
		totalDown, totalUp, vmCount)

	// 找出哪些VM属于VPC交换机，避免重复限速
	vpcSwitches := make(map[uint]bool)

	var lastErr error
	for _, vmName := range runningVMs {
		// 跳过静态获取MAC失败的VM（可能刚关闭）
		mac := getVMMAC(vmName)
		if mac == "" {
			fmt.Printf("[全局带宽] 跳过 VM %s: 无法获取MAC地址\n", vmName)
			continue
		}

		if sw, ok := getVPCSwitchForVM(vmName); ok && sw != nil {
			// VPC VM：标记交换机需要重新应用带宽，不在VM级单独限制
			vpcSwitches[sw.ID] = true
			fmt.Printf("[全局带宽] VM %s 属于VPC交换机 %s (ID=%d), 由交换机聚合限速\n", vmName, sw.Name, sw.ID)
			continue
		}

		// 非VPC VM：应用全量有效带宽限制（TCP拥塞控制在多VM之间自然分享）
		downAvgKB := MbpsToKBps(totalDown)
		upAvgKB := MbpsToKBps(totalUp)
		downBurstKB := downAvgKB * 30
		upBurstKB := upAvgKB * 30

		if err := ApplyVMBandwidth(vmName, downAvgKB, downAvgKB, downBurstKB, upAvgKB, upAvgKB, upBurstKB); err != nil {
			fmt.Printf("[全局带宽] 应用VM %s 带宽限制失败: %v\n", vmName, err)
			lastErr = err
		}
	}

	// 为标记的VPC交换机重新应用带宽限制（effectiveVPCSwitchBandwidth 会自动考虑全局带宽上限）
	for switchID := range vpcSwitches {
		var sw model.VPCSwitch
		if err := model.DB.First(&sw, switchID).Error; err != nil {
			fmt.Printf("[全局带宽] 查找VPC交换机 %d 失败: %v\n", switchID, err)
			continue
		}
		if err := applyVPCSwitchBandwidth(sw); err != nil {
			fmt.Printf("[全局带宽] 应用VPC交换机 %s(%d) 带宽限制失败: %v\n", sw.Name, sw.ID, err)
			lastErr = err
		}
	}

	return lastErr
}

// ClearGlobalBandwidthLimit 清除全局带宽限制（当管理员将两个方向都设为0时）
// 清除所有VM和VPC交换机上的全局带宽限制，恢复由用户配额决定的带宽分配
func ClearGlobalBandwidthLimit() error {
	runningVMs := listNonLightweightRunningVMs()
	vpcSwitches := make(map[uint]bool)

	for _, vmName := range runningVMs {
		if mac := getVMMAC(vmName); mac == "" {
			continue
		}
		if sw, ok := getVPCSwitchForVM(vmName); ok && sw != nil {
			vpcSwitches[sw.ID] = true
			continue
		}
		if err := ClearVMBandwidth(vmName); err != nil {
			fmt.Printf("[全局带宽] 清除VM %s 限速失败: %v\n", vmName, err)
		}
	}

	for switchID := range vpcSwitches {
		var sw model.VPCSwitch
		if err := model.DB.First(&sw, switchID).Error; err != nil {
			fmt.Printf("[全局带宽] 查找VPC交换机 %d 失败: %v\n", switchID, err)
			continue
		}
		if err := applyVPCSwitchBandwidth(sw); err != nil {
			fmt.Printf("[全局带宽] 恢复VPC交换机 %s(%d) 原始带宽失败: %v\n", sw.Name, sw.ID, err)
		}
	}

	return nil
}
