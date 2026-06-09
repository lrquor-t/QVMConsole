package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"kvm_console/config"
	"kvm_console/utils"
)

// LinkedCloneParams 原生链式克隆参数
type LinkedCloneParams struct {
	Name                string                  `json:"name"`
	Remark              string                  `json:"remark,omitempty"`
	Template            string                  `json:"template"`
	TemplateType        string                  `json:"template_type,omitempty"`
	CloneMode           string                  `json:"clone_mode,omitempty"` // 克隆模式: linked（链式克隆，默认）/ full（完整克隆）
	VCPU                int                     `json:"vcpu"`
	MaxVCPU             int                     `json:"max_vcpu,omitempty"`               // CPU 热添加上限
	RAM                 int                     `json:"ram"`
	DiskSize            int                     `json:"disk_size,omitempty"`
	Network             string                  `json:"network,omitempty"`
	Autostart           bool                    `json:"autostart,omitempty"`
	Freeze              bool                    `json:"freeze,omitempty"`
	APIC                *bool                   `json:"apic,omitempty"`
	PAE                 *bool                   `json:"pae,omitempty"`
	RTCOffset           string                  `json:"rtc_offset,omitempty"`
	RTCStartDate        string                  `json:"rtc_startdate,omitempty"`
	GuestAgent          *VMGuestAgentConfig     `json:"guest_agent,omitempty"`
	SMBIOS1             *VMSMBIOS1Config        `json:"smbios1,omitempty"`
	BootType            string                  `json:"boot_type,omitempty"`
	DiskBus             string                  `json:"disk_bus,omitempty"`
	VideoModel          string                  `json:"video_model,omitempty"`
	CPUTopologyMode     string                  `json:"cpu_topology_mode,omitempty"`
	CPULimitPercent     int                     `json:"cpu_limit_percent,omitempty"`
	CPUAffinity         string                  `json:"cpu_affinity,omitempty"`    // CPU 亲和性，如 "0,2,4"
	FirstBootRebootMode string                  `json:"first_boot_reboot_mode,omitempty"`
	MemoryDynamic       *VMMemoryDynamicRequest `json:"memory_dynamic,omitempty"`
	SwitchID            uint                    `json:"switch_id,omitempty"`
	SecurityGroupID     uint                    `json:"security_group_id,omitempty"`
	ExtraNics           []AddVMInterfaceRequest `json:"extra_nics,omitempty"`
	StoragePoolID       string                  `json:"storage_pool_id,omitempty"`
	ExtraDisks          []ExtraDiskParam        `json:"extra_disks,omitempty"`
	NicModel            string                  `json:"nic_model,omitempty"`
	SystemDiskIOPS      *DiskIOPSTune           `json:"system_disk_iops,omitempty"` // 系统盘 IOPS 限制
	IsAdmin             bool                    `json:"is_admin,omitempty"`
	PCIERootPorts       int                     `json:"pcie_root_ports,omitempty"` // q35 预留 pcie-root-port 数量
}

// LinkedCloneResult 原生链式克隆结果
type LinkedCloneResult struct {
	VMName   string `json:"vm_name"`
	DiskPath string `json:"disk_path"`
	Template string `json:"template"`
}

// ParseLinkedCloneParams 从 JSON 解析原生链式克隆参数
func ParseLinkedCloneParams(jsonStr string) (*LinkedCloneParams, error) {
	var params LinkedCloneParams
	if err := json.Unmarshal([]byte(jsonStr), &params); err != nil {
		return nil, err
	}
	return &params, nil
}

// LinkedCloneVM 原生链式克隆虚拟机，不执行任何来宾初始化。
func LinkedCloneVM(ctx context.Context, params *LinkedCloneParams, progressFn func(int, string)) (*LinkedCloneResult, error) {
	if progressFn == nil {
		progressFn = func(int, string) {}
	}
	if err := ValidateVMName(params.Name); err != nil {
		return nil, err
	}

	templateDir := config.GlobalConfig.TemplateDir
	cloneDir, resolvedStoragePoolID, err := ResolveVMStorageDir(params.StoragePoolID, params.IsAdmin)
	if err != nil {
		return nil, err
	}
	params.StoragePoolID = resolvedStoragePoolID

	if params.Network == "" {
		params.Network = config.GlobalConfig.DefaultNetwork
	}
	if !params.IsAdmin {
		params.CPULimitPercent = VMCPULimitUnlimited
	}
	if err := ValidateVMCPULimitPercent(params.CPULimitPercent); err != nil {
		return nil, err
	}
	params.NicModel = NormalizeVMNicModel(params.NicModel)
	params.DiskBus = NormalizeVMDiskBus(params.DiskBus)
	params.BootType = strings.TrimSpace(params.BootType)

	templatePath := filepath.Join(templateDir, params.Template+".qcow2")
	checkResult := utils.ExecShell(fmt.Sprintf("test -f %s && echo ok", utils.ShellSingleQuote(templatePath)))
	if checkResult.Stdout != "ok" {
		return nil, fmt.Errorf("模板不存在: %s", params.Template)
	}

	checkVM := utils.ExecCommand("virsh", "dominfo", params.Name)
	if checkVM.ExitCode == 0 {
		return nil, fmt.Errorf("虚拟机 '%s' 已存在", params.Name)
	}

	meta := GetTemplateMeta(params.Template)
	if params.DiskBus == "" && meta.DefaultConfig != nil && strings.TrimSpace(meta.DefaultConfig.DiskBus) != "" {
		params.DiskBus = NormalizeVMDiskBus(meta.DefaultConfig.DiskBus)
	}
	if strings.TrimSpace(params.VideoModel) == "" && meta.DefaultConfig != nil && strings.TrimSpace(meta.DefaultConfig.VideoModel) != "" {
		params.VideoModel = strings.TrimSpace(meta.DefaultConfig.VideoModel)
	}
	if strings.TrimSpace(params.CPUTopologyMode) == "" && meta.DefaultConfig != nil && strings.TrimSpace(meta.DefaultConfig.CPUTopologyMode) != "" {
		params.CPUTopologyMode = strings.TrimSpace(meta.DefaultConfig.CPUTopologyMode)
	}
	params.CPUTopologyMode = NormalizeVMCPUTopologyMode(params.CPUTopologyMode)
	if strings.TrimSpace(params.FirstBootRebootMode) == "" && meta.DefaultConfig != nil && strings.TrimSpace(meta.DefaultConfig.FirstBootRebootMode) != "" {
		params.FirstBootRebootMode = strings.TrimSpace(meta.DefaultConfig.FirstBootRebootMode)
	}
	params.FirstBootRebootMode = NormalizeVMFirstBootRebootMode(params.FirstBootRebootMode)
	templateType := strings.ToLower(strings.TrimSpace(params.TemplateType))
	if templateType == "" {
		templateType = strings.ToLower(strings.TrimSpace(meta.Type))
	}
	if templateType == "" {
		templateType = "linux"
	}
	params.TemplateType = templateType
	if params.DiskBus == "" {
		params.DiskBus = "virtio"
	}

	resolvedDiskSize, err := ResolveCloneDiskSizeGB(params.Template, params.DiskSize)
	if err != nil {
		return nil, err
	}
	params.DiskSize = resolvedDiskSize

	bootType := params.BootType
	if bootType == "" {
		bootType = meta.BootType
	}
	bootType, _ = resolveTemplateBootType(templatePath, templateType, bootType, true, DetectTemplateBootType)
	if bootType == "" {
		bootType = "bios"
	}
	params.BootType = bootType

	cloneDisk := filepath.Join(cloneDir, params.Name+".qcow2")

	if params.CloneMode == "full" {
		progressFn(10, "创建原生完整克隆磁盘（脱离链式条件）...")
		var convertCmd string
		if params.DiskSize > 0 {
			convertCmd = fmt.Sprintf("qemu-img convert -f qcow2 -O qcow2 %s %s && qemu-img resize %s %dG",
				utils.ShellSingleQuote(templatePath), utils.ShellSingleQuote(cloneDisk), utils.ShellSingleQuote(cloneDisk), params.DiskSize)
		} else {
			convertCmd = fmt.Sprintf("qemu-img convert -f qcow2 -O qcow2 %s %s",
				utils.ShellSingleQuote(templatePath), utils.ShellSingleQuote(cloneDisk))
		}
		result := utils.ExecShellWithTimeout(convertCmd, 2*time.Hour)
		if result.Error != nil {
			return nil, fmt.Errorf("创建完整克隆磁盘失败: %s", result.Stderr)
		}
	} else {
		progressFn(10, "创建原生链式克隆磁盘...")
		createCmd := fmt.Sprintf("qemu-img create -f qcow2 -F qcow2 -b %s %s", utils.ShellSingleQuote(templatePath), utils.ShellSingleQuote(cloneDisk))
		if params.DiskSize > 0 {
			createCmd = fmt.Sprintf("qemu-img create -f qcow2 -F qcow2 -b %s %s %dG", utils.ShellSingleQuote(templatePath), utils.ShellSingleQuote(cloneDisk), params.DiskSize)
		}
		result := utils.ExecShell(createCmd)
		if result.Error != nil {
			return nil, fmt.Errorf("创建链式克隆磁盘失败: %s", result.Stderr)
		}
	}

	if err := checkCanceled(ctx, "", cloneDisk); err != nil {
		return nil, err
	}

	progressFn(30, "准备虚拟机网络与资源配置...")
	if err := EnsureOVSNetworkReady(); err != nil {
		cleanupLinkedCloneArtifacts("", cloneDisk)
		return nil, err
	}

	memoryMeta, ramMB, _, err := BuildVMMemoryMetadataForCreate(params.RAM, params.MemoryDynamic)
	if err != nil {
		cleanupLinkedCloneArtifacts("", cloneDisk)
		return nil, err
	}

	progressFn(55, "生成虚拟机定义...")
	vcpuArg := fmt.Sprintf("--vcpus %d", params.VCPU)
	if params.MaxVCPU > params.VCPU {
		vcpuArg = fmt.Sprintf("--vcpus %d,maxvcpus=%d", params.VCPU, params.MaxVCPU)
	}
	cmdParts := []string{
		"virt-install",
		fmt.Sprintf("--name %s", utils.ShellSingleQuote(params.Name)),
		fmt.Sprintf("--ram %d", ramMB),
		vcpuArg,
		"--machine q35",
		fmt.Sprintf("--disk %s,format=qcow2,bus=%s,discard=unmap,detect_zeroes=unmap", utils.ShellSingleQuote(cloneDisk), params.DiskBus),
		"--osinfo detect=on,require=off",
		BuildOVSVirtInstallNetworkArg(params.NicModel),
		"--graphics vnc,listen=0.0.0.0",
		"--video virtio",
		"--import",
		"--cpu host-passthrough",
	}
	switch bootType {
	case "uefi":
		cmdParts = append(cmdParts, "--boot uefi")
	case "uefi-secure":
		cmdParts = append(cmdParts, "--boot uefi,firmware.feature0.name=secure-boot,firmware.feature0.enabled=yes")
	}

	// q35 机型预留额外的 pcie-root-port 热插槽
	portCount := params.PCIERootPorts
	if portCount <= 0 {
		portCount = 4
	}
	for i := 0; i < portCount; i++ {
		cmdParts = append(cmdParts, "--controller type=pci,model=pcie-root-port")
	}

	cmdParts = append(cmdParts, "--print-xml")

	installCmd := strings.Join(cmdParts, " ")
	installResult := utils.ExecCommandLongRunning("bash", "-c", installCmd)
	if installResult.Error != nil {
		cleanupLinkedCloneArtifacts("", cloneDisk)
		return nil, fmt.Errorf("生成虚拟机 XML 失败: %s", installResult.Stderr)
	}

	xmlOutput := installResult.Stdout
	if idx := strings.Index(xmlOutput, "</domain>"); idx != -1 {
		xmlOutput = xmlOutput[:idx+len("</domain>")]
	}
	if idx := strings.Index(xmlOutput, "<domain"); idx > 0 {
		xmlOutput = xmlOutput[idx:]
	}

	enableFPR := templateType != "windows" && templateType != "other"
	vmXML := injectMemballoonConfig(xmlOutput, enableFPR)
	if memoryMeta != nil {
		vmXML, err = ApplyMemoryMetadataToDomainXML(vmXML, memoryMeta, enableFPR)
		if err != nil {
			cleanupLinkedCloneArtifacts("", cloneDisk)
			return nil, err
		}
	}
	vmXML, err = ApplyRTCConfigToDomainXML(vmXML, params.RTCOffset, params.RTCStartDate, templateType)
	if err != nil {
		cleanupLinkedCloneArtifacts("", cloneDisk)
		return nil, err
	}
	vmXML, err = ApplyVMGuestAgentConfigToDomainXML(vmXML, params.GuestAgent)
	if err != nil {
		cleanupLinkedCloneArtifacts("", cloneDisk)
		return nil, err
	}
	vmXML, err = ApplySMBIOS1ConfigToDomainXML(vmXML, params.SMBIOS1, true)
	if err != nil {
		cleanupLinkedCloneArtifacts("", cloneDisk)
		return nil, err
	}
	vmXML, err = ApplyVMAPICToDomainXML(vmXML, params.APIC)
	if err != nil {
		cleanupLinkedCloneArtifacts("", cloneDisk)
		return nil, err
	}
	vmXML, err = ApplyVMPAEToDomainXML(vmXML, params.PAE)
	if err != nil {
		cleanupLinkedCloneArtifacts("", cloneDisk)
		return nil, err
	}
	vmXML = ApplyVMVideoModelToDomainXML(vmXML, params.VideoModel, templateType)
	if templateType == "windows" {
		vmXML = ApplyWindowsGuestOptimizationsToDomainXML(vmXML)
	}
	topoVCPU := EffectiveTopologyVCPU(params.VCPU, params.MaxVCPU)
	vmXML = ApplyCPUTopologyModeToDomainXML(vmXML, params.CPUTopologyMode, templateType, topoVCPU)
	vmXML = ApplyVMCPULimitToDomainXML(vmXML, params.VCPU, params.CPULimitPercent)
	if params.CPUAffinity != "" {
		affinityCores, affErr := ParseCPUAffinity(params.CPUAffinity)
		if affErr != nil {
			cleanupLinkedCloneArtifacts("", cloneDisk)
			return nil, fmt.Errorf("CPU 亲和性格式错误: %w", affErr)
		}
		if len(affinityCores) > 0 {
			if affErr := ValidateCPUAffinity(affinityCores); affErr != nil {
				cleanupLinkedCloneArtifacts("", cloneDisk)
				return nil, affErr
			}
		}
		vmXML = ApplyCPUAffinityToDomainXML(vmXML, topoVCPU, affinityCores)
	}
	normalizedBootType := NormalizeVMBootType(bootType)
	if normalizedBootType != "" {
		vmXML, err = ApplyVMBootTypeToDomainXML(params.Name, vmXML, normalizedBootType)
		if err != nil {
			cleanupLinkedCloneArtifacts("", cloneDisk)
			return nil, err
		}
	}
	firstBootColdReboot := ShouldUseWindowsFirstBootColdReboot(params.FirstBootRebootMode, templateType)
	if firstBootColdReboot {
		vmXML = ApplyFirstBootRebootModeToDomainXML(vmXML, params.FirstBootRebootMode)
	}
	vmXML, err = ApplyVPCSwitchToDomainXML(vmXML, params.SwitchID)
	if err != nil {
		cleanupLinkedCloneArtifacts("", cloneDisk)
		return nil, err
	}
	if err := ensureVMUEFINVRAMFile(params.Name, vmXML, normalizedBootType); err != nil {
		cleanupLinkedCloneArtifacts("", cloneDisk)
		return nil, err
	}

	xmlPath := fmt.Sprintf("/tmp/_vm-linked-clone-%s.xml", params.Name)
	if err := os.WriteFile(xmlPath, []byte(vmXML), 0644); err != nil {
		cleanupLinkedCloneArtifacts("", cloneDisk)
		return nil, fmt.Errorf("写入虚拟机 XML 失败: %w", err)
	}

	defineResult := utils.ExecCommand("virsh", "define", xmlPath)
	utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(xmlPath)))
	if defineResult.Error != nil {
		cleanupLinkedCloneArtifacts("", cloneDisk)
		return nil, fmt.Errorf("定义虚拟机失败: %s", defineResult.Stderr)
	}

	if memoryMeta != nil {
		if err := writeVMMemoryMetadata(params.Name, memoryMeta); err != nil {
			cleanupLinkedCloneArtifacts(params.Name, cloneDisk)
			return nil, err
		}
	}
	if err := WriteVMTemplateSource(params.Name, params.Template, params.CloneMode); err != nil {
		cleanupLinkedCloneArtifacts(params.Name, cloneDisk)
		return nil, err
	}
	if err := SetVMRemark(params.Name, params.Remark); err != nil {
		cleanupLinkedCloneArtifacts(params.Name, cloneDisk)
		return nil, err
	}
	if err := SetVMFreeze(params.Name, params.Freeze); err != nil {
		cleanupLinkedCloneArtifacts(params.Name, cloneDisk)
		return nil, err
	}

	progressFn(80, "启动虚拟机...")
	startFn := StartVM
	if firstBootColdReboot {
		startFn = StartVMPreserveRebootAction
	}
	if err := startFn(params.Name); err != nil {
		cleanupLinkedCloneArtifacts(params.Name, cloneDisk)
		return nil, err
	}
	if firstBootColdReboot {
		if err := CompleteWindowsFirstBootColdReboot(ctx, params.Name, progressFn); err != nil {
			return nil, err
		}
	}

	if err := checkCanceled(ctx, params.Name, cloneDisk); err != nil {
		return nil, err
	}

	if params.Autostart {
		utils.ExecCommand("virsh", "autostart", params.Name)
	}
	FixOnReboot(params.Name)
	if len(params.ExtraDisks) > 0 {
		progressFn(92, "挂载额外磁盘...")
		if err := AddExtraDisksForVM(params.Name, params.ExtraDisks, cloneDir, params.DiskBus, params.IsAdmin, func(_ int, msg string) {
			progressFn(92, msg)
		}); err != nil {
			return nil, err
		}
	}

	progressFn(100, "原生链式克隆完成")
	return &LinkedCloneResult{
		VMName:   params.Name,
		DiskPath: cloneDisk,
		Template: params.Template,
	}, nil
}

func cleanupLinkedCloneArtifacts(vmName, diskPath string) {
	if strings.TrimSpace(vmName) != "" {
		utils.ExecCommand("virsh", "destroy", vmName)
		utils.ExecCommand("virsh", "undefine", vmName, "--nvram", "--snapshots-metadata")
	}
	if strings.TrimSpace(diskPath) != "" {
		utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(diskPath)))
	}
}
