package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"kvm_console/config"
	"kvm_console/taskqueue"
	"kvm_console/utils"
)

// ImportVMParams 导入虚拟机参数
type ImportVMParams struct {
	Name             string                  `json:"name"`                         // 虚拟机名称
	Remark           string                  `json:"remark,omitempty"`             // 虚拟机备注
	DiskFile         string                  `json:"disk_file"`                    // 磁盘文件名（在用户 disk 目录中）
	Username         string                  `json:"username"`                     // 所属用户
	CopyDisk         bool                    `json:"copy_disk,omitempty"`          // true=复制磁盘文件，false=移动磁盘文件
	VCPU             int                     `json:"vcpu"`                         // CPU 核心数
	MaxVCPU          int                     `json:"max_vcpu,omitempty"`           // CPU 热添加上限
	RAM              int                     `json:"ram"`                          // 内存（GB）
	Network          string                  `json:"network,omitempty"`            // 网络
	InitType         string                  `json:"init_type,omitempty"`          // 初始化类型: linux/windows/other/空（不初始化）
	Hostname         string                  `json:"hostname,omitempty"`           // 主机名
	User             string                  `json:"user,omitempty"`               // 用户名（Linux 初始化用）
	Password         string                  `json:"password,omitempty"`           // 密码
	Autostart        bool                    `json:"autostart,omitempty"`          // 开机自启
	Freeze           bool                    `json:"freeze,omitempty"`             // 启动时冻结 CPU
	APIC             *bool                   `json:"apic,omitempty"`               // APIC 开关，默认启用
	PAE              *bool                   `json:"pae,omitempty"`                // PAE 开关，默认启用
	RTCOffset        string                  `json:"rtc_offset,omitempty"`         // RTC 使用本地时间还是 UTC
	RTCStartDate     string                  `json:"rtc_startdate,omitempty"`      // RTC 开始日期
	GuestAgent       *VMGuestAgentConfig     `json:"guest_agent,omitempty"`        // QEMU Guest Agent 配置
	SMBIOS1          *VMSMBIOS1Config        `json:"smbios1,omitempty"`            // SMBIOS 类型 1 设置
	BootType         string                  `json:"boot_type,omitempty"`          // 启动类型: bios/uefi
	MachineType      string                  `json:"machine_type,omitempty"`       // 机器类型: q35/pc
	NicModel         string                  `json:"nic_model,omitempty"`          // 网卡模型
	VideoModel       string                  `json:"video_model,omitempty"`        // 视频模型: virtio/vga/vmvga/cirrus
	CPUTopologyMode  string                  `json:"cpu_topology_mode,omitempty"`  // CPU 拓扑模式
	CPULimitPercent  int                     `json:"cpu_limit_percent,omitempty"`  // CPU 限制百分比，0 表示无限制
	CPUAffinity      string                  `json:"cpu_affinity,omitempty"`      // CPU 亲和性，如 "0,2,4"
	TemplateRootPass string                  `json:"template_root_pass,omitempty"` // 模板 root 密码（SSH 初始化用）
	TemplateUser     string                  `json:"template_user,omitempty"`      // 模板用户名
	MemoryDynamic     *VMMemoryDynamicRequest `json:"memory_dynamic,omitempty"`
	SwitchID          uint                    `json:"switch_id,omitempty"`
	SecurityGroupID   uint                    `json:"security_group_id,omitempty"`
	ExtraNics         []AddVMInterfaceRequest `json:"extra_nics,omitempty"`
	IsAdmin           bool                    `json:"is_admin,omitempty"`
	StartAfterImport  bool                    `json:"start_after_import"` // 导入完成后是否开启虚拟机，默认 true
}

// ImportVMResult 导入结果
type ImportVMResult struct {
	VMName   string `json:"vm_name"`
	DiskPath string `json:"disk_path"`
	IP       string `json:"ip,omitempty"`
}

// ParseImportVMParams 从 JSON 解析导入参数
func ParseImportVMParams(jsonStr string) (*ImportVMParams, error) {
	var params ImportVMParams
	if err := json.Unmarshal([]byte(jsonStr), &params); err != nil {
		return nil, err
	}
	// 向后兼容：旧任务 JSON 中无 start_after_import 字段时，默认开启虚拟机
	if !strings.Contains(jsonStr, `"start_after_import"`) {
		params.StartAfterImport = true
	}
	return &params, nil
}

// ImportVM 从磁盘文件导入虚拟机
func ImportVM(ctx context.Context, params *ImportVMParams, progressFn func(int, string)) (*ImportVMResult, error) {
	cloneDir := config.GlobalConfig.CloneDir
	if err := ValidateVMName(params.Name); err != nil {
		return nil, err
	}

	// 默认值
	if params.Network == "" {
		params.Network = config.GlobalConfig.DefaultNetwork
	}
	if !params.IsAdmin {
		params.CPULimitPercent = VMCPULimitUnlimited
	}
	if err := ValidateVMCPULimitPercent(params.CPULimitPercent); err != nil {
		return nil, err
	}
	params.CPUTopologyMode = NormalizeVMCPUTopologyMode(params.CPUTopologyMode)
	if params.Hostname == "" {
		params.Hostname = params.Name
	}
	if params.MachineType == "" {
		params.MachineType = "q35"
	}
	if params.BootType == "" {
		params.BootType = "bios"
	}
	if params.NicModel == "" {
		params.NicModel = "virtio"
	}

	// 检查虚拟机是否已存在
	checkVM := utils.ExecCommand("virsh", "dominfo", params.Name)
	if checkVM.ExitCode == 0 {
		return nil, fmt.Errorf("虚拟机 '%s' 已存在", params.Name)
	}

	// 安全检查：文件名不能包含路径分隔符
	if strings.Contains(params.DiskFile, "/") || strings.Contains(params.DiskFile, "..") {
		return nil, fmt.Errorf("非法磁盘文件名: %s", params.DiskFile)
	}

	// 源磁盘路径（用户 disk 目录中）
	srcDiskPath := filepath.Join(GetUserDiskDir(params.Username), params.DiskFile)

	// 检查源文件是否存在
	checkResult := utils.ExecShell(fmt.Sprintf("test -f %s && echo ok", utils.ShellSingleQuote(srcDiskPath)))
	if checkResult.Stdout != "ok" {
		return nil, fmt.Errorf("磁盘文件不存在: %s", params.DiskFile)
	}
	if err := EnsureOVSNetworkReady(); err != nil {
		return nil, err
	}

	progressFn(5, "检测磁盘格式...")

	// 检查取消
	select {
	case <-ctx.Done():
		return nil, taskqueue.ErrTaskCanceled
	default:
	}

	// 确定目标磁盘路径（移动到 CloneDir）
	// 检测磁盘格式
	format := "qcow2"
	infoResult := utils.ExecShell(fmt.Sprintf("qemu-img info -U --output=json %s 2>/dev/null", utils.ShellSingleQuote(srcDiskPath)))
	if infoResult.Error == nil {
		detected := parseQemuInfoStr(infoResult.Stdout, "format")
		if detected != "" {
			format = detected
		}
	}

	destDiskPath := filepath.Join(cloneDir, fmt.Sprintf("%s.%s", params.Name, format))

	// 根据 CopyDisk 选项决定移动还是复制
	if params.CopyDisk {
		// 保留原文件，复制到 CloneDir
		progressFn(12, fmt.Sprintf("检测到 %s 格式，正在复制磁盘文件到虚拟机目录（保留原文件）...", format))
		cpResult := utils.ExecCommandLongRunning("cp", "--sparse=always", srcDiskPath, destDiskPath)
		if cpResult.Error != nil {
			return nil, fmt.Errorf("复制磁盘文件失败: %s", cpResult.Stderr)
		}
		progressFn(20, "磁盘文件复制完成")
	} else {
		// 不保留原文件，移动到 CloneDir
		progressFn(12, fmt.Sprintf("检测到 %s 格式，正在移动磁盘文件到虚拟机目录（不保留原文件）...", format))
		moveResult := utils.ExecCommandLongRunning("mv", srcDiskPath, destDiskPath)
		if moveResult.Error != nil {
			// 如果跨设备移动失败，回退到 cp + rm
			log.Printf("mv 失败（可能跨设备），尝试 cp + rm: %s", moveResult.Stderr)
			progressFn(15, "跨设备复制磁盘文件到虚拟机目录...")
			cpResult := utils.ExecCommandLongRunning("cp", "--sparse=always", srcDiskPath, destDiskPath)
			if cpResult.Error != nil {
				return nil, fmt.Errorf("移动磁盘文件失败: %s", cpResult.Stderr)
			}
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(srcDiskPath)))
		}
		progressFn(20, "磁盘文件移动完成")
	}

	// 设置权限
	utils.ExecCommand("chown", "libvirt-qemu:kvm", destDiskPath)

	// 检查取消
	select {
	case <-ctx.Done():
		utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
		return nil, taskqueue.ErrTaskCanceled
	default:
	}

	progressFn(30, "创建虚拟机定义...")

	memoryMeta, ramMB, _, err := BuildVMMemoryMetadataForCreate(params.RAM, params.MemoryDynamic)
	if err != nil {
		utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
		return nil, err
	}

	// 检测是否为 UEFI 磁盘
	normalizedBootType := NormalizeVMBootType(params.BootType)
	needUEFI := false
	if normalizedBootType == VMBootTypeUEFI || normalizedBootType == VMBootTypeUEFISecure {
		needUEFI = true
	} else if params.BootType == "" || params.BootType == "bios" {
		// 自动检测
		efiCheck := utils.ExecShell(fmt.Sprintf(
			"virt-filesystems -a %s --filesystems --long 2>/dev/null | head -5 | grep -q 'vfat' && echo 'uefi'",
			utils.ShellSingleQuote(destDiskPath)))
		if efiCheck.Error == nil && strings.TrimSpace(efiCheck.Stdout) == "uefi" {
			needUEFI = true
		}
	}

	initType := strings.ToLower(params.InitType)
	isWindows := initType == "windows"

	if isWindows {
		// ===== Windows 导入 =====
		progressFn(35, "创建 Windows 虚拟机...")

		// 生成 MAC 地址
		macResult := utils.ExecShell(`printf '52:54:00:%02x:%02x:%02x' $((RANDOM%256)) $((RANDOM%256)) $((RANDOM%256))`)
		macAddr := strings.TrimSpace(macResult.Stdout)
		if macAddr == "" {
			macAddr = "52:54:00:aa:bb:cc"
		}

		// 生成 qcow2 NVRAM，避免 pflash 虚拟机创建内部内存快照失败。
		nvramClone := fmt.Sprintf("/var/lib/libvirt/qemu/nvram/%s_VARS.fd", params.Name)
		if err := createQCOW2NVRAMFromTemplate("/usr/share/OVMF/OVMF_VARS_4M.ms.fd", nvramClone); err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
		}

		ramKiB := ramMB * 1024

		rtcOffset := ResolveRTCOffset(params.RTCOffset, "windows")
		rtcStartDate := NormalizeRTCStartDate(params.RTCStartDate)
		clockOpenTag := fmt.Sprintf("<clock offset='%s'>", rtcOffset)
		if rtcStartDate != VMRTCStartDateNow {
			epoch, err := ParseRTCStartDateToEpoch(rtcStartDate)
			if err != nil {
				utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
				return nil, err
			}
			rtcOffset = VMRTCOffsetAbsolute
			clockOpenTag = fmt.Sprintf("<clock offset='%s' start='%s'>", rtcOffset, epoch)
		}
		vmXML := fmt.Sprintf(`<domain type='kvm'>
  <name>%s</name>
  <memory unit='KiB'>%d</memory>
%s
  <os firmware='efi'>
    <type arch='x86_64' machine='pc-q35-noble'>hvm</type>
    <firmware>
      <feature enabled='yes' name='enrolled-keys'/>
      <feature enabled='yes' name='secure-boot'/>
    </firmware>
    <loader readonly='yes' secure='yes' type='pflash'>/usr/share/OVMF/OVMF_CODE_4M.ms.fd</loader>
    <nvram template='/usr/share/OVMF/OVMF_VARS_4M.ms.fd' templateFormat='raw' format='qcow2'>%s</nvram>
    <boot dev='hd'/>
  </os>
  <features>
    <acpi/><apic/>
    <hyperv mode='custom'>
      <relaxed state='on'/><vapic state='on'/><spinlocks state='on' retries='8191'/>
    </hyperv>
    <vmport state='off'/><smm state='on'/>
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
      <driver name='qemu' type='%s' discard='unmap' detect_zeroes='unmap'/>
      <source file='%s'/><target dev='vda' bus='virtio'/>
    </disk>
    <controller type='usb' index='0' model='qemu-xhci' ports='15'/>
    <controller type='virtio-serial' index='0'/>
%s
    <input type='tablet' bus='usb'/>
    <tpm model='tpm-crb'><backend type='emulator' version='2.0'/></tpm>
    <graphics type='vnc' port='-1' autoport='yes' listen='0.0.0.0'>
      <listen type='address' address='0.0.0.0'/>
    </graphics>
    <video><model type='virtio' heads='1' primary='yes'/></video>
    <watchdog model='itco' action='reset'/>
    <memballoon model='virtio' freePageReporting='on'><stats period='5'/></memballoon>
  </devices>
</domain>`,
			params.Name, ramKiB, BuildVCPUTag(params.VCPU, params.MaxVCPU), nvramClone, clockOpenTag, format, destDiskPath, BuildOVSInterfaceXML(macAddr, params.NicModel))
		if memoryMeta != nil {
			vmXML, err = ApplyMemoryMetadataToDomainXML(vmXML, memoryMeta, false)
			if err != nil {
				utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
				return nil, err
			}
		}
		vmXML, err = ApplyVMGuestAgentConfigToDomainXML(vmXML, params.GuestAgent)
		if err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
		}
		vmXML, err = ApplySMBIOS1ConfigToDomainXML(vmXML, params.SMBIOS1, true)
		if err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
		}
		vmXML, err = ApplyVMAPICToDomainXML(vmXML, params.APIC)
		if err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
		}
		vmXML, err = ApplyVMPAEToDomainXML(vmXML, params.PAE)
		if err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
		}
		vmXML = ApplyVMVideoModelToDomainXML(vmXML, params.VideoModel, "windows")
		vmXML = ApplyWindowsGuestOptimizationsToDomainXML(vmXML)
		topoVCPU := EffectiveTopologyVCPU(params.VCPU, params.MaxVCPU)
		vmXML = ApplyCPUTopologyModeToDomainXML(vmXML, params.CPUTopologyMode, "windows", topoVCPU)
		vmXML = ApplyVMCPULimitToDomainXML(vmXML, params.VCPU, params.CPULimitPercent)
		if params.CPUAffinity != "" {
			affinityCores, affErr := ParseCPUAffinity(params.CPUAffinity)
			if affErr != nil {
				utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
				return nil, fmt.Errorf("CPU 亲和性格式错误: %w", affErr)
			}
			if len(affinityCores) > 0 {
				if affErr := ValidateCPUAffinity(affinityCores); affErr != nil {
					utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
					return nil, affErr
				}
			}
			vmXML = ApplyCPUAffinityToDomainXML(vmXML, topoVCPU, affinityCores)
		}
		vmXML, err = ApplyVPCSwitchToDomainXML(vmXML, params.SwitchID)
		if err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
		}

		xmlPath := fmt.Sprintf("/tmp/_vm-import-%s.xml", params.Name)
		utils.ExecShell(fmt.Sprintf("cat > %s << 'XMLEOF'\n%s\nXMLEOF", utils.ShellSingleQuote(xmlPath), vmXML))

		defineResult := utils.ExecCommand("virsh", "define", xmlPath)
		utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(xmlPath)))
		if defineResult.Error != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, fmt.Errorf("定义虚拟机失败: %s", defineResult.Stderr)
		}
		if memoryMeta != nil {
			if err := writeVMMemoryMetadata(params.Name, memoryMeta); err != nil {
				utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
				return nil, err
			}
		}
		if err := SetVMRemark(params.Name, params.Remark); err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
		}

		if err := SetVMFreeze(params.Name, params.Freeze); err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
		}

		if params.StartAfterImport {
			if err := StartVM(params.Name); err != nil {
				return nil, err
			}
		}
	} else {
		// ===== Linux / Other 导入 =====
		bootOpt := ""
		if needUEFI {
			bootOpt = "--boot uefi "
		}

		vcpuArg := fmt.Sprintf("--vcpus %d", params.VCPU)
		if params.MaxVCPU > params.VCPU {
			vcpuArg = fmt.Sprintf("--vcpus %d,maxvcpus=%d", params.VCPU, params.MaxVCPU)
		}

		installCmd := fmt.Sprintf(
			"virt-install --name '%s' --ram %d %s "+
				"--machine %s "+
				bootOpt+
				"--disk '%s,format=%s,bus=virtio,discard=unmap,detect_zeroes=unmap' "+
				"--osinfo detect=on,require=off "+
				BuildOVSVirtInstallNetworkArg(params.NicModel)+" "+
				"--graphics vnc,listen=0.0.0.0 "+
				"--video virtio "+
				"--import --cpu host-passthrough --print-xml",
			params.Name, ramMB, vcpuArg, params.MachineType, destDiskPath, format,
		)
		result := utils.ExecCommandLongRunning("bash", "-c", installCmd)
		if result.Error != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, fmt.Errorf("生成虚拟机 XML 失败: %s", result.Stderr)
		}

		// 注入 memballoon 配置
		enableFPR := initType != "windows" && initType != "other"
		vmXML := injectMemballoonConfig(result.Stdout, enableFPR)
		if memoryMeta != nil {
			vmXML, err = ApplyMemoryMetadataToDomainXML(vmXML, memoryMeta, enableFPR)
			if err != nil {
				utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
				return nil, err
			}
		}
		vmXML, err = ApplyRTCConfigToDomainXML(vmXML, params.RTCOffset, params.RTCStartDate, initType)
		if err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
		return nil, err
	}
	vmXML, err = ApplyVMGuestAgentConfigToDomainXML(vmXML, params.GuestAgent)
		if err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
		}
		vmXML, err = ApplySMBIOS1ConfigToDomainXML(vmXML, params.SMBIOS1, true)
		if err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
		}
		vmXML, err = ApplyVMAPICToDomainXML(vmXML, params.APIC)
		if err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
		}
		vmXML, err = ApplyVMPAEToDomainXML(vmXML, params.PAE)
		if err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
		}
		vmXML = ApplyVMVideoModelToDomainXML(vmXML, params.VideoModel, initType)
		topoVCPU := EffectiveTopologyVCPU(params.VCPU, params.MaxVCPU)
		vmXML = ApplyCPUTopologyModeToDomainXML(vmXML, params.CPUTopologyMode, initType, topoVCPU)
		vmXML = ApplyVMCPULimitToDomainXML(vmXML, params.VCPU, params.CPULimitPercent)
		if params.CPUAffinity != "" {
			var affErr error
			vmXML, affErr = ApplyCPUAffinityIfSet(vmXML, topoVCPU, params.CPUAffinity)
			if affErr != nil {
				utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
				return nil, affErr
			}
		}
		appliedBootType := ""
		if needUEFI {
			bootType := normalizedBootType
			if bootType == "" {
				bootType = VMBootTypeUEFI
			}
			vmXML, err = ApplyVMBootTypeToDomainXML(params.Name, vmXML, bootType)
			if err != nil {
				utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
				return nil, err
			}
			appliedBootType = bootType
		}
		vmXML, err = ApplyVPCSwitchToDomainXML(vmXML, params.SwitchID)
		if err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
		}
		if err := ensureVMUEFINVRAMFile(params.Name, vmXML, appliedBootType); err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
		}

		// 写入临时文件并定义虚拟机
		xmlPath := fmt.Sprintf("/tmp/_vm-import-%s.xml", params.Name)
		utils.ExecShell(fmt.Sprintf("cat > %s << 'XMLEOF'\n%s\nXMLEOF", utils.ShellSingleQuote(xmlPath), vmXML))

		defineResult := utils.ExecCommand("virsh", "define", xmlPath)
		utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(xmlPath)))
		if defineResult.Error != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, fmt.Errorf("定义虚拟机失败: %s", defineResult.Stderr)
		}
		if memoryMeta != nil {
			if err := writeVMMemoryMetadata(params.Name, memoryMeta); err != nil {
				utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
				return nil, err
			}
		}
		if err := SetVMRemark(params.Name, params.Remark); err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
		}

		if err := SetVMFreeze(params.Name, params.Freeze); err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
		}

		if params.StartAfterImport {
			if err := StartVM(params.Name); err != nil {
				return nil, err
			}
		}
	}

	// 检查取消
	if err := checkCanceled(ctx, params.Name, destDiskPath); err != nil {
		return nil, err
	}

	progressFn(50, "虚拟机创建成功...")

	// 开机自启
	if params.Autostart {
		utils.ExecCommand("virsh", "autostart", params.Name)
	}

	// 修复重启变关机
	FixOnReboot(params.Name)

	// 获取 VM 的 MAC 地址，清理宿主机侧旧的 DHCP 租约
	// 防止导入的磁盘中 machine-id 不同导致同一 MAC 出现多条租约
	cleanImportDHCPLeases(params.Name, params.Network)

	// 根据初始化类型执行初始化（仅当启动了虚拟机时）
	ip := ""
	if params.StartAfterImport {
		if initType == "linux" {
			// 检查取消
			if err := checkCanceled(ctx, params.Name, destDiskPath); err != nil {
				return nil, err
			}

			progressFn(60, "等待虚拟机启动...")

			// 等待获取 IP
			time.Sleep(5 * time.Second)
			ip = waitForIPWithContext(ctx, params.Name, linuxCloneIPWaitSeconds)

			if ip == "" {
				return nil, fmt.Errorf("未获取到虚拟机 IP，Linux 初始化无法执行")
			}
			progressFn(70, "SSH 初始化中...")

			// 构造 CloneParams 复用 initLinuxClone
			cloneParams := &CloneParams{
				Name:             params.Name,
				Hostname:         params.Hostname,
				User:             params.User,
				Password:         params.Password,
				TemplateRootPass: params.TemplateRootPass,
				TemplateUser:     params.TemplateUser,
			}
			if err := initLinuxClone(cloneParams, ip, progressFn); err != nil {
				return nil, err
			}

			// 等待网络刷新获取新 IP
			progressFn(96, "等待虚拟机网络刷新...")
			oldIP := ip
			time.Sleep(15 * time.Second)
			newIP := getVMIP(params.Name, true)
			if newIP != "" && newIP != oldIP {
				ip = newIP
			}
		} else if initType == "" || initType == "other" {
			// 不做初始化，仅等待一会获取 IP
			time.Sleep(5 * time.Second)
			ip = getVMIP(params.Name, true)
		}
	}

	progressFn(100, "导入完成")

	return &ImportVMResult{
		VMName:   params.Name,
		DiskPath: destDiskPath,
		IP:       ip,
	}, nil
}

// ImportDiskByPathParams 管理员通过绝对路径导入磁盘创建虚拟机参数
type ImportDiskByPathParams struct {
	Name             string                  `json:"name"`
	Remark           string                  `json:"remark,omitempty"`
	DiskPath         string                  `json:"disk_path"` // 磁盘绝对路径（主磁盘）
	DiskFile         string                  `json:"disk_file,omitempty"` // 存储文件名（主磁盘 storage 模式）
	DiskSourceType   string                  `json:"disk_source_type,omitempty"` // path/storage（主磁盘）
	StoragePoolID    string                  `json:"storage_pool_id,omitempty"`
	VCPU             int                     `json:"vcpu"`
	MaxVCPU          int                     `json:"max_vcpu,omitempty"`           // CPU 热添加上限
	RAM              int                     `json:"ram"`
	InitType         string                  `json:"init_type,omitempty"`
	Hostname         string                  `json:"hostname,omitempty"`
	User             string                  `json:"user,omitempty"`
	Password         string                  `json:"password,omitempty"`
	Autostart        bool                    `json:"autostart,omitempty"`
	Freeze           bool                    `json:"freeze,omitempty"`
	APIC             *bool                   `json:"apic,omitempty"`
	PAE              *bool                   `json:"pae,omitempty"`
	RTCOffset        string                  `json:"rtc_offset,omitempty"`
	RTCStartDate     string                  `json:"rtc_startdate,omitempty"`
	GuestAgent       *VMGuestAgentConfig     `json:"guest_agent,omitempty"`
	SMBIOS1          *VMSMBIOS1Config        `json:"smbios1,omitempty"`
	BootType         string                  `json:"boot_type,omitempty"`
	MachineType      string                  `json:"machine_type,omitempty"`
	NicModel         string                  `json:"nic_model,omitempty"`
	VideoModel       string                  `json:"video_model,omitempty"`
	CPUTopologyMode  string                  `json:"cpu_topology_mode,omitempty"`
	CPULimitPercent  int                     `json:"cpu_limit_percent,omitempty"`
	CPUAffinity      string                  `json:"cpu_affinity,omitempty"`       // CPU 亲和性，如 "0,2,4"
	TemplateRootPass string                  `json:"template_root_pass,omitempty"`
	TemplateUser     string                  `json:"template_user,omitempty"`
	MemoryDynamic    *VMMemoryDynamicRequest `json:"memory_dynamic,omitempty"`
	SwitchID         uint                    `json:"switch_id,omitempty"`
	SecurityGroupID  uint                    `json:"security_group_id,omitempty"`
	ExtraNics        []AddVMInterfaceRequest `json:"extra_nics,omitempty"`
	CopyDisk         bool                    `json:"copy_disk,omitempty"`
	ExtraImportDisks []ExtraImportDiskEntry  `json:"extra_import_disks,omitempty"` // 额外导入磁盘列表
	Username         string                  `json:"username,omitempty"` // 所属用户（存储模式需要）
	SystemDiskIOPS   *DiskIOPSTune           `json:"system_disk_iops,omitempty"` // 系统盘 IOPS 限制
	StartAfterImport bool                    `json:"start_after_import"` // 导入完成后是否开启虚拟机，默认 true
}

// ExtraImportDiskEntry 额外导入磁盘条目
type ExtraImportDiskEntry struct {
	DiskPath       string `json:"disk_path,omitempty"`
	DiskFile       string `json:"disk_file,omitempty"`
	DiskSourceType string `json:"disk_source_type,omitempty"` // path/storage
	StoragePoolID  string `json:"storage_pool_id,omitempty"`
	CopyDisk       bool   `json:"copy_disk,omitempty"`
	Bus            string `json:"bus,omitempty"` // virtio/scsi/sata/ide
	IOPSTotal      int    `json:"iops_total,omitempty"`  // 总 IOPS 限制
	IOPSRead       int    `json:"iops_read,omitempty"`   // 读 IOPS 限制
	IOPSWrite      int    `json:"iops_write,omitempty"`  // 写 IOPS 限制
}

// ParseImportDiskByPathParams 从 JSON 解析导入磁盘参数
func ParseImportDiskByPathParams(jsonStr string) (*ImportDiskByPathParams, error) {
	var params ImportDiskByPathParams
	if err := json.Unmarshal([]byte(jsonStr), &params); err != nil {
		return nil, err
	}
	// 向后兼容：旧任务 JSON 中无 start_after_import 字段时，默认开启虚拟机
	if !strings.Contains(jsonStr, `"start_after_import"`) {
		params.StartAfterImport = true
	}
	return &params, nil
}

// ImportDiskByPath 管理员通过绝对路径导入磁盘创建虚拟机（自动转换非qcow2格式）
func ImportDiskByPath(ctx context.Context, params *ImportDiskByPathParams, progressFn func(int, string)) (*ImportVMResult, error) {
	if err := ValidateVMName(params.Name); err != nil {
		return nil, err
	}

	// 默认值设置
	if params.MachineType == "" {
		params.MachineType = "q35"
	}
	if params.BootType == "" {
		params.BootType = "bios"
	}
	if params.NicModel == "" {
		params.NicModel = "virtio"
	}
	if params.Hostname == "" {
		params.Hostname = params.Name
	}
	if err := ValidateVMCPULimitPercent(params.CPULimitPercent); err != nil {
		return nil, err
	}
	params.CPUTopologyMode = NormalizeVMCPUTopologyMode(params.CPUTopologyMode)

	// 检查虚拟机是否已存在
	checkVM := utils.ExecCommand("virsh", "dominfo", params.Name)
	if checkVM.ExitCode == 0 {
		return nil, fmt.Errorf("虚拟机 '%s' 已存在", params.Name)
	}

	// 解析主磁盘源路径（支持绝对路径和存储模式）
	var mainDiskSrc string
	if params.DiskSourceType == "storage" || (params.DiskPath == "" && params.DiskFile != "") {
		if params.DiskFile == "" {
			return nil, fmt.Errorf("存储模式下磁盘文件名为必填")
		}
		if params.Username == "" {
			return nil, fmt.Errorf("存储模式下需要提供用户名")
		}
		mainDiskSrc = filepath.Join(GetUserDiskDir(params.Username), params.DiskFile)
	} else {
		mainDiskSrc = params.DiskPath
	}

	if mainDiskSrc == "" {
		return nil, fmt.Errorf("磁盘路径不能为空")
	}
	if !filepath.IsAbs(mainDiskSrc) {
		return nil, fmt.Errorf("磁盘路径必须是绝对路径: %s", mainDiskSrc)
	}
	checkResult := utils.ExecShell(fmt.Sprintf("test -f %s && echo ok", utils.ShellSingleQuote(mainDiskSrc)))
	if strings.TrimSpace(checkResult.Stdout) != "ok" {
		return nil, fmt.Errorf("磁盘文件不存在: %s", mainDiskSrc)
	}

	if err := EnsureOVSNetworkReady(); err != nil {
		return nil, err
	}

	// 解析存储位置
	targetDir, resolvedPoolID, err := ResolveVMStorageDir(params.StoragePoolID, true)
	if err != nil {
		return nil, err
	}
	params.StoragePoolID = resolvedPoolID

	progressFn(10, "检测磁盘格式...")

	// 检测源磁盘格式
	srcFormat := "qcow2"
	infoResult := utils.ExecShell(fmt.Sprintf("qemu-img info -U --output=json %s 2>/dev/null", utils.ShellSingleQuote(mainDiskSrc)))
	if infoResult.Error == nil {
		detected := parseQemuInfoStr(infoResult.Stdout, "format")
		if detected != "" {
			srcFormat = detected
		}
	}

	// 目标磁盘路径（始终使用 qcow2 格式）
	destDiskPath := filepath.Join(targetDir, fmt.Sprintf("%s.qcow2", params.Name))

	// 检查取消
	select {
	case <-ctx.Done():
		return nil, taskqueue.ErrTaskCanceled
	default:
	}

	needsConversion := srcFormat != "qcow2"

	if needsConversion {
		// 非 qcow2 格式，使用 qemu-img convert 转换
		progressFn(12, fmt.Sprintf("检测到 %s 格式，正在转换为 qcow2（此过程可能需要较长时间）...", srcFormat))
		convertCmd := fmt.Sprintf("qemu-img convert -f '%s' -O qcow2 '%s' '%s'",
			srcFormat, mainDiskSrc, destDiskPath)
		convertResult := utils.ExecCommandLongRunning("bash", "-c", convertCmd)
		if convertResult.Error != nil {
			return nil, fmt.Errorf("磁盘格式转换失败: %s", convertResult.Stderr)
		}
		// 如果不保留原磁盘，删除源文件
		if !params.CopyDisk {
			progressFn(18, "正在删除原磁盘文件...")
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(mainDiskSrc)))
		}
		progressFn(20, "磁盘格式转换完成")
	} else {
		// 已经是 qcow2 格式，按是否保留原磁盘处理
		if params.CopyDisk {
			progressFn(12, "检测到 qcow2 格式，正在复制磁盘文件到目标存储位置（保留原文件）...")
			cpResult := utils.ExecCommandLongRunning("cp", "--sparse=always", mainDiskSrc, destDiskPath)
			if cpResult.Error != nil {
				return nil, fmt.Errorf("复制磁盘文件失败: %s", cpResult.Stderr)
			}
			progressFn(20, "磁盘文件复制完成")
		} else {
			progressFn(12, "检测到 qcow2 格式，正在移动磁盘文件到目标存储位置（不保留原文件）...")
			moveResult := utils.ExecCommandLongRunning("mv", mainDiskSrc, destDiskPath)
			if moveResult.Error != nil {
				// 跨设备移动失败，回退到 cp + rm
				log.Printf("mv 失败（可能跨设备），尝试 cp + rm: %s", moveResult.Stderr)
				progressFn(15, "跨设备复制磁盘文件到目标存储位置...")
				cpResult := utils.ExecCommandLongRunning("cp", "--sparse=always", mainDiskSrc, destDiskPath)
				if cpResult.Error != nil {
					return nil, fmt.Errorf("移动磁盘文件失败: %s", cpResult.Stderr)
				}
				utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(mainDiskSrc)))
			}
			progressFn(20, "磁盘文件移动完成")
		}
	}

	// 设置权限
	utils.ExecCommand("chown", "libvirt-qemu:kvm", destDiskPath)

	// 检查取消
	select {
	case <-ctx.Done():
		utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
		return nil, taskqueue.ErrTaskCanceled
	default:
	}

	progressFn(30, "创建虚拟机定义...")

	memoryMeta, ramMB, _, err := BuildVMMemoryMetadataForCreate(params.RAM, params.MemoryDynamic)
	if err != nil {
		utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
		return nil, err
	}

	// 检测是否为 UEFI 磁盘
	normalizedBootType := NormalizeVMBootType(params.BootType)
	needUEFI := false
	if normalizedBootType == VMBootTypeUEFI || normalizedBootType == VMBootTypeUEFISecure {
		needUEFI = true
	} else if params.BootType == "" || params.BootType == "bios" {
		efiCheck := utils.ExecShell(fmt.Sprintf(
			"virt-filesystems -a '%s' --filesystems --long 2>/dev/null | head -5 | grep -q 'vfat' && echo 'uefi'",
			destDiskPath))
		if efiCheck.Error == nil && strings.TrimSpace(efiCheck.Stdout) == "uefi" {
			needUEFI = true
		}
	}

	initType := strings.ToLower(params.InitType)
	isWindows := initType == "windows"
	format := "qcow2" // 目标格式始终是 qcow2

	if isWindows {
		// ===== Windows 导入 =====
		progressFn(35, "创建 Windows 虚拟机...")

		macResult := utils.ExecShell(`printf '52:54:00:%02x:%02x:%02x' $((RANDOM%256)) $((RANDOM%256)) $((RANDOM%256))`)
		macAddr := strings.TrimSpace(macResult.Stdout)
		if macAddr == "" {
			macAddr = "52:54:00:aa:bb:cc"
		}

		nvramClone := fmt.Sprintf("/var/lib/libvirt/qemu/nvram/%s_VARS.fd", params.Name)
		if err := createQCOW2NVRAMFromTemplate("/usr/share/OVMF/OVMF_VARS_4M.ms.fd", nvramClone); err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
		}

		ramKiB := ramMB * 1024

		rtcOffset := ResolveRTCOffset(params.RTCOffset, "windows")
		rtcStartDate := NormalizeRTCStartDate(params.RTCStartDate)
		clockOpenTag := fmt.Sprintf("<clock offset='%s'>", rtcOffset)
		if rtcStartDate != VMRTCStartDateNow {
			epoch, err := ParseRTCStartDateToEpoch(rtcStartDate)
			if err != nil {
				utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
				return nil, err
			}
			rtcOffset = VMRTCOffsetAbsolute
			clockOpenTag = fmt.Sprintf("<clock offset='%s' start='%s'>", rtcOffset, epoch)
		}
		vmXML := fmt.Sprintf(`<domain type='kvm'>
  <name>%s</name>
  <memory unit='KiB'>%d</memory>
%s
  <os firmware='efi'>
    <type arch='x86_64' machine='pc-q35-noble'>hvm</type>
    <firmware>
      <feature enabled='yes' name='enrolled-keys'/>
      <feature enabled='yes' name='secure-boot'/>
    </firmware>
    <loader readonly='yes' secure='yes' type='pflash'>/usr/share/OVMF/OVMF_CODE_4M.ms.fd</loader>
    <nvram template='/usr/share/OVMF/OVMF_VARS_4M.ms.fd' templateFormat='raw' format='qcow2'>%s</nvram>
    <boot dev='hd'/>
  </os>
  <features>
    <acpi/><apic/>
    <hyperv mode='custom'>
      <relaxed state='on'/><vapic state='on'/><spinlocks state='on' retries='8191'/>
    </hyperv>
    <vmport state='off'/><smm state='on'/>
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
      <driver name='qemu' type='%s' discard='unmap' detect_zeroes='unmap'/>
      <source file='%s'/><target dev='vda' bus='virtio'/>
    </disk>
    <controller type='usb' index='0' model='qemu-xhci' ports='15'/>
    <controller type='virtio-serial' index='0'/>
%s
    <input type='tablet' bus='usb'/>
    <tpm model='tpm-crb'><backend type='emulator' version='2.0'/></tpm>
    <graphics type='vnc' port='-1' autoport='yes' listen='0.0.0.0'>
      <listen type='address' address='0.0.0.0'/>
    </graphics>
    <video><model type='virtio' heads='1' primary='yes'/></video>
    <watchdog model='itco' action='reset'/>
    <memballoon model='virtio' freePageReporting='on'><stats period='5'/></memballoon>
  </devices>
</domain>`,
			params.Name, ramKiB, BuildVCPUTag(params.VCPU, params.MaxVCPU), nvramClone, clockOpenTag, format, destDiskPath, BuildOVSInterfaceXML(macAddr, params.NicModel))
		if memoryMeta != nil {
			vmXML, err = ApplyMemoryMetadataToDomainXML(vmXML, memoryMeta, false)
			if err != nil {
				utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
				return nil, err
			}
		}
		vmXML, err = ApplyVMGuestAgentConfigToDomainXML(vmXML, params.GuestAgent)
		if err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
		}
		vmXML, err = ApplySMBIOS1ConfigToDomainXML(vmXML, params.SMBIOS1, true)
		if err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
		}
		vmXML, err = ApplyVMAPICToDomainXML(vmXML, params.APIC)
		if err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
		}
		vmXML, err = ApplyVMPAEToDomainXML(vmXML, params.PAE)
		if err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
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
				utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
				return nil, affErr
			}
		}
		vmXML, err = ApplyVPCSwitchToDomainXML(vmXML, params.SwitchID)
		if err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
		}

		xmlPath := fmt.Sprintf("/tmp/_vm-importd-%s.xml", params.Name)
		utils.ExecShell(fmt.Sprintf("cat > %s << 'XMLEOF'\n%s\nXMLEOF", utils.ShellSingleQuote(xmlPath), vmXML))

		defineResult := utils.ExecCommand("virsh", "define", xmlPath)
		utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(xmlPath)))
		if defineResult.Error != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, fmt.Errorf("定义虚拟机失败: %s", defineResult.Stderr)
		}
		if memoryMeta != nil {
			if err := writeVMMemoryMetadata(params.Name, memoryMeta); err != nil {
				utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
				return nil, err
			}
		}
		if err := SetVMRemark(params.Name, params.Remark); err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
		}
		if err := SetVMFreeze(params.Name, params.Freeze); err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
		}
		if params.StartAfterImport {
			if err := StartVM(params.Name); err != nil {
				return nil, err
			}
		}
	} else {
		// ===== Linux / Other 导入 =====
		bootOpt := ""
		if needUEFI {
			bootOpt = "--boot uefi "
		}

		vcpuArg := fmt.Sprintf("--vcpus %d", params.VCPU)
		if params.MaxVCPU > params.VCPU {
			vcpuArg = fmt.Sprintf("--vcpus %d,maxvcpus=%d", params.VCPU, params.MaxVCPU)
		}

		installCmd := fmt.Sprintf(
			"virt-install --name '%s' --ram %d %s "+
				"--machine %s "+
				bootOpt+
				"--disk '%s,format=%s,bus=virtio,discard=unmap,detect_zeroes=unmap' "+
				"--osinfo detect=on,require=off "+
				BuildOVSVirtInstallNetworkArg(params.NicModel)+" "+
				"--graphics vnc,listen=0.0.0.0 "+
				"--video virtio "+
				"--import --cpu host-passthrough --virt-type kvm --print-xml",
			params.Name, ramMB, vcpuArg, params.MachineType, destDiskPath, format,
		)
		result := utils.ExecCommandLongRunning("bash", "-c", installCmd)
		if result.Error != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, fmt.Errorf("生成虚拟机 XML 失败: %s", result.Stderr)
		}

		xmlOutput := result.Stdout
		if idx := strings.Index(xmlOutput, "</domain>"); idx != -1 {
			xmlOutput = xmlOutput[:idx+len("</domain>")]
		}
		if idx := strings.Index(xmlOutput, "<domain"); idx > 0 {
			xmlOutput = xmlOutput[idx:]
		}

		enableFPR := initType != "windows" && initType != "other"
		vmXML := injectMemballoonConfig(xmlOutput, enableFPR)
		if memoryMeta != nil {
			vmXML, err = ApplyMemoryMetadataToDomainXML(vmXML, memoryMeta, enableFPR)
			if err != nil {
				utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
				return nil, err
			}
		}
		vmXML, err = ApplyRTCConfigToDomainXML(vmXML, params.RTCOffset, params.RTCStartDate, initType)
		if err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
		}
		vmXML, err = ApplyVMGuestAgentConfigToDomainXML(vmXML, params.GuestAgent)
		if err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
		}
		vmXML, err = ApplySMBIOS1ConfigToDomainXML(vmXML, params.SMBIOS1, true)
		if err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
		}
		vmXML, err = ApplyVMAPICToDomainXML(vmXML, params.APIC)
		if err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
		}
		vmXML, err = ApplyVMPAEToDomainXML(vmXML, params.PAE)
		if err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
		}
		vmXML = ApplyVMVideoModelToDomainXML(vmXML, params.VideoModel, initType)
		topoVCPU := EffectiveTopologyVCPU(params.VCPU, params.MaxVCPU)
		vmXML = ApplyCPUTopologyModeToDomainXML(vmXML, params.CPUTopologyMode, initType, topoVCPU)
		vmXML = ApplyVMCPULimitToDomainXML(vmXML, params.VCPU, params.CPULimitPercent)
		if params.CPUAffinity != "" {
			var affErr error
			vmXML, affErr = ApplyCPUAffinityIfSet(vmXML, topoVCPU, params.CPUAffinity)
			if affErr != nil {
				utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
				return nil, affErr
			}
		}
		appliedBootType := ""
		if needUEFI {
			bootType := normalizedBootType
			if bootType == "" {
				bootType = VMBootTypeUEFI
			}
			vmXML, err = ApplyVMBootTypeToDomainXML(params.Name, vmXML, bootType)
			if err != nil {
				utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
				return nil, err
			}
			appliedBootType = bootType
		}
		vmXML, err = ApplyVPCSwitchToDomainXML(vmXML, params.SwitchID)
		if err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
		}
		if err := ensureVMUEFINVRAMFile(params.Name, vmXML, appliedBootType); err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
		}

		xmlPath := fmt.Sprintf("/tmp/_vm-importd-%s.xml", params.Name)
		utils.ExecShell(fmt.Sprintf("cat > %s << 'XMLEOF'\n%s\nXMLEOF", utils.ShellSingleQuote(xmlPath), vmXML))

		defineResult := utils.ExecCommand("virsh", "define", xmlPath)
		utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(xmlPath)))
		if defineResult.Error != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, fmt.Errorf("定义虚拟机失败: %s", defineResult.Stderr)
		}
		if memoryMeta != nil {
			if err := writeVMMemoryMetadata(params.Name, memoryMeta); err != nil {
				utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
				return nil, err
			}
		}
		if err := SetVMRemark(params.Name, params.Remark); err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
		}
		if err := SetVMFreeze(params.Name, params.Freeze); err != nil {
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
			return nil, err
		}
		if params.StartAfterImport {
			if err := StartVM(params.Name); err != nil {
				return nil, err
			}
		}
	}

	// 检查取消
	if err := checkCanceled(ctx, params.Name, destDiskPath); err != nil {
		return nil, err
	}

	progressFn(50, "虚拟机创建成功...")

	if params.Autostart {
		utils.ExecCommand("virsh", "autostart", params.Name)
	}

	FixOnReboot(params.Name)

	// 获取网络名称用于清理DHCP租约
	network := config.GlobalConfig.DefaultNetwork
	cleanImportDHCPLeases(params.Name, network)

	// 根据初始化类型执行初始化（仅当启动了虚拟机时）
	ip := ""
	if params.StartAfterImport {
		if initType == "linux" {
			if err := checkCanceled(ctx, params.Name, destDiskPath); err != nil {
				return nil, err
			}

			progressFn(60, "等待虚拟机启动...")
			time.Sleep(5 * time.Second)
			ip = waitForIPWithContext(ctx, params.Name, linuxCloneIPWaitSeconds)

			if ip == "" {
				return nil, fmt.Errorf("未获取到虚拟机 IP，Linux 初始化无法执行")
			}
			progressFn(70, "SSH 初始化中...")

			cloneParams := &CloneParams{
				Name:             params.Name,
				Hostname:         params.Hostname,
				User:             params.User,
				Password:         params.Password,
				TemplateRootPass: params.TemplateRootPass,
				TemplateUser:     params.TemplateUser,
			}
			if err := initLinuxClone(cloneParams, ip, progressFn); err != nil {
				return nil, err
			}

			progressFn(96, "等待虚拟机网络刷新...")
			oldIP := ip
			time.Sleep(15 * time.Second)
			newIP := getVMIP(params.Name, true)
			if newIP != "" && newIP != oldIP {
				ip = newIP
			}
		} else if initType == "" || initType == "other" {
			time.Sleep(5 * time.Second)
			ip = getVMIP(params.Name, true)
		}
	}

	// 处理额外导入磁盘：逐个挂载到已创建的虚拟机
	if len(params.ExtraImportDisks) > 0 {
		progressFn(90, fmt.Sprintf("正在导入 %d 块额外磁盘...", len(params.ExtraImportDisks)))
		for i, extra := range params.ExtraImportDisks {
			select {
			case <-ctx.Done():
				return nil, taskqueue.ErrTaskCanceled
			default:
			}
			subProgressFn := func(p int, msg string) {
				progressFn(90+p/(len(params.ExtraImportDisks)*10)*(len(params.ExtraImportDisks)-i), fmt.Sprintf("[额外磁盘 %d/%d] %s", i+1, len(params.ExtraImportDisks), msg))
			}
			bus := extra.Bus
			if bus == "" {
				bus = "virtio"
			}
			if _, err := importSingleDiskToVM(ctx, params.Name, &ExtraImportDiskEntry{
				DiskPath:       extra.DiskPath,
				DiskFile:       extra.DiskFile,
				DiskSourceType: extra.DiskSourceType,
				StoragePoolID:  extra.StoragePoolID,
				CopyDisk:       extra.CopyDisk,
				Bus:            bus,
			}, params.Username, subProgressFn); err != nil {
				progressFn(95, fmt.Sprintf("额外磁盘 %d 导入失败: %s", i+1, err.Error()))
				continue
			}
		}
	}

	progressFn(100, "导入完成")

	return &ImportVMResult{
		VMName:   params.Name,
		DiskPath: destDiskPath,
		IP:       ip,
	}, nil
}

// cleanImportDHCPLeases 清理导入VM对应MAC地址的宿主机侧旧DHCP租约
// 避免磁盘中旧的 machine-id 导致同一 MAC 出现多条不同 client-id 的租约
func cleanImportDHCPLeases(vmName, network string) {
	// 获取 VM 的 MAC 地址
	mac := getFirstVMMAC(vmName)
	if mac == "" {
		return
	}

	CleanOVSDHCPLease(mac, "")
	ReloadOVSDNSMasq()

	log.Printf("[Import] 已清理 MAC %s 的旧 OVS DHCP 租约", mac)
}

// resolveImportDiskSource 解析导入磁盘的源路径（支持绝对路径和存储模式）
func resolveImportDiskSource(entry *ExtraImportDiskEntry, username string) (string, error) {
	if entry.DiskSourceType == "path" || (entry.DiskSourceType == "" && entry.DiskPath != "") {
		if !filepath.IsAbs(entry.DiskPath) {
			return "", fmt.Errorf("磁盘路径必须是绝对路径: %s", entry.DiskPath)
		}
		return entry.DiskPath, nil
	}
	// storage 模式
	if entry.DiskFile == "" {
		return "", fmt.Errorf("存储模式下磁盘文件名为必填")
	}
	if username == "" {
		return "", fmt.Errorf("存储模式下需要提供用户名")
	}
	srcDiskPath := filepath.Join(GetUserDiskDir(username), entry.DiskFile)
	return srcDiskPath, nil
}

// importSingleDiskToVM 将单块磁盘导入并挂载到已有虚拟机（供 ImportDiskByPath 和 ImportDiskForExistingVM 复用）
func importSingleDiskToVM(ctx context.Context, vmName string, entry *ExtraImportDiskEntry, username string, progressFn func(int, string)) (string, error) {
	// 解析源路径
	srcDiskPath, err := resolveImportDiskSource(entry, username)
	if err != nil {
		return "", err
	}

	// 检查源文件
	checkResult := utils.ExecShell(fmt.Sprintf("test -f %s && echo ok", utils.ShellSingleQuote(srcDiskPath)))
	if strings.TrimSpace(checkResult.Stdout) != "ok" {
		return "", fmt.Errorf("磁盘文件不存在: %s", srcDiskPath)
	}

	// 解析目标存储位置
	targetDir := config.GlobalConfig.CloneDir
	if entry.StoragePoolID != "" {
		resolvedDir, _, err := ResolveVMStorageDir(entry.StoragePoolID, true)
		if err != nil {
			return "", err
		}
		targetDir = resolvedDir
	}
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return "", fmt.Errorf("创建目标目录失败: %w", err)
	}

	// 检测格式
	progressFn(5, "检测磁盘格式...")
	srcFormat := "qcow2"
	infoResult := utils.ExecShell(fmt.Sprintf("qemu-img info -U --output=json %s 2>/dev/null", utils.ShellSingleQuote(srcDiskPath)))
	if infoResult.Error == nil {
		detected := parseQemuInfoStr(infoResult.Stdout, "format")
		if detected != "" {
			srcFormat = detected
		}
	}

	// 查找可用设备名
	existingDisks, _ := ListDisks(vmName)
	devPrefix := getDevPrefix(entry.Bus)
	if devPrefix == "" {
		devPrefix = "vd"
	}
	usedDevs := make(map[string]bool)
	for _, d := range existingDisks {
		usedDevs[d.Device] = true
	}
	nextDev := ""
	for _, letter := range "bcdefghijklmnop" {
		dev := devPrefix + string(letter)
		if !usedDevs[dev] {
			nextDev = dev
			break
		}
	}
	if nextDev == "" {
		return "", fmt.Errorf("没有可用的设备名")
	}

	// 目标路径
	ts := time.Now().Format("20060102150405")
	destDiskPath := filepath.Join(targetDir, fmt.Sprintf("%s-%s-%s.qcow2", vmName, nextDev, ts))

	// 复制/转换/移动
	select {
	case <-ctx.Done():
		return "", taskqueue.ErrTaskCanceled
	default:
	}

	needsConversion := srcFormat != "qcow2"
	if needsConversion {
		progressFn(10, fmt.Sprintf("检测到 %s 格式，正在转换为 qcow2...", srcFormat))
		convertCmd := fmt.Sprintf("qemu-img convert -f '%s' -O qcow2 '%s' '%s'", srcFormat, srcDiskPath, destDiskPath)
		convertResult := utils.ExecCommandLongRunning("bash", "-c", convertCmd)
		if convertResult.Error != nil {
			return "", fmt.Errorf("磁盘格式转换失败: %s", convertResult.Stderr)
		}
		if !entry.CopyDisk {
			progressFn(18, "正在删除原磁盘文件...")
			utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(srcDiskPath)))
		}
	} else {
		if entry.CopyDisk {
			progressFn(10, "正在复制磁盘文件（保留原文件）...")
			cpResult := utils.ExecCommandLongRunning("cp", "--sparse=always", srcDiskPath, destDiskPath)
			if cpResult.Error != nil {
				return "", fmt.Errorf("复制磁盘文件失败: %s", cpResult.Stderr)
			}
		} else {
			progressFn(10, "正在移动磁盘文件（不保留原文件）...")
			moveResult := utils.ExecCommandLongRunning("mv", srcDiskPath, destDiskPath)
			if moveResult.Error != nil {
				log.Printf("mv 失败（可能跨设备），尝试 cp + rm: %s", moveResult.Stderr)
				cpResult := utils.ExecCommandLongRunning("cp", "--sparse=always", srcDiskPath, destDiskPath)
				if cpResult.Error != nil {
					return "", fmt.Errorf("移动磁盘文件失败: %s", cpResult.Stderr)
				}
				utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(srcDiskPath)))
			}
		}
	}

	utils.ExecCommand("chown", "libvirt-qemu:kvm", destDiskPath)

	progressFn(80, "挂载磁盘到虚拟机...")
	if _, attachErr := AttachExistingDisk(vmName, destDiskPath, entry.Bus); attachErr != nil {
		utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(destDiskPath)))
		return "", fmt.Errorf("挂载磁盘失败: %w", attachErr)
	}

	progressFn(100, "磁盘导入完成")
	return nextDev, nil
}

// ImportDiskForExistingVMParams 为已有虚拟机导入磁盘参数
type ImportDiskForExistingVMParams struct {
	VMName         string `json:"vm_name"`
	DiskPath       string `json:"disk_path,omitempty"`
	DiskFile       string `json:"disk_file,omitempty"`
	DiskSourceType string `json:"disk_source_type,omitempty"`
	StoragePoolID  string `json:"storage_pool_id,omitempty"`
	CopyDisk       bool   `json:"copy_disk,omitempty"`
	Bus            string `json:"bus,omitempty"`
	Username       string `json:"username,omitempty"`
}

// ParseImportDiskForExistingVMParams 解析参数
func ParseImportDiskForExistingVMParams(jsonStr string) (*ImportDiskForExistingVMParams, error) {
	var params ImportDiskForExistingVMParams
	if err := json.Unmarshal([]byte(jsonStr), &params); err != nil {
		return nil, err
	}
	return &params, nil
}

// ImportDiskForExistingVM 为已有虚拟机通过绝对路径导入磁盘
func ImportDiskForExistingVM(ctx context.Context, params *ImportDiskForExistingVMParams, progressFn func(int, string)) (string, error) {
	if params.Bus == "" {
		params.Bus = "virtio"
	}
	return importSingleDiskToVM(ctx, params.VMName, &ExtraImportDiskEntry{
		DiskPath:       params.DiskPath,
		DiskFile:       params.DiskFile,
		DiskSourceType: params.DiskSourceType,
		StoragePoolID:  params.StoragePoolID,
		CopyDisk:       params.CopyDisk,
		Bus:            params.Bus,
	}, params.Username, progressFn)
}
