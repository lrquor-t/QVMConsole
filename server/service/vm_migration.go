package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"kvm_console/model"
	"kvm_console/utils"
)

const (
	MigrationModeCold                   = "cold"
	MigrationModeLive                   = "live"
	defaultMigrationCPUThrottlePercent  = 50
	liveMigrationDirtyRateBlockRatio    = 0.50
	liveMigrationDirtyRateThrottleRatio = 0.20
)

type VMMigrationRequest struct {
	NodeID                uint                         `json:"node_id"`
	Mode                  string                       `json:"mode"`
	PreviewID             string                       `json:"preview_id"`
	SkipPrecheck          bool                         `json:"skip_precheck"`
	TargetStoragePoolID   string                       `json:"target_storage_pool_id"`
	DiskStorageTargets    []MigrationDiskStorageTarget `json:"disk_storage_targets"`
	TargetSwitchID        uint                         `json:"target_switch_id"`
	TargetSecurityGroupID uint                         `json:"target_security_group_id"`
	EnableCPUThrottle     bool                         `json:"enable_cpu_throttle"`
	CPUThrottlePercent    int                          `json:"cpu_throttle_percent"`
}

type VMMigrationTaskParams struct {
	VMName                string                       `json:"vm_name"`
	NodeID                uint                         `json:"node_id"`
	Mode                  string                       `json:"mode"`
	PreviewID             string                       `json:"preview_id"`
	SkipPrecheck          bool                         `json:"skip_precheck"`
	TargetStoragePoolID   string                       `json:"target_storage_pool_id"`
	DiskStorageTargets    []MigrationDiskStorageTarget `json:"disk_storage_targets"`
	TargetSwitchID        uint                         `json:"target_switch_id"`
	TargetSecurityGroupID uint                         `json:"target_security_group_id"`
	EnableCPUThrottle     bool                         `json:"enable_cpu_throttle"`
	CPUThrottlePercent    int                          `json:"cpu_throttle_percent"`
}

type VMMigrationPreview struct {
	PreviewID             string                       `json:"preview_id,omitempty"`
	VMName                string                       `json:"vm_name"`
	Mode                  string                       `json:"mode"`
	Node                  HostNodeView                 `json:"node"`
	Owner                 string                       `json:"owner"`
	CloudType             string                       `json:"cloud_type"`
	TargetUserExists      bool                         `json:"target_user_exists"`
	WillCreateTargetUser  bool                         `json:"will_create_target_user"`
	IsLightweight         bool                         `json:"is_lightweight"`
	SourceState           string                       `json:"source_state"`
	Disks                 []MigrationDisk              `json:"disks"`
	BackingChecks         []MigrationBackingCheck      `json:"backing_checks"`
	SourceBinding         *model.VPCVMBinding          `json:"source_binding,omitempty"`
	TargetStoragePoolID   string                       `json:"target_storage_pool_id"`
	TargetStorageDir      string                       `json:"target_storage_dir"`
	DiskStorageTargets    []MigrationDiskStorageTarget `json:"disk_storage_targets"`
	TargetStorageTargets  []VMStorageTarget            `json:"target_storage_targets"`
	RequiredStorageBytes  int64                        `json:"required_storage_bytes"`
	TargetSwitches        []model.VPCSwitch            `json:"target_switches"`
	TargetSecurityGroups  []model.VPCSecurityGroup     `json:"target_security_groups"`
	TargetSwitchID        uint                         `json:"target_switch_id"`
	TargetSecurityGroupID uint                         `json:"target_security_group_id"`
	PortForwards          []MigrationPortForwardMap    `json:"port_forwards"`
	Credential            *VMCredentialInfo            `json:"credential,omitempty"`
	LiveAssessment        *MigrationLiveAssessment     `json:"live_assessment,omitempty"`
	Warnings              []string                     `json:"warnings"`
	Blockers              []string                     `json:"blockers"`
	Allowed               bool                         `json:"allowed"`
}

type MigrationDisk struct {
	Target              string `json:"target"`
	SourcePath          string `json:"source_path"`
	TargetPath          string `json:"target_path"`
	TargetStoragePoolID string `json:"target_storage_pool_id"`
	TargetStorageDir    string `json:"target_storage_dir"`
	BackingPath         string `json:"backing_path"`
	BackingFormat       string `json:"backing_format"`
	VirtualSize         int64  `json:"virtual_size"`
	ActualSize          int64  `json:"actual_size"`
	Format              string `json:"format"`
}

type MigrationDiskStorageTarget struct {
	Target              string `json:"target"`
	Device              string `json:"device,omitempty"`
	TargetStoragePoolID string `json:"target_storage_pool_id"`
}

type MigrationBackingCheck struct {
	Path              string `json:"path"`
	SourceFormat      string `json:"source_format"`
	TargetFormat      string `json:"target_format"`
	SourceVirtualSize int64  `json:"source_virtual_size"`
	TargetVirtualSize int64  `json:"target_virtual_size"`
	SourceSHA256      string `json:"source_sha256"`
	TargetSHA256      string `json:"target_sha256"`
	OK                bool   `json:"ok"`
	Message           string `json:"message"`
}

type MigrationPortForwardMap struct {
	Protocol       string `json:"protocol"`
	SourceHostPort string `json:"source_host_port"`
	TargetHostPort string `json:"target_host_port"`
	VMPort         string `json:"vm_port"`
	DestIP         string `json:"dest_ip"`
	AutoAllocated  bool   `json:"auto_allocated"`
}

type MigrationLiveAssessment struct {
	AverageBandwidthMiB   float64           `json:"average_bandwidth_mib"`
	SpeedTestSeconds      float64           `json:"speed_test_seconds"`
	DirtyRateMiB          float64           `json:"dirty_rate_mib"`
	DirtyRateRatio        float64           `json:"dirty_rate_ratio"`
	DirtyRateRatioPercent float64           `json:"dirty_rate_ratio_percent"`
	Allowed               bool              `json:"allowed"`
	RequiresCPUThrottle   bool              `json:"requires_cpu_throttle"`
	CPUThrottleEnabled    bool              `json:"cpu_throttle_enabled"`
	CPUThrottlePercent    int               `json:"cpu_throttle_percent"`
	KVMStatAvailable      bool              `json:"kvm_stat_available"`
	KVMPageFaultRate      float64           `json:"kvm_page_fault_rate"`
	KVMStatMessage        string            `json:"kvm_stat_message"`
	DirtyRateStats        map[string]string `json:"dirty_rate_stats,omitempty"`
	Dommemstat            map[string]int64  `json:"dommemstat,omitempty"`
	BlockReason           string            `json:"block_reason,omitempty"`
	Warnings              []string          `json:"warnings,omitempty"`
}

type MigrationAdoptRequest struct {
	VMName                string                     `json:"vm_name"`
	Owner                 string                     `json:"owner"`
	CloudType             string                     `json:"cloud_type"`
	TargetSwitchID        uint                       `json:"target_switch_id"`
	TargetSecurityGroupID uint                       `json:"target_security_group_id"`
	User                  MigrationUserSnapshot      `json:"user"`
	LightweightQuota      *LightweightVMQuotaRequest `json:"lightweight_quota,omitempty"`
	Credential            *VMCredentialInfo          `json:"credential,omitempty"`
	PortForwards          []MigrationPortForwardMap  `json:"port_forwards"`
}

type MigrationUserSnapshot struct {
	Username             string     `json:"username"`
	PasswordHash         string     `json:"password_hash,omitempty"`
	Email                string     `json:"email"`
	CloudType            string     `json:"cloud_type"`
	Status               string     `json:"status"`
	EmailVerifiedAt      *time.Time `json:"email_verified_at,omitempty"`
	LoginVerifiedUntil   *time.Time `json:"login_verified_until,omitempty"`
	SecurityUpdatedAt    *time.Time `json:"security_updated_at,omitempty"`
	MaxCPU               int        `json:"max_cpu"`
	MaxMemory            int        `json:"max_memory"`
	MaxDisk              int        `json:"max_disk"`
	MaxVM                int        `json:"max_vm"`
	MaxStorage           int        `json:"max_storage"`
	MaxRuntimeHours      int        `json:"max_runtime_hours"`
	EnablePortForward    bool       `json:"enable_port_forward"`
	MaxPortForwards      int        `json:"max_port_forwards"`
	MaxSnapshots         int        `json:"max_snapshots"`
	MaxBandwidthUp       float64    `json:"max_bandwidth_up"`
	MaxBandwidthDown     float64    `json:"max_bandwidth_down"`
	MaxTrafficDown       float64    `json:"max_traffic_down"`
	MaxTrafficUp         float64    `json:"max_traffic_up"`
	MaxPublicIPs         int        `json:"max_public_ips"`
	DedicatedVPCSwitchID uint       `json:"dedicated_vpc_switch_id"`
}

type MigrationAdoptResult struct {
	VMName       string                    `json:"vm_name"`
	Owner        string                    `json:"owner"`
	CreatedUser  bool                      `json:"created_user"`
	PortForwards []MigrationPortForwardMap `json:"port_forwards"`
	Warnings     []string                  `json:"warnings"`
}

type qemuImgInfo struct {
	Filename            string `json:"filename"`
	Format              string `json:"format"`
	VirtualSize         int64  `json:"virtual-size"`
	ActualSize          int64  `json:"actual-size"`
	BackingFilename     string `json:"backing-filename"`
	FullBackingFilename string `json:"full-backing-filename"`
}

type VMMigrationOptions struct {
	VMName                string                       `json:"vm_name"`
	SourceState           string                       `json:"source_state"`
	Mode                  string                       `json:"mode"`
	Owner                 string                       `json:"owner"`
	CloudType             string                       `json:"cloud_type"`
	IsLightweight         bool                         `json:"is_lightweight"`
	SourceBinding         *model.VPCVMBinding          `json:"source_binding,omitempty"`
	TargetUserExists      bool                         `json:"target_user_exists"`
	WillCreateTargetUser  bool                         `json:"will_create_target_user"`
	TargetStorageTargets  []VMStorageTarget            `json:"target_storage_targets"`
	DiskStorageTargets    []MigrationDiskStorageTarget `json:"disk_storage_targets"`
	TargetSwitches        []model.VPCSwitch            `json:"target_switches"`
	TargetSecurityGroups  []model.VPCSecurityGroup     `json:"target_security_groups"`
	TargetSwitchID        uint                         `json:"target_switch_id"`
	TargetSecurityGroupID uint                         `json:"target_security_group_id"`
}

type cachedMigrationPreview struct {
	Preview   VMMigrationPreview
	ExpiresAt time.Time
}

var migrationPreviewCache = struct {
	sync.Mutex
	items map[string]cachedMigrationPreview
}{items: map[string]cachedMigrationPreview{}}

func ParseVMMigrationTaskParams(raw string) (VMMigrationTaskParams, error) {
	var params VMMigrationTaskParams
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		return params, err
	}
	params.Mode = detectMigrationModeFromState(getDomainState(params.VMName))
	return params, nil
}

func GetVMMigrationOptions(vmName string, nodeID uint) (*VMMigrationOptions, error) {
	vmName = strings.TrimSpace(vmName)
	if vmName == "" {
		return nil, fmt.Errorf("虚拟机名称不能为空")
	}
	node, err := GetHostNode(nodeID)
	if err != nil {
		return nil, err
	}
	if !domainExists(vmName) {
		return nil, fmt.Errorf("源虚拟机不存在")
	}
	state := getDomainState(vmName)
	owner := FindVMOwner(vmName)
	if owner == "" {
		owner = "admin"
	}
	sourceUser := loadUserSnapshot(owner)
	cloudType := NormalizeCloudType(sourceUser.CloudType)
	isLightweight := IsLightweightCloudType(cloudType) || IsLightweightCloudVM(vmName)
	if isLightweight {
		cloudType = CloudTypeLightweight
	}
	options := &VMMigrationOptions{
		VMName:        vmName,
		SourceState:   state,
		Mode:          detectMigrationModeFromState(state),
		Owner:         owner,
		CloudType:     cloudType,
		IsLightweight: isLightweight,
	}
	options.TargetUserExists = targetUserExists(*node, owner)
	options.WillCreateTargetUser = owner != "admin" && !options.TargetUserExists
	options.TargetStorageTargets = fetchTargetStorageTargets(*node)
	_, sws, groups := fetchTargetNetworkOptions(*node)
	options.TargetSwitches, options.TargetSecurityGroups = filterTargetMigrationNetworks(owner, isLightweight, options.TargetUserExists, sws, groups)
	var binding model.VPCVMBinding
	if err := model.DB.Where("vm_name = ?", vmName).First(&binding).Error; err == nil {
		options.SourceBinding = &binding
		options.TargetSwitchID = matchTargetSwitch(binding, options.TargetSwitches, isLightweight)
		options.TargetSecurityGroupID = matchTargetSecurityGroup(binding, options.TargetSecurityGroups)
	}
	return options, nil
}

func CreateVMMigrationPreview(vmName string, req VMMigrationRequest) (*VMMigrationPreview, error) {
	preview, err := BuildVMMigrationPreview(vmName, req)
	if err != nil {
		return nil, err
	}
	if preview.Allowed {
		preview.PreviewID = storeMigrationPreview(*preview)
	}
	return preview, nil
}

func BuildVMMigrationPreview(vmName string, req VMMigrationRequest) (*VMMigrationPreview, error) {
	vmName = strings.TrimSpace(vmName)
	if vmName == "" {
		return nil, fmt.Errorf("虚拟机名称不能为空")
	}
	node, err := GetHostNode(req.NodeID)
	if err != nil {
		return nil, err
	}
	if !node.Enabled {
		return nil, fmt.Errorf("目标节点已禁用")
	}
	state := getDomainState(vmName)
	mode := detectMigrationModeFromState(state)
	owner := FindVMOwner(vmName)
	if owner == "" {
		owner = "admin"
	}
	sourceUser := loadUserSnapshot(owner)
	cloudType := NormalizeCloudType(sourceUser.CloudType)
	isLightweight := IsLightweightCloudType(cloudType) || IsLightweightCloudVM(vmName)
	if isLightweight {
		cloudType = CloudTypeLightweight
	}
	preview := &VMMigrationPreview{
		VMName:                vmName,
		Mode:                  mode,
		Node:                  buildHostNodeView(*node),
		Owner:                 owner,
		CloudType:             cloudType,
		IsLightweight:         isLightweight,
		SourceState:           state,
		TargetStoragePoolID:   strings.TrimSpace(req.TargetStoragePoolID),
		TargetSwitchID:        req.TargetSwitchID,
		TargetSecurityGroupID: req.TargetSecurityGroupID,
	}
	if !domainExists(vmName) {
		preview.Blockers = append(preview.Blockers, "源虚拟机不存在")
		return finishPreview(preview), nil
	}
	if mode == MigrationModeCold && !strings.Contains(strings.ToLower(state), "shut off") {
		preview.Blockers = append(preview.Blockers, "冷迁移要求源虚拟机先关机")
	}
	if mode == MigrationModeLive && !strings.Contains(strings.ToLower(state), "running") {
		preview.Blockers = append(preview.Blockers, "热迁移要求源虚拟机正在运行")
	}
	if out, err := remoteSSHCommand(context.Background(), *node, "virsh dominfo "+utils.ShellSingleQuote(vmName)+" >/dev/null 2>&1 && echo exists || echo missing", 30*time.Second); err == nil {
		if strings.TrimSpace(out) == "exists" {
			preview.Blockers = append(preview.Blockers, "目标节点已存在同名虚拟机")
		}
	}
	preview.TargetStorageTargets = fetchTargetStorageTargets(*node)
	targetStorage, ok := findTargetStorage(preview.TargetStorageTargets, req.TargetStoragePoolID)
	hasDiskStorageTargets := len(req.DiskStorageTargets) > 0
	if strings.TrimSpace(req.TargetStoragePoolID) == "" && !hasDiskStorageTargets {
		preview.Blockers = append(preview.Blockers, "请选择目标存储")
	} else if strings.TrimSpace(req.TargetStoragePoolID) != "" && !ok {
		preview.Blockers = append(preview.Blockers, "目标存储不存在或不可用于 VM")
	} else if ok {
		preview.TargetStoragePoolID = targetStorage.ID
		preview.TargetStorageDir = targetStorage.VMDir
	}
	disks, backingChecks, err := buildDiskAndBackingChecks(*node, vmName, "", req.SkipPrecheck)
	if err != nil {
		preview.Blockers = append(preview.Blockers, err.Error())
	} else {
		if diskBlockers := applyMigrationDiskStorageTargets(disks, preview.TargetStorageTargets, targetStorage, ok, req.DiskStorageTargets); len(diskBlockers) > 0 {
			preview.Blockers = append(preview.Blockers, diskBlockers...)
		}
		preview.DiskStorageTargets = buildResolvedMigrationDiskStorageTargets(disks)
		preview.Disks = disks
		preview.BackingChecks = backingChecks
		if req.SkipPrecheck {
			preview.Warnings = append(preview.Warnings, "已跳过完整预检：不会在提交前计算 backing hash，迁移任务执行失败时请按任务错误处理")
		}
		requiredByPool := map[string]int64{}
		seenTargetPath := map[string]bool{}
		for _, disk := range disks {
			preview.RequiredStorageBytes += disk.ActualSize
			if disk.TargetStoragePoolID != "" {
				requiredByPool[disk.TargetStoragePoolID] += disk.ActualSize
			}
			if disk.TargetPath != "" {
				if seenTargetPath[disk.TargetPath] {
					preview.Blockers = append(preview.Blockers, "多块磁盘目标路径重复: "+disk.TargetPath)
				}
				seenTargetPath[disk.TargetPath] = true
			}
			if diskTargetExists(*node, disk.TargetPath) {
				preview.Blockers = append(preview.Blockers, "目标磁盘已存在: "+disk.TargetPath)
			}
		}
		for poolID, requiredBytes := range requiredByPool {
			storage, storageOK := findTargetStorage(preview.TargetStorageTargets, poolID)
			if storageOK && storage.Available > 0 && requiredBytes > storage.Available {
				preview.Blockers = append(preview.Blockers, fmt.Sprintf("目标存储 %s 可用空间不足", storage.DisplayName))
			}
		}
		for _, check := range backingChecks {
			if !req.SkipPrecheck && !check.OK {
				preview.Blockers = append(preview.Blockers, "链式 backing 校验失败: "+check.Message)
			}
		}
	}
	preview.TargetUserExists = targetUserExists(*node, owner)
	preview.WillCreateTargetUser = owner != "admin" && !preview.TargetUserExists
	fillTargetVPCOptions(*node, preview)
	validateTargetNetwork(preview, req)
	if attachments := ListPublicIPAttachmentsForVM(vmName); len(attachments) > 0 {
		preview.Blockers = append(preview.Blockers, "当前 VM 绑定了公网 IP，请先在目标节点配置同等公网 IP 资源后再迁移")
	}
	fillMigrationPortForwards(*node, preview)
	if cred, err := GetVMCredential(vmName); err == nil && cred != nil {
		preview.Credential = cred
	}
	if mode == MigrationModeLive && len(preview.Blockers) == 0 {
		assessment, err := AssessLiveMigration(context.Background(), *node, vmName, req.EnableCPUThrottle, req.CPUThrottlePercent)
		if err != nil {
			preview.Blockers = append(preview.Blockers, err.Error())
		} else {
			preview.LiveAssessment = assessment
			preview.Warnings = append(preview.Warnings, assessment.Warnings...)
			if !assessment.Allowed {
				preview.Blockers = append(preview.Blockers, assessment.BlockReason)
			}
		}
	}
	return finishPreview(preview), nil
}

func ExecuteVMMigration(ctx context.Context, params VMMigrationTaskParams, progress func(int, string)) (*MigrationAdoptResult, error) {
	if progress == nil {
		progress = func(int, string) {}
	}
	var preview *VMMigrationPreview
	if strings.TrimSpace(params.PreviewID) != "" {
		progress(5, "正在读取迁移预检结果...")
		cachedPreview, err := loadMigrationPreview(params.PreviewID)
		if err != nil {
			return nil, err
		}
		if !cachedPreview.Allowed {
			return nil, fmt.Errorf("迁移预检未通过: %s", strings.Join(cachedPreview.Blockers, "；"))
		}
		if err := validateCachedPreviewForExecution(cachedPreview, params); err != nil {
			return nil, err
		}
		preview = cachedPreview
	} else {
		progress(5, "正在生成迁移执行计划...")
		builtPreview, err := BuildVMMigrationPreview(params.VMName, VMMigrationRequest{
			NodeID:                params.NodeID,
			Mode:                  params.Mode,
			SkipPrecheck:          params.SkipPrecheck,
			TargetStoragePoolID:   params.TargetStoragePoolID,
			DiskStorageTargets:    params.DiskStorageTargets,
			TargetSwitchID:        params.TargetSwitchID,
			TargetSecurityGroupID: params.TargetSecurityGroupID,
			EnableCPUThrottle:     params.EnableCPUThrottle,
			CPUThrottlePercent:    params.CPUThrottlePercent,
		})
		if err != nil {
			return nil, err
		}
		if !builtPreview.Allowed {
			return nil, fmt.Errorf("迁移执行计划未通过: %s", strings.Join(builtPreview.Blockers, "；"))
		}
		preview = builtPreview
	}
	node, err := GetHostNode(preview.Node.ID)
	if err != nil {
		return nil, err
	}
	progress(18, "正在导出虚拟机 XML...")
	xmlText := utils.ExecCommand("virsh", "dumpxml", preview.VMName).Stdout
	if strings.TrimSpace(xmlText) == "" {
		return nil, fmt.Errorf("导出虚拟机 XML 失败")
	}
	xmlText = applyMigrationDiskPathsToXML(xmlText, preview.Disks)
	if preview.Mode == MigrationModeCold {
		if err := executeColdMigration(ctx, *node, preview, xmlText, progress); err != nil {
			return nil, err
		}
	} else {
		if err := executeLiveMigration(ctx, *node, preview, params.EnableCPUThrottle, params.CPUThrottlePercent, progress); err != nil {
			return nil, err
		}
	}
	progress(82, "正在让目标面板接管虚拟机...")
	adoptReq := buildAdoptRequest(preview)
	var adoptResult MigrationAdoptResult
	if _, err := callNodeAPI(*node, "POST", "/api/migration/adopt-vm", adoptReq, &adoptResult); err != nil {
		return nil, err
	}
	if len(preview.Warnings) > 0 {
		adoptResult.Warnings = append(preview.Warnings, adoptResult.Warnings...)
	}
	progress(100, "虚拟机迁移完成，源节点副本已保留")
	return &adoptResult, nil
}

// ensureMigratedVMNVRAM 确保迁移后的虚拟机 NVRAM 文件存在。
func ensureMigratedVMNVRAM(vmName string) error {
	xmlResult := utils.ExecCommand("virsh", "dumpxml", vmName, "--inactive")
	if xmlResult.Error != nil {
		return fmt.Errorf("获取虚拟机 XML 失败: %s", xmlResult.Stderr)
	}
	bootType := ParseVMBootTypeFromDomainXML(xmlResult.Stdout)
	if err := ensureVMUEFINVRAMFile(vmName, xmlResult.Stdout, bootType); err != nil {
		return fmt.Errorf("创建 UEFI NVRAM 文件失败: %w", err)
	}
	return nil
}

func AdoptMigratedVM(req MigrationAdoptRequest) (*MigrationAdoptResult, error) {
	req.VMName = strings.TrimSpace(req.VMName)
	req.Owner = strings.TrimSpace(req.Owner)
	if req.VMName == "" || req.Owner == "" {
		return nil, fmt.Errorf("虚拟机和用户不能为空")
	}
	if !domainExists(req.VMName) {
		return nil, fmt.Errorf("目标节点尚未定义虚拟机 %s", req.VMName)
	}

	if err := ensureMigratedVMNVRAM(req.VMName); err != nil {
		return nil, fmt.Errorf("确保虚拟机 NVRAM 文件失败: %w", err)
	}

	createdUser, err := ensureMigrationTargetUser(req)
	if err != nil {
		return nil, err
	}
	if req.Owner != "admin" {
		if err := AddVMToUser(req.Owner, req.VMName); err != nil {
			return nil, fmt.Errorf("绑定 VM 到目标用户失败: %w", err)
		}
	}
	if IsLightweightCloudType(req.CloudType) {
		if req.LightweightQuota != nil {
			req.LightweightQuota.VMName = req.VMName
			if _, err := UpsertLightweightVMQuota(req.Owner, *req.LightweightQuota); err != nil {
				return nil, fmt.Errorf("同步轻量云配额失败: %w", err)
			}
		}
		if err := EnsureLightweightVMNetwork(req.Owner, req.VMName); err != nil {
			return nil, fmt.Errorf("绑定轻量云 VPC 失败: %w", err)
		}
	} else {
		switchID := req.TargetSwitchID
		securityGroupID := req.TargetSecurityGroupID
		if switchID == 0 && req.Owner != "admin" {
			sw, group, err := ensureMigrationDefaultVPC(req.Owner)
			if err != nil {
				return nil, fmt.Errorf("准备目标默认 VPC 失败: %w", err)
			}
			switchID = sw.ID
			securityGroupID = group.ID
		}
		if switchID > 0 {
			if err := BindVMToVPCAsAdmin(req.VMName, switchID, securityGroupID); err != nil {
				return nil, fmt.Errorf("绑定目标 VPC 失败: %w", err)
			}
		}
	}
	if req.Credential != nil {
		if err := SaveVMCredential(req.VMName, req.Credential.Username, req.Credential.Password, "migration", "migration", false); err != nil {
			return nil, fmt.Errorf("同步 VM 凭据失败: %w", err)
		}
	}
	if err := RefreshVMCacheByName(req.VMName); err != nil {
		return nil, fmt.Errorf("同步目标虚拟机缓存失败: %w", err)
	}
	result := &MigrationAdoptResult{VMName: req.VMName, Owner: req.Owner, CreatedUser: createdUser}
	for _, rule := range req.PortForwards {
		applied := rule
		hostPort := strings.TrimSpace(rule.TargetHostPort)
		if hostPort == "" {
			hostPort = strings.TrimSpace(rule.SourceHostPort)
		}
		if hostPort == "" {
			port, err := AutoAllocatePort()
			if err != nil {
				result.Warnings = append(result.Warnings, "自动分配端口失败: "+err.Error())
				continue
			}
			hostPort = strconv.Itoa(port)
			applied.AutoAllocated = true
		} else if err := CheckRequestedPortForwardHostPortAvailable(hostPort, rule.Protocol, nil); err != nil {
			port, allocErr := AutoAllocatePort()
			if allocErr != nil {
				result.Warnings = append(result.Warnings, "端口 "+hostPort+" 被占用且自动分配失败: "+allocErr.Error())
				continue
			}
			hostPort = strconv.Itoa(port)
			applied.AutoAllocated = true
		}
		vmIP := strings.TrimSpace(rule.DestIP)
		if resolved, err := ResolvePortForwardTargetIP(req.VMName, ""); err == nil && strings.TrimSpace(resolved) != "" {
			vmIP = resolved
		}
		if vmIP == "" {
			result.Warnings = append(result.Warnings, "未能解析 "+req.VMName+" 的端口转发目标 IP，已跳过")
			continue
		}
		applied.TargetHostPort = hostPort
		if err := AddPortForward(&PortForwardAddParams{
			VMIP:           vmIP,
			HostPort:       hostPort,
			VMPort:         rule.VMPort,
			Protocol:       rule.Protocol,
			Comment:        req.VMName,
			CreatedBy:      "migration",
			CreatedByAdmin: true,
		}); err != nil {
			result.Warnings = append(result.Warnings, "端口转发 "+rule.SourceHostPort+" 同步失败: "+err.Error())
			continue
		}
		result.PortForwards = append(result.PortForwards, applied)
		_ = EnsureSecurityGroupAllowsPortForward(req.VMName, rule.Protocol, rule.VMPort)
	}
	return result, nil
}

func executeColdMigration(ctx context.Context, node model.HostNode, preview *VMMigrationPreview, xmlText string, progress func(int, string)) error {
	for i, disk := range preview.Disks {
		progress(25+(i*30)/maxInt(1, len(preview.Disks)), "正在复制磁盘 overlay: "+filepath.Base(disk.SourcePath))
		if _, err := remoteSSHCommand(ctx, node, "test ! -e "+utils.ShellSingleQuote(disk.TargetPath), 30*time.Second); err != nil {
			return fmt.Errorf("目标磁盘已存在或无法访问: %s", disk.TargetPath)
		}
		if err := remoteRsyncFile(ctx, node, disk.SourcePath, disk.TargetPath, 6*time.Hour); err != nil {
			return fmt.Errorf("复制磁盘 %s 失败: %w", disk.SourcePath, err)
		}
		_, _ = remoteSSHCommand(ctx, node, "chown libvirt-qemu:kvm "+utils.ShellSingleQuote(disk.TargetPath)+" || true", 30*time.Second)
	}
	progress(62, "正在复制并定义虚拟机 XML...")
	targetXML := "/tmp/kvm-migrate-" + preview.VMName + ".xml"
	if err := writeRemoteFile(ctx, node, xmlText, targetXML, 30*time.Second); err != nil {
		return err
	}
	if _, err := remoteSSHCommand(ctx, node, "virsh define "+utils.ShellSingleQuote(targetXML), 60*time.Second); err != nil {
		return fmt.Errorf("目标节点定义 VM 失败: %w", err)
	}
	return nil
}

func executeLiveMigration(ctx context.Context, node model.HostNode, preview *VMMigrationPreview, enableCPUThrottle bool, cpuThrottlePercent int, progress func(int, string)) error {
	progress(32, "正在评估热迁移线路与脏页速率...")
	assessment, err := AssessLiveMigration(ctx, node, preview.VMName, enableCPUThrottle, cpuThrottlePercent)
	if err != nil {
		return err
	}
	preview.LiveAssessment = assessment
	preview.Warnings = append(preview.Warnings, assessment.Warnings...)
	if !assessment.Allowed {
		return fmt.Errorf("%s", assessment.BlockReason)
	}
	var cpuRestore *migrationCPUThrottleRestore
	if assessment.CPUThrottleEnabled {
		progress(34, fmt.Sprintf("正在限制 VM CPU 使用率为 %d%% 以降低脏页生成...", assessment.CPUThrottlePercent))
		cpuRestore, err = applyMigrationCPUThrottle(preview.VMName, assessment.CPUThrottlePercent)
		if err != nil {
			return fmt.Errorf("设置迁移 CPU 限制失败: %w", err)
		}
		defer func() {
			if restoreErr := cpuRestore.Restore(context.Background(), node, preview.VMName); restoreErr != nil {
				preview.Warnings = append(preview.Warnings, "迁移后恢复 CPU 限制失败: "+restoreErr.Error())
			}
		}()
	}
	progress(36, "正在准备热迁移 SSH 信任...")
	if err := ensureDefaultSSHKeyTrusted(ctx, node); err != nil {
		return fmt.Errorf("准备热迁移 SSH 信任失败: %w", err)
	}
	progress(40, "正在准备热迁移目标磁盘...")
	createdTargets, err := prepareLiveMigrationTargets(ctx, node, preview)
	if err != nil {
		return err
	}
	cleanupTargets := true
	defer func() {
		if cleanupTargets && len(createdTargets) > 0 {
			cleanupLiveMigrationTargets(context.Background(), node, createdTargets)
		}
	}()

	progress(43, "正在准备热迁移目标 NVRAM...")
	vmXML := utils.ExecCommand("virsh", "dumpxml", preview.VMName).Stdout
	vmXML = applyMigrationDiskPathsToXML(vmXML, preview.Disks)
	nvramPath, vmXML, err := prepareMigrationNVRAMOnTarget(ctx, node, vmXML)
	if err != nil {
		return err
	}
	if nvramPath != "" {
		createdTargets = append(createdTargets, nvramPath)
	}

	progress(46, "正在执行热迁移...")
	sshURI := fmt.Sprintf("qemu+ssh://%s@%s/system", node.SSHUser, node.SSHHost)
	localXML := filepath.Join("/tmp", "kvm-migrate-"+preview.VMName+"-live.xml")
	if err := writeLocalFile(localXML, vmXML); err != nil {
		return fmt.Errorf("写入热迁移目标 XML 失败: %w", err)
	}
	migrateHost := migrationURIHost(node.SSHHost)
	cmd := fmt.Sprintf("virsh migrate --live --persistent --copy-storage-inc --verbose --xml %s --migrateuri %s --disks-uri %s %s %s",
		utils.ShellSingleQuote(localXML),
		utils.ShellSingleQuote("tcp://"+migrateHost),
		utils.ShellSingleQuote("tcp://"+migrateHost),
		utils.ShellSingleQuote(preview.VMName),
		utils.ShellSingleQuote(sshURI))
	result := utils.ExecShellContextWithTimeout(ctx, cmd, 6*time.Hour)
	if result.Error != nil {
		return fmt.Errorf("热迁移失败: %s", firstNonEmpty(result.Stderr, result.Error.Error()))
	}
	cleanupTargets = false
	return nil
}

func AssessLiveMigration(ctx context.Context, node model.HostNode, vmName string, enableCPUThrottle bool, cpuThrottlePercent int) (*MigrationLiveAssessment, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	percent := normalizeMigrationCPUThrottlePercent(cpuThrottlePercent)
	assessment := &MigrationLiveAssessment{
		Allowed:            true,
		CPUThrottlePercent: percent,
		DirtyRateStats:     map[string]string{},
		Dommemstat:         map[string]int64{},
	}
	speed, seconds, err := runMigrationSpeedTest(ctx, node)
	if err != nil {
		return nil, fmt.Errorf("热迁移线路测速失败: %w", err)
	}
	assessment.AverageBandwidthMiB = speed
	assessment.SpeedTestSeconds = seconds
	dirtyRate, stats, err := readDomainDirtyRateMiB(vmName)
	if err != nil {
		return nil, fmt.Errorf("读取热迁移脏页速率失败: %w", err)
	}
	assessment.DirtyRateMiB = dirtyRate
	assessment.DirtyRateStats = stats
	assessment.Dommemstat = readDomainMemstat(vmName)
	kvmAvailable, pageFaultRate, message := readKVMPageFaultRate()
	assessment.KVMStatAvailable = kvmAvailable
	assessment.KVMPageFaultRate = pageFaultRate
	assessment.KVMStatMessage = message
	if !kvmAvailable {
		assessment.Warnings = append(assessment.Warnings, "kvm_stat 不可用，已仅使用 libvirt dirty-rate 作为热迁移判断依据")
	}
	if speed <= 0 {
		assessment.Allowed = false
		assessment.BlockReason = "热迁移线路测速结果无效，无法判断迁移风险"
		return assessment, nil
	}
	assessment.DirtyRateRatio = dirtyRate / speed
	assessment.DirtyRateRatioPercent = assessment.DirtyRateRatio * 100
	if assessment.DirtyRateRatio >= liveMigrationDirtyRateBlockRatio {
		assessment.Allowed = false
		assessment.BlockReason = fmt.Sprintf("热迁移风险过高：脏页速率 %.2f MiB/s，占平均带宽 %.2f MiB/s 的 %.1f%%，已达到或超过 50%% 阈值",
			dirtyRate, speed, assessment.DirtyRateRatioPercent)
		return assessment, nil
	}
	if assessment.DirtyRateRatio >= liveMigrationDirtyRateThrottleRatio {
		assessment.RequiresCPUThrottle = true
		assessment.CPUThrottleEnabled = true
		assessment.CPUThrottlePercent = percent
		assessment.Warnings = append(assessment.Warnings, fmt.Sprintf("脏页速率占平均带宽 %.1f%%，迁移时将强制限制 VM CPU 使用率为 %d%%", assessment.DirtyRateRatioPercent, percent))
		return assessment, nil
	}
	if enableCPUThrottle {
		assessment.CPUThrottleEnabled = true
		assessment.CPUThrottlePercent = percent
		assessment.Warnings = append(assessment.Warnings, fmt.Sprintf("已启用迁移 CPU 限制，迁移时 VM CPU 使用率将限制为 %d%%", percent))
	}
	return assessment, nil
}

func normalizeMigrationCPUThrottlePercent(value int) int {
	if value <= 0 {
		return defaultMigrationCPUThrottlePercent
	}
	if value < 10 {
		return 10
	}
	if value > 100 {
		return 100
	}
	return value
}

func runMigrationSpeedTest(ctx context.Context, node model.HostNode) (float64, float64, error) {
	tmp, err := os.CreateTemp("", "kvm-console-migration-speed-*.bin")
	if err != nil {
		return 0, 0, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Truncate(100 * 1024 * 1024); err != nil {
		_ = tmp.Close()
		return 0, 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, 0, err
	}
	sourceIP, err := localAddressForRemote(node.SSHHost)
	if err != nil {
		return 0, 0, err
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(sourceIP, "0"))
	if err != nil {
		return 0, 0, err
	}
	defer listener.Close()
	mux := http.NewServeMux()
	token := "speed-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	mux.HandleFunc("/"+token, func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, tmpPath)
	})
	server := &http.Server{Handler: mux}
	defer server.Close()
	go func() {
		_ = server.Serve(listener)
	}()
	url := "http://" + net.JoinHostPort(sourceIP, strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)) + "/" + token
	cmd := "curl -fsS --max-time 180 -w '%{speed_download} %{time_total}' -o /dev/null " + utils.ShellSingleQuote(url)
	out, err := remoteSSHCommand(ctx, node, cmd, 4*time.Minute)
	if err != nil {
		return 0, 0, err
	}
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) < 2 {
		return 0, 0, fmt.Errorf("测速输出格式无效: %s", strings.TrimSpace(out))
	}
	bytesPerSecond, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, 0, fmt.Errorf("解析测速速度失败: %w", err)
	}
	seconds, _ := strconv.ParseFloat(fields[1], 64)
	return bytesPerSecond / 1024 / 1024, seconds, nil
}

func localAddressForRemote(remoteHost string) (string, error) {
	remoteHost = strings.Trim(remoteHost, "[]")
	if remoteHost == "" {
		return "", fmt.Errorf("目标 SSH 地址为空")
	}
	conn, err := net.DialTimeout("udp", net.JoinHostPort(remoteHost, "22"), 5*time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP == nil {
		return "", fmt.Errorf("无法识别源节点出口地址")
	}
	return addr.IP.String(), nil
}

func readDomainDirtyRateMiB(vmName string) (float64, map[string]string, error) {
	calc := utils.ExecCommandWithTimeout("virsh", 90*time.Second, "domdirtyrate-calc", vmName, "--seconds", "5")
	if calc.Error != nil {
		message := firstNonEmpty(calc.Stderr, calc.Error.Error())
		if !strings.Contains(strings.ToLower(message), "already being measured") {
			return 0, nil, fmt.Errorf("%s", message)
		}
	}
	deadline := time.Now().Add(70 * time.Second)
	var lastStats map[string]string
	for {
		stats, dirtyRate, hasRate, status, _, err := readDomainDirtyRateStats(vmName)
		if err != nil {
			return 0, stats, err
		}
		lastStats = stats
		if hasRate && status != 1 {
			return dirtyRate, stats, nil
		}
		if time.Now().After(deadline) {
			return 0, lastStats, fmt.Errorf("dirty-rate 测量未完成，请稍后重试")
		}
		time.Sleep(2 * time.Second)
	}
}

func readDomainDirtyRateStats(vmName string) (map[string]string, float64, bool, int, bool, error) {
	statsResult := utils.ExecCommandWithTimeout("virsh", 30*time.Second, "domstats", "--dirtyrate", vmName)
	if statsResult.Error != nil {
		return nil, 0, false, 0, false, fmt.Errorf("%s", firstNonEmpty(statsResult.Stderr, statsResult.Error.Error()))
	}
	stats := map[string]string{}
	var dirtyRate float64
	foundDirtyRate := false
	var status int
	foundStatus := false
	for _, line := range strings.Split(statsResult.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "=") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		key := strings.TrimSpace(parts[0])
		value := strings.Trim(strings.TrimSpace(parts[1]), `"'`)
		if !strings.Contains(strings.ToLower(key), "dirtyrate") {
			continue
		}
		stats[key] = value
		if strings.EqualFold(key, "dirtyrate.calc_status") {
			if parsed, err := strconv.Atoi(value); err == nil {
				status = parsed
				foundStatus = true
			}
			continue
		}
		if isDirtyRateValueKey(key) {
			if parsed, err := strconv.ParseFloat(value, 64); err == nil {
				foundDirtyRate = true
				if parsed > dirtyRate {
					dirtyRate = parsed
				}
			}
		}
	}
	if !foundDirtyRate {
		return stats, 0, false, status, foundStatus, nil
	}
	return stats, dirtyRate, true, status, foundStatus, nil
}

func isDirtyRateValueKey(key string) bool {
	key = strings.ToLower(key)
	return strings.HasSuffix(key, "megabytes_per_second") || strings.HasSuffix(key, "mib_per_second")
}

func readDomainMemstat(vmName string) map[string]int64 {
	result := utils.ExecCommandWithTimeout("virsh", 15*time.Second, "dommemstat", vmName)
	stats := map[string]int64{}
	if result.Error != nil {
		return stats
	}
	for _, line := range strings.Split(result.Stdout, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 {
			continue
		}
		if value, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
			stats[fields[0]] = value
		}
	}
	return stats
}

func readKVMPageFaultRate() (bool, float64, string) {
	cmd := "KVM_STAT=$(command -v kvm_stat || find /usr/lib/linux-tools -name kvm_stat -type f 2>/dev/null | sort | tail -1); " +
		"test -n \"$KVM_STAT\" || exit 7; timeout 5 \"$KVM_STAT\" -1 2>/dev/null || timeout 5 \"$KVM_STAT\" 2>/dev/null"
	result := utils.ExecShell(cmd)
	if result.Error != nil {
		return false, 0, firstNonEmpty(strings.TrimSpace(result.Stderr), "未找到可用 kvm_stat")
	}
	for _, line := range strings.Split(result.Stdout, "\n") {
		if !strings.Contains(strings.ToLower(line), "kvm_page_fault") {
			continue
		}
		if value, ok := lastFloatInText(line); ok {
			return true, value, strings.TrimSpace(line)
		}
		return true, 0, strings.TrimSpace(line)
	}
	return true, 0, "kvm_stat 未返回 kvm_page_fault 行"
}

func lastFloatInText(text string) (float64, bool) {
	replacer := strings.NewReplacer(",", " ", ":", " ", "=", " ", "/s", " ")
	fields := strings.Fields(replacer.Replace(text))
	for i := len(fields) - 1; i >= 0; i-- {
		if value, err := strconv.ParseFloat(fields[i], 64); err == nil {
			return value, true
		}
	}
	return 0, false
}

type migrationCPUThrottleRestore struct {
	Previous  map[string]string
	PeriodKey string
	QuotaKey  string
}

func applyMigrationCPUThrottle(vmName string, percent int) (*migrationCPUThrottleRestore, error) {
	percent = normalizeMigrationCPUThrottlePercent(percent)
	current := parseVirshSchedInfo(utils.ExecCommand("virsh", "schedinfo", vmName).Stdout)
	vcpus := 1
	if info := utils.ExecCommand("virsh", "dominfo", vmName); info.Error == nil {
		if parsed := parseInfoInt(info.Stdout, "CPU(s):"); parsed > 0 {
			vcpus = parsed
		}
	}
	period := 100000
	quota := period * vcpus * percent / 100
	if err := setVirshSchedInfo(vmName, "global_period", strconv.Itoa(period), "global_quota", strconv.Itoa(quota)); err != nil {
		if fallbackErr := setVirshSchedInfo(vmName, "vcpu_period", strconv.Itoa(period), "vcpu_quota", strconv.Itoa(quota)); fallbackErr != nil {
			return nil, err
		}
		return &migrationCPUThrottleRestore{Previous: current, PeriodKey: "vcpu_period", QuotaKey: "vcpu_quota"}, nil
	}
	return &migrationCPUThrottleRestore{Previous: current, PeriodKey: "global_period", QuotaKey: "global_quota"}, nil
}

func parseVirshSchedInfo(output string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		values[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	return values
}

func setVirshSchedInfo(vmName, periodKey, periodValue, quotaKey, quotaValue string) error {
	result := utils.ExecCommand("virsh", "schedinfo", vmName, "--live", "--set", periodKey+"="+periodValue, "--set", quotaKey+"="+quotaValue)
	if result.Error != nil {
		return fmt.Errorf("%s", firstNonEmpty(result.Stderr, result.Error.Error()))
	}
	return nil
}

func restoreVirshSchedInfo(vmName string, previous map[string]string, periodKey, quotaKey string) error {
	periodValue := firstNonEmpty(previous[periodKey], "0")
	quotaValue := firstNonEmpty(previous[quotaKey], "0")
	return setVirshSchedInfo(vmName, periodKey, periodValue, quotaKey, quotaValue)
}

func (restore *migrationCPUThrottleRestore) Restore(ctx context.Context, node model.HostNode, vmName string) error {
	if restore == nil {
		return nil
	}
	periodValue := firstNonEmpty(restore.Previous[restore.PeriodKey], "0")
	quotaValue := firstNonEmpty(restore.Previous[restore.QuotaKey], "0")
	if err := setVirshSchedInfo(vmName, restore.PeriodKey, periodValue, restore.QuotaKey, quotaValue); err == nil {
		return nil
	}
	cmd := "virsh schedinfo " + utils.ShellSingleQuote(vmName) + " --live --set " +
		utils.ShellSingleQuote(restore.PeriodKey+"="+periodValue) + " --set " + utils.ShellSingleQuote(restore.QuotaKey+"="+quotaValue)
	if _, err := remoteSSHCommand(ctx, node, cmd, 30*time.Second); err != nil {
		return err
	}
	return nil
}

func migrationURIHost(host string) string {
	host = strings.TrimSpace(host)
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

func prepareLiveMigrationTargets(ctx context.Context, node model.HostNode, preview *VMMigrationPreview) ([]string, error) {
	var created []string
	for _, disk := range preview.Disks {
		if strings.TrimSpace(disk.TargetPath) == "" {
			return created, fmt.Errorf("目标磁盘路径为空: %s", disk.Target)
		}
		if _, err := remoteSSHCommand(ctx, node, "test ! -e "+utils.ShellSingleQuote(disk.TargetPath), 30*time.Second); err != nil {
			return created, fmt.Errorf("目标磁盘已存在或无法访问: %s", disk.TargetPath)
		}
		cmd, err := buildLiveMigrationTargetCreateCommand(disk)
		if err != nil {
			return created, err
		}
		if _, err := remoteSSHCommand(ctx, node, cmd, 2*time.Minute); err != nil {
			return created, fmt.Errorf("创建热迁移目标磁盘 %s 失败: %w", disk.TargetPath, err)
		}
		created = append(created, disk.TargetPath)
	}
	return created, nil
}

func buildLiveMigrationTargetCreateCommand(disk MigrationDisk) (string, error) {
	format := strings.TrimSpace(disk.Format)
	if format == "" {
		return "", fmt.Errorf("磁盘 %s 缺少格式信息，无法创建热迁移目标盘", disk.SourcePath)
	}
	if disk.VirtualSize <= 0 {
		return "", fmt.Errorf("磁盘 %s 缺少容量信息，无法创建热迁移目标盘", disk.SourcePath)
	}
	dir := filepath.Dir(disk.TargetPath)
	cmd := "set -e; mkdir -p " + utils.ShellSingleQuote(dir) + "; test ! -e " + utils.ShellSingleQuote(disk.TargetPath) + "; qemu-img create -f " + utils.ShellSingleQuote(format)
	if strings.TrimSpace(disk.BackingPath) != "" {
		backingFormat := strings.TrimSpace(disk.BackingFormat)
		if backingFormat == "" {
			return "", fmt.Errorf("磁盘 %s 缺少 backing 格式信息，无法创建热迁移目标盘", disk.SourcePath)
		}
		cmd += " -F " + utils.ShellSingleQuote(backingFormat) + " -b " + utils.ShellSingleQuote(disk.BackingPath)
	}
	cmd += " " + utils.ShellSingleQuote(disk.TargetPath) + " " + strconv.FormatInt(disk.VirtualSize, 10) + "; chown libvirt-qemu:kvm " + utils.ShellSingleQuote(disk.TargetPath) + " || true"
	return cmd, nil
}

func cleanupLiveMigrationTargets(ctx context.Context, node model.HostNode, paths []string) {
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		_, _ = remoteSSHCommand(ctx, node, "rm -f "+utils.ShellSingleQuote(path), 30*time.Second)
	}
}

var (
	vmMigrationNVRAMTemplateAttr = regexp.MustCompile(`\s+template=['"][^'"]+['"]`)
	vmMigrationNVRAMTemplateFmt  = regexp.MustCompile(`\s+templateFormat=['"][^'"]+['"]`)
	vmMigrationNVRAMTag          = regexp.MustCompile(`(?s)<nvram\b[^>]*(?:/>|>.*?</nvram>)`)
)

// stripNVRAMTemplateFromXML 从 XML 中移除 <nvram> 标签的 template 和 templateFormat 属性
// 用于热迁移场景：目标节点已预先创建好 qcow2 NVRAM 文件，不需要 libvirt 再从模板转换
func stripNVRAMTemplateFromXML(xmlContent string) string {
	return vmMigrationNVRAMTag.ReplaceAllStringFunc(xmlContent, func(tag string) string {
		tag = vmMigrationNVRAMTemplateAttr.ReplaceAllString(tag, "")
		tag = vmMigrationNVRAMTemplateFmt.ReplaceAllString(tag, "")
		return tag
	})
}

// prepareMigrationNVRAMOnTarget 在目标节点预创建 UEFI NVRAM qcow2 文件
// 返回 NVRAM 文件路径（用于失败清理）和修改后的 XML（已移除 template 属性）
func prepareMigrationNVRAMOnTarget(ctx context.Context, node model.HostNode, xmlText string) (nvramPath string, modifiedXML string, err error) {
	bootType := ParseVMBootTypeFromDomainXML(xmlText)
	if bootType != VMBootTypeUEFI && bootType != VMBootTypeUEFISecure {
		return "", xmlText, nil
	}
	nvramPath = extractDomainNVRAMPath(xmlText)
	if nvramPath == "" {
		return "", xmlText, nil
	}
	secure := bootType == VMBootTypeUEFISecure
	templatePath := resolveOVMFVarsTemplatePath(secure)
	nvramDir := filepath.Dir(nvramPath)
	mkdirCmd := "mkdir -p " + utils.ShellSingleQuote(nvramDir)
	convertCmd := fmt.Sprintf("qemu-img convert -f raw -O qcow2 %s %s && chmod 600 %s && (chown libvirt-qemu:kvm %s 2>/dev/null || chown qemu:qemu %s 2>/dev/null || true)",
		utils.ShellSingleQuote(templatePath),
		utils.ShellSingleQuote(nvramPath),
		utils.ShellSingleQuote(nvramPath),
		utils.ShellSingleQuote(nvramPath),
		utils.ShellSingleQuote(nvramPath))
	fullCmd := mkdirCmd + " && " + convertCmd
	if _, err := remoteSSHCommand(ctx, node, fullCmd, 60*time.Second); err != nil {
		return nvramPath, xmlText, fmt.Errorf("目标节点创建 NVRAM 文件失败: %w", err)
	}
	modifiedXML = stripNVRAMTemplateFromXML(xmlText)
	return nvramPath, modifiedXML, nil
}

func buildAdoptRequest(preview *VMMigrationPreview) MigrationAdoptRequest {
	req := MigrationAdoptRequest{
		VMName:                preview.VMName,
		Owner:                 preview.Owner,
		CloudType:             preview.CloudType,
		TargetSwitchID:        preview.TargetSwitchID,
		TargetSecurityGroupID: preview.TargetSecurityGroupID,
		User:                  loadUserSnapshot(preview.Owner),
		Credential:            preview.Credential,
		PortForwards:          preview.PortForwards,
	}
	if quota, err := GetLightweightVMQuota(preview.VMName); err == nil && quota != nil {
		req.LightweightQuota = &LightweightVMQuotaRequest{
			VMName:            preview.VMName,
			TrafficDownGB:     quota.TrafficDownGB,
			TrafficUpGB:       quota.TrafficUpGB,
			BandwidthDownMbps: quota.BandwidthDownMbps,
			BandwidthUpMbps:   quota.BandwidthUpMbps,
			MaxPortForwards:   quota.MaxPortForwards,
			MaxSnapshots:      quota.MaxSnapshots,
			MaxRuntimeHours:   quota.MaxRuntimeHours,
		}
	}
	req.User.CloudType = preview.CloudType
	if preview.IsLightweight {
		req.User.DedicatedVPCSwitchID = preview.TargetSwitchID
	}
	return req
}

func buildDiskAndBackingChecks(node model.HostNode, vmName, targetDir string, skipHash bool) ([]MigrationDisk, []MigrationBackingCheck, error) {
	disks, err := listDomainDisks(vmName)
	if err != nil {
		return nil, nil, err
	}
	var checks []MigrationBackingCheck
	for i := range disks {
		chain, err := qemuInfoChain(disks[i].SourcePath)
		if err != nil {
			return nil, nil, err
		}
		if len(chain) > 0 {
			disks[i].Format = chain[0].Format
			disks[i].VirtualSize = chain[0].VirtualSize
			disks[i].ActualSize = chain[0].ActualSize
			disks[i].BackingPath = firstNonEmpty(chain[0].FullBackingFilename, chain[0].BackingFilename)
		}
		if len(chain) > 1 {
			disks[i].BackingFormat = chain[1].Format
		}
		if strings.TrimSpace(targetDir) != "" {
			disks[i].TargetPath = filepath.Join(targetDir, filepath.Base(disks[i].SourcePath))
		}
		for _, backing := range chain[1:] {
			check := compareRemoteBacking(node, backing, skipHash)
			checks = append(checks, check)
		}
	}
	return disks, checks, nil
}

func applyMigrationDiskStorageTargets(disks []MigrationDisk, targets []VMStorageTarget, defaultStorage VMStorageTarget, hasDefault bool, requested []MigrationDiskStorageTarget) []string {
	requestedByTarget := map[string]string{}
	for _, item := range requested {
		target := strings.TrimSpace(firstNonEmpty(item.Target, item.Device))
		poolID := strings.TrimSpace(item.TargetStoragePoolID)
		if target == "" || poolID == "" {
			continue
		}
		requestedByTarget[target] = poolID
	}

	var blockers []string
	for i := range disks {
		poolID := strings.TrimSpace(requestedByTarget[disks[i].Target])
		if poolID == "" && hasDefault {
			poolID = defaultStorage.ID
		}
		if poolID == "" {
			blockers = append(blockers, fmt.Sprintf("请选择磁盘 %s 的目标存储", disks[i].Target))
			continue
		}
		storage, ok := findTargetStorage(targets, poolID)
		if !ok {
			blockers = append(blockers, fmt.Sprintf("磁盘 %s 的目标存储不存在或不可用于 VM", disks[i].Target))
			continue
		}
		disks[i].TargetStoragePoolID = storage.ID
		disks[i].TargetStorageDir = storage.VMDir
		disks[i].TargetPath = filepath.Join(storage.VMDir, filepath.Base(disks[i].SourcePath))
	}
	return blockers
}

func buildResolvedMigrationDiskStorageTargets(disks []MigrationDisk) []MigrationDiskStorageTarget {
	items := make([]MigrationDiskStorageTarget, 0, len(disks))
	for _, disk := range disks {
		if strings.TrimSpace(disk.Target) == "" || strings.TrimSpace(disk.TargetStoragePoolID) == "" {
			continue
		}
		items = append(items, MigrationDiskStorageTarget{
			Target:              disk.Target,
			Device:              disk.Target,
			TargetStoragePoolID: disk.TargetStoragePoolID,
		})
	}
	return items
}

func listDomainDisks(vmName string) ([]MigrationDisk, error) {
	result := utils.ExecCommand("virsh", "domblklist", vmName, "--details")
	if result.Error != nil {
		return nil, fmt.Errorf("读取虚拟机磁盘失败: %s", result.Stderr)
	}
	var disks []MigrationDisk
	for _, line := range strings.Split(result.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[1] != "disk" {
			continue
		}
		path := fields[3]
		if path == "-" || !strings.HasPrefix(path, "/") {
			continue
		}
		disks = append(disks, MigrationDisk{
			Target:     fields[2],
			SourcePath: path,
			TargetPath: path,
		})
	}
	if len(disks) == 0 {
		return nil, fmt.Errorf("未找到可迁移磁盘")
	}
	return disks, nil
}

func qemuInfoChain(path string) ([]qemuImgInfo, error) {
	result := utils.ExecCommandWithTimeout("qemu-img", 3*time.Minute, "info", "-U", "--backing-chain", "--output=json", path)
	if result.Error != nil {
		return nil, fmt.Errorf("读取磁盘链失败: %s", result.Stderr)
	}
	var chain []qemuImgInfo
	if err := json.Unmarshal([]byte(result.Stdout), &chain); err != nil {
		return nil, fmt.Errorf("解析磁盘链失败: %w", err)
	}
	return chain, nil
}

func compareRemoteBacking(node model.HostNode, backing qemuImgInfo, skipHash bool) MigrationBackingCheck {
	path := strings.TrimSpace(backing.Filename)
	check := MigrationBackingCheck{
		Path:              path,
		SourceFormat:      backing.Format,
		SourceVirtualSize: backing.VirtualSize,
	}
	if path == "" {
		check.Message = "backing 路径为空"
		return check
	}
	if skipHash {
		remoteCmd := "qemu-img info -U --output=json " + utils.ShellSingleQuote(path)
		out, err := remoteSSHCommand(context.Background(), node, remoteCmd, 2*time.Minute)
		if err != nil {
			check.Message = "目标 backing 不存在或不可读: " + err.Error()
			return check
		}
		var target qemuImgInfo
		if err := json.Unmarshal([]byte(out), &target); err != nil {
			check.Message = "解析目标 backing 信息失败: " + err.Error()
			return check
		}
		check.TargetFormat = target.Format
		check.TargetVirtualSize = target.VirtualSize
		check.OK = check.SourceFormat == check.TargetFormat && check.SourceVirtualSize == check.TargetVirtualSize
		if !check.OK {
			check.Message = "format 或 virtual size 不一致"
		} else {
			check.Message = "已跳过 hash 校验"
		}
		return check
	}
	sourceHash := utils.ExecCommandWithTimeout("sha256sum", 10*time.Minute, path)
	if sourceHash.Error != nil {
		check.Message = "源 backing hash 失败: " + sourceHash.Stderr
		return check
	}
	check.SourceSHA256 = strings.Fields(sourceHash.Stdout)[0]
	remoteCmd := "set -e; qemu-img info -U --output=json " + utils.ShellSingleQuote(path) + "; sha256sum " + utils.ShellSingleQuote(path)
	out, err := remoteSSHCommand(context.Background(), node, remoteCmd, 12*time.Minute)
	if err != nil {
		check.Message = "目标 backing 不存在或不可读: " + err.Error()
		return check
	}
	parts := strings.Split(strings.TrimSpace(out), "\n")
	if len(parts) < 2 {
		check.Message = "目标 backing 信息格式无效"
		return check
	}
	var target qemuImgInfo
	if err := json.Unmarshal([]byte(strings.Join(parts[:len(parts)-1], "\n")), &target); err != nil {
		check.Message = "解析目标 backing 信息失败: " + err.Error()
		return check
	}
	check.TargetFormat = target.Format
	check.TargetVirtualSize = target.VirtualSize
	hashFields := strings.Fields(parts[len(parts)-1])
	if len(hashFields) > 0 {
		check.TargetSHA256 = hashFields[0]
	}
	check.OK = check.SourceFormat == check.TargetFormat &&
		check.SourceVirtualSize == check.TargetVirtualSize &&
		check.SourceSHA256 != "" &&
		check.SourceSHA256 == check.TargetSHA256
	if !check.OK {
		check.Message = "format、virtual size 或 hash 不一致"
	} else {
		check.Message = "校验通过"
	}
	return check
}

func fillTargetVPCOptions(node model.HostNode, preview *VMMigrationPreview) {
	_, switches, groups := fetchTargetNetworkOptions(node)
	preview.TargetSwitches, preview.TargetSecurityGroups = filterTargetMigrationNetworks(preview.Owner, preview.IsLightweight, preview.TargetUserExists, switches, groups)
	if !preview.TargetUserExists && preview.Owner != "admin" {
		preview.TargetSwitchID = 0
		preview.TargetSecurityGroupID = 0
		preview.Warnings = append(preview.Warnings, "目标节点将先创建用户，再使用该用户默认网络绑定 VM")
		return
	}
	var binding model.VPCVMBinding
	if err := model.DB.Where("vm_name = ?", preview.VMName).First(&binding).Error; err == nil {
		preview.SourceBinding = &binding
		if preview.TargetSwitchID == 0 {
			preview.TargetSwitchID = matchTargetSwitch(binding, preview.TargetSwitches, preview.IsLightweight)
		}
		if preview.TargetSecurityGroupID == 0 {
			preview.TargetSecurityGroupID = matchTargetSecurityGroup(binding, preview.TargetSecurityGroups)
		}
	}
}

func validateTargetNetwork(preview *VMMigrationPreview, req VMMigrationRequest) {
	if !preview.TargetUserExists && preview.Owner != "admin" {
		return
	}
	if preview.IsLightweight {
		if preview.TargetSwitchID == 0 {
			preview.Blockers = append(preview.Blockers, "轻量云迁移必须选择目标节点轻量云 VPC")
			return
		}
		if !switchExistsInList(preview.TargetSwitchID, preview.TargetSwitches, true) {
			preview.Blockers = append(preview.Blockers, "目标轻量云 VPC 无效或不是 NAT 网络")
		}
		return
	}
	if preview.Owner != "admin" && preview.TargetSwitchID == 0 {
		preview.Blockers = append(preview.Blockers, "目标已有同名用户，请选择该用户下的目标 VPC")
		return
	}
	if preview.TargetSwitchID > 0 && !switchExistsInList(preview.TargetSwitchID, preview.TargetSwitches, false) {
		preview.Blockers = append(preview.Blockers, "目标 VPC 不存在")
	}
	if req.TargetSecurityGroupID > 0 && !securityGroupExistsInList(req.TargetSecurityGroupID, preview.TargetSecurityGroups) {
		preview.Blockers = append(preview.Blockers, "目标安全组不存在")
	}
}

func fillMigrationPortForwards(node model.HostNode, preview *VMMigrationPreview) {
	rules, err := ListPortForwards()
	if err != nil {
		preview.Warnings = append(preview.Warnings, "读取源端口转发失败: "+err.Error())
		return
	}
	targetUsed := map[string]bool{}
	var targetRules []PortForwardRule
	if _, err := callNodeAPI(node, "GET", "/api/network/port-forward/list", nil, &targetRules); err == nil {
		for _, rule := range targetRules {
			targetUsed[strings.ToLower(rule.Protocol)+"|"+rule.HostPort] = true
		}
	}
	for _, rule := range rules {
		if strings.TrimSpace(rule.VMName) != preview.VMName {
			continue
		}
		item := MigrationPortForwardMap{
			Protocol:       strings.ToLower(firstNonEmpty(rule.Protocol, "tcp")),
			SourceHostPort: rule.HostPort,
			TargetHostPort: rule.HostPort,
			VMPort:         rule.DestPort,
			DestIP:         rule.DestIP,
		}
		if targetUsed[item.Protocol+"|"+item.TargetHostPort] {
			item.TargetHostPort = ""
			item.AutoAllocated = true
			preview.Warnings = append(preview.Warnings, "目标节点端口 "+rule.HostPort+"/"+item.Protocol+" 已占用，将自动分配新端口")
		}
		preview.PortForwards = append(preview.PortForwards, item)
	}
}

func targetUserExists(node model.HostNode, username string) bool {
	if username == "admin" {
		return true
	}
	var users []VMUserInfo
	if _, err := callNodeAPI(node, "GET", "/api/user/list", nil, &users); err != nil {
		return false
	}
	for _, user := range users {
		if user.Username == username {
			return true
		}
	}
	return false
}

func ensureMigrationTargetUser(req MigrationAdoptRequest) (bool, error) {
	if req.Owner == "admin" {
		return false, nil
	}
	var user model.User
	if err := model.DB.Where("username = ?", req.Owner).First(&user).Error; err == nil {
		updates := migrationUserUpdateMap(req)
		if IsLightweightCloudType(req.CloudType) {
			updates["cloud_type"] = CloudTypeLightweight
			updates["dedicated_vpc_switch_id"] = req.TargetSwitchID
		}
		if len(updates) > 0 {
			if err := model.DB.Model(&user).Updates(updates).Error; err != nil {
				return false, err
			}
		}
		return false, nil
	} else if err != gorm.ErrRecordNotFound {
		return false, err
	}
	_ = model.DB.Unscoped().Where("username = ? AND deleted_at IS NOT NULL", req.Owner).Delete(&model.User{}).Error
	systemPassword, err := generateMigrationPassword()
	if err != nil {
		return false, err
	}
	passwordHash := strings.TrimSpace(req.User.PasswordHash)
	if passwordHash == "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(systemPassword), bcrypt.DefaultCost)
		if err != nil {
			return false, err
		}
		passwordHash = string(hash)
	}
	user = model.User{
		Username:             req.Owner,
		PasswordHash:         passwordHash,
		Email:                strings.TrimSpace(req.User.Email),
		Role:                 "user",
		CloudType:            NormalizeCloudType(req.CloudType),
		DedicatedVPCSwitchID: req.TargetSwitchID,
		Status:               firstNonEmpty(req.User.Status, UserStatusActive),
		EmailVerifiedAt:      req.User.EmailVerifiedAt,
		LoginVerifiedUntil:   req.User.LoginVerifiedUntil,
		SecurityUpdatedAt:    req.User.SecurityUpdatedAt,
		MaxCPU:               req.User.MaxCPU,
		MaxMemory:            req.User.MaxMemory,
		MaxDisk:              req.User.MaxDisk,
		MaxVM:                req.User.MaxVM,
		MaxStorage:           req.User.MaxStorage,
		MaxRuntimeHours:      req.User.MaxRuntimeHours,
		EnablePortForward:    req.User.EnablePortForward,
		MaxPortForwards:      req.User.MaxPortForwards,
		MaxSnapshots:         req.User.MaxSnapshots,
		MaxBandwidthUp:       req.User.MaxBandwidthUp,
		MaxBandwidthDown:     req.User.MaxBandwidthDown,
		MaxTrafficDown:       req.User.MaxTrafficDown,
		MaxTrafficUp:         req.User.MaxTrafficUp,
		MaxPublicIPs:         req.User.MaxPublicIPs,
	}
	if err := model.DB.Create(&user).Error; err != nil {
		var existing model.User
		if fetchErr := model.DB.Where("username = ?", req.Owner).First(&existing).Error; fetchErr == nil {
			return false, nil
		}
		return false, err
	}
	if err := provisionSystemUserResources(&user, systemPassword); err != nil {
		return true, err
	}
	if IsLightweightCloudType(user.CloudType) {
		if req.TargetSwitchID == 0 {
			sw, err := EnsureDefaultVPCSwitch(user.Username)
			if err != nil {
				return true, err
			}
			if sw != nil {
				user.DedicatedVPCSwitchID = sw.ID
				if err := model.DB.Model(&user).Update("dedicated_vpc_switch_id", sw.ID).Error; err != nil {
					return true, err
				}
			}
		}
	} else {
		if _, _, err := ensureMigrationDefaultVPC(user.Username); err != nil {
			return true, err
		}
	}
	return true, nil
}

func ensureMigrationDefaultVPC(username string) (*model.VPCSwitch, *model.VPCSecurityGroup, error) {
	group, err := EnsureDefaultSecurityGroup(username)
	if err != nil {
		return nil, nil, err
	}
	sw, err := EnsureDefaultVPCSwitch(username)
	if err != nil {
		return nil, nil, err
	}
	if sw == nil {
		return nil, nil, fmt.Errorf("无法为用户 %s 准备默认 VPC", username)
	}
	return sw, group, nil
}

func migrationUserUpdateMap(req MigrationAdoptRequest) map[string]interface{} {
	updates := map[string]interface{}{}
	if passwordHash := strings.TrimSpace(req.User.PasswordHash); passwordHash != "" {
		updates["password_hash"] = passwordHash
	}
	if email := strings.TrimSpace(req.User.Email); email != "" {
		updates["email"] = email
	}
	if status := strings.TrimSpace(req.User.Status); status != "" {
		updates["status"] = status
	}
	if req.User.EmailVerifiedAt != nil {
		updates["email_verified_at"] = req.User.EmailVerifiedAt
	}
	if req.User.LoginVerifiedUntil != nil {
		updates["login_verified_until"] = req.User.LoginVerifiedUntil
	}
	if req.User.SecurityUpdatedAt != nil {
		updates["security_updated_at"] = req.User.SecurityUpdatedAt
	}
	return updates
}

func loadUserSnapshot(username string) MigrationUserSnapshot {
	snap := MigrationUserSnapshot{Username: username, CloudType: CloudTypeElastic, Status: UserStatusActive, EnablePortForward: true, MaxPortForwards: 10, MaxSnapshots: 5}
	var user model.User
	if model.DB.Where("username = ?", username).First(&user).Error != nil {
		return snap
	}
	snap.PasswordHash = user.PasswordHash
	snap.Email = user.Email
	snap.CloudType = NormalizeCloudType(user.CloudType)
	snap.Status = user.Status
	snap.EmailVerifiedAt = user.EmailVerifiedAt
	snap.LoginVerifiedUntil = user.LoginVerifiedUntil
	snap.SecurityUpdatedAt = user.SecurityUpdatedAt
	snap.MaxCPU = user.MaxCPU
	snap.MaxMemory = user.MaxMemory
	snap.MaxDisk = user.MaxDisk
	snap.MaxVM = user.MaxVM
	snap.MaxStorage = user.MaxStorage
	snap.MaxRuntimeHours = user.MaxRuntimeHours
	snap.EnablePortForward = user.EnablePortForward
	snap.MaxPortForwards = user.MaxPortForwards
	snap.MaxSnapshots = user.MaxSnapshots
	snap.MaxBandwidthUp = user.MaxBandwidthUp
	snap.MaxBandwidthDown = user.MaxBandwidthDown
	snap.MaxTrafficDown = user.MaxTrafficDown
	snap.MaxTrafficUp = user.MaxTrafficUp
	snap.MaxPublicIPs = user.MaxPublicIPs
	snap.DedicatedVPCSwitchID = user.DedicatedVPCSwitchID
	return snap
}

func finishPreview(preview *VMMigrationPreview) *VMMigrationPreview {
	preview.Allowed = len(preview.Blockers) == 0
	return preview
}

func detectMigrationModeFromState(state string) string {
	if strings.Contains(strings.ToLower(strings.TrimSpace(state)), "running") {
		return MigrationModeLive
	}
	return MigrationModeCold
}

func domainExists(vmName string) bool {
	return utils.ExecCommand("virsh", "dominfo", vmName).Error == nil
}

func getDomainState(vmName string) string {
	return strings.TrimSpace(utils.ExecCommand("virsh", "domstate", vmName).Stdout)
}

func matchTargetSwitch(binding model.VPCVMBinding, switches []model.VPCSwitch, lightweight bool) uint {
	var source model.VPCSwitch
	if model.DB.First(&source, binding.SwitchID).Error != nil {
		return 0
	}
	for _, sw := range switches {
		if lightweight && strings.TrimSpace(sw.BridgeMode) != "" && !strings.EqualFold(sw.BridgeMode, BridgeModeNAT) {
			continue
		}
		if sw.Username == binding.Username && sw.Name == source.Name && sw.CIDR == source.CIDR {
			return sw.ID
		}
	}
	return 0
}

func matchTargetSecurityGroup(binding model.VPCVMBinding, groups []model.VPCSecurityGroup) uint {
	var source model.VPCSecurityGroup
	if model.DB.First(&source, binding.SecurityGroupID).Error != nil {
		return 0
	}
	for _, group := range groups {
		if group.Username == source.Username && group.Name == source.Name {
			return group.ID
		}
	}
	return 0
}

func filterTargetMigrationNetworks(owner string, lightweight, targetUserExists bool, switches []model.VPCSwitch, groups []model.VPCSecurityGroup) ([]model.VPCSwitch, []model.VPCSecurityGroup) {
	owner = strings.TrimSpace(owner)
	if owner != "admin" && !targetUserExists {
		return nil, nil
	}
	filteredSwitches := make([]model.VPCSwitch, 0, len(switches))
	for _, sw := range switches {
		if owner != "" && sw.Username != owner {
			continue
		}
		if lightweight && strings.TrimSpace(sw.BridgeMode) != "" && !strings.EqualFold(sw.BridgeMode, BridgeModeNAT) {
			continue
		}
		filteredSwitches = append(filteredSwitches, sw)
	}
	filteredGroups := make([]model.VPCSecurityGroup, 0, len(groups))
	for _, group := range groups {
		if owner != "" && group.Username != owner {
			continue
		}
		filteredGroups = append(filteredGroups, group)
	}
	return filteredSwitches, filteredGroups
}

func switchExistsInList(id uint, switches []model.VPCSwitch, requireNAT bool) bool {
	for _, sw := range switches {
		if sw.ID != id {
			continue
		}
		if !requireNAT {
			return true
		}
		mode := strings.TrimSpace(sw.BridgeMode)
		return mode == "" || strings.EqualFold(mode, BridgeModeNAT)
	}
	return false
}

func securityGroupExistsInList(id uint, groups []model.VPCSecurityGroup) bool {
	for _, group := range groups {
		if group.ID == id {
			return true
		}
	}
	return false
}

func generateMigrationPassword() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func fetchTargetStorageTargets(node model.HostNode) []VMStorageTarget {
	var targets []VMStorageTarget
	_, _ = callNodeAPI(node, "GET", "/api/storage-pool/vm-targets", nil, &targets)
	return targets
}

func fetchTargetNetworkOptions(node model.HostNode) (bool, []model.VPCSwitch, []model.VPCSecurityGroup) {
	var switches []model.VPCSwitch
	var groups []model.VPCSecurityGroup
	_, swErr := callNodeAPI(node, "GET", "/api/vpc/switches", nil, &switches)
	_, groupErr := callNodeAPI(node, "GET", "/api/vpc/security-groups", nil, &groups)
	return swErr == nil && groupErr == nil, switches, groups
}

func findTargetStorage(targets []VMStorageTarget, id string) (VMStorageTarget, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return VMStorageTarget{}, false
	}
	for _, target := range targets {
		if target.ID == id && target.Enabled && strings.TrimSpace(target.VMDir) != "" {
			return target, true
		}
	}
	return VMStorageTarget{}, false
}

func diskTargetExists(node model.HostNode, path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	out, err := remoteSSHCommand(context.Background(), node, "test -e "+utils.ShellSingleQuote(path)+" && echo exists || echo missing", 30*time.Second)
	return err == nil && strings.TrimSpace(out) == "exists"
}

func applyMigrationDiskPathsToXML(xmlText string, disks []MigrationDisk) string {
	for _, disk := range disks {
		if strings.TrimSpace(disk.SourcePath) == "" || strings.TrimSpace(disk.TargetPath) == "" || disk.SourcePath == disk.TargetPath {
			continue
		}
		xmlText = strings.ReplaceAll(xmlText, disk.SourcePath, disk.TargetPath)
	}
	return xmlText
}

func storeMigrationPreview(preview VMMigrationPreview) string {
	id, err := randomMigrationPreviewID()
	if err != nil {
		id = fmt.Sprintf("mig_%d", time.Now().UnixNano())
	}
	preview.PreviewID = id
	migrationPreviewCache.Lock()
	defer migrationPreviewCache.Unlock()
	cleanupExpiredMigrationPreviewsLocked()
	migrationPreviewCache.items[id] = cachedMigrationPreview{
		Preview:   preview,
		ExpiresAt: time.Now().Add(30 * time.Minute),
	}
	return id
}

func loadMigrationPreview(id string) (*VMMigrationPreview, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("请先完成迁移预检")
	}
	migrationPreviewCache.Lock()
	defer migrationPreviewCache.Unlock()
	cleanupExpiredMigrationPreviewsLocked()
	cached, ok := migrationPreviewCache.items[id]
	if !ok {
		return nil, fmt.Errorf("迁移预检已过期，请重新预检")
	}
	preview := cached.Preview
	return &preview, nil
}

func cleanupExpiredMigrationPreviewsLocked() {
	now := time.Now()
	for id, cached := range migrationPreviewCache.items {
		if now.After(cached.ExpiresAt) {
			delete(migrationPreviewCache.items, id)
		}
	}
}

func randomMigrationPreviewID() (string, error) {
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "mig_" + base64.RawURLEncoding.EncodeToString(raw), nil
}

func validateCachedPreviewForExecution(preview *VMMigrationPreview, params VMMigrationTaskParams) error {
	if preview == nil {
		return fmt.Errorf("迁移预检为空")
	}
	if preview.VMName != params.VMName || preview.Node.ID != params.NodeID || preview.TargetStoragePoolID != params.TargetStoragePoolID ||
		preview.TargetSwitchID != params.TargetSwitchID || preview.TargetSecurityGroupID != params.TargetSecurityGroupID {
		return fmt.Errorf("迁移表单已变化，请重新预检")
	}
	if migrationDiskStorageTargetsChanged(preview, params) {
		return fmt.Errorf("磁盘目标存储选择已变化，请重新预检")
	}
	currentMode := detectMigrationModeFromState(getDomainState(preview.VMName))
	if currentMode != preview.Mode {
		return fmt.Errorf("虚拟机运行状态已变化，请重新预检")
	}
	if !domainExists(preview.VMName) {
		return fmt.Errorf("源虚拟机不存在")
	}
	node, err := GetHostNode(preview.Node.ID)
	if err != nil {
		return err
	}
	out, err := remoteSSHCommand(context.Background(), *node, "virsh dominfo "+utils.ShellSingleQuote(preview.VMName)+" >/dev/null 2>&1 && echo exists || echo missing", 30*time.Second)
	if err != nil {
		return fmt.Errorf("检查目标 VM 名称失败: %w", err)
	}
	if strings.TrimSpace(out) == "exists" {
		return fmt.Errorf("目标节点已存在同名虚拟机")
	}
	for _, disk := range preview.Disks {
		if diskTargetExists(*node, disk.TargetPath) {
			return fmt.Errorf("目标磁盘已存在: %s", disk.TargetPath)
		}
	}
	return nil
}

func migrationDiskStorageTargetsChanged(preview *VMMigrationPreview, params VMMigrationTaskParams) bool {
	requested := map[string]string{}
	for _, item := range params.DiskStorageTargets {
		target := strings.TrimSpace(firstNonEmpty(item.Target, item.Device))
		if target != "" {
			requested[target] = strings.TrimSpace(item.TargetStoragePoolID)
		}
	}
	for _, disk := range preview.Disks {
		expected := strings.TrimSpace(disk.TargetStoragePoolID)
		actual := strings.TrimSpace(requested[disk.Target])
		if actual == "" {
			actual = strings.TrimSpace(params.TargetStoragePoolID)
		}
		if expected != actual {
			return true
		}
	}
	return false
}

func writeLocalFile(path, content string) error {
	result := utils.ExecShell(fmt.Sprintf("cat > %s <<'EOF'\n%s\nEOF", utils.ShellSingleQuote(path), content))
	if result.Error != nil {
		return fmt.Errorf("%s", firstNonEmpty(result.Stderr, result.Error.Error()))
	}
	return nil
}
