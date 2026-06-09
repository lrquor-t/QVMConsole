package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"kvm_console/config"
	"kvm_console/taskqueue"
	"kvm_console/utils"
)

const linuxCloneIPWaitSeconds = 180

var fnOSDeviceIDRegexp = regexp.MustCompile(`^[0-9a-fA-F]{32}([0-9a-fA-F]{8})?$`)

// checkCanceled 检查任务是否已被取消，如果已取消执行清理并返回错误
func checkCanceled(ctx context.Context, vmName, diskPath string) error {
	select {
	case <-ctx.Done():
		// 任务被取消，清理资源
		log.Printf("任务被取消，开始清理资源: vm=%s, disk=%s", vmName, diskPath)
		if vmName != "" {
			utils.ExecCommand("virsh", "destroy", vmName)
			utils.ExecCommand("virsh", "undefine", vmName, "--remove-all-storage", "--nvram", "--snapshots-metadata")
		}
		if diskPath != "" {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(diskPath)))
		}
		return taskqueue.ErrTaskCanceled
	default:
		return nil
	}
}

// CloneParams 克隆参数
type CloneParams struct {
	Name                  string                  `json:"name"`                             // 虚拟机名称
	Remark                string                  `json:"remark,omitempty"`                 // 虚拟机备注
	Template              string                  `json:"template"`                         // 模板名称
	TemplateType          string                  `json:"template_type,omitempty"`          // 模板类型: linux/windows/fnos/other
	CloneMode             string                  `json:"clone_mode,omitempty"`             // 克隆模式: linked（链式克隆，默认）/ full（完整克隆）
	VCPU                  int                     `json:"vcpu"`                             // CPU 核心数
	MaxVCPU               int                     `json:"max_vcpu,omitempty"`               // CPU 热添加上限，0 或 <= vcpu 表示不启用
	RAM                   int                     `json:"ram"`                              // 内存（GB）
	DiskSize              int                     `json:"disk_size,omitempty"`              // 磁盘大小（GB，可选）
	Network               string                  `json:"network,omitempty"`                // 网络（默认 default）
	Hostname              string                  `json:"hostname,omitempty"`               // 主机名
	User                  string                  `json:"user,omitempty"`                   // 新用户名
	Password              string                  `json:"password,omitempty"`               // 新密码
	Autostart             bool                    `json:"autostart,omitempty"`              // 开机自启
	Freeze                bool                    `json:"freeze,omitempty"`                 // 启动时冻结 CPU
	APIC                  *bool                   `json:"apic,omitempty"`                   // APIC 开关，默认启用
	PAE                   *bool                   `json:"pae,omitempty"`                    // PAE 开关，默认启用
	RTCOffset             string                  `json:"rtc_offset,omitempty"`             // RTC 使用本地时间还是 UTC
	RTCStartDate          string                  `json:"rtc_startdate,omitempty"`          // RTC 开始日期
	GuestAgent            *VMGuestAgentConfig     `json:"guest_agent,omitempty"`            // QEMU Guest Agent 配置
	SMBIOS1               *VMSMBIOS1Config        `json:"smbios1,omitempty"`                // SMBIOS 类型 1 设置
	UEFI                  *bool                   `json:"uefi,omitempty"`                   // 是否使用 UEFI 启动（nil=自动检测）
	TemplateRootPass      string                  `json:"template_root_pass,omitempty"`     // 模板 root 密码（用于 SSH 初始化）
	TemplateUser          string                  `json:"template_user,omitempty"`          // 模板中已有的用户名
	DiskBus               string                  `json:"disk_bus,omitempty"`               // 系统盘总线类型: virtio/scsi/sata/ide
	VideoModel            string                  `json:"video_model,omitempty"`            // 视频模型: virtio/vga/vmvga/cirrus
	CPUTopologyMode       string                  `json:"cpu_topology_mode,omitempty"`      // CPU 拓扑模式: auto/single_socket/host_default
	CPULimitPercent       int                     `json:"cpu_limit_percent,omitempty"`      // CPU 限制百分比，0 表示无限制
	CPUAffinity           string                  `json:"cpu_affinity,omitempty"`          // CPU 亲和性，如 "0,2,4"
	FirstBootRebootMode   string                  `json:"first_boot_reboot_mode,omitempty"` // 首次重启策略: normal/cold
	MemoryDynamic         *VMMemoryDynamicRequest `json:"memory_dynamic,omitempty"`
	SwitchID              uint                    `json:"switch_id,omitempty"`
	SecurityGroupID       uint                    `json:"security_group_id,omitempty"`
	ExtraNics             []AddVMInterfaceRequest `json:"extra_nics,omitempty"`
	StoragePoolID         string                  `json:"storage_pool_id,omitempty"`
	ExtraDisks            []ExtraDiskParam        `json:"extra_disks,omitempty"`
	NicModel              string                  `json:"nic_model,omitempty"` // 网卡模型: virtio/e1000e/rtl8139
	PreserveFnOSDeviceID  bool                    `json:"preserve_fnos_device_id,omitempty"`
	FnOSDeviceID          string                  `json:"fnos_device_id,omitempty"`
	SystemDiskIOPS        *DiskIOPSTune           `json:"system_disk_iops,omitempty"` // 系统盘 IOPS 限制
	IsAdmin               bool                    `json:"is_admin,omitempty"`
	LinuxIdentityPrepared bool                    `json:"-"` // Linux 首次启动前是否已离线重置 machine-id/DHCP 身份
	PCIERootPorts         int                     `json:"pcie_root_ports,omitempty"` // q35 预留 pcie-root-port 数量
}

// BatchCloneParams 批量克隆参数
type BatchCloneParams struct {
	Prefix              string                  `json:"prefix"`                  // 名称前缀
	StartNum            int                     `json:"start_num"`               // 起始编号
	Count               int                     `json:"count"`                   // 数量
	Template            string                  `json:"template"`                // 模板
	TemplateType        string                  `json:"template_type,omitempty"` // 模板类型
	CloneMode           string                  `json:"clone_mode,omitempty"`    // 克隆模式: linked / full
	VCPU                int                     `json:"vcpu"`
	MaxVCPU             int                     `json:"max_vcpu,omitempty"`               // CPU 热添加上限
	RAM                 int                     `json:"ram"`
	DiskSize            int                     `json:"disk_size,omitempty"`
	Network             string                  `json:"network,omitempty"`
	Hostname            string                  `json:"hostname,omitempty"`            // 主机名（空则由系统自动生成）
	User                string                  `json:"user,omitempty"`                // 新用户名
	Password            string                  `json:"password,omitempty"`
	Autostart           bool                    `json:"autostart,omitempty"`
	Freeze              bool                    `json:"freeze,omitempty"`
	APIC                *bool                   `json:"apic,omitempty"`
	PAE                 *bool                   `json:"pae,omitempty"`
	RTCOffset           string                  `json:"rtc_offset,omitempty"`
	RTCStartDate        string                  `json:"rtc_startdate,omitempty"`
	GuestAgent          *VMGuestAgentConfig     `json:"guest_agent,omitempty"`
	SMBIOS1             *VMSMBIOS1Config        `json:"smbios1,omitempty"`
	UEFI                *bool                   `json:"uefi,omitempty"`
	TemplateRootPass    string                  `json:"template_root_pass,omitempty"`     // 模板 root 密码
	TemplateUser        string                  `json:"template_user,omitempty"`          // 模板中已有的用户名
	VideoModel          string                  `json:"video_model,omitempty"`            // 视频模型
	DiskBus             string                  `json:"disk_bus,omitempty"`               // 系统盘总线类型
	CPUTopologyMode     string                  `json:"cpu_topology_mode,omitempty"`      // CPU 拓扑模式
	CPULimitPercent     int                     `json:"cpu_limit_percent,omitempty"`      // CPU 限制百分比，0 表示无限制
	CPUAffinity         string                  `json:"cpu_affinity,omitempty"`           // CPU 亲和性，如 "0,2,4"
	FirstBootRebootMode string                  `json:"first_boot_reboot_mode,omitempty"` // 首次重启策略
	NicModel            string                  `json:"nic_model,omitempty"`              // 网卡模型
	StoragePoolID       string                  `json:"storage_pool_id,omitempty"`        // 存储池
	SwitchID            uint                    `json:"switch_id,omitempty"`               // VPC 交换机 ID
	SecurityGroupID     uint                    `json:"security_group_id,omitempty"`      // 安全组 ID
	ExtraNics           []AddVMInterfaceRequest `json:"extra_nics,omitempty"`
	IsAdmin             bool                    `json:"is_admin,omitempty"`               // 是否管理员
}

// ReinstallParams 重装系统参数
type ReinstallParams struct {
	Name                  string `json:"name"`                              // 虚拟机名称
	Template              string `json:"template"`                          // 新模板名称
	TemplateType          string `json:"template_type,omitempty"`           // 模板类型
	DiskSize              int    `json:"disk_size,omitempty"`               // 系统盘大小（GB）
	Hostname              string `json:"hostname,omitempty"`                // 主机名
	User                  string `json:"user,omitempty"`                    // 登录用户
	Password              string `json:"password,omitempty"`                // 登录密码
	TemplateRootPass      string `json:"template_root_pass,omitempty"`      // 模板 root 密码
	TemplateUser          string `json:"template_user,omitempty"`           // 模板默认用户
	FirstBootRebootMode   string `json:"first_boot_reboot_mode,omitempty"`  // Windows 首次重启策略
	PreserveFnOSDeviceID  bool   `json:"preserve_fnos_device_id,omitempty"` // 是否保留 FnOS 设备 ID
	FnOSDeviceID          string `json:"fnos_device_id,omitempty"`          // 自定义 FnOS 设备 ID
	Operator              string `json:"operator,omitempty"`                // 操作人
	LinuxIdentityPrepared bool   `json:"-"`                                 // Linux 身份是否已离线重置
}

// CloneResult 克隆结果
type CloneResult struct {
	VMName   string `json:"vm_name"`
	IP       string `json:"ip"`
	DiskPath string `json:"disk_path"`
	Template string `json:"template"`
	Password string `json:"password,omitempty"` // 实际使用的密码（批量模式为空时自动生成）
	Error    string `json:"error,omitempty"`    // 失败原因（空表示成功）
}

// CloneVM 链式克隆虚拟机（主逻辑，对应 vm-linked-clone.sh clone）
func CloneVM(ctx context.Context, params *CloneParams, progressFn func(int, string)) (*CloneResult, error) {
	if err := ValidateVMName(params.Name); err != nil {
		return nil, err
	}
	templateDir := config.GlobalConfig.TemplateDir
	cloneDir, resolvedStoragePoolID, err := ResolveVMStorageDir(params.StoragePoolID, params.IsAdmin)
	if err != nil {
		return nil, err
	}
	params.StoragePoolID = resolvedStoragePoolID

	// 默认值
	if params.Network == "" {
		params.Network = config.GlobalConfig.DefaultNetwork
	}
	params.NicModel = NormalizeVMNicModel(params.NicModel)
	params.DiskBus = NormalizeVMDiskBus(params.DiskBus)
	params.Hostname = strings.TrimSpace(params.Hostname)
	params.User = strings.TrimSpace(params.User)
	if params.Hostname == "" {
		params.Hostname = GenerateRandomCloneHostname()
	}

	// 确定模板路径
	templatePath := filepath.Join(templateDir, params.Template+".qcow2")
	checkResult := utils.ExecShell(fmt.Sprintf("test -f %s && echo ok", utils.ShellSingleQuote(templatePath)))
	if checkResult.Stdout != "ok" {
		return nil, fmt.Errorf("模板不存在: %s", params.Template)
	}

	// 检查虚拟机是否已存在
	checkVM := utils.ExecCommand("virsh", "dominfo", params.Name)
	if checkVM.ExitCode == 0 {
		return nil, fmt.Errorf("虚拟机 '%s' 已存在", params.Name)
	}

	cloneDisk := filepath.Join(cloneDir, params.Name+".qcow2")

	// 从模板元数据获取类型和凭据
	meta := GetTemplateMeta(params.Template)
	if params.DiskBus == "" && meta.DefaultConfig != nil && strings.TrimSpace(meta.DefaultConfig.DiskBus) != "" {
		params.DiskBus = NormalizeVMDiskBus(meta.DefaultConfig.DiskBus)
	}
	if strings.TrimSpace(params.VideoModel) == "" && meta.DefaultConfig != nil && strings.TrimSpace(meta.DefaultConfig.VideoModel) != "" {
		params.VideoModel = strings.TrimSpace(meta.DefaultConfig.VideoModel)
	}
	if !params.IsAdmin {
		params.CPULimitPercent = VMCPULimitUnlimited
	}
	if err := ValidateVMCPULimitPercent(params.CPULimitPercent); err != nil {
		return nil, err
	}
	if strings.TrimSpace(params.CPUTopologyMode) == "" && meta.DefaultConfig != nil && strings.TrimSpace(meta.DefaultConfig.CPUTopologyMode) != "" {
		params.CPUTopologyMode = strings.TrimSpace(meta.DefaultConfig.CPUTopologyMode)
	}
	params.CPUTopologyMode = NormalizeVMCPUTopologyMode(params.CPUTopologyMode)
	if strings.TrimSpace(params.FirstBootRebootMode) == "" && meta.DefaultConfig != nil && strings.TrimSpace(meta.DefaultConfig.FirstBootRebootMode) != "" {
		params.FirstBootRebootMode = strings.TrimSpace(meta.DefaultConfig.FirstBootRebootMode)
	}
	params.FirstBootRebootMode = NormalizeVMFirstBootRebootMode(params.FirstBootRebootMode)

	// 确定模板类型（优先使用 CloneParams 中显式指定的，否则用元数据）
	tplType := strings.ToLower(params.TemplateType)
	if tplType == "" {
		tplType = meta.Type
	}
	if tplType == "" {
		tplType = "linux"
	}
	params.TemplateType = tplType
	if params.DiskBus == "" {
		params.DiskBus = "virtio"
	}

	// 凭据：优先用 CloneParams 中的，否则用元数据中的
	if params.TemplateRootPass == "" && meta.RootPassword != "" {
		params.TemplateRootPass = meta.RootPassword
	}
	if params.TemplateUser == "" && meta.TemplateUser != "" {
		params.TemplateUser = meta.TemplateUser
	}
	params.User = NormalizeCloneUsernameForTemplate(tplType, params.User)
	if err := ValidateCloneCredentialsForTemplate(tplType, params.Hostname, params.User, params.Password, false); err != nil {
		return nil, err
	}
	resolvedDiskSize, err := ResolveCloneDiskSizeGB(params.Template, params.DiskSize)
	if err != nil {
		return nil, err
	}
	params.DiskSize = resolvedDiskSize

	isWindows := tplType == "windows"
	isFnOS := tplType == "fnos"
	isOther := tplType == "other"

	if isOther {
		// ===== Other 类型：直接复制模板磁盘，不做任何初始化 =====
		progressFn(10, "复制模板磁盘（完整复制）...")
		result := utils.ExecCommandLongRunning("cp", "--sparse=always", templatePath, cloneDisk)
		if result.Error != nil {
			return nil, fmt.Errorf("复制模板磁盘失败: %s", result.Stderr)
		}

		// 如果指定了磁盘大小，进行扩容
		if params.DiskSize > 0 {
			utils.ExecShell(fmt.Sprintf("qemu-img resize %s %dG", utils.ShellSingleQuote(cloneDisk), params.DiskSize))
		}
	} else if params.CloneMode == "full" {
		// ===== 完整克隆：将模板数据完整复制到新磁盘，脱离链式依赖 =====
		progressFn(10, "创建完整克隆磁盘（脱离链式条件）...")
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
		// ===== 链式克隆（Linux/Windows/FnOS） =====
		progressFn(10, "创建链式克隆磁盘...")
		var createCmd string
		if params.DiskSize > 0 {
			createCmd = fmt.Sprintf("qemu-img create -f qcow2 -F qcow2 -b %s %s %dG",
				utils.ShellSingleQuote(templatePath), utils.ShellSingleQuote(cloneDisk), params.DiskSize)
		} else {
			createCmd = fmt.Sprintf("qemu-img create -f qcow2 -F qcow2 -b %s %s",
				utils.ShellSingleQuote(templatePath), utils.ShellSingleQuote(cloneDisk))
		}
		result := utils.ExecShell(createCmd)
		if result.Error != nil {
			return nil, fmt.Errorf("创建克隆磁盘失败: %s", result.Stderr)
		}
	}

	// 检查取消
	if err := checkCanceled(ctx, "", cloneDisk); err != nil {
		return nil, err
	}

	if isFnOS {
		if err := prepareFnOSSystemDiskExpansion(ctx, cloneDisk, progressFn); err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(cloneDisk)))
			return nil, err
		}
		if err := cloneFnOS(params, cloneDisk, progressFn); err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(cloneDisk)))
			return nil, err
		}
	}
	if tplType == "linux" {
		progressFn(25, "重置 Linux 首次启动身份...")
		if err := prepareLinuxCloneFirstBootIdentity(params, cloneDisk); err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(cloneDisk)))
			return nil, err
		}
		params.LinuxIdentityPrepared = true
	}
	if isWindows {
		if err := prepareWindowsSystemDiskExpansion(ctx, cloneDisk, progressFn); err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(cloneDisk)))
			return nil, err
		}
	}

	progressFn(30, "创建虚拟机定义...")
	if err := EnsureOVSNetworkReady(); err != nil {
		utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(cloneDisk)))
		return nil, err
	}

	memoryMeta, ramMB, _, err := BuildVMMemoryMetadataForCreate(params.RAM, params.MemoryDynamic)
	if err != nil {
		utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(cloneDisk)))
		return nil, err
	}

	// 检测模板是否使用 UEFI 启动，并保留普通 UEFI / 安全引导的区别。
	templateBootType := normalizeTemplateBootType(meta.BootType)
	cloneBootType := ""
	needUEFI := false
	if params.UEFI != nil {
		needUEFI = *params.UEFI
		if needUEFI {
			cloneBootType = VMBootTypeUEFI
		} else {
			cloneBootType = VMBootTypeBIOS
		}
	} else if templateBootType == VMBootTypeUEFI || templateBootType == VMBootTypeUEFISecure {
		needUEFI = true
		cloneBootType = templateBootType
	} else {
		needUEFI = DetectTemplateBootType(templatePath) == "uefi"
		if needUEFI {
			cloneBootType = VMBootTypeUEFI
		}
	}

	if isWindows {
		// ===== Windows 克隆 =====
		if err := cloneWindows(ctx, params, cloneDisk, ramMB, memoryMeta, needUEFI, progressFn); err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(cloneDisk)))
			return nil, err
		}
	} else {
		// ===== Linux / FnOS / Other 克隆 =====
		bootOpt := ""
		if needUEFI {
			bootOpt = "--boot uefi "
		}
		vcpuArg := fmt.Sprintf("--vcpus %d", params.VCPU)
		if params.MaxVCPU > params.VCPU {
			vcpuArg = fmt.Sprintf("--vcpus %d,maxvcpus=%d", params.VCPU, params.MaxVCPU)
		}
		installCmd := fmt.Sprintf(
			"virt-install --name %s --ram %d %s "+
				"--machine q35 "+
				bootOpt+
				"--disk %s,format=qcow2,bus=%s,discard=unmap,detect_zeroes=unmap "+
				"--osinfo detect=on,require=off "+
				BuildOVSVirtInstallNetworkArg(params.NicModel)+" "+
				"--graphics vnc,listen=0.0.0.0 "+
				"--video virtio "+
				"--import --cpu host-passthrough --print-xml",
			utils.ShellSingleQuote(params.Name), ramMB, vcpuArg, utils.ShellSingleQuote(cloneDisk), params.DiskBus,
		)
		result := utils.ExecCommandLongRunning("bash", "-c", installCmd)
		if result.Error != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(cloneDisk)))
			return nil, fmt.Errorf("生成虚拟机 XML 失败: %s", result.Stderr)
		}

		// 注入 memballoon 配置（Linux/FnOS 启用 freePageReporting，Other 仅设置 stats）
		vmXML := injectMemballoonConfig(result.Stdout, !isOther)

		// 注入 pcie-root-port 控制器（q35 机型热插拔预留，默认 4 个）
		pciePortCount := params.PCIERootPorts
		if pciePortCount <= 0 {
			pciePortCount = 4
		}
		vmXML = injectPCIERootPorts(vmXML, pciePortCount)

		if memoryMeta != nil {
			vmXML, err = ApplyMemoryMetadataToDomainXML(vmXML, memoryMeta, !isOther)
			if err != nil {
				utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(cloneDisk)))
				return nil, err
			}
		}
		vmXML, err = ApplyRTCConfigToDomainXML(vmXML, params.RTCOffset, params.RTCStartDate, tplType)
		if err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(cloneDisk)))
			return nil, err
		}
		vmXML, err = ApplyVMGuestAgentConfigToDomainXML(vmXML, params.GuestAgent)
		if err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(cloneDisk)))
			return nil, err
		}
		vmXML, err = ApplySMBIOS1ConfigToDomainXML(vmXML, params.SMBIOS1, true)
		if err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(cloneDisk)))
			return nil, err
		}
		vmXML, err = ApplyVMAPICToDomainXML(vmXML, params.APIC)
		if err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(cloneDisk)))
			return nil, err
		}
		vmXML, err = ApplyVMPAEToDomainXML(vmXML, params.PAE)
		if err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(cloneDisk)))
			return nil, err
		}
		vmXML = ApplyVMVideoModelToDomainXML(vmXML, params.VideoModel, tplType)
		topoVCPU := EffectiveTopologyVCPU(params.VCPU, params.MaxVCPU)
		vmXML = ApplyCPUTopologyModeToDomainXML(vmXML, params.CPUTopologyMode, tplType, topoVCPU)
		vmXML = ApplyVMCPULimitToDomainXML(vmXML, params.VCPU, params.CPULimitPercent)
		if params.CPUAffinity != "" {
			var affErr error
			vmXML, affErr = ApplyCPUAffinityIfSet(vmXML, topoVCPU, params.CPUAffinity)
			if affErr != nil {
				utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(cloneDisk)))
				return nil, affErr
			}
		}
		if cloneBootType != "" {
			vmXML, err = ApplyVMBootTypeToDomainXML(params.Name, vmXML, cloneBootType)
			if err != nil {
				utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(cloneDisk)))
				return nil, err
			}
		}
		vmXML, err = ApplyVPCSwitchToDomainXML(vmXML, params.SwitchID)
		if err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(cloneDisk)))
			return nil, err
		}
		if needUEFI {
			vmXML, err = prepareUEFITemplateNVRAMForClone(vmXML, params.Name, meta.NVRAMPath)
			if err != nil {
				utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(cloneDisk)))
				return nil, err
			}
		}
		if err := ensureVMUEFINVRAMFile(params.Name, vmXML, cloneBootType); err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(cloneDisk)))
			return nil, err
		}

		// 写入临时文件并定义虚拟机
		xmlPath := fmt.Sprintf("/tmp/_vm-%s.xml", params.Name)
		utils.ExecShell(fmt.Sprintf("cat > %s << 'XMLEOF'\n%s\nXMLEOF", utils.ShellSingleQuote(xmlPath), vmXML))

		defineResult := utils.ExecCommand("virsh", "define", xmlPath)
		utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(xmlPath)))
		if defineResult.Error != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(cloneDisk)))
			return nil, fmt.Errorf("定义虚拟机失败: %s", defineResult.Stderr)
		}
		if memoryMeta != nil {
			if err := writeVMMemoryMetadata(params.Name, memoryMeta); err != nil {
				utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(cloneDisk)))
				return nil, err
			}
		}
		if err := WriteVMTemplateSource(params.Name, params.Template, params.CloneMode); err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(cloneDisk)))
			return nil, err
		}
		if err := SetVMRemark(params.Name, params.Remark); err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(cloneDisk)))
			return nil, err
		}

		if err := SetVMFreeze(params.Name, params.Freeze); err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(cloneDisk)))
			return nil, err
		}

		if err := StartVM(params.Name); err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(cloneDisk)))
			return nil, err
		}
	}

	// 检查取消（VM 已创建，需要传 vmName 以便清理）
	if err := checkCanceled(ctx, params.Name, cloneDisk); err != nil {
		return nil, err
	}

	progressFn(50, "虚拟机创建成功...")

	// 开机自启
	if params.Autostart {
		utils.ExecCommand("virsh", "autostart", params.Name)
	}

	// 修复重启变关机
	FixOnReboot(params.Name)

	if len(params.ExtraDisks) > 0 {
		progressFn(52, "挂载额外磁盘...")
		if err := AddExtraDisksForVM(params.Name, params.ExtraDisks, cloneDir, params.DiskBus, params.IsAdmin, func(_ int, msg string) {
			progressFn(52, msg)
		}); err != nil {
			return nil, err
		}
	}

	// Other 类型不做任何初始化，直接完成
	if isOther {
		progressFn(100, "克隆完成（直接复制模式）")
		return &CloneResult{
			VMName:   params.Name,
			DiskPath: cloneDisk,
			Template: params.Template,
		}, nil
	}

	// 检查取消
	if err := checkCanceled(ctx, params.Name, cloneDisk); err != nil {
		return nil, err
	}

	progressFn(60, "等待虚拟机启动...")

	// 等待获取 IP（可取消）
	time.Sleep(5 * time.Second)
	ip := waitForIPWithContext(ctx, params.Name, linuxCloneIPWaitSeconds)

	// Linux 初始化（仅 linux 类型）
	if tplType == "linux" {
		if ip == "" {
			return nil, fmt.Errorf("未获取到虚拟机 IP，Linux 初始化无法执行")
		}
		// 检查取消
		if err := checkCanceled(ctx, params.Name, cloneDisk); err != nil {
			return nil, err
		}
		progressFn(70, "SSH 初始化中...")
		if err := initLinuxClone(params, ip, progressFn); err != nil {
			return nil, err
		}

		if !params.LinuxIdentityPrepared {
			// 兼容导入等旧路径：若未离线重置身份，SSH 初始化后仍会重启网络并可能换 IP。
			progressFn(96, "等待虚拟机网络刷新...")
			oldIP := ip
			time.Sleep(15 * time.Second)
			newIP := getVMIP(params.Name, true)
			if newIP != "" && newIP != oldIP {
				ip = newIP
			}
		}
	}

	progressFn(100, "克隆完成")

	return &CloneResult{
		VMName:   params.Name,
		IP:       ip,
		DiskPath: cloneDisk,
		Template: params.Template,
	}, nil
}

func prepareLinuxCloneFirstBootIdentity(params *CloneParams, cloneDisk string) error {
	args := []string{
		"-a", cloneDisk,
		"--no-network",
	}
	for _, cmd := range buildLinuxFirstBootIdentityCommands(params.Hostname) {
		args = append(args, "--run-command", cmd)
	}
	args = append(args, "--quiet")

	result := utils.ExecCommandLongRunning("virt-customize", args...)
	if result.Error != nil {
		return fmt.Errorf("Linux 首次启动身份重置失败: %s", firstNonEmpty(result.Stderr, result.Error.Error()))
	}
	return nil
}

func buildLinuxFirstBootIdentityCommands(hostname string) []string {
	return []string{
		"truncate -s 0 /etc/machine-id 2>/dev/null || rm -f /etc/machine-id",
		"rm -f /var/lib/dbus/machine-id 2>/dev/null || true",
		"rm -f /var/lib/dhcp/*.leases 2>/dev/null || true",
		"rm -f /var/lib/NetworkManager/*.lease 2>/dev/null || true",
		"rm -f /var/lib/systemd/network/*.lease 2>/dev/null || true",
		"rm -rf /run/systemd/netif/leases/* 2>/dev/null || true",
		"rm -rf /var/lib/cloud/instances/* /var/lib/cloud/instance 2>/dev/null || true",
		fmt.Sprintf("printf '%%s\\n' %s > /etc/hostname", utils.ShellSingleQuote(hostname)),
		buildLinuxHostsCommand(hostname),
	}
}

func buildLinuxHostsCommand(hostname string) string {
	return fmt.Sprintf(`TARGET_HOSTNAME=%s
if grep -q '^127\.0\.1\.1[[:space:]]' /etc/hosts; then
  sed -i "s/^127\.0\.1\.1[[:space:]].*/127.0.1.1\t${TARGET_HOSTNAME}/" /etc/hosts
else
  printf '127.0.1.1\t%%s\n' "$TARGET_HOSTNAME" >> /etc/hosts
fi`, utils.ShellSingleQuote(hostname))
}

// cloneFnOS FnOS 克隆逻辑（离线注入首个管理员账号和初始化状态）
func cloneFnOS(params *CloneParams, cloneDisk string, progressFn func(int, string)) error {
	if strings.TrimSpace(params.User) == "" {
		params.User = "admin"
	}
	if params.Password == "" {
		return fmt.Errorf("FnOS 模板克隆需要设置登录密码")
	}

	progressFn(27, "注入 FnOS 首次登录信息...")

	identityCommand := buildFnOSIdentityResetCommand()
	if strings.TrimSpace(params.FnOSDeviceID) != "" {
		command, err := buildFnOSCustomDeviceIDCommand(params.FnOSDeviceID)
		if err != nil {
			return err
		}
		identityCommand = command
	} else if params.PreserveFnOSDeviceID {
		identityCommand = buildFnOSIdentityPreservationCommand()
	}

	customizeArgs := []string{
		"-a", cloneDisk,
		"--no-network",
		"--run-command", buildFnOSUserProvisionCommand(params.User),
		"--run-command", buildFnOSPasswordCommand(params.User, params.Password),
		"--run-command", buildFnOSHostnameCommand(params.Hostname),
		"--run-command", buildFnOSHostsCommand(params.Hostname),
		"--run-command", "mkdir -p /usr/trim/etc && date +%s > /usr/trim/etc/system_inited_timestamp",
		"--run-command", identityCommand,
		"--run-command", "rm -f /var/lib/dhcp/*.leases 2>/dev/null || true",
		"--run-command", "rm -f /var/lib/NetworkManager/*.lease 2>/dev/null || true",
		"--run-command", "rm -f /var/lib/systemd/network/*.lease 2>/dev/null || true",
		"--run-command", "rm -rf /run/systemd/netif/leases/* 2>/dev/null || true",
		"--selinux-relabel",
		"--quiet",
	}

	result := utils.ExecCommandLongRunning("virt-customize", customizeArgs...)
	if result.Error != nil {
		return fmt.Errorf("FnOS 首次登录信息注入失败: %s", result.Stderr)
	}

	return nil
}

func windowsSystemDiskTargetDev(bus string) string {
	switch NormalizeVMDiskBus(bus) {
	case "sata", "scsi":
		return "sda"
	case "ide":
		return "hda"
	default:
		return "vda"
	}
}

func windowsDiskControllerXML(bus string) string {
	switch NormalizeVMDiskBus(bus) {
	case "sata":
		return "    <controller type='sata' index='0'/>\n"
	case "scsi":
		return "    <controller type='scsi' index='0' model='virtio-scsi'/>\n"
	default:
		return ""
	}
}

func buildWindowsUnattendXML(hostname, password string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<unattend xmlns="urn:schemas-microsoft-com:unattend">
  <settings pass="specialize">
    <component name="Microsoft-Windows-Shell-Setup" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <ComputerName>%s</ComputerName>
    </component>
  </settings>
  <settings pass="oobeSystem">
    <component name="Microsoft-Windows-Shell-Setup" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <OOBE>
        <HideEULAPage>true</HideEULAPage>
        <HideLocalAccountScreen>true</HideLocalAccountScreen>
        <HideOEMRegistrationScreen>true</HideOEMRegistrationScreen>
        <HideOnlineAccountScreens>true</HideOnlineAccountScreens>
        <HideWirelessSetupInOOBE>true</HideWirelessSetupInOOBE>
        <ProtectYourPC>3</ProtectYourPC>
        <SkipMachineOOBE>true</SkipMachineOOBE>
        <SkipUserOOBE>true</SkipUserOOBE>
      </OOBE>
      <UserAccounts>
        <AdministratorPassword>
          <Value>%s</Value>
          <PlainText>true</PlainText>
        </AdministratorPassword>
      </UserAccounts>
      <AutoLogon>
        <Enabled>true</Enabled>
        <Username>Administrator</Username>
        <Password>
          <Value>%s</Value>
          <PlainText>true</PlainText>
        </Password>
        <LogonCount>1</LogonCount>
      </AutoLogon>
    </component>
    <component name="Microsoft-Windows-International-Core" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">
      <InputLocale>zh-CN</InputLocale>
      <SystemLocale>zh-CN</SystemLocale>
      <UILanguage>zh-CN</UILanguage>
      <UserLocale>zh-CN</UserLocale>
    </component>
  </settings>
</unattend>`, hostname, password, password)
}

func injectWindowsUnattendFile(vmName, cloneDisk, hostname, password string, progressFn func(int, string)) {
	if progressFn == nil {
		progressFn = func(int, string) {}
	}
	if password == "" {
		password = "Qwert333"
	}

	progressFn(35, "注入 Windows 应答文件...")
	unattendXML := buildWindowsUnattendXML(hostname, password)
	unattendPath := fmt.Sprintf("/tmp/_unattend-%s.xml", vmName)
	_ = os.WriteFile(unattendPath, []byte(unattendXML), 0600)

	injectResult := utils.ExecCommandLongRunning("virt-customize", "-a", cloneDisk, "--no-network",
		"--upload", unattendPath+":/Windows/Panther/unattend.xml",
		"--quiet")
	_ = os.Remove(unattendPath)

	if injectResult.Error != nil {
		progressFn(38, "Windows 应答文件注入失败，首次启动可能需要手动 OOBE")
	}
}

// cloneWindows Windows 克隆逻辑
func cloneWindows(ctx context.Context, params *CloneParams, cloneDisk string, ramMB int, memoryMeta *vmMemoryMetadata, needUEFI bool, progressFn func(int, string)) error {
	templateDir := config.GlobalConfig.TemplateDir

	password := params.Password
	if password == "" {
		password = "Qwert333"
	}

	injectWindowsUnattendFile(params.Name, cloneDisk, params.Hostname, password, progressFn)

	nvramClone := ""
	if needUEFI {
		// 生成 qcow2 NVRAM，内部内存快照要求 pflash 变量文件支持快照。
		nvramTemplate := filepath.Join(templateDir, "win2k22-nvram.fd")
		nvramClone = fmt.Sprintf("/var/lib/libvirt/qemu/nvram/%s_VARS.fd", params.Name)

		checkNvram := utils.ExecShell(fmt.Sprintf("test -f %s && echo ok", utils.ShellSingleQuote(nvramTemplate)))
		if checkNvram.Stdout == "ok" {
			if err := createQCOW2NVRAMFromTemplate(nvramTemplate, nvramClone); err != nil {
				return err
			}
		} else {
			if err := createQCOW2NVRAMFromTemplate("/usr/share/OVMF/OVMF_VARS_4M.ms.fd", nvramClone); err != nil {
				return err
			}
		}
	}

	progressFn(40, "生成 Windows VM XML...")

	// 生成 MAC 地址
	macResult := utils.ExecShell(`printf '52:54:00:%02x:%02x:%02x' $((RANDOM%256)) $((RANDOM%256)) $((RANDOM%256))`)
	macAddr := strings.TrimSpace(macResult.Stdout)
	if macAddr == "" {
		macAddr = "52:54:00:aa:bb:cc"
	}

	ramKiB := ramMB * 1024
	diskBus := NormalizeVMDiskBus(params.DiskBus)
	if diskBus == "" {
		diskBus = "virtio"
	}
	diskTargetDev := windowsSystemDiskTargetDev(diskBus)
	diskControllerXML := windowsDiskControllerXML(diskBus)
	osXML := `  <os>
    <type arch='x86_64' machine='pc-q35-noble'>hvm</type>
    <boot dev='hd'/>
  </os>`
	smmXML := ""
	tpmXML := ""
	if needUEFI {
		osXML = fmt.Sprintf(`  <os firmware='efi'>
    <type arch='x86_64' machine='pc-q35-noble'>hvm</type>
    <firmware>
      <feature enabled='yes' name='enrolled-keys'/>
      <feature enabled='yes' name='secure-boot'/>
    </firmware>
    <loader readonly='yes' secure='yes' type='pflash'>/usr/share/OVMF/OVMF_CODE_4M.ms.fd</loader>
    <nvram template='/usr/share/OVMF/OVMF_VARS_4M.ms.fd' templateFormat='raw' format='qcow2'>%s</nvram>
    <boot dev='hd'/>
  </os>`, nvramClone)
		smmXML = "<smm state='on'/>"
		tpmXML = "    <tpm model='tpm-crb'><backend type='emulator' version='2.0'/></tpm>\n"
	}

	// 生成 Windows VM XML
	rtcOffset := ResolveRTCOffset(params.RTCOffset, "windows")
	rtcStartDate := NormalizeRTCStartDate(params.RTCStartDate)
	clockOpenTag := fmt.Sprintf("<clock offset='%s'>", rtcOffset)
	if rtcStartDate != VMRTCStartDateNow {
		epoch, err := ParseRTCStartDateToEpoch(rtcStartDate)
		if err != nil {
			return err
		}
		rtcOffset = VMRTCOffsetAbsolute
		clockOpenTag = fmt.Sprintf("<clock offset='%s' start='%s'>", rtcOffset, epoch)
	}
	vmXML := fmt.Sprintf(`<domain type='kvm'>
  <name>%s</name>
  <memory unit='KiB'>%d</memory>
%s
%s
  <features>
    <acpi/><apic/>
    <hyperv mode='custom'>
      <relaxed state='on'/><vapic state='on'/><spinlocks state='on' retries='8191'/>
    </hyperv>
    <vmport state='off'/>%s
  </features>
  <cpu mode='host-passthrough' check='none' migratable='on'/>
  %s
    <timer name='rtc' tickpolicy='catchup'/><timer name='pit' tickpolicy='delay'/>
    <timer name='hpet' present='no'/><timer name='hypervclock' present='yes'/>
  </clock>
  <on_poweroff>destroy</on_poweroff><on_reboot>restart</on_reboot><on_crash>destroy</on_crash>
  <pm><suspend-to-mem enabled='no'/><suspend-to-disk enabled='no'/></pm>
  <devices>
    <emulator>/usr/bin/qemu-system-x86_64</emulator>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2' discard='unmap' detect_zeroes='unmap'/>
      <source file='%s'/><target dev='%s' bus='%s'/>
    </disk>
    <controller type='usb' index='0' model='qemu-xhci' ports='15'/>
    <controller type='virtio-serial' index='0'/>
%s
%s
    <input type='tablet' bus='usb'/>
%s
    <graphics type='vnc' port='-1' autoport='yes' listen='0.0.0.0'>
      <listen type='address' address='0.0.0.0'/>
    </graphics>
    <video><model type='virtio' heads='1' primary='yes'/></video>
    <watchdog model='itco' action='reset'/>
    <memballoon model='virtio' freePageReporting='on'><stats period='5'/></memballoon>
  </devices>
</domain>`,
		params.Name, ramKiB, BuildVCPUTag(params.VCPU, params.MaxVCPU), osXML, smmXML, clockOpenTag, cloneDisk, diskTargetDev, diskBus, diskControllerXML, BuildOVSInterfaceXML(macAddr, params.NicModel), tpmXML)
	var err error
	if memoryMeta != nil {
		vmXML, err = ApplyMemoryMetadataToDomainXML(vmXML, memoryMeta, false)
		if err != nil {
			return err
		}
	}
	vmXML, err = ApplyVMGuestAgentConfigToDomainXML(vmXML, params.GuestAgent)
	if err != nil {
		return err
	}
	vmXML, err = ApplySMBIOS1ConfigToDomainXML(vmXML, params.SMBIOS1, true)
	if err != nil {
		return err
	}
	vmXML, err = ApplyVMAPICToDomainXML(vmXML, params.APIC)
	if err != nil {
		return err
	}
	vmXML, err = ApplyVMPAEToDomainXML(vmXML, params.PAE)
	if err != nil {
		return err
	}
	vmXML = ApplyVMVideoModelToDomainXML(vmXML, params.VideoModel, "windows")
	vmXML = ApplyWindowsGuestOptimizationsToDomainXML(vmXML)
	topoVCPU := EffectiveTopologyVCPU(params.VCPU, params.MaxVCPU)
	vmXML = ApplyCPUTopologyModeToDomainXML(vmXML, params.CPUTopologyMode, "windows", topoVCPU)
	vmXML = ApplyVMCPULimitToDomainXML(vmXML, params.VCPU, params.CPULimitPercent)
	if params.CPUAffinity != "" {
		var affErr error
		vmXML, affErr = ApplyCPUAffinityIfSet(vmXML, topoVCPU, params.CPUAffinity)
		if affErr != nil {
			return affErr
		}
	}
	firstBootColdReboot := ShouldUseWindowsFirstBootColdReboot(params.FirstBootRebootMode, "windows")
	if firstBootColdReboot {
		vmXML = ApplyFirstBootRebootModeToDomainXML(vmXML, params.FirstBootRebootMode)
	}
	vmXML, err = ApplyVPCSwitchToDomainXML(vmXML, params.SwitchID)
	if err != nil {
		return err
	}

	// 写入 XML 并定义
	xmlPath := fmt.Sprintf("/tmp/_win-clone-%s.xml", params.Name)
	utils.ExecShell(fmt.Sprintf("cat > %s << 'XMLEOF'\n%s\nXMLEOF", utils.ShellSingleQuote(xmlPath), vmXML))

	defineResult := utils.ExecCommand("virsh", "define", xmlPath)
	utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(xmlPath)))
	if defineResult.Error != nil {
		return fmt.Errorf("定义虚拟机失败: %s", defineResult.Stderr)
	}
	if memoryMeta != nil {
		if err := writeVMMemoryMetadata(params.Name, memoryMeta); err != nil {
			return err
		}
	}
	if err := WriteVMTemplateSource(params.Name, params.Template, "linked"); err != nil {
		return err
	}
	if err := SetVMRemark(params.Name, params.Remark); err != nil {
		return err
	}

	if err := SetVMFreeze(params.Name, params.Freeze); err != nil {
		return err
	}

	startFn := StartVM
	if firstBootColdReboot {
		startFn = StartVMPreserveRebootAction
	}
	if err := startFn(params.Name); err != nil {
		return err
	}
	if firstBootColdReboot {
		if err := CompleteWindowsFirstBootColdReboot(ctx, params.Name, progressFn); err != nil {
			return err
		}
	}

	return nil
}

func buildFnOSUserProvisionCommand(username string) string {
	quotedUser := utils.ShellSingleQuote(username)
	quotedHome := utils.ShellSingleQuote("/home/" + username)
	return fmt.Sprintf(`TARGET_USER=%s
TARGET_HOME=%s
if ! getent group Users >/dev/null 2>&1; then
  echo '缺少 Users 组' >&2
  exit 1
fi
if ! getent group Administrators >/dev/null 2>&1; then
  echo '缺少 Administrators 组' >&2
  exit 1
fi
if id -u "$TARGET_USER" >/dev/null 2>&1; then
  usermod -g Users -G Administrators -s /bin/bash -d "$TARGET_HOME" -m "$TARGET_USER"
else
  useradd -m -N -g Users -G Administrators -s /bin/bash "$TARGET_USER"
fi
passwd -u "$TARGET_USER" 2>/dev/null || true`, quotedUser, quotedHome)
}

func buildFnOSPasswordCommand(username, password string) string {
	return fmt.Sprintf("printf '%%s:%%s\\n' %s %s | chpasswd",
		utils.ShellSingleQuote(username),
		utils.ShellSingleQuote(password),
	)
}

func buildFnOSHostnameCommand(hostname string) string {
	return fmt.Sprintf("printf '%%s\\n' %s > /etc/hostname", utils.ShellSingleQuote(hostname))
}

func buildFnOSHostsCommand(hostname string) string {
	return fmt.Sprintf(`TARGET_HOSTNAME=%s
if grep -q '^127\.0\.1\.1[[:space:]]' /etc/hosts; then
  sed -i "s/^127\.0\.1\.1[[:space:]].*/127.0.1.1\t${TARGET_HOSTNAME}/" /etc/hosts
else
  printf '127.0.1.1\t%%s\n' "$TARGET_HOSTNAME" >> /etc/hosts
fi`, utils.ShellSingleQuote(hostname))
}

func buildFnOSIdentityResetCommand() string {
	return "truncate -s 0 /etc/machine-id && rm -f /var/lib/dbus/machine-id 2>/dev/null || true"
}

func buildFnOSIdentityPreservationCommand() string {
	return strings.TrimSpace(`
if [ -s /etc/machine-id ] && [ ! -e /var/lib/dbus/machine-id ]; then
  mkdir -p /var/lib/dbus
  cp /etc/machine-id /var/lib/dbus/machine-id
fi`)
}

func buildFnOSCustomDeviceIDCommand(deviceID string) (string, error) {
	normalized, machineID, err := normalizeFnOSDeviceID(deviceID)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`CUSTOM_DEVICE_ID=%s
CUSTOM_MACHINE_ID=%s
mkdir -p /usr/trim/etc /var/lib/dbus
chattr -i /usr/trim/etc/machine_id /etc/device_id 2>/dev/null || true
printf '%%s\n' "$CUSTOM_DEVICE_ID" > /etc/device_id
printf '%%s\n' "$CUSTOM_MACHINE_ID" > /usr/trim/etc/machine_id
if [ -s /etc/machine-id ] && [ ! -e /var/lib/dbus/machine-id ]; then
  cp /etc/machine-id /var/lib/dbus/machine-id
fi
chattr +i /usr/trim/etc/machine_id /etc/device_id 2>/dev/null || true`, utils.ShellSingleQuote(normalized), utils.ShellSingleQuote(machineID)), nil
}

func normalizeFnOSDeviceID(deviceID string) (string, string, error) {
	normalized := strings.ToLower(strings.TrimSpace(deviceID))
	if !fnOSDeviceIDRegexp.MatchString(normalized) {
		return "", "", fmt.Errorf("FnOS 设备 ID 格式错误，请填写 32 位或 40 位十六进制字符串")
	}
	if len(normalized) == 40 {
		return normalized[:32], normalized, nil
	}
	return normalized, normalized + "00000000", nil
}

func ValidateFnOSDeviceID(deviceID string) error {
	_, _, err := normalizeFnOSDeviceID(deviceID)
	return err
}

// injectMemballoonConfig 向 virt-install --print-xml 生成的 XML 注入 memballoon 配置
// enableFPR 为 true 时启用 freePageReporting（Linux/FnOS），为 false 时仅设置 stats period
func injectMemballoonConfig(xmlStr string, enableFPR bool) string {
	var memballoonXML string
	if enableFPR {
		memballoonXML = `    <memballoon model="virtio" freePageReporting="on"><stats period="5"/></memballoon>`
	} else {
		memballoonXML = `    <memballoon model="virtio"><stats period="5"/></memballoon>`
	}

	// 如果已有 memballoon 标签，直接替换
	if strings.Contains(xmlStr, "<memballoon") {
		// 非自闭合标签
		re := regexp.MustCompile(`(?s)<memballoon[^>]*>.*?</memballoon>`)
		if re.MatchString(xmlStr) {
			return re.ReplaceAllString(xmlStr, memballoonXML)
		}
		// 自闭合标签
		reSelf := regexp.MustCompile(`<memballoon[^/]*/\s*>`)
		if reSelf.MatchString(xmlStr) {
			return reSelf.ReplaceAllString(xmlStr, memballoonXML)
		}
	}

	// virt-install --print-xml 通常不生成 memballoon 标签，在 </devices> 前插入
	return strings.Replace(xmlStr, "</devices>", memballoonXML+"\n  </devices>", 1)
}

func prepareUEFITemplateNVRAMForClone(domainXML, vmName, templateNVRAMPath string) (string, error) {
	templateNVRAMPath = strings.TrimSpace(templateNVRAMPath)
	if templateNVRAMPath == "" {
		return domainXML, nil
	}
	checkTemplate := utils.ExecShell(fmt.Sprintf("test -f %s && echo ok", utils.ShellSingleQuote(templateNVRAMPath)))
	if strings.TrimSpace(checkTemplate.Stdout) != "ok" {
		return domainXML, nil
	}
	cloneNVRAMPath := extractDomainNVRAMPath(domainXML)
	if cloneNVRAMPath == "" {
		vmName = strings.TrimSpace(vmName)
		if vmName == "" {
			return domainXML, fmt.Errorf("无法生成克隆虚拟机 NVRAM 路径")
		}
		cloneNVRAMPath = fmt.Sprintf("/var/lib/libvirt/qemu/nvram/%s_VARS.fd", vmName)
		domainXML = ensureDomainNVRAMPath(domainXML, cloneNVRAMPath)
	}
	if err := createQCOW2NVRAMFromTemplate(templateNVRAMPath, cloneNVRAMPath); err != nil {
		return domainXML, fmt.Errorf("复制模板 UEFI NVRAM 失败: %w", err)
	}
	return setDomainNVRAMFormat(domainXML, "qcow2"), nil
}

func ensureDomainNVRAMPath(domainXML, nvramPath string) string {
	nvramPath = strings.TrimSpace(nvramPath)
	if strings.TrimSpace(domainXML) == "" || nvramPath == "" {
		return domainXML
	}
	reWithContent := regexp.MustCompile(`(?s)<nvram([^>]*)>\s*[^<]*\s*</nvram>`)
	if reWithContent.MatchString(domainXML) {
		return setDomainNVRAMFormat(reWithContent.ReplaceAllString(domainXML, "<nvram$1>"+nvramPath+"</nvram>"), "qcow2")
	}
	reSelfClosing := regexp.MustCompile(`(?s)<nvram([^>]*)/\s*>`)
	if reSelfClosing.MatchString(domainXML) {
		return setDomainNVRAMFormat(reSelfClosing.ReplaceAllString(domainXML, "<nvram$1>"+nvramPath+"</nvram>"), "qcow2")
	}
	nvramXML := fmt.Sprintf("    <nvram template='/usr/share/OVMF/OVMF_VARS_4M.ms.fd' templateFormat='raw' format='qcow2'>%s</nvram>\n", nvramPath)
	if strings.Contains(domainXML, "</os>") {
		return strings.Replace(domainXML, "</os>", nvramXML+"  </os>", 1)
	}
	return domainXML
}

// waitForIP 等待虚拟机获取 IP
func waitForIP(vmName string, maxWaitSeconds int) string {
	for waited := 0; waited < maxWaitSeconds; waited += 3 {
		ip := getVMIP(vmName, true)
		if ip != "" {
			return ip
		}
		time.Sleep(3 * time.Second)
	}
	return ""
}

// waitForIPWithContext 等待虚拟机获取 IP（支持取消）
func waitForIPWithContext(ctx context.Context, vmName string, maxWaitSeconds int) string {
	for waited := 0; waited < maxWaitSeconds; waited += 3 {
		select {
		case <-ctx.Done():
			return ""
		default:
		}
		ip := getVMIP(vmName, true)
		if ip != "" {
			return ip
		}
		// 可取消的 sleep
		select {
		case <-ctx.Done():
			return ""
		case <-time.After(3 * time.Second):
		}
	}
	return ""
}

// initLinuxClone Linux 克隆初始化（SSH 设置 hostname/user/password/扩容）
func initLinuxClone(params *CloneParams, ip string, progressFn func(int, string)) error {
	// 使用参数中的值，若未指定则使用默认值
	templateRootPass := params.TemplateRootPass
	if templateRootPass == "" {
		templateRootPass = "Qwert333"
	}
	templateUser := params.TemplateUser
	if templateUser == "" {
		templateUser = "xinyu"
	}

	// 等待 SSH 初始化
	progressFn(75, "等待 SSH 就绪...")
	time.Sleep(30 * time.Second)

	// 清理 known_hosts
	utils.ExecShell(fmt.Sprintf("ssh-keygen -f /root/.ssh/known_hosts -R %s 2>/dev/null", utils.ShellSingleQuote(ip)))

	// 使用模板中设置的用户登录 SSH
	sshUser := templateUser
	sshPass := templateRootPass
	sshReady := false

	for i := 0; i < 12; i++ {
		testResult := utils.ExecShell(fmt.Sprintf(
			"sshpass -p %s ssh -o StrictHostKeyChecking=no -o ConnectTimeout=3 -o UserKnownHostsFile=/dev/null %s 'echo ok' 2>/dev/null",
			utils.ShellSingleQuote(sshPass), utils.ShellSingleQuote(fmt.Sprintf("%s@%s", sshUser, ip))))
		if strings.TrimSpace(testResult.Stdout) == "ok" {
			sshReady = true
			break
		}
		time.Sleep(5 * time.Second)
	}

	if !sshReady {
		return fmt.Errorf("SSH 连接超时，Linux 初始化未执行")
	}

	progressFn(80, fmt.Sprintf("SSH 初始化 (用户: %s): hostname/用户/密码/磁盘扩容...", sshUser))

	// 构建初始化脚本
	var initCmds []string

	if !params.LinuxIdentityPrepared {
		// 兼容导入等没有走离线预处理的路径，模板克隆会在首次启动前处理。
		initCmds = append(initCmds,
			"rm -f /etc/machine-id",
			"systemd-machine-id-setup",
			"rm -f /var/lib/dhcp/*.leases 2>/dev/null || true",
			"rm -f /var/lib/NetworkManager/*.lease 2>/dev/null || true",
			"rm -f /var/lib/systemd/network/*.lease 2>/dev/null || true",
			"rm -rf /run/systemd/netif/leases/* 2>/dev/null || true",
		)
	}

	// 设置 hostname
	initCmds = append(initCmds,
		fmt.Sprintf("hostnamectl set-hostname %s", utils.ShellSingleQuote(params.Hostname)),
		fmt.Sprintf("echo %s > /etc/hostname", utils.ShellSingleQuote(params.Hostname)),
		fmt.Sprintf("sed -i 's/127.0.1.1.*/127.0.1.1\\t%s/' /etc/hosts", params.Hostname),
	)

	// 设置用户名（直接编辑配置文件，避免 pkill 杀掉当前会话导致 usermod 失败）
	if params.User != "" && params.User != templateUser {
		initCmds = append(initCmds,
			// 修改 /etc/passwd 中的用户名和 home 目录
			fmt.Sprintf("sed -i 's/^%s:/%s:/' /etc/passwd", templateUser, params.User),
			fmt.Sprintf("sed -i 's|/home/%s|/home/%s|' /etc/passwd", templateUser, params.User),
			// 修改 /etc/shadow 中的用户名
			fmt.Sprintf("sed -i 's/^%s:/%s:/' /etc/shadow", templateUser, params.User),
			// 修改 /etc/group 中的组名
			fmt.Sprintf("sed -i 's/^%s:/%s:/' /etc/group", templateUser, params.User),
			fmt.Sprintf("sed -i 's/^%s:/%s:/' /etc/gshadow 2>/dev/null || true", templateUser, params.User),
			// 移动 home 目录
			fmt.Sprintf("mv '/home/%s' '/home/%s' 2>/dev/null || true", templateUser, params.User),
			// 修改 sudoers 中的引用
			fmt.Sprintf("sed -i 's/%s/%s/g' /etc/sudoers.d/* 2>/dev/null || true", templateUser, params.User),
		)
	}

	// 设置密码
	if params.Password != "" {
		targetUser := params.User
		if targetUser == "" {
			targetUser = templateUser
		}
		initCmds = append(initCmds,
			fmt.Sprintf("printf '%%s:%%s\\n' %s %s | chpasswd", utils.ShellSingleQuote("root"), utils.ShellSingleQuote(params.Password)),
			fmt.Sprintf("printf '%%s:%%s\\n' %s %s | chpasswd", utils.ShellSingleQuote(targetUser), utils.ShellSingleQuote(params.Password)),
		)
	}

	// 磁盘扩容
	initCmds = append(initCmds, buildLinuxDiskResizeScript())

	if !params.LinuxIdentityPrepared {
		// 延迟重启网络服务（等 SSH 会话退出后再执行，避免 SSH 挂死）
		// systemd-networkd 需要重启才能读取新的 machine-id 生成新的 DHCP DUID
		initCmds = append(initCmds,
			"sleep 8",
			"systemctl restart systemd-networkd 2>/dev/null || netplan apply 2>/dev/null || systemctl restart NetworkManager 2>/dev/null || true",
		)
	}

	script := strings.Join(initCmds, "\n")

	// 根据登录用户决定执行方式
	var sshCmd string
	if sshUser == "root" {
		// root 直接执行
		sshCmd = fmt.Sprintf(
			"sshpass -p %s ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null %s bash -s << 'INITEOF'\n%s\nINITEOF",
			utils.ShellSingleQuote(sshPass), utils.ShellSingleQuote("root@"+ip), script)
	} else {
		// 普通用户：先写脚本到临时文件，再通过 sudo 或 su 提权执行。
		// Debian 默认可能禁用 root SSH 密码登录，但允许普通用户 su 到 root。
		sshCmd = fmt.Sprintf(
			"sshpass -p %s ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null %s bash -s << 'INITEOF'\n"+
				"cat > /tmp/_clone_init.sh << 'SCRIPTEOF'\n%s\nSCRIPTEOF\n"+
				"if command -v sudo >/dev/null 2>&1 && printf '%%s\\n' %s | sudo -S -p '' true >/tmp/_clone_privilege_check.log 2>&1; then\n"+
				"  printf '%%s\\n' %s | sudo -S -p '' bash /tmp/_clone_init.sh > /tmp/_clone_init.log 2>&1\n"+
				"  INIT_STATUS=$?\n"+
				"else\n"+
				"  printf '%%s\\n' %s | su - root -c 'bash /tmp/_clone_init.sh > /tmp/_clone_init.log 2>&1'\n"+
				"  INIT_STATUS=$?\n"+
				"fi\n"+
				"if [ \"$INIT_STATUS\" -ne 0 ]; then\n"+
				"  echo '初始化脚本执行失败，日志如下:' >&2\n"+
				"  cat /tmp/_clone_init.log >&2 2>/dev/null || true\n"+
				"  exit \"$INIT_STATUS\"\n"+
				"fi\n"+
				"INITEOF",
			utils.ShellSingleQuote(sshPass), utils.ShellSingleQuote(fmt.Sprintf("%s@%s", sshUser, ip)), script,
			utils.ShellSingleQuote(sshPass), utils.ShellSingleQuote(sshPass), utils.ShellSingleQuote(sshPass))
	}

	result := utils.ExecShellWithTimeout(sshCmd, 5*time.Minute)
	if result.Error != nil {
		return fmt.Errorf("SSH 初始化执行失败: %s", firstNonEmpty(result.Stderr, result.Error.Error()))
	}
	if params.Password != "" {
		targetUser := params.User
		if targetUser == "" {
			targetUser = templateUser
		}
		if err := waitForLinuxCloneCredential(targetUser, params.Password, ip, 90*time.Second); err != nil {
			return err
		}
	}
	progressFn(95, "SSH 初始化完成")
	return nil
}

func buildLinuxDiskResizeScript() string {
	return strings.TrimSpace(`
set -e

get_parent_disk() {
  local DEV="$1"
  local DEV_NAME
  local SYS_PATH
  local PARENT_NAME
  local PKNAME

  DEV_NAME=$(basename "$(readlink -f "$DEV" 2>/dev/null || echo "$DEV")")
  SYS_PATH=$(readlink -f "/sys/class/block/$DEV_NAME" 2>/dev/null || true)
  if [ -n "$SYS_PATH" ]; then
    PARENT_NAME=$(basename "$(dirname "$SYS_PATH")")
    if [ -n "$PARENT_NAME" ] && [ "$PARENT_NAME" != "block" ] && [ "$PARENT_NAME" != "$DEV_NAME" ]; then
      echo "/dev/$PARENT_NAME"
      return 0
    fi
  fi

  PKNAME=$(lsblk -no PKNAME "$DEV" 2>/dev/null | head -1 | tr -d ' ')
  if [ -n "$PKNAME" ] && [ "$PKNAME" != "$DEV_NAME" ]; then
    echo "/dev/$PKNAME"
    return 0
  fi
  if echo "$DEV" | grep -Eq 'p[0-9]+$'; then
    echo "$DEV" | sed -E 's/p[0-9]+$//'
  else
    echo "$DEV" | sed -E 's/[0-9]+$//'
  fi
}

get_partition_number() {
  local DEV="$1"
  local DEV_NAME
  local PART_NUM

  DEV_NAME=$(basename "$(readlink -f "$DEV" 2>/dev/null || echo "$DEV")")
  PART_NUM=$(cat "/sys/class/block/$DEV_NAME/partition" 2>/dev/null || true)
  if [ -n "$PART_NUM" ]; then
    echo "$PART_NUM"
    return 0
  fi

  PART_NUM=$(lsblk -no PARTN "$DEV" 2>/dev/null | head -1 | tr -d ' ')
  if echo "$PART_NUM" | grep -Eq '^[0-9]+$'; then
    echo "$PART_NUM"
    return 0
  fi
  echo "$DEV" | sed -E 's/^.*[^0-9]([0-9]+)$/\1/'
}

reread_partition_table() {
  local DISK="$1"
  partprobe "$DISK" 2>/dev/null || true
  partx -u "$DISK" 2>/dev/null || true
  blockdev --rereadpt "$DISK" 2>/dev/null || true
  udevadm settle 2>/dev/null || true
}

partition_has_grow_room() {
  local DISK="$1"
  local PART_DEV="$2"
  local PART_NAME
  local DISK_SECTORS
  local PART_START
  local PART_SECTORS
  local PART_END

  PART_NAME=$(basename "$(readlink -f "$PART_DEV" 2>/dev/null || echo "$PART_DEV")")
  DISK_SECTORS=$(blockdev --getsz "$DISK" 2>/dev/null || true)
  PART_START=$(cat "/sys/class/block/$PART_NAME/start" 2>/dev/null || true)
  PART_SECTORS=$(cat "/sys/class/block/$PART_NAME/size" 2>/dev/null || true)
  if [ -z "$DISK_SECTORS" ] || [ -z "$PART_START" ] || [ -z "$PART_SECTORS" ]; then
    return 0
  fi

  PART_END=$((PART_START + PART_SECTORS))
  [ $((DISK_SECTORS - PART_END)) -gt 2048 ]
}

grow_partition() {
  local PART_DEV="$1"
  local DISK
  local PART_NUM
  DISK=$(get_parent_disk "$PART_DEV")
  PART_NUM=$(get_partition_number "$PART_DEV")

  if [ -z "$DISK" ] || [ -z "$PART_NUM" ] || [ "$DISK" = "$PART_DEV" ]; then
    echo "无法识别根分区所在磁盘: $PART_DEV" >&2
    return 1
  fi

  if ! partition_has_grow_room "$DISK" "$PART_DEV"; then
    echo "分区已占满磁盘，跳过分区扩容: $PART_DEV"
    return 0
  fi

  if command -v growpart >/dev/null 2>&1; then
    if growpart "$DISK" "$PART_NUM"; then
      reread_partition_table "$DISK"
      return 0
    fi
    if ! partition_has_grow_room "$DISK" "$PART_DEV"; then
      echo "growpart 执行后分区已占满磁盘，继续后续扩容"
      return 0
    fi
    echo "growpart 扩容失败，尝试使用其他分区工具" >&2
  fi
	if command -v parted >/dev/null 2>&1; then
		parted -s "$DISK" resizepart "$PART_NUM" 100%
	elif command -v sfdisk >/dev/null 2>&1; then
		printf ', +\n' | sfdisk --no-reread -N "$PART_NUM" "$DISK"
	else
		echo "缺少分区扩容工具，请在模板内安装 cloud-guest-utils、parted 或 util-linux(sfdisk)" >&2
		return 1
	fi

	reread_partition_table "$DISK"
}

resize_filesystem() {
  local TARGET="$1"
  if command -v resize2fs >/dev/null 2>&1; then
    resize2fs "$TARGET" 2>/dev/null && return 0
  fi
  if command -v xfs_growfs >/dev/null 2>&1; then
    xfs_growfs / && return 0
  fi
  echo "缺少文件系统扩容工具，请在模板内安装 e2fsprogs 或 xfsprogs" >&2
  return 1
}

ROOT_DEV=$(findmnt -n -o SOURCE /)
if echo "$ROOT_DEV" | grep -q "mapper"; then
  VG_NAME=$(lvs --noheadings -o vg_name "$ROOT_DEV" 2>/dev/null | awk '{print $1}' | head -1)
  PV_DEV=$(pvs --noheadings -o pv_name,vg_name 2>/dev/null | awk -v vg="$VG_NAME" '$2 == vg {print $1; exit}')
  if [ -z "$PV_DEV" ]; then
    PV_DEV=$(pvs --noheadings -o pv_name 2>/dev/null | awk '{print $1}' | head -1)
  fi
  if [ -z "$PV_DEV" ]; then
    echo "未找到 LVM 物理卷，无法扩容根分区" >&2
    exit 1
  fi

  grow_partition "$PV_DEV"
  pvresize "$PV_DEV"

  FREE_EXTENTS=$(vgs --noheadings -o vg_free_count "$VG_NAME" 2>/dev/null | awk '{print $1}' | head -1)
  case "${FREE_EXTENTS:-0}" in
    ''|*[!0-9]*) FREE_EXTENTS=0 ;;
  esac
  if [ "$FREE_EXTENTS" -gt 0 ]; then
    if ! lvextend -r -l +100%FREE "$ROOT_DEV"; then
      echo "lvextend -r 失败，尝试分步扩容根逻辑卷"
      FREE_EXTENTS=$(vgs --noheadings -o vg_free_count "$VG_NAME" 2>/dev/null | awk '{print $1}' | head -1)
      case "${FREE_EXTENTS:-0}" in
        ''|*[!0-9]*) FREE_EXTENTS=0 ;;
      esac
      if [ "$FREE_EXTENTS" -gt 0 ]; then
        lvextend -l +100%FREE "$ROOT_DEV"
      fi
      resize_filesystem "$ROOT_DEV"
    fi
  else
    echo "VG 无可用空间，跳过 LV 扩容，仅检查文件系统"
    resize_filesystem "$ROOT_DEV"
  fi
else
  grow_partition "$ROOT_DEV"
  resize_filesystem "$ROOT_DEV"
fi
`)
}

func waitForLinuxCloneCredential(username, password, ip string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		result := utils.ExecShell(fmt.Sprintf(
			"sshpass -p %s ssh -o StrictHostKeyChecking=no -o ConnectTimeout=3 -o UserKnownHostsFile=/dev/null %s 'echo ok' 2>/dev/null",
			utils.ShellSingleQuote(password),
			utils.ShellSingleQuote(fmt.Sprintf("%s@%s", username, ip)),
		))
		if strings.TrimSpace(result.Stdout) == "ok" {
			return nil
		}
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("Linux 初始化后无法使用新账号密码登录，请检查模板 sudo 权限或初始化日志")
}

// BatchCloneVM 批量克隆（支持取消）
func BatchCloneVM(ctx context.Context, params *BatchCloneParams, progressFn func(int, string)) ([]CloneResult, error) {
	maxConcurrency := config.GlobalConfig.BatchCloneMaxConcurrency
	if maxConcurrency <= 0 {
		maxConcurrency = 10
	}

	results := make([]CloneResult, params.Count)
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxConcurrency)
	var completed int32
	var cancelled int32

	progressFn(0, fmt.Sprintf("开始批量克隆 %d 台虚拟机（最大并发 %d）...", params.Count, maxConcurrency))

	for i := 0; i < params.Count; i++ {
		select {
		case <-ctx.Done():
			return results[:completed], taskqueue.ErrTaskCanceled
		default:
		}

		wg.Add(1)
		sem <- struct{}{}

		go func(index int) {
			defer wg.Done()
			defer func() { <-sem }()

			if atomic.LoadInt32(&cancelled) == 1 {
				return
			}

			vmName := fmt.Sprintf("%s-%s", params.Prefix, padNum(params.StartNum+index))

			vmPassword := params.Password
			if vmPassword == "" {
				vmPassword = GenerateRandomStrongPassword()
			}

			cloneParams := &CloneParams{
				Name:                vmName,
				Template:            params.Template,
				TemplateType:        params.TemplateType,
				CloneMode:           params.CloneMode,
				VCPU:                params.VCPU,
				MaxVCPU:             params.MaxVCPU,
				RAM:                 params.RAM,
				DiskSize:            params.DiskSize,
				Network:             params.Network,
				Hostname:            params.Hostname,
				User:                params.User,
				Password:            vmPassword,
				Autostart:           params.Autostart,
				Freeze:              params.Freeze,
				APIC:                params.APIC,
				PAE:                 params.PAE,
				RTCOffset:           params.RTCOffset,
				RTCStartDate:        params.RTCStartDate,
				GuestAgent:          params.GuestAgent,
				SMBIOS1:             params.SMBIOS1,
				UEFI:                params.UEFI,
				TemplateRootPass:    params.TemplateRootPass,
				TemplateUser:        params.TemplateUser,
				VideoModel:          params.VideoModel,
				DiskBus:             params.DiskBus,
				NicModel:            params.NicModel,
				StoragePoolID:       params.StoragePoolID,
				CPUTopologyMode:     params.CPUTopologyMode,
				CPULimitPercent:     params.CPULimitPercent,
				CPUAffinity:         params.CPUAffinity,
				FirstBootRebootMode: params.FirstBootRebootMode,
				SwitchID:            params.SwitchID,
				SecurityGroupID:     params.SecurityGroupID,
				IsAdmin:             params.IsAdmin,
			}

			subProgress := func(_ int, msg string) {
				log.Printf("[批量克隆 %s] %s", vmName, msg)
			}

			result, err := CloneVM(ctx, cloneParams, subProgress)
			if err != nil {
				if err == taskqueue.ErrTaskCanceled {
					atomic.StoreInt32(&cancelled, 1)
					return
				}
			}

			mu.Lock()
			if err != nil {
				results[index] = CloneResult{VMName: vmName, Error: err.Error()}
			} else {
				if result.Password == "" {
					result.Password = vmPassword
				}
				results[index] = *result
			}
			atomic.AddInt32(&completed, 1)
			done := atomic.LoadInt32(&completed)
			progressFn(int(done*100/int32(params.Count)), fmt.Sprintf("已完成 %d/%d 台", done, params.Count))
			mu.Unlock()
		}(i)
	}

	wg.Wait()

	if atomic.LoadInt32(&cancelled) == 1 {
		return results, taskqueue.ErrTaskCanceled
	}

	return results, nil
}

type reinstallSystemDiskInfo struct {
	Path      string
	Device    string
	Bus       string
	SizeGB    int
	SizeBytes int64
}

func NormalizeReinstallDiskSizeGB(requestedDiskSize, currentDiskSize, minDiskSize int) int {
	resolved := requestedDiskSize
	if resolved <= 0 {
		resolved = currentDiskSize
	}
	if resolved <= 0 {
		resolved = minDiskSize
	}
	if minDiskSize > 0 && resolved < minDiskSize {
		resolved = minDiskSize
	}
	return resolved
}

func ResolveReinstallDiskSizeGB(vmName, templateName string, requestedDiskSize int) (int, error) {
	minDiskSize, err := GetTemplateMinDiskSizeGB(templateName)
	if err != nil {
		return 0, err
	}
	currentDiskSize, err := getVMSystemDiskSizeGB(vmName)
	if err != nil {
		return 0, err
	}
	resolved := NormalizeReinstallDiskSizeGB(requestedDiskSize, currentDiskSize, minDiskSize)
	if resolved <= 0 {
		return 0, fmt.Errorf("无法确定重装后的系统盘大小")
	}
	return resolved, nil
}

func getVMSystemDiskSizeGB(vmName string) (int, error) {
	info, err := inspectVMSystemDisk(vmName)
	if err != nil {
		return 0, err
	}
	return info.SizeGB, nil
}

func inspectVMSystemDisk(vmName string) (*reinstallSystemDiskInfo, error) {
	disks, err := ListDisks(vmName)
	if err != nil {
		return nil, err
	}

	for _, disk := range disks {
		if disk.DeviceType == "cdrom" || strings.TrimSpace(disk.Path) == "" {
			continue
		}
		info := &reinstallSystemDiskInfo{
			Path:   strings.TrimSpace(disk.Path),
			Device: strings.TrimSpace(disk.Device),
			Bus:    NormalizeVMDiskBus(disk.Bus),
		}
		qemuInfo := utils.ExecCommand("qemu-img", "info", "--output=json", "-U", info.Path)
		if qemuInfo.Error == nil {
			info.SizeBytes = parseQemuInfoBytes(qemuInfo.Stdout, "virtual-size")
			info.SizeGB = bytesToCeilGB(info.SizeBytes)
		}
		if info.SizeGB <= 0 {
			info.SizeGB = parseCapacityGBString(disk.CapacityGB)
		}
		if info.Bus == "" {
			info.Bus = "virtio"
		}
		if info.Path == "" {
			break
		}
		return info, nil
	}

	fallback := getVMDiskInfo(vmName)
	if strings.TrimSpace(fallback.path) == "" {
		return nil, fmt.Errorf("未找到虚拟机系统盘")
	}
	info := &reinstallSystemDiskInfo{
		Path:   strings.TrimSpace(fallback.path),
		Device: strings.TrimSpace(fallback.device),
		Bus:    "virtio",
		SizeGB: parseCapacityGBString(fallback.size),
	}
	qemuInfo := utils.ExecCommand("qemu-img", "info", "--output=json", "-U", info.Path)
	if qemuInfo.Error == nil {
		info.SizeBytes = parseQemuInfoBytes(qemuInfo.Stdout, "virtual-size")
		info.SizeGB = bytesToCeilGB(info.SizeBytes)
	}
	return info, nil
}

func parseCapacityGBString(raw string) int {
	normalized := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), "GB"))
	if normalized == "" {
		return 0
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(normalized), 64)
	if err != nil || value <= 0 {
		return 0
	}
	rounded := int(value)
	if float64(rounded) < value {
		rounded++
	}
	return rounded
}

func bytesToCeilGB(sizeBytes int64) int {
	if sizeBytes <= 0 {
		return 0
	}
	const gib = int64(1 << 30)
	sizeGB := sizeBytes / gib
	if sizeBytes%gib != 0 {
		sizeGB++
	}
	return int(sizeGB)
}

func normalizeBootFamily(bootType string) string {
	switch NormalizeVMBootType(bootType) {
	case VMBootTypeUEFI, VMBootTypeUEFISecure:
		return "uefi"
	case VMBootTypeBIOS:
		return "bios"
	default:
		return ""
	}
}

func IsReinstallBootFamilyCompatible(currentBootType, templateBootType string) bool {
	currentFamily := normalizeBootFamily(currentBootType)
	templateFamily := normalizeBootFamily(templateBootType)
	if currentFamily == "" || templateFamily == "" {
		return true
	}
	return currentFamily == templateFamily
}

func detectTemplateBootTypeForReinstall(templateName string, meta *TemplateMeta) (string, error) {
	if meta == nil {
		meta = &TemplateMeta{}
	}
	bootType := NormalizeVMBootType(normalizeTemplateBootType(meta.BootType))
	if bootType != "" {
		return bootType, nil
	}
	templatePath, err := ensureTemplatePath(templateName)
	if err != nil {
		return "", err
	}
	if DetectTemplateBootType(templatePath) == VMBootTypeUEFI {
		return VMBootTypeUEFI, nil
	}
	return VMBootTypeBIOS, nil
}

func shutdownVMForReinstall(ctx context.Context, vmName string, progressFn func(int, string)) error {
	if progressFn != nil {
		progressFn(18, "正在强制关闭虚拟机...")
	}
	result := utils.ExecCommand("virsh", "destroy", vmName)
	if result.Error != nil {
		state := strings.ToLower(strings.TrimSpace(utils.ExecCommand("virsh", "domstate", vmName).Stdout))
		if state != "shut off" && state != "shutoff" {
			return fmt.Errorf("强制断电失败: %s", result.Stderr)
		}
	}
	shutOff, err := waitForVMShutOff(ctx, vmName, 30*time.Second)
	if err != nil {
		return err
	}
	if !shutOff {
		return fmt.Errorf("强制关闭虚拟机超时，请稍后重试")
	}
	return nil
}

func createReinstallSystemDisk(templatePath, targetPath string, diskSize int) error {
	sizeArg := ""
	if diskSize > 0 {
		sizeArg = fmt.Sprintf(" '%dG'", diskSize)
	}
	cmd := fmt.Sprintf("qemu-img create -f qcow2 -F qcow2 -b %s %s%s", utils.ShellSingleQuote(templatePath), utils.ShellSingleQuote(targetPath), sizeArg)
	result := utils.ExecShell(cmd)
	if result.Error != nil {
		return fmt.Errorf("创建新系统盘失败: %s", firstNonEmpty(result.Stderr, result.Error.Error()))
	}
	_ = utils.ExecCommand("chown", "libvirt-qemu:kvm", targetPath)
	return nil
}

func bestEffortRestoreReinstallDisk(originalDiskPath, backupDiskPath string) error {
	if strings.TrimSpace(backupDiskPath) == "" {
		return nil
	}
	if strings.TrimSpace(originalDiskPath) != "" {
		_ = os.Remove(originalDiskPath)
	}
	if err := os.Rename(backupDiskPath, originalDiskPath); err != nil {
		return fmt.Errorf("恢复原系统盘失败: %w", err)
	}
	_ = utils.ExecCommand("chown", "libvirt-qemu:kvm", originalDiskPath)
	return nil
}

func buildReinstallCloneParams(params *ReinstallParams, diskBus string, templateMeta *TemplateMeta) *CloneParams {
	if templateMeta == nil {
		templateMeta = &TemplateMeta{}
	}
	cloneParams := &CloneParams{
		Name:                 params.Name,
		Template:             params.Template,
		TemplateType:         params.TemplateType,
		DiskSize:             params.DiskSize,
		Hostname:             strings.TrimSpace(params.Hostname),
		User:                 strings.TrimSpace(params.User),
		Password:             params.Password,
		TemplateRootPass:     params.TemplateRootPass,
		TemplateUser:         params.TemplateUser,
		DiskBus:              NormalizeVMDiskBus(diskBus),
		FirstBootRebootMode:  NormalizeVMFirstBootRebootMode(params.FirstBootRebootMode),
		PreserveFnOSDeviceID: params.PreserveFnOSDeviceID,
		FnOSDeviceID:         params.FnOSDeviceID,
	}
	if cloneParams.TemplateType == "" {
		cloneParams.TemplateType = strings.ToLower(strings.TrimSpace(templateMeta.Type))
	}
	if cloneParams.TemplateType == "" {
		cloneParams.TemplateType = "linux"
	}
	cloneParams.User = NormalizeCloneUsernameForTemplate(cloneParams.TemplateType, cloneParams.User)
	if cloneParams.Hostname == "" {
		cloneParams.Hostname = GenerateRandomCloneHostname()
	}
	if cloneParams.TemplateRootPass == "" {
		cloneParams.TemplateRootPass = templateMeta.RootPassword
	}
	if cloneParams.TemplateUser == "" {
		cloneParams.TemplateUser = templateMeta.TemplateUser
	}
	if cloneParams.DiskBus == "" {
		cloneParams.DiskBus = "virtio"
	}
	return cloneParams
}

// ReinstallVM 重装系统
func ReinstallVM(ctx context.Context, params *ReinstallParams, progressFn func(int, string)) (err error) {
	if progressFn == nil {
		progressFn = func(int, string) {}
	}
	if err = EnsureVMNotMigrating(params.Name, "重装系统"); err != nil {
		return err
	}

	templatePath, err := ensureTemplatePath(params.Template)
	if err != nil {
		return err
	}

	meta := GetTemplateMeta(params.Template)
	cloneParams := buildReinstallCloneParams(params, "", meta)
	requireCredentials := cloneParams.TemplateType == "linux" || cloneParams.TemplateType == "windows" || cloneParams.TemplateType == "fnos"
	if err := ValidateCloneCredentialsForTemplate(cloneParams.TemplateType, cloneParams.Hostname, cloneParams.User, cloneParams.Password, requireCredentials); err != nil {
		return err
	}
	if strings.TrimSpace(cloneParams.FnOSDeviceID) != "" {
		if err := ValidateFnOSDeviceID(cloneParams.FnOSDeviceID); err != nil {
			return err
		}
		cloneParams.PreserveFnOSDeviceID = true
	}

	originalXML, err := GetVMInactiveDomainXML(params.Name)
	if err != nil {
		return err
	}
	currentBootType := ParseVMBootTypeFromDomainXML(originalXML)
	templateBootType, err := detectTemplateBootTypeForReinstall(params.Template, meta)
	if err != nil {
		return err
	}
	if !IsReinstallBootFamilyCompatible(currentBootType, templateBootType) {
		return fmt.Errorf("所选模板的启动方式与当前虚拟机不兼容，仅支持相同启动族之间重装（当前：%s，模板：%s）", normalizeBootFamily(currentBootType), normalizeBootFamily(templateBootType))
	}

	systemDisk, err := inspectVMSystemDisk(params.Name)
	if err != nil {
		return err
	}
	cloneParams.DiskBus = systemDisk.Bus
	progressFn(8, "正在检查重装环境...")

	progressFn(12, "正在清理现有快照...")
	if _, err := DeleteAllSnapshots(params.Name, func(progress int, message string) {
		progressFn(12+progress/4, message)
	}); err != nil {
		return fmt.Errorf("重装前清理快照失败: %w", err)
	}

	if err := shutdownVMForReinstall(ctx, params.Name, progressFn); err != nil {
		return err
	}

	backupDiskPath := fmt.Sprintf("%s.reinstall-backup-%d", systemDisk.Path, time.Now().UnixNano())
	if err := os.Rename(systemDisk.Path, backupDiskPath); err != nil {
		return fmt.Errorf("备份原系统盘失败: %w", err)
	}

	started := false
	xmlModified := false
	rollbackNeeded := true
	defer func() {
		if rollbackNeeded {
			_ = utils.ExecCommand("virsh", "destroy", params.Name)
			var rollbackMessages []string
			if xmlModified {
				if restoreXMLErr := SetVMInactiveDomainXML(params.Name, originalXML); restoreXMLErr != nil {
					rollbackMessages = append(rollbackMessages, restoreXMLErr.Error())
				}
			}
			if started {
				time.Sleep(2 * time.Second)
			}
			if restoreDiskErr := bestEffortRestoreReinstallDisk(systemDisk.Path, backupDiskPath); restoreDiskErr != nil {
				rollbackMessages = append(rollbackMessages, restoreDiskErr.Error())
			}
			if len(rollbackMessages) > 0 {
				rollbackMessage := strings.Join(rollbackMessages, "；")
				if err != nil {
					err = fmt.Errorf("%w；回滚阶段还出现问题：%s", err, rollbackMessage)
				} else {
					err = fmt.Errorf("重装回滚失败：%s", rollbackMessage)
				}
			}
		}
	}()

	progressFn(30, "正在基于模板创建新系统盘...")
	if err := createReinstallSystemDisk(templatePath, systemDisk.Path, cloneParams.DiskSize); err != nil {
		return err
	}

	switch cloneParams.TemplateType {
	case "fnos":
		if err := prepareFnOSSystemDiskExpansion(ctx, systemDisk.Path, progressFn); err != nil {
			return err
		}
		if err := cloneFnOS(cloneParams, systemDisk.Path, progressFn); err != nil {
			return err
		}
	case "linux":
		progressFn(25, "正在重置 Linux 首次启动身份...")
		if err := prepareLinuxCloneFirstBootIdentity(cloneParams, systemDisk.Path); err != nil {
			return err
		}
		cloneParams.LinuxIdentityPrepared = true
	case "windows":
		if err := prepareWindowsSystemDiskExpansion(ctx, systemDisk.Path, progressFn); err != nil {
			return err
		}
		injectWindowsUnattendFile(params.Name, systemDisk.Path, cloneParams.Hostname, cloneParams.Password, progressFn)
	}

	firstBootColdReboot := ShouldUseWindowsFirstBootColdReboot(cloneParams.FirstBootRebootMode, cloneParams.TemplateType)
	if firstBootColdReboot {
		progressFn(40, "正在准备 Windows 首次冷重启策略...")
		updatedXML := ApplyFirstBootRebootModeToDomainXML(originalXML, cloneParams.FirstBootRebootMode)
		if err := SetVMInactiveDomainXML(params.Name, updatedXML); err != nil {
			return fmt.Errorf("设置 Windows 首次冷重启策略失败: %w", err)
		}
		xmlModified = true
	}

	progressFn(50, "正在启动虚拟机...")
	startFn := StartVM
	if firstBootColdReboot {
		startFn = StartVMPreserveRebootAction
	}
	if err := startFn(params.Name); err != nil {
		return err
	}
	started = true

	if firstBootColdReboot {
		if err := CompleteWindowsFirstBootColdReboot(ctx, params.Name, progressFn); err != nil {
			return err
		}
		if err := SetVMInactiveDomainXML(params.Name, originalXML); err != nil {
			return fmt.Errorf("恢复首次重启策略失败: %w", err)
		}
		xmlModified = false
	}

	if cloneParams.TemplateType == "linux" {
		progressFn(60, "等待虚拟机获取 IP...")
		time.Sleep(5 * time.Second)
		ip := waitForIPWithContext(ctx, params.Name, linuxCloneIPWaitSeconds)
		if ip == "" {
			return fmt.Errorf("未获取到虚拟机 IP，Linux 初始化无法执行")
		}
		progressFn(70, "正在执行 Linux SSH 初始化...")
		if err := initLinuxClone(cloneParams, ip, progressFn); err != nil {
			return err
		}
	}

	progressFn(95, "正在更新虚拟机模板与凭据记录...")
	if err := WriteVMTemplateSource(params.Name, params.Template, "linked"); err != nil {
		return err
	}
	if err := SaveVMCredential(params.Name, cloneParams.User, cloneParams.Password, "reinstall", params.Operator, false); err != nil {
		log.Printf("[警告] 保存虚拟机 %s 的重装凭据失败: %v", params.Name, err)
	}

	if err := os.Remove(backupDiskPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("重装成功，但清理旧系统盘备份失败: %w", err)
	}

	rollbackNeeded = false
	progressFn(100, "重装完成")
	return nil
}

// DeleteVM 删除虚拟机（含磁盘、静态IP、端口转发）- 兼容接口，删除所有磁盘
func DeleteVM(name string) error {
	return DeleteVMWithDisks(name, nil, nil, "")
}

// DeleteVMWithDisks 删除虚拟机（支持选择性删除/转移磁盘）
// deleteDisks: 要删除的磁盘路径列表（nil 表示删除所有磁盘）
// transferDisks: 要转移到用户存储的磁盘路径列表
// transferUser: 转移目标用户名
func DeleteVMWithDisks(name string, deleteDisks []string, transferDisks []string, transferUser string) error {
	if err := EnsureVMNotMigrating(name, "删除虚拟机"); err != nil {
		return err
	}
	if _, err := DeleteAllSnapshots(name, nil); err != nil {
		return fmt.Errorf("删除虚拟机前清理快照失败: %w", err)
	}

	// 强制关机
	utils.ExecCommand("virsh", "destroy", name)
	time.Sleep(1 * time.Second)

	// 在 undefine 之前收集虚拟机的所有 IP 地址（undefine 后拿不到 MAC/IP 了）
	vmIPs := collectVMIPs(name)

	// 解绑静态 IP（内部会级联删除对应静态 IP 的端口转发规则）
	unbindErr := UnbindStaticIP(name)
	if unbindErr != nil {
		log.Printf("清理静态 IP 绑定: %s (可忽略)", unbindErr)
	}

	// 无论是否有静态绑定，都确保清理所有关联 IP 的端口转发规则
	for _, ip := range vmIPs {
		removePortForwardsForIP(ip)
	}

	// 如果没有指定磁盘列表，收集所有磁盘路径用于删除
	var allDiskPaths []string
	if deleteDisks == nil && transferDisks == nil {
		// 兼容模式：收集所有磁盘路径，全部删除
		disks, err := ListDisks(name)
		if err == nil {
			for _, d := range disks {
				if d.Path != "" && d.DeviceType != "cdrom" {
					allDiskPaths = append(allDiskPaths, d.Path)
				}
			}
		}
	}

	// 弹出 cdrom（防止 undefine --remove-all-storage 删除 ISO 文件）
	utils.ExecShell(fmt.Sprintf("virsh change-media %s hda --eject 2>/dev/null || true", utils.ShellSingleQuote(name)))

	// 取消定义（含快照和 NVRAM，不使用 --remove-all-storage 避免删除 ISO 和需要转移的磁盘）
	result := utils.ExecCommand("virsh", "undefine", name, "--nvram", "--snapshots-metadata")
	if result.Error != nil {
		// 尝试不带 --nvram
		result = utils.ExecCommand("virsh", "undefine", name, "--snapshots-metadata")
		if result.Error != nil {
			return fmt.Errorf("删除虚拟机失败: %s", result.Stderr)
		}
	}

	// 处理磁盘：删除指定的磁盘
	if deleteDisks != nil {
		for _, diskPath := range deleteDisks {
			if diskPath != "" {
				utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(diskPath)))
			}
		}
	} else if len(allDiskPaths) > 0 {
		// 兼容模式：删除所有磁盘
		for _, diskPath := range allDiskPaths {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(diskPath)))
		}
	}

	// 转移需要保留的磁盘到用户存储
	if len(transferDisks) > 0 && transferUser != "" {
		diskDir := GetUserDiskDir(transferUser)
		// 确保目标目录存在
		utils.ExecCommand("mkdir", "-p", diskDir)
		for _, diskPath := range transferDisks {
			if diskPath == "" {
				continue
			}
			filename := filepath.Base(diskPath)
			destPath := filepath.Join(diskDir, filename)
			// 如果目标文件已存在，加上时间戳避免冲突
			checkResult := utils.ExecShell(fmt.Sprintf("test -f %s && echo exists", utils.ShellSingleQuote(destPath)))
			if strings.TrimSpace(checkResult.Stdout) == "exists" {
				ts := time.Now().Format("20060102150405")
				ext := filepath.Ext(filename)
				nameOnly := strings.TrimSuffix(filename, ext)
				destPath = filepath.Join(diskDir, fmt.Sprintf("%s_%s%s", nameOnly, ts, ext))
			}
			// 移动文件
			mvResult := utils.ExecShell(fmt.Sprintf("mv %s %s", utils.ShellSingleQuote(diskPath), utils.ShellSingleQuote(destPath)))
			if mvResult.Error != nil {
				log.Printf("[警告] 转移磁盘 %s 到用户存储失败: %s", diskPath, mvResult.Stderr)
				// 转移失败不阻断删除流程
			} else {
				// 设置文件权限
				utils.ExecCommand("chown", "libvirt-qemu:kvm", destPath)
			}
		}
	}

	// 清理资源历史记录
	DeleteVMStatsRecords(name)
	DeleteVMRuntimeRecord(name)
	CleanupVMVPCBinding(name)
	CleanupLightweightVMResources(name)
	_ = DeleteVMSchedules(name)

	return nil
}

// CheckDiskTransferQuota 检查转移磁盘是否有足够的存储配额
func CheckDiskTransferQuota(username string, diskPaths []string) (int64, error) {
	if len(diskPaths) == 0 {
		return 0, nil
	}

	// 计算要转移的磁盘总大小
	var totalBytes int64
	for _, diskPath := range diskPaths {
		duResult := utils.ExecShell(fmt.Sprintf("du -b %s 2>/dev/null | awk '{print $1}'", utils.ShellSingleQuote(diskPath)))
		if duResult.Error == nil {
			size, _ := strconv.ParseInt(strings.TrimSpace(duResult.Stdout), 10, 64)
			totalBytes += size
		}
	}

	// 检查配额
	if err := CheckStorageQuota(username, totalBytes); err != nil {
		return totalBytes, err
	}

	return totalBytes, nil
}

// collectVMIPs 收集虚拟机的所有关联 IP 地址（静态绑定 + DHCP 租约）
// 用于删除虚拟机时确保所有端口转发规则都被清理
func collectVMIPs(vmName string) []string {
	ipSet := make(map[string]bool)

	// 获取虚拟机 MAC 地址
	mac := getFirstVMMAC(vmName)
	if mac == "" {
		return nil
	}

	// 从静态绑定中获取 IP
	if ip := GetOVSStaticIPByMAC(mac); ip != "" {
		ipSet[ip] = true
	}
	// 补充查询所有 VPC 交换机的静态 MAC 绑定
	if allVpcHosts, err := ListAllVPCStaticHosts(); err == nil {
		for _, host := range allVpcHosts {
			if strings.EqualFold(host.MAC, mac) {
				ipSet[host.IP] = true
			}
		}
	}

	// 从 DHCP 租约中获取 IP（包含 OVS + VPC 租约）
	if ip := GetOVSLeaseIPByMAC(mac); ip != "" {
		ipSet[ip] = true
	}

	// 尝试通过 domifaddr 获取当前运行时 IP（可能和租约/静态不同）
	ipRe := regexp.MustCompile(`(\d+\.\d+\.\d+\.\d+)`)
	for _, source := range []string{"agent", "arp", "lease"} {
		addrResult := utils.ExecCommand("virsh", "domifaddr", vmName, "--source", source)
		if addrResult.Error == nil {
			allMatches := ipRe.FindAllStringSubmatch(addrResult.Stdout, -1)
			for _, m := range allMatches {
				if m[1] != "127.0.0.1" {
					ipSet[m[1]] = true
				}
			}
		}
	}

	var ips []string
	for ip := range ipSet {
		ips = append(ips, ip)
	}
	return ips
}

// padNum 零填充数字
func padNum(n int) string {
	return fmt.Sprintf("%02d", n)
}

// CloneTaskHandler 克隆任务处理器（用于任务队列）
func CloneTaskHandler(task interface{}, progressFn func(int, string)) (string, error) {
	// 实际使用时从 task.Params 反序列化参数
	return "", nil
}

// RegisterCloneHandlers 注册克隆相关任务处理器
func RegisterCloneHandlers() {
	// 这些处理器会在 main.go 中通过 taskqueue.RegisterHandler 注册
}

// ParseCloneParams 从 JSON 解析克隆参数
func ParseCloneParams(jsonStr string) (*CloneParams, error) {
	var params CloneParams
	if err := json.Unmarshal([]byte(jsonStr), &params); err != nil {
		return nil, err
	}
	return &params, nil
}

// ParseBatchCloneParams 从 JSON 解析批量克隆参数
func ParseBatchCloneParams(jsonStr string) (*BatchCloneParams, error) {
	var params BatchCloneParams
	if err := json.Unmarshal([]byte(jsonStr), &params); err != nil {
		return nil, err
	}
	return &params, nil
}

// ParseReinstallParams 从 JSON 解析重装参数
func ParseReinstallParams(jsonStr string) (*ReinstallParams, error) {
	var params ReinstallParams
	if err := json.Unmarshal([]byte(jsonStr), &params); err != nil {
		return nil, err
	}
	return &params, nil
}

// init 中不引用 strconv 以避免编译器警告
var _ = strconv.Itoa
