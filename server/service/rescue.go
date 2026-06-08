package service

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"kvm_console/utils"
)

// ==================== 救援系统 ====================

// RescueOriginalConfig 保存救援前的原始配置（用于恢复）
type RescueOriginalConfig struct {
	// DiskBuses 记录每个磁盘设备的原始总线类型：key=设备名, value=总线类型
	DiskBuses map[string]string `json:"disk_buses"`
	// NicModel 原始网卡类型
	NicModel string `json:"nic_model"`
	// BootOrder 原始引导顺序
	BootOrder []string `json:"boot_order"`
}

// rescueConfigPath 获取救援配置文件路径
func rescueConfigPath(vmName string) string {
	return fmt.Sprintf("/tmp/_rescue-%s.json", vmName)
}

// StartRescue 启动救援系统
// 流程: 强制关机 → 记录原始配置 → 改磁盘为sata → 改网卡为e1000e → 挂载ISO → 改引导 → 开机
func StartRescue(vmName, rescueISO string, progress func(int, string)) error {
	if err := EnsureVMNotMigrating(vmName, "启动救援系统"); err != nil {
		return err
	}
	if rescueISO == "" {
		return fmt.Errorf("未配置救援系统 ISO，请先在系统设置中选择救援 ISO")
	}

	// 检查 ISO 文件是否存在
	checkResult := utils.ExecShell(fmt.Sprintf("test -f %s && echo ok", utils.ShellSingleQuote(rescueISO)))
	if strings.TrimSpace(checkResult.Stdout) != "ok" {
		return fmt.Errorf("救援 ISO 文件不存在: %s", rescueISO)
	}

	// 步骤 1: 强制关机
	progress(5, "正在强制关闭虚拟机...")
	state := strings.TrimSpace(utils.ExecCommand("virsh", "domstate", vmName).Stdout)
	if state == "running" {
		if err := DestroyVM(vmName); err != nil {
			return fmt.Errorf("强制关机失败: %w", err)
		}
		// 等待虚拟机完全关机
		for i := 0; i < 15; i++ {
			time.Sleep(time.Second)
			state = strings.TrimSpace(utils.ExecCommand("virsh", "domstate", vmName).Stdout)
			if state == "shut off" {
				break
			}
		}
	}
	progress(15, "虚拟机已关闭")

	// 步骤 2: 记录原始配置
	progress(20, "正在记录原始配置...")
	origConfig, err := saveOriginalConfig(vmName)
	if err != nil {
		return fmt.Errorf("记录原始配置失败: %w", err)
	}

	// 步骤 3: 修改磁盘总线为 sata
	progress(30, "正在将磁盘总线改为 SATA 兼容模式...")
	if err := switchDiskBusForRescue(vmName, origConfig); err != nil {
		return fmt.Errorf("修改磁盘总线失败: %w", err)
	}

	// 步骤 4: 修改网卡为 e1000e
	progress(45, "正在将网卡改为 e1000e 兼容模式...")
	if err := switchNicModelForRescue(vmName); err != nil {
		return fmt.Errorf("修改网卡类型失败: %w", err)
	}

	// 步骤 5: 挂载救援 ISO
	progress(55, "正在挂载救援 ISO...")
	if err := ChangeCDROM(vmName, rescueISO, "", false); err != nil {
		return fmt.Errorf("挂载救援 ISO 失败: %w", err)
	}

	// 步骤 6: 修改引导顺序为 cdrom 优先
	progress(70, "正在设置 CDROM 优先引导...")
	if err := SetVMBootOrder(vmName, []string{"cdrom", "hd"}); err != nil {
		return fmt.Errorf("设置引导顺序失败: %w", err)
	}

	// 步骤 7: 开机
	progress(85, "正在启动虚拟机（救援模式）...")
	if err := StartVM(vmName); err != nil {
		return fmt.Errorf("启动虚拟机失败: %w", err)
	}

	progress(100, "救援系统已启动")
	return nil
}

// StopRescue 关闭救援系统
// 流程: 强制关机 → 弹出ISO → 恢复磁盘总线 → 恢复网卡 → 恢复引导 → 开机 → 清理
func StopRescue(vmName string, progress func(int, string)) error {
	if err := EnsureVMNotMigrating(vmName, "关闭救援系统"); err != nil {
		return err
	}
	// 步骤 1: 强制关机
	progress(5, "正在强制关闭虚拟机...")
	state := strings.TrimSpace(utils.ExecCommand("virsh", "domstate", vmName).Stdout)
	if state == "running" {
		if err := DestroyVM(vmName); err != nil {
			return fmt.Errorf("强制关机失败: %w", err)
		}
		for i := 0; i < 15; i++ {
			time.Sleep(time.Second)
			state = strings.TrimSpace(utils.ExecCommand("virsh", "domstate", vmName).Stdout)
			if state == "shut off" {
				break
			}
		}
	}
	progress(10, "虚拟机已关闭")

	// 步骤 2: 弹出救援 ISO
	progress(20, "正在弹出救援 ISO...")
	_ = EjectCDROM(vmName, "")

	// 步骤 3: 读取原始配置
	progress(30, "正在读取原始配置...")
	origConfig, err := loadOriginalConfig(vmName)
	if err != nil {
		// 如果配置文件不存在，仍然尝试恢复引导顺序并开机
		fmt.Printf("[警告] 读取救援原始配置失败: %v，将仅恢复引导顺序\n", err)
		_ = SetVMBootOrder(vmName, []string{"hd"})
		_ = StartVM(vmName)
		return nil
	}

	// 步骤 4: 恢复磁盘总线
	progress(40, "正在恢复磁盘总线为原始类型...")
	if err := restoreDiskBus(vmName, origConfig); err != nil {
		fmt.Printf("[警告] 恢复磁盘总线失败: %v\n", err)
	}

	// 步骤 5: 恢复网卡类型
	progress(55, "正在恢复网卡类型...")
	if origConfig.NicModel != "" {
		if err := SetVMNicModel(vmName, origConfig.NicModel); err != nil {
			fmt.Printf("[警告] 恢复网卡类型失败: %v\n", err)
		}
	}

	// 步骤 6: 恢复引导顺序
	progress(70, "正在恢复引导顺序...")
	bootOrder := origConfig.BootOrder
	if len(bootOrder) == 0 {
		bootOrder = []string{"hd"}
	}
	if err := SetVMBootOrder(vmName, bootOrder); err != nil {
		fmt.Printf("[警告] 恢复引导顺序失败: %v\n", err)
	}

	// 步骤 7: 开机
	progress(85, "正在启动虚拟机（正常模式）...")
	if err := StartVM(vmName); err != nil {
		return fmt.Errorf("启动虚拟机失败: %w", err)
	}

	// 步骤 8: 清理临时配置文件
	os.Remove(rescueConfigPath(vmName))

	progress(100, "救援系统已关闭，虚拟机已恢复正常启动")
	return nil
}

// IsInRescueMode 判断虚拟机是否处于救援模式
// 检查方式: 救援临时配置文件是否存在
func IsInRescueMode(vmName string) bool {
	configPath := rescueConfigPath(vmName)
	checkResult := utils.ExecShell(fmt.Sprintf("test -f %s && echo ok", utils.ShellSingleQuote(configPath)))
	return strings.TrimSpace(checkResult.Stdout) == "ok"
}

// ==================== 内部辅助函数 ====================

// saveOriginalConfig 记录虚拟机的原始配置并保存到临时文件
func saveOriginalConfig(vmName string) (*RescueOriginalConfig, error) {
	origConfig := &RescueOriginalConfig{
		DiskBuses: make(map[string]string),
	}

	// 获取 XML
	xmlResult := utils.ExecCommand("virsh", "dumpxml", vmName, "--inactive")
	if xmlResult.Error != nil {
		return nil, fmt.Errorf("获取虚拟机 XML 失败: %s", xmlResult.Stderr)
	}
	xmlStr := xmlResult.Stdout

	// 解析磁盘总线信息
	diskRe := regexp.MustCompile(`(?s)<disk type='[^']*' device='disk'>(.*?)</disk>`)
	targetRe := regexp.MustCompile(`<target dev='([^']*)' bus='([^']*)'`)
	diskMatches := diskRe.FindAllStringSubmatch(xmlStr, -1)
	for _, m := range diskMatches {
		if tm := targetRe.FindStringSubmatch(m[1]); len(tm) > 2 {
			origConfig.DiskBuses[tm[1]] = tm[2] // dev -> bus
		}
	}

	// 解析网卡类型
	nicModelRe := regexp.MustCompile(`<model type='([^']*)'`)
	ifRe := regexp.MustCompile(`(?s)<interface type='[^']*'>(.*?)</interface>`)
	ifMatches := ifRe.FindAllStringSubmatch(xmlStr, -1)
	if len(ifMatches) > 0 {
		if nm := nicModelRe.FindStringSubmatch(ifMatches[0][1]); len(nm) > 1 {
			origConfig.NicModel = nm[1]
		}
	}

	// 解析引导顺序
	bootDevRe := regexp.MustCompile(`<boot dev='([^']+)'/>`)
	bootMatches := bootDevRe.FindAllStringSubmatch(xmlStr, -1)
	for _, bm := range bootMatches {
		origConfig.BootOrder = append(origConfig.BootOrder, bm[1])
	}
	if len(origConfig.BootOrder) == 0 {
		origConfig.BootOrder = []string{"hd"}
	}

	// 保存到临时文件
	data, err := json.MarshalIndent(origConfig, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("序列化配置失败: %w", err)
	}

	configPath := rescueConfigPath(vmName)
	writeResult := utils.ExecShell(fmt.Sprintf("cat > %s << 'JSONEOF'\n%s\nJSONEOF", utils.ShellSingleQuote(configPath), string(data)))
	if writeResult.Error != nil {
		return nil, fmt.Errorf("保存配置文件失败: %s", writeResult.Stderr)
	}

	return origConfig, nil
}

// loadOriginalConfig 从临时文件加载原始配置
func loadOriginalConfig(vmName string) (*RescueOriginalConfig, error) {
	configPath := rescueConfigPath(vmName)
	catResult := utils.ExecShell(fmt.Sprintf("cat %s", utils.ShellSingleQuote(configPath)))
	if catResult.Error != nil {
		return nil, fmt.Errorf("读取配置文件失败: %s", catResult.Stderr)
	}

	var origConfig RescueOriginalConfig
	if err := json.Unmarshal([]byte(catResult.Stdout), &origConfig); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	return &origConfig, nil
}

// switchDiskBusForRescue 将所有磁盘总线改为 sata（通过编辑 XML）
func switchDiskBusForRescue(vmName string, origConfig *RescueOriginalConfig) error {
	xmlResult := utils.ExecCommand("virsh", "dumpxml", vmName, "--inactive")
	if xmlResult.Error != nil {
		return fmt.Errorf("获取虚拟机 XML 失败: %s", xmlResult.Stderr)
	}

	xmlStr := xmlResult.Stdout
	lines := strings.Split(xmlStr, "\n")
	var newLines []string
	inDisk := false
	isDiskDevice := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// 检测 <disk ... device='disk'>
		if strings.HasPrefix(trimmed, "<disk ") {
			inDisk = true
			isDiskDevice = strings.Contains(trimmed, "device='disk'")
		}

		if inDisk && isDiskDevice {
			// 修改 <target dev='vda' bus='virtio'/> → <target dev='sda' bus='sata'/>
			if strings.Contains(trimmed, "<target") && strings.Contains(trimmed, "bus='") {
				indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
				// 提取原设备名的字母后缀
				devRe := regexp.MustCompile(`dev='([^']*)'`)
				devMatch := devRe.FindStringSubmatch(trimmed)
				if len(devMatch) > 1 {
					origDev := devMatch[1]
					letter := origDev[len(origDev)-1:]
					newDev := "sd" + letter
					line = fmt.Sprintf("%s<target dev='%s' bus='sata'/>", indent, newDev)
				}
			}

			// 删除 address 行（让 libvirt 自动重新分配地址）
			if strings.Contains(trimmed, "<address ") {
				continue
			}
		}

		if inDisk && strings.Contains(trimmed, "</disk>") {
			inDisk = false
			isDiskDevice = false
		}

		newLines = append(newLines, line)
	}

	newXML := strings.Join(newLines, "\n")
	xmlPath := fmt.Sprintf("/tmp/_rescue-disk-%s.xml", vmName)
	utils.ExecShell(fmt.Sprintf("cat > %s << 'XMLEOF'\n%s\nXMLEOF", utils.ShellSingleQuote(xmlPath), newXML))
	defineResult := utils.ExecCommand("virsh", "define", xmlPath)
	utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(xmlPath)))
	if defineResult.Error != nil {
		return fmt.Errorf("定义磁盘配置失败: %s", defineResult.Stderr)
	}

	return nil
}

// switchNicModelForRescue 将网卡改为 e1000e 兼容模式
func switchNicModelForRescue(vmName string) error {
	return SetVMNicModel(vmName, "e1000e")
}

// restoreDiskBus 恢复磁盘总线为原始类型
func restoreDiskBus(vmName string, origConfig *RescueOriginalConfig) error {
	if len(origConfig.DiskBuses) == 0 {
		return nil
	}

	xmlResult := utils.ExecCommand("virsh", "dumpxml", vmName, "--inactive")
	if xmlResult.Error != nil {
		return fmt.Errorf("获取虚拟机 XML 失败: %s", xmlResult.Stderr)
	}

	xmlStr := xmlResult.Stdout
	lines := strings.Split(xmlStr, "\n")
	var newLines []string
	inDisk := false
	isDiskDevice := false
	currentDev := ""

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "<disk ") {
			inDisk = true
			isDiskDevice = strings.Contains(trimmed, "device='disk'")
			currentDev = ""
		}

		if inDisk && isDiskDevice {
			if strings.Contains(trimmed, "<target") && strings.Contains(trimmed, "dev='") {
				indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
				// 提取当前设备名
				devRe := regexp.MustCompile(`dev='([^']*)'`)
				devMatch := devRe.FindStringSubmatch(trimmed)
				if len(devMatch) > 1 {
					currentDev = devMatch[1]
					letter := currentDev[len(currentDev)-1:]

					// 查找原始总线类型：遍历原始配置，找到匹配字母后缀的设备
					origBus := ""
					origDevName := ""
					for origDev, bus := range origConfig.DiskBuses {
						if origDev[len(origDev)-1:] == letter {
							origBus = bus
							origDevName = origDev
							break
						}
					}

					if origBus != "" {
						line = fmt.Sprintf("%s<target dev='%s' bus='%s'/>", indent, origDevName, origBus)
					}
				}
			}

			// 删除 address 行
			if strings.Contains(trimmed, "<address ") {
				continue
			}
		}

		if inDisk && strings.Contains(trimmed, "</disk>") {
			inDisk = false
			isDiskDevice = false
		}

		newLines = append(newLines, line)
	}

	newXML := strings.Join(newLines, "\n")
	xmlPath := fmt.Sprintf("/tmp/_rescue-restore-%s.xml", vmName)
	utils.ExecShell(fmt.Sprintf("cat > %s << 'XMLEOF'\n%s\nXMLEOF", utils.ShellSingleQuote(xmlPath), newXML))
	defineResult := utils.ExecCommand("virsh", "define", xmlPath)
	utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(xmlPath)))
	if defineResult.Error != nil {
		return fmt.Errorf("恢复磁盘配置失败: %s", defineResult.Stderr)
	}

	return nil
}
