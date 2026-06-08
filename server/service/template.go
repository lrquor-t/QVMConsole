package service

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"kvm_console/config"
	"kvm_console/utils"
)

const (
	vmTemplateSourceMetadataURI  = "https://kvm-console.local/template-source"
	vmTemplateSourceMetadataKey  = "template-source"
	templateBootDetectTimeout    = 2 * time.Minute
	TemplateDeleteModeCascade    = "cascade"
	TemplateDeleteModePromote    = "promote_children"
	TemplateDeleteModePromoteHot = "promote_children_hot"
	templateDiskInfoWorkerLimit  = 4
)

type templateDiskInfoCacheEntry struct {
	FileSize        int64
	ModTimeUnixNano int64
	ActualSize      string
	VirtualSize     string
}

var templateDiskInfoStat = os.Stat

var templateDiskInfoCommand = func(path string) *utils.CmdResult {
	return utils.ExecCommand("qemu-img", "info", "--output=json", "-U", path)
}

var templateDiskInfoCache = struct {
	sync.RWMutex
	items map[string]templateDiskInfoCacheEntry
}{
	items: make(map[string]templateDiskInfoCacheEntry),
}

// TemplateMeta 模板元数据（保存在 .meta.json 文件中，由程序维护）
type TemplateMeta struct {
	Type          string                 `json:"type"`                      // 类型: linux/windows/fnos/other
	Category      string                 `json:"category,omitempty"`        // 二级分类，当前用于 Linux 发行版和 Windows 版本
	BootType      string                 `json:"boot_type,omitempty"`       // 启动类型: bios/uefi
	BootVerified  bool                   `json:"boot_verified,omitempty"`   // 是否已确认启动类型
	NVRAMPath     string                 `json:"nvram_path,omitempty"`      // UEFI 模板 NVRAM 变量文件
	RootPassword  string                 `json:"root_password,omitempty"`   // 模板 root 密码
	TemplateUser  string                 `json:"template_user,omitempty"`   // 模板中的普通用户名
	DefaultConfig *TemplateDefaultConfig `json:"default_config,omitempty"`  // 模板默认硬件配置
	TemplateUID   string                 `json:"template_uid,omitempty"`    // 模板族唯一标识
	NodeID        string                 `json:"node_id,omitempty"`         // 当前节点唯一标识
	ParentNodeID  string                 `json:"parent_node_id,omitempty"`  // 父节点 ID
	RootNodeID    string                 `json:"root_node_id,omitempty"`    // 根节点 ID
	AdminName     string                 `json:"admin_name,omitempty"`      // 管理员侧名称
	DisplayName   string                 `json:"display_name,omitempty"`    // 用户侧显示文本
	CloneVisible  bool                   `json:"clone_visible"`             // 是否允许普通用户克隆
	Disabled      bool                   `json:"disabled"`                  // 是否禁用克隆
	CreatedFromVM string                 `json:"created_from_vm,omitempty"` // 来源 VM
	CreatedAt     string                 `json:"created_at,omitempty"`      // 创建时间
	MD5           string                 `json:"md5,omitempty"`             // 模板磁盘 MD5
	SHA256        string                 `json:"sha256,omitempty"`          // 模板磁盘 SHA256
	FileSize      int64                  `json:"file_size,omitempty"`       // 模板磁盘字节数
}

type TemplateDefaultConfig struct {
	VCPU                int    `json:"vcpu,omitempty"`
	RAM                 int    `json:"ram,omitempty"`
	DiskSize            int    `json:"disk_size,omitempty"`
	DiskBus             string `json:"disk_bus,omitempty"`
	NicModel            string `json:"nic_model,omitempty"`
	VideoModel          string `json:"video_model,omitempty"`
	CPUTopologyMode     string `json:"cpu_topology_mode,omitempty"`
	FirstBootRebootMode string `json:"first_boot_reboot_mode,omitempty"`
}

// TemplateInfo 模板信息
type TemplateInfo struct {
	Name          string                 `json:"name"`                    // 文件名兼容标识
	ActualSize    string                 `json:"actual_size"`             // 实际磁盘占用
	VirtualSize   string                 `json:"virtual_size"`            // 虚拟大小
	Type          string                 `json:"type"`                    // 类型: linux/windows/fnos/other
	Category      string                 `json:"category,omitempty"`      // 二级分类，当前用于 Linux 发行版和 Windows 版本
	BootType      string                 `json:"boot_type,omitempty"`     // 启动类型: bios/uefi
	NVRAMPath     string                 `json:"nvram_path,omitempty"`    // UEFI 模板 NVRAM 变量文件
	IsDefault     bool                   `json:"is_default"`              // 是否默认模板
	Path          string                 `json:"path"`                    // 完整路径
	RootPassword  string                 `json:"root_password,omitempty"` // 模板 root 密码
	TemplateUser  string                 `json:"template_user,omitempty"` // 模板中的普通用户名
	DefaultConfig *TemplateDefaultConfig `json:"default_config,omitempty"`
	HasMeta       bool                   `json:"has_meta"`              // 是否有元数据文件
	Exported      bool                   `json:"exported"`              // 是否存在导出文件
	ExportPath    string                 `json:"export_path,omitempty"` // 当前导出文件下载路径
	TemplateUID   string                 `json:"template_uid,omitempty"`
	NodeID        string                 `json:"node_id,omitempty"`
	ParentNodeID  string                 `json:"parent_node_id,omitempty"`
	RootNodeID    string                 `json:"root_node_id,omitempty"`
	AdminName     string                 `json:"admin_name,omitempty"`
	DisplayName   string                 `json:"display_name,omitempty"`
	CloneVisible  bool                   `json:"clone_visible"`
	Disabled      bool                   `json:"disabled"`
	CreatedFromVM string                 `json:"created_from_vm,omitempty"`
	CreatedAt     string                 `json:"created_at,omitempty"`
	MD5           string                 `json:"md5,omitempty"`
	SHA256        string                 `json:"sha256,omitempty"`
	FileSize      int64                  `json:"file_size,omitempty"`
	HashStatus    string                 `json:"hash_status"` // ok/missing/size_mismatch
	Level         int                    `json:"level"`
	IsRoot        bool                   `json:"is_root"`
	HasChildren   bool                   `json:"has_children"`
	ChildrenCount int                    `json:"children_count"`
	DirectVMCount int                    `json:"direct_vm_count"`
	TreeVMCount   int                    `json:"tree_vm_count"`
}

// TemplateRelatedVM 模板关联的虚拟机信息
type TemplateRelatedVM struct {
	Name      string `json:"name"`       // 虚拟机名称
	Status    string `json:"status"`     // 虚拟机状态
	IP        string `json:"ip"`         // 虚拟机 IP
	Template  string `json:"template"`   // 直接来源模板
	NodeID    string `json:"node_id"`    // 直接来源节点
	CloneMode string `json:"clone_mode"` // 克隆模式: linked / full
}

// PrepareTemplateParams 制作模板参数
type PrepareTemplateParams struct {
	VMName       string `json:"vm_name"`
	TemplateName string `json:"template_name"`           // 管理员侧名称，同时作为文件名
	DisplayName  string `json:"display_name,omitempty"`  // 用户侧显示文本
	Type         string `json:"type,omitempty"`          // linux/windows/fnos/other
	Category     string `json:"category,omitempty"`      // 二级分类，当前用于 Linux 发行版和 Windows 版本
	RootPassword string `json:"root_password,omitempty"` // 模板 root 密码
	TemplateUser string `json:"template_user,omitempty"` // 模板中的普通用户名
}

// DeleteTemplateParams 删除模板参数
type DeleteTemplateParams struct {
	TemplateName string   `json:"template_name"`
	DeleteVMs    bool     `json:"delete_vms"`
	ExpectedVMs  []string `json:"expected_vms,omitempty"`
	DeleteMode   string   `json:"delete_mode,omitempty"`
}

// DeleteTemplateResult 删除模板结果
type DeleteTemplateResult struct {
	TemplateName      string   `json:"template_name"`
	DeletedTemplates  []string `json:"deleted_templates"`
	DeletedVMs        []string `json:"deleted_vms"`
	PromotedTemplates []string `json:"promoted_templates,omitempty"`
	RebasedVMs        []string `json:"rebased_vms,omitempty"`
}

// DeleteTemplatePreview 删除模板预览
type DeleteTemplatePreview struct {
	TemplateName       string              `json:"template_name"`
	Templates          []TemplateInfo      `json:"templates"`
	RelatedVMs         []TemplateRelatedVM `json:"related_vms"`
	ParentTemplate     *TemplateInfo       `json:"parent_template,omitempty"`
	PromotedTemplates  []TemplateInfo      `json:"promoted_templates,omitempty"`
	RebasedVMs         []TemplateRelatedVM `json:"rebased_vms,omitempty"`
	CanPromote         bool                `json:"can_promote"`
	PromoteBlockers    []string            `json:"promote_blockers,omitempty"`
	CanPromoteHot      bool                `json:"can_promote_hot"`
	PromoteHotBlockers []string            `json:"promote_hot_blockers,omitempty"`
}

// UpdateTemplatePublishParams 更新模板可见性参数
type UpdateTemplatePublishParams struct {
	AdminName           string `json:"admin_name,omitempty"`
	DisplayName         string `json:"display_name,omitempty"`
	CloneVisible        bool   `json:"clone_visible"`
	Disabled            bool   `json:"disabled"`
	Category            string `json:"category,omitempty"`
	VCPU                int    `json:"vcpu,omitempty"`
	RAM                 int    `json:"ram,omitempty"`
	DiskSize            int    `json:"disk_size,omitempty"`
	DiskBus             string `json:"disk_bus,omitempty"`
	NicModel            string `json:"nic_model,omitempty"`
	VideoModel          string `json:"video_model,omitempty"`
	CPUTopologyMode     string `json:"cpu_topology_mode,omitempty"`
	FirstBootRebootMode string `json:"first_boot_reboot_mode,omitempty"`
}

type templateTreeData struct {
	templates []TemplateInfo
	byName    map[string]TemplateInfo
	byNodeID  map[string]TemplateInfo
	children  map[string][]string
	vmByNode  map[string][]TemplateRelatedVM
}

type vmTemplateSource struct {
	XMLName      xml.Name `xml:"template-source"`
	XMLNS        string   `xml:"xmlns,attr"`
	TemplateName string   `xml:"template_name,attr"`
	TemplateUID  string   `xml:"template_uid,attr,omitempty"`
	NodeID       string   `xml:"node_id,attr,omitempty"`
	CloneMode    string   `xml:"clone_mode,attr,omitempty"`
}

var (
	templateSourceNamePattern      = regexp.MustCompile(`template_name=['"]([^'"]+)['"]`)
	templateSourceNodePattern      = regexp.MustCompile(`node_id=['"]([^'"]+)['"]`)
	templateSourceCloneModePattern = regexp.MustCompile(`clone_mode=['"]([^'"]+)['"]`)
)

const (
	defaultLinuxTemplateCategory   = "Ubuntu"
	defaultWindowsTemplateCategory = "WindowsServer2022"
)

var linuxTemplateCategories = []string{
	defaultLinuxTemplateCategory,
	"Debian",
}

var windowsTemplateCategories = []string{
	defaultWindowsTemplateCategory,
	"Windows10",
	"WindowsServer2012R2",
}

func getMetaPath(templatePath string) string {
	return strings.TrimSuffix(templatePath, ".qcow2") + ".meta.json"
}

func getTemplateNVRAMPath(templatePath string) string {
	return strings.TrimSuffix(templatePath, ".qcow2") + ".nvram.fd"
}

func generateTemplateID(prefix string) string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(buf[:])
}

func normalizeTemplateBootType(bootType string) string {
	switch strings.ToLower(strings.TrimSpace(bootType)) {
	case "uefi":
		return "uefi"
	case "bios":
		return "bios"
	default:
		return ""
	}
}

func normalizeTemplateCategory(templateType, category string) string {
	return normalizeTemplateCategoryForName(templateType, category, "")
}

func normalizeTemplateCategoryForName(templateType, category, templateName string) string {
	normalizedType := normalizeTemplateType(templateType)
	if normalizedType != "linux" && normalizedType != "windows" {
		return ""
	}
	category = strings.TrimSpace(category)
	if category == "" {
		if normalizedType == "windows" {
			return detectWindowsTemplateCategoryFromName(templateName)
		}
		return defaultLinuxTemplateCategory
	}
	allowedCategories := linuxTemplateCategories
	defaultCategory := defaultLinuxTemplateCategory
	if normalizedType == "windows" {
		allowedCategories = windowsTemplateCategories
		defaultCategory = detectWindowsTemplateCategoryFromName(templateName)
	}
	for _, allowed := range allowedCategories {
		if strings.EqualFold(category, allowed) {
			return allowed
		}
	}
	return defaultCategory
}

func detectWindowsTemplateCategoryFromName(templateName string) string {
	nameLower := strings.ToLower(strings.TrimSpace(templateName))
	compact := strings.NewReplacer(" ", "", "_", "", "-", "", ".", "").Replace(nameLower)
	switch {
	case strings.Contains(compact, "windowsserver2012r2") ||
		strings.Contains(compact, "server2012r2") ||
		strings.Contains(compact, "win2012r2") ||
		strings.Contains(compact, "win2k12r2"):
		return "WindowsServer2012R2"
	case strings.Contains(compact, "windows10") ||
		strings.Contains(compact, "win10"):
		return "Windows10"
	default:
		return defaultWindowsTemplateCategory
	}
}

func ValidateTemplateCategory(templateType, category string) error {
	normalizedType := normalizeTemplateType(templateType)
	category = strings.TrimSpace(category)
	if normalizedType != "linux" && normalizedType != "windows" {
		if category != "" {
			return fmt.Errorf("仅 Linux 和 Windows 模板支持设置二级分类")
		}
		return nil
	}
	if category != "" {
		allowedCategories := linuxTemplateCategories
		if normalizedType == "windows" {
			allowedCategories = windowsTemplateCategories
		}
		for _, allowed := range allowedCategories {
			if strings.EqualFold(category, allowed) {
				return nil
			}
		}
		if normalizedType == "windows" {
			return fmt.Errorf("Windows 模板分类仅支持 WindowsServer2022、Windows10 或 WindowsServer2012R2")
		}
		return fmt.Errorf("Linux 模板分类仅支持 Ubuntu 或 Debian")
	}
	return nil
}

func normalizeTemplateDefaultConfig(config *TemplateDefaultConfig) *TemplateDefaultConfig {
	if config == nil {
		return nil
	}
	normalized := *config
	if normalized.VCPU < 0 {
		normalized.VCPU = 0
	}
	if normalized.RAM < 0 {
		normalized.RAM = 0
	}
	if normalized.DiskSize < 0 {
		normalized.DiskSize = 0
	}
	if strings.TrimSpace(normalized.DiskBus) != "" {
		normalized.DiskBus = NormalizeVMDiskBus(normalized.DiskBus)
	}
	if strings.TrimSpace(normalized.NicModel) != "" {
		normalized.NicModel = NormalizeVMNicModel(normalized.NicModel)
	}
	if strings.TrimSpace(normalized.VideoModel) != "" {
		switch strings.ToLower(strings.TrimSpace(normalized.VideoModel)) {
		case VMVideoModelVirtio, VMVideoModelVGA, VMVideoModelVMVGA, VMVideoModelCirrus:
			normalized.VideoModel = strings.ToLower(strings.TrimSpace(normalized.VideoModel))
		default:
			normalized.VideoModel = ""
		}
	}
	if strings.TrimSpace(normalized.CPUTopologyMode) != "" {
		normalized.CPUTopologyMode = NormalizeVMCPUTopologyMode(normalized.CPUTopologyMode)
	}
	if strings.TrimSpace(normalized.FirstBootRebootMode) != "" {
		normalized.FirstBootRebootMode = NormalizeVMFirstBootRebootMode(normalized.FirstBootRebootMode)
	}
	if normalized.VCPU <= 0 && normalized.RAM <= 0 && normalized.DiskSize <= 0 &&
		strings.TrimSpace(normalized.DiskBus) == "" && strings.TrimSpace(normalized.NicModel) == "" &&
		strings.TrimSpace(normalized.VideoModel) == "" && strings.TrimSpace(normalized.CPUTopologyMode) == "" &&
		strings.TrimSpace(normalized.FirstBootRebootMode) == "" {
		return nil
	}
	return &normalized
}

func getVMVideoModel(vmName string) string {
	vmName = strings.TrimSpace(vmName)
	if vmName == "" {
		return ""
	}
	if xmlResult := utils.ExecCommand("virsh", "dumpxml", vmName, "--inactive"); xmlResult.Error == nil {
		if videoModel := ParseVMVideoModelFromDomainXML(xmlResult.Stdout); videoModel != "" {
			return videoModel
		}
	}
	if xmlResult := utils.ExecCommand("virsh", "dumpxml", vmName); xmlResult.Error == nil {
		return ParseVMVideoModelFromDomainXML(xmlResult.Stdout)
	}
	return ""
}

func getVMCPUTopologyMode(vmName string) string {
	vmName = strings.TrimSpace(vmName)
	if vmName == "" {
		return ""
	}
	if xmlResult := utils.ExecCommand("virsh", "dumpxml", vmName, "--inactive"); xmlResult.Error == nil {
		return ParseVMCPUTopologyModeFromDomainXML(xmlResult.Stdout)
	}
	if xmlResult := utils.ExecCommand("virsh", "dumpxml", vmName); xmlResult.Error == nil {
		return ParseVMCPUTopologyModeFromDomainXML(xmlResult.Stdout)
	}
	return ""
}

func inheritMissingTemplateVideoModelFromSource(meta *TemplateMeta) {
	if meta == nil || strings.TrimSpace(meta.CreatedFromVM) == "" {
		return
	}
	if meta.DefaultConfig != nil && strings.TrimSpace(meta.DefaultConfig.VideoModel) != "" {
		return
	}
	videoModel := getVMVideoModel(meta.CreatedFromVM)
	if videoModel == "" {
		return
	}
	if meta.DefaultConfig == nil {
		meta.DefaultConfig = &TemplateDefaultConfig{}
	}
	meta.DefaultConfig.VideoModel = videoModel
	meta.DefaultConfig = normalizeTemplateDefaultConfig(meta.DefaultConfig)
}

func inheritMissingTemplateCPUTopologyFromSource(meta *TemplateMeta) {
	if meta == nil || strings.TrimSpace(meta.CreatedFromVM) == "" {
		return
	}
	if meta.DefaultConfig != nil && strings.TrimSpace(meta.DefaultConfig.CPUTopologyMode) != "" {
		return
	}
	mode := getVMCPUTopologyMode(meta.CreatedFromVM)
	if mode == "" || mode == VMCPUTopologyAuto {
		return
	}
	if meta.DefaultConfig == nil {
		meta.DefaultConfig = &TemplateDefaultConfig{}
	}
	meta.DefaultConfig.CPUTopologyMode = mode
	meta.DefaultConfig = normalizeTemplateDefaultConfig(meta.DefaultConfig)
}

func parseSizeValueToGB(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	matched := regexp.MustCompile(`[\d.]+`).FindString(value)
	if matched == "" {
		return 0
	}
	size, err := strconv.ParseFloat(matched, 64)
	if err != nil || size <= 0 {
		return 0
	}
	return int(math.Ceil(size))
}

func collectVMTemplateDefaultConfig(vmName string) *TemplateDefaultConfig {
	config := &TemplateDefaultConfig{}

	infoResult := utils.ExecCommand("virsh", "dominfo", vmName)
	if infoResult.Error == nil {
		config.VCPU = parseInfoInt(infoResult.Stdout, "CPU(s):")
		memoryMB := parseInfoInt(infoResult.Stdout, "Max memory:") / 1024
		if memoryMB <= 0 {
			memoryMB = parseInfoInt(infoResult.Stdout, "Used memory:") / 1024
		}
		if memoryMB > 0 {
			config.RAM = int(math.Round(float64(memoryMB) / 1024.0))
			if config.RAM <= 0 {
				config.RAM = 1
			}
		}
	}

	if disks, err := ListDisks(vmName); err == nil {
		for _, disk := range disks {
			if disk.DeviceType != "" && disk.DeviceType != "disk" {
				continue
			}
			if config.DiskSize <= 0 {
				config.DiskSize = parseSizeValueToGB(disk.CapacityGB)
			}
			if strings.TrimSpace(config.DiskBus) == "" {
				config.DiskBus = strings.TrimSpace(disk.Bus)
			}
			if config.DiskSize > 0 && strings.TrimSpace(config.DiskBus) != "" {
				break
			}
		}
	}

	if config.DiskSize <= 0 {
		if diskInfo := getVMDiskInfo(vmName); strings.TrimSpace(diskInfo.path) != "" {
			result := utils.ExecCommand("qemu-img", "info", "--output=json", "-U", diskInfo.path)
			if result.Error == nil {
				var info struct {
					VirtualSize int64 `json:"virtual-size"`
				}
				if err := json.Unmarshal([]byte(result.Stdout), &info); err == nil && info.VirtualSize > 0 {
					config.DiskSize = int(math.Ceil(float64(info.VirtualSize) / float64(1<<30)))
				}
			}
		}
	}

	netInfo := getVMNetworkInfo(vmName)
	if strings.TrimSpace(netInfo.nicModel) != "" {
		config.NicModel = netInfo.nicModel
	}
	config.VideoModel = getVMVideoModel(vmName)
	config.CPUTopologyMode = getVMCPUTopologyMode(vmName)

	return normalizeTemplateDefaultConfig(config)
}

func shouldDetectTemplateBootType(templateType, bootType string, bootVerified bool) bool {
	normalizedType := strings.ToLower(strings.TrimSpace(templateType))
	normalizedBootType := normalizeTemplateBootType(bootType)
	if normalizedBootType != "" && bootVerified {
		return false
	}
	if normalizedBootType == "" {
		return true
	}
	return normalizedType == "windows" && normalizedBootType == "bios" && !bootVerified
}

func resolveTemplateBootType(templatePath, templateType, bootType string, bootVerified bool, detector func(string) string) (string, bool) {
	normalized := normalizeTemplateBootType(bootType)
	if !shouldDetectTemplateBootType(templateType, normalized, bootVerified) {
		return normalized, normalized != ""
	}
	if strings.TrimSpace(templatePath) == "" || detector == nil {
		return normalized, bootVerified && normalized != ""
	}
	detected := normalizeTemplateBootType(detector(templatePath))
	if detected != "" {
		return detected, true
	}
	return normalized, bootVerified && normalized != ""
}

func DetectTemplateBootType(templatePath string) string {
	result := utils.ExecShellWithTimeout(fmt.Sprintf(
		"virt-filesystems -a %s --filesystems --long 2>/dev/null | awk 'tolower($0) ~ /(^|[[:space:]])vfat([[:space:]]|$)|efi/ {found=1} END {if (found) print \"uefi\"; else print \"bios\"}'",
		utils.ShellSingleQuote(templatePath),
	), templateBootDetectTimeout)
	if result.Error == nil {
		bootType := normalizeTemplateBootType(result.Stdout)
		if bootType != "" {
			return bootType
		}
	}
	return "bios"
}

func detectBootTypeFromDomainXML(xmlContent string) string {
	xmlContent = strings.TrimSpace(xmlContent)
	if xmlContent == "" {
		return ""
	}
	if strings.Contains(xmlContent, "firmware='efi'") || strings.Contains(xmlContent, `firmware="efi"`) {
		return "uefi"
	}
	return "bios"
}

func DetectVMBootType(vmName string) string {
	for _, args := range [][]string{
		{"dumpxml", vmName, "--inactive"},
		{"dumpxml", vmName},
	} {
		result := utils.ExecCommand("virsh", args...)
		if result.Error != nil {
			continue
		}
		bootType := detectBootTypeFromDomainXML(result.Stdout)
		if bootType != "" {
			return bootType
		}
	}
	return ""
}

func DetectVMNVRAMPath(vmName string) string {
	for _, args := range [][]string{
		{"dumpxml", vmName, "--inactive"},
		{"dumpxml", vmName},
	} {
		result := utils.ExecCommand("virsh", args...)
		if result.Error != nil {
			continue
		}
		if path := extractDomainNVRAMPath(result.Stdout); path != "" {
			return path
		}
	}
	return ""
}

func extractDomainNVRAMPath(xmlContent string) string {
	matches := regexp.MustCompile(`(?s)<nvram[^>]*>\s*([^<]+?)\s*</nvram>`).FindStringSubmatch(xmlContent)
	if len(matches) < 2 {
		return ""
	}
	return strings.TrimSpace(matches[1])
}

func copyTemplateNVRAMFromVM(vmName, templatePath string) string {
	sourcePath := DetectVMNVRAMPath(vmName)
	if sourcePath == "" {
		return ""
	}
	if _, err := os.Stat(sourcePath); err != nil {
		return ""
	}
	targetPath := getTemplateNVRAMPath(templatePath)
	result := utils.ExecCommand("cp", sourcePath, targetPath)
	if result.Error != nil {
		return ""
	}
	_ = utils.ExecCommand("chown", "libvirt-qemu:kvm", targetPath)
	_ = os.Chmod(targetPath, 0o600)
	return targetPath
}

func loadTemplateMeta(templatePath string) *TemplateMeta {
	metaPath := getMetaPath(templatePath)
	data, err := os.ReadFile(metaPath)
	if err != nil || len(data) == 0 {
		return nil
	}
	var meta TemplateMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil
	}
	return &meta
}

func saveTemplateMeta(templatePath string, meta *TemplateMeta) error {
	metaPath := getMetaPath(templatePath)
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(metaPath, data, 0o644); err != nil {
		return fmt.Errorf("保存元数据失败: %w", err)
	}
	return nil
}

func normalizeLoadedTemplateMetaWithDetector(name, path string, meta *TemplateMeta, hasMeta bool, detector func(string) string) TemplateMeta {
	normalized := TemplateMeta{}
	if meta != nil {
		normalized = *meta
	}
	if strings.TrimSpace(normalized.Type) == "" {
		normalized.Type = detectTemplateTypeFromName(name)
	}
	normalized.Category = normalizeTemplateCategoryForName(normalized.Type, normalized.Category, name)
	normalized.BootType, normalized.BootVerified = resolveTemplateBootType(path, normalized.Type, normalized.BootType, normalized.BootVerified, detector)
	if strings.TrimSpace(normalized.NVRAMPath) == "" {
		nvramPath := getTemplateNVRAMPath(path)
		if _, err := os.Stat(nvramPath); err == nil {
			normalized.NVRAMPath = nvramPath
		}
	} else if !filepath.IsAbs(normalized.NVRAMPath) {
		normalized.NVRAMPath = filepath.Join(filepath.Dir(path), normalized.NVRAMPath)
	}
	normalized.DefaultConfig = normalizeTemplateDefaultConfig(normalized.DefaultConfig)
	inheritMissingTemplateVideoModelFromSource(&normalized)
	inheritMissingTemplateCPUTopologyFromSource(&normalized)
	if strings.TrimSpace(normalized.AdminName) == "" {
		normalized.AdminName = name
	}
	if strings.TrimSpace(normalized.DisplayName) == "" {
		normalized.DisplayName = normalized.AdminName
	}
	if strings.TrimSpace(normalized.NodeID) == "" {
		normalized.NodeID = "legacy_" + name
	}
	if strings.TrimSpace(normalized.TemplateUID) == "" {
		normalized.TemplateUID = "legacy_" + normalized.NodeID
	}
	if strings.TrimSpace(normalized.RootNodeID) == "" {
		if strings.TrimSpace(normalized.ParentNodeID) == "" {
			normalized.RootNodeID = normalized.NodeID
		} else {
			normalized.RootNodeID = normalized.ParentNodeID
		}
	}
	if strings.TrimSpace(normalized.CreatedAt) == "" {
		if stat, err := os.Stat(path); err == nil {
			normalized.CreatedAt = stat.ModTime().Format(time.RFC3339)
		}
	}
	if !hasMeta {
		normalized.CloneVisible = false
	}
	return normalized
}

func normalizeLoadedTemplateMeta(name, path string, meta *TemplateMeta, hasMeta bool) TemplateMeta {
	return normalizeLoadedTemplateMetaWithDetector(name, path, meta, hasMeta, DetectTemplateBootType)
}

func parseQemuInfoBytes(output, key string) int64 {
	var data map[string]json.RawMessage
	if err := json.Unmarshal([]byte(output), &data); err != nil {
		return 0
	}
	raw, ok := data[key]
	if !ok {
		return 0
	}
	var bytes int64
	if err := json.Unmarshal(raw, &bytes); err != nil {
		return 0
	}
	return bytes
}

func loadTemplateDiskInfo(path string) (templateDiskInfoCacheEntry, error) {
	info, err := templateDiskInfoStat(path)
	if err != nil {
		return templateDiskInfoCacheEntry{}, err
	}
	size := info.Size()
	modTimeUnixNano := info.ModTime().UnixNano()

	templateDiskInfoCache.RLock()
	cached, ok := templateDiskInfoCache.items[path]
	templateDiskInfoCache.RUnlock()
	if ok && cached.FileSize == size && cached.ModTimeUnixNano == modTimeUnixNano {
		return cached, nil
	}

	entry := templateDiskInfoCacheEntry{
		FileSize:        size,
		ModTimeUnixNano: modTimeUnixNano,
	}
	if size > 0 {
		entry.ActualSize = FormatBytesPublic(size)
	}
	result := templateDiskInfoCommand(path)
	if result != nil && result.Error == nil {
		if actualSize := parseQemuInfoBytes(result.Stdout, "actual-size"); actualSize > 0 {
			entry.ActualSize = FormatBytesPublic(actualSize)
		}
		if virtualSize := parseQemuInfoBytes(result.Stdout, "virtual-size"); virtualSize > 0 {
			entry.VirtualSize = fmt.Sprintf("%.2f GiB", float64(virtualSize)/float64(1<<30))
		}
	}

	templateDiskInfoCache.Lock()
	templateDiskInfoCache.items[path] = entry
	templateDiskInfoCache.Unlock()
	return entry, nil
}

func fillTemplateInfoSizes(tpl *TemplateInfo) {
	if tpl == nil || tpl.Path == "" {
		return
	}
	diskInfo, err := loadTemplateDiskInfo(tpl.Path)
	if err != nil {
		return
	}
	tpl.ActualSize = diskInfo.ActualSize
	tpl.VirtualSize = diskInfo.VirtualSize
	if tpl.FileSize <= 0 {
		tpl.FileSize = diskInfo.FileSize
	}
	if tpl.MD5 == "" || tpl.SHA256 == "" || tpl.FileSize <= 0 {
		tpl.HashStatus = "missing"
	} else if tpl.FileSize != diskInfo.FileSize {
		tpl.HashStatus = "size_mismatch"
	} else {
		tpl.HashStatus = "ok"
	}
}

func fillTemplateInfoSizesBatch(templates []TemplateInfo) {
	if len(templates) == 0 {
		return
	}
	workerCount := runtime.GOMAXPROCS(0)
	if workerCount <= 0 {
		workerCount = 1
	}
	if workerCount > templateDiskInfoWorkerLimit {
		workerCount = templateDiskInfoWorkerLimit
	}
	if workerCount > len(templates) {
		workerCount = len(templates)
	}
	if workerCount <= 1 {
		for i := range templates {
			fillTemplateInfoSizes(&templates[i])
		}
		return
	}

	indexCh := make(chan int, len(templates))
	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range indexCh {
				fillTemplateInfoSizes(&templates[idx])
			}
		}()
	}
	for i := range templates {
		indexCh <- i
	}
	close(indexCh)
	wg.Wait()
}

// ListTemplates 列出所有可用模板
func ListTemplates() ([]TemplateInfo, error) {
	tree, err := buildTemplateTreeData()
	if err != nil {
		return nil, err
	}
	return tree.templates, nil
}

func buildTemplateTreeData() (*templateTreeData, error) {
	templateDir := config.GlobalConfig.TemplateDir
	files, err := filepath.Glob(filepath.Join(templateDir, "*.qcow2"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)

	tree := &templateTreeData{
		byName:   make(map[string]TemplateInfo),
		byNodeID: make(map[string]TemplateInfo),
		children: make(map[string][]string),
		vmByNode: make(map[string][]TemplateRelatedVM),
	}
	templates := make([]TemplateInfo, 0, len(files))

	for _, filePath := range files {
		name := strings.TrimSuffix(filepath.Base(filePath), ".qcow2")
		meta := loadTemplateMeta(filePath)
		hasMeta := meta != nil
		normalized := normalizeLoadedTemplateMeta(name, filePath, meta, hasMeta)
		if hasMeta && (normalizeTemplateBootType(meta.BootType) != normalized.BootType ||
			strings.TrimSpace(meta.Category) != normalized.Category ||
			meta.BootVerified != normalized.BootVerified) {
			if err := saveTemplateMeta(filePath, &normalized); err != nil {
				return nil, err
			}
		}
		tpl := TemplateInfo{
			Name:          name,
			Path:          filePath,
			Type:          normalized.Type,
			Category:      normalized.Category,
			BootType:      normalized.BootType,
			NVRAMPath:     normalized.NVRAMPath,
			RootPassword:  normalized.RootPassword,
			TemplateUser:  normalized.TemplateUser,
			DefaultConfig: normalized.DefaultConfig,
			HasMeta:       hasMeta,
			Exported:      HasExportedTemplate(name),
			TemplateUID:   normalized.TemplateUID,
			NodeID:        normalized.NodeID,
			ParentNodeID:  normalized.ParentNodeID,
			RootNodeID:    normalized.RootNodeID,
			AdminName:     normalized.AdminName,
			DisplayName:   normalized.DisplayName,
			CloneVisible:  normalized.CloneVisible,
			Disabled:      normalized.Disabled,
			CreatedFromVM: normalized.CreatedFromVM,
			CreatedAt:     normalized.CreatedAt,
			MD5:           normalized.MD5,
			SHA256:        normalized.SHA256,
			FileSize:      normalized.FileSize,
		}
		if tpl.Exported {
			tpl.ExportPath = getTemplateDownloadPath(GetTemplateExportFileName(name))
		}
		templates = append(templates, tpl)
	}

	fillTemplateInfoSizesBatch(templates)
	for _, tpl := range templates {
		tree.byName[tpl.Name] = tpl
		tree.byNodeID[tpl.NodeID] = tpl
	}

	for _, tpl := range tree.byName {
		parentID := tpl.ParentNodeID
		if parentID != "" {
			if _, ok := tree.byNodeID[parentID]; ok {
				tree.children[parentID] = append(tree.children[parentID], tpl.NodeID)
			}
		}
	}
	for parentID := range tree.children {
		sort.Slice(tree.children[parentID], func(i, j int) bool {
			left := tree.byNodeID[tree.children[parentID][i]]
			right := tree.byNodeID[tree.children[parentID][j]]
			return left.AdminName < right.AdminName
		})
	}

	if err := attachTemplateVMCounts(tree); err != nil {
		return nil, err
	}

	var roots []string
	for _, tpl := range tree.byName {
		if tpl.ParentNodeID == "" || tree.byNodeID[tpl.ParentNodeID].NodeID == "" {
			roots = append(roots, tpl.NodeID)
		}
	}
	sort.Slice(roots, func(i, j int) bool {
		left := tree.byNodeID[roots[i]]
		right := tree.byNodeID[roots[j]]
		return left.AdminName < right.AdminName
	})

	var computeTotal func(string) int
	computeTotal = func(nodeID string) int {
		tpl := tree.byNodeID[nodeID]
		tpl.ChildrenCount = len(tree.children[nodeID])
		tpl.HasChildren = tpl.ChildrenCount > 0
		tpl.DirectVMCount = countLinkedVMs(tree.vmByNode[nodeID])
		total := tpl.DirectVMCount
		for _, childID := range tree.children[nodeID] {
			total += computeTotal(childID)
		}
		tpl.TreeVMCount = total
		tree.byName[tpl.Name] = tpl
		tree.byNodeID[tpl.NodeID] = tpl
		return total
	}
	for _, rootID := range roots {
		computeTotal(rootID)
	}

	var ordered []TemplateInfo
	var walkOrder func(string, int)
	walkOrder = func(nodeID string, level int) {
		tpl := tree.byNodeID[nodeID]
		tpl.Level = level
		tpl.IsRoot = tpl.ParentNodeID == "" || tree.byNodeID[tpl.ParentNodeID].NodeID == ""
		tree.byName[tpl.Name] = tpl
		tree.byNodeID[tpl.NodeID] = tpl
		ordered = append(ordered, tpl)
		for _, childID := range tree.children[nodeID] {
			walkOrder(childID, level+1)
		}
	}
	for _, rootID := range roots {
		walkOrder(rootID, 0)
	}
	tree.templates = ordered
	return tree, nil
}

func attachTemplateVMCounts(tree *templateTreeData) error {
	vmSources, err := listVMTemplateSources()
	if err != nil {
		return err
	}
	for _, vm := range vmSources {
		tplName := strings.TrimSpace(vm.Template)
		if tplName == "" {
			continue
		}
		tpl, ok := tree.byName[tplName]
		if !ok {
			continue
		}
		nodeID := tpl.NodeID
		if vm.NodeID != "" {
			if sourceTpl, exists := tree.byNodeID[vm.NodeID]; exists {
				nodeID = sourceTpl.NodeID
				tplName = sourceTpl.Name
			}
		}
		tree.vmByNode[nodeID] = append(tree.vmByNode[nodeID], TemplateRelatedVM{
			Name:      vm.Name,
			Template:  tplName,
			NodeID:    nodeID,
			CloneMode: vm.CloneMode,
		})
	}
	for nodeID := range tree.vmByNode {
		sort.Slice(tree.vmByNode[nodeID], func(i, j int) bool {
			return tree.vmByNode[nodeID][i].Name < tree.vmByNode[nodeID][j].Name
		})
	}
	return nil
}

func listVMTemplateSources() ([]TemplateRelatedVM, error) {
	xmlPaths, err := filepath.Glob("/etc/libvirt/qemu/*.xml")
	if err != nil {
		return nil, err
	}
	sort.Strings(xmlPaths)

	result := make([]TemplateRelatedVM, 0, len(xmlPaths))
	for _, xmlPath := range xmlPaths {
		content, err := os.ReadFile(xmlPath)
		if err != nil {
			continue
		}
		text := string(content)
		nameMatch := templateSourceNamePattern.FindStringSubmatch(text)
		if len(nameMatch) < 2 {
			continue
		}
		vmName := strings.TrimSuffix(filepath.Base(xmlPath), ".xml")
		nodeID := ""
		nodeMatch := templateSourceNodePattern.FindStringSubmatch(text)
		if len(nodeMatch) >= 2 {
			nodeID = strings.TrimSpace(nodeMatch[1])
		}
		cloneMode := ""
		cloneMatch := templateSourceCloneModePattern.FindStringSubmatch(text)
		if len(cloneMatch) >= 2 {
			cloneMode = strings.TrimSpace(cloneMatch[1])
		}
		result = append(result, TemplateRelatedVM{
			Name:      vmName,
			Template:  strings.TrimSpace(nameMatch[1]),
			NodeID:    nodeID,
			CloneMode: cloneMode,
		})
	}
	return result, nil
}

func GetTemplateMeta(templateName string) *TemplateMeta {
	if config.GlobalConfig != nil && strings.TrimSpace(config.GlobalConfig.TemplateDir) != "" {
		templatePath := filepath.Join(config.GlobalConfig.TemplateDir, templateName+".qcow2")
		if meta := loadTemplateMeta(templatePath); meta != nil {
			normalized := normalizeLoadedTemplateMeta(templateName, templatePath, meta, true)
			return &normalized
		}
		bootType, bootVerified := resolveTemplateBootType(templatePath, detectTemplateTypeFromName(templateName), "", false, DetectTemplateBootType)
		return &TemplateMeta{
			Type:         detectTemplateTypeFromName(templateName),
			Category:     normalizeTemplateCategoryForName(detectTemplateTypeFromName(templateName), "", templateName),
			BootType:     bootType,
			BootVerified: bootVerified,
		}
	}
	detectedType := detectTemplateTypeFromName(templateName)
	return &TemplateMeta{
		Type:     detectedType,
		Category: normalizeTemplateCategoryForName(detectedType, "", templateName),
	}
}

func GetTemplateInfoByName(templateName string) (*TemplateInfo, error) {
	tree, err := buildTemplateTreeData()
	if err != nil {
		return nil, err
	}
	tpl, ok := tree.byName[templateName]
	if !ok {
		return nil, fmt.Errorf("模板不存在: %s", templateName)
	}
	return &tpl, nil
}

func GetTemplateInfoByNodeID(nodeID string) (*TemplateInfo, error) {
	tree, err := buildTemplateTreeData()
	if err != nil {
		return nil, err
	}
	tpl, ok := tree.byNodeID[nodeID]
	if !ok {
		return nil, fmt.Errorf("模板节点不存在: %s", nodeID)
	}
	return &tpl, nil
}

func PrepareTemplate(params *PrepareTemplateParams) error {
	templateDir := config.GlobalConfig.TemplateDir
	if err := os.MkdirAll(templateDir, 0o755); err != nil {
		return fmt.Errorf("创建模板目录失败: %w", err)
	}
	if err := ValidateTemplateName(params.TemplateName); err != nil {
		return err
	}

	diskInfo := getVMDiskInfo(params.VMName)
	if diskInfo.path == "" {
		return fmt.Errorf("无法获取虚拟机 %s 的磁盘路径", params.VMName)
	}

	stateResult := utils.ExecCommand("virsh", "domstate", params.VMName)
	if stateResult.Error != nil {
		return fmt.Errorf("获取虚拟机状态失败: %s", stateResult.Stderr)
	}
	if strings.TrimSpace(stateResult.Stdout) == "running" {
		return fmt.Errorf("虚拟机正在运行，请先关机再制作模板")
	}

	destPath := filepath.Join(templateDir, params.TemplateName+".qcow2")
	if _, err := os.Stat(destPath); err == nil {
		return fmt.Errorf("模板已存在: %s", params.TemplateName)
	}
	result := utils.ExecCommandLongRunning("cp", "--sparse=always", diskInfo.path, destPath)
	if result.Error != nil {
		return fmt.Errorf("复制磁盘失败: %s", result.Stderr)
	}

	tplType := normalizeTemplateType(params.Type)
	if tplType == "" {
		tplType = "linux"
	}
	if err := ValidateTemplateCategory(tplType, params.Category); err != nil {
		return err
	}
	adminName := strings.TrimSpace(params.TemplateName)
	displayName := strings.TrimSpace(params.DisplayName)
	if displayName == "" {
		displayName = adminName
	}

	bootType := DetectVMBootType(params.VMName)
	if bootType == "" {
		bootType = DetectTemplateBootType(destPath)
	}
	defaultConfig := collectVMTemplateDefaultConfig(params.VMName)
	meta := &TemplateMeta{
		Type:          tplType,
		Category:      normalizeTemplateCategoryForName(tplType, params.Category, params.TemplateName),
		BootType:      bootType,
		RootPassword:  params.RootPassword,
		TemplateUser:  params.TemplateUser,
		DefaultConfig: defaultConfig,
		NodeID:        generateTemplateID("node"),
		AdminName:     adminName,
		DisplayName:   displayName,
		CreatedFromVM: params.VMName,
		CreatedAt:     time.Now().Format(time.RFC3339),
	}
	if bootType == "uefi" {
		meta.NVRAMPath = copyTemplateNVRAMFromVM(params.VMName, destPath)
	}

	if sourceTpl, err := resolveSourceTemplateForVM(params.VMName, diskInfo.template); err == nil && sourceTpl != nil {
		meta.TemplateUID = sourceTpl.TemplateUID
		meta.ParentNodeID = sourceTpl.NodeID
		meta.RootNodeID = sourceTpl.RootNodeID
		if meta.RootNodeID == "" {
			meta.RootNodeID = sourceTpl.NodeID
		}
		meta.CloneVisible = false
	} else {
		meta.TemplateUID = generateTemplateID("tpl")
		meta.RootNodeID = meta.NodeID
		meta.CloneVisible = true
	}

	hash, err := CalculateFileHashes(destPath)
	if err != nil {
		_ = os.Remove(destPath)
		return err
	}
	meta.MD5 = hash.MD5
	meta.SHA256 = hash.SHA256
	meta.FileSize = hash.FileSize

	if err := saveTemplateMeta(destPath, meta); err != nil {
		_ = os.Remove(destPath)
		return err
	}
	_ = utils.ExecCommand("chown", "libvirt-qemu:kvm", destPath)
	_ = utils.ExecCommand("chown", "libvirt-qemu:kvm", getMetaPath(destPath))
	return nil
}

func resolveSourceTemplateForVM(vmName, fallbackTemplateName string) (*TemplateInfo, error) {
	tree, err := buildTemplateTreeData()
	if err != nil {
		return nil, err
	}
	if source := ReadVMTemplateSource(vmName); source != nil {
		if source.NodeID != "" {
			if tpl, ok := tree.byNodeID[source.NodeID]; ok {
				return &tpl, nil
			}
		}
		if source.TemplateName != "" {
			if tpl, ok := tree.byName[source.TemplateName]; ok {
				return &tpl, nil
			}
		}
	}
	if fallbackTemplateName != "" {
		if tpl, ok := tree.byName[fallbackTemplateName]; ok {
			return &tpl, nil
		}
	}
	return nil, fmt.Errorf("未找到来源模板")
}

func ListTemplateVMs(templateName string) ([]TemplateRelatedVM, error) {
	tree, err := buildTemplateTreeData()
	if err != nil {
		return nil, err
	}
	tpl, ok := tree.byName[templateName]
	if !ok {
		return nil, fmt.Errorf("模板不存在: %s", templateName)
	}
	return append([]TemplateRelatedVM{}, tree.vmByNode[tpl.NodeID]...), nil
}

func ListTemplateSubtreeVMs(templateName string) ([]TemplateRelatedVM, error) {
	tree, err := buildTemplateTreeData()
	if err != nil {
		return nil, err
	}
	tpl, ok := tree.byName[templateName]
	if !ok {
		return nil, fmt.Errorf("模板不存在: %s", templateName)
	}
	return collectTemplateSubtreeVMs(tree, tpl.NodeID), nil
}

func collectTemplateSubtreeVMs(tree *templateTreeData, nodeID string) []TemplateRelatedVM {
	vms := make([]TemplateRelatedVM, 0)
	for _, vm := range tree.vmByNode[nodeID] {
		if vm.CloneMode == "full" {
			continue
		}
		vms = append(vms, vm)
	}
	for _, childID := range tree.children[nodeID] {
		vms = append(vms, collectTemplateSubtreeVMs(tree, childID)...)
	}
	sort.Slice(vms, func(i, j int) bool {
		return vms[i].Name < vms[j].Name
	})
	return vms
}

func filterLinkedVMs(vms []TemplateRelatedVM) []TemplateRelatedVM {
	filtered := make([]TemplateRelatedVM, 0, len(vms))
	for _, vm := range vms {
		if vm.CloneMode == "full" {
			continue
		}
		filtered = append(filtered, vm)
	}
	return filtered
}

func countLinkedVMs(vms []TemplateRelatedVM) int {
	n := 0
	for _, vm := range vms {
		if vm.CloneMode != "full" {
			n++
		}
	}
	return n
}

func GetDeleteTemplatePreview(templateName string) (*DeleteTemplatePreview, error) {
	tree, err := buildTemplateTreeData()
	if err != nil {
		return nil, err
	}
	tpl, ok := tree.byName[templateName]
	if !ok {
		return nil, fmt.Errorf("模板不存在: %s", templateName)
	}
	templates := collectTemplateSubtree(tree, tpl.NodeID)
	vms := hydrateTemplateRelatedVMs(collectTemplateSubtreeVMs(tree, tpl.NodeID))
	directVMs := hydrateTemplateRelatedVMs(filterLinkedVMs(tree.vmByNode[tpl.NodeID]))
	preview := &DeleteTemplatePreview{
		TemplateName:      templateName,
		Templates:         templates,
		RelatedVMs:        vms,
		PromotedTemplates: directChildTemplates(tree, tpl.NodeID),
		RebasedVMs:        directVMs,
	}
	if parentID := strings.TrimSpace(tpl.ParentNodeID); parentID != "" {
		if parent, ok := tree.byNodeID[parentID]; ok {
			parentCopy := parent
			preview.ParentTemplate = &parentCopy
		}
	}
	preview.PromoteBlockers = buildTemplatePromoteBlockers(preview)
	preview.CanPromote = len(preview.PromoteBlockers) == 0
	preview.PromoteHotBlockers = buildTemplatePromoteHotBlockers(preview)
	preview.CanPromoteHot = len(preview.PromoteHotBlockers) == 0
	return preview, nil
}

func collectTemplateSubtree(tree *templateTreeData, nodeID string) []TemplateInfo {
	var templates []TemplateInfo
	if tpl, ok := tree.byNodeID[nodeID]; ok {
		templates = append(templates, tpl)
	}
	for _, childID := range tree.children[nodeID] {
		templates = append(templates, collectTemplateSubtree(tree, childID)...)
	}
	return templates
}

func directChildTemplates(tree *templateTreeData, nodeID string) []TemplateInfo {
	children := make([]TemplateInfo, 0, len(tree.children[nodeID]))
	for _, childID := range tree.children[nodeID] {
		if child, ok := tree.byNodeID[childID]; ok {
			children = append(children, child)
		}
	}
	return children
}

func hydrateTemplateRelatedVMs(vms []TemplateRelatedVM) []TemplateRelatedVM {
	for i := range vms {
		state := strings.TrimSpace(getDomainState(vms[i].Name))
		if state == "" {
			state = "unknown"
		}
		vms[i].Status = state
		vms[i].IP = getVMIP(vms[i].Name, state == "running")
	}
	return vms
}

func buildTemplatePromoteBlockers(preview *DeleteTemplatePreview) []string {
	var blockers []string
	if preview == nil {
		return []string{"删除预览为空"}
	}
	if preview.ParentTemplate == nil {
		blockers = append(blockers, "根模板没有上级节点，无法提升子节点")
	}
	if len(preview.PromotedTemplates) == 0 && len(preview.RebasedVMs) == 0 {
		blockers = append(blockers, "当前节点没有可提升的子模板或可重定向的直接 VM")
	}
	for _, vm := range preview.RelatedVMs {
		if !isVMStateShutoff(vm.Status) {
			blockers = append(blockers, fmt.Sprintf("关联虚拟机 %s 当前状态为 %s，请先关机", vm.Name, firstNonEmpty(vm.Status, "unknown")))
		}
	}
	return blockers
}

func buildTemplatePromoteHotBlockers(preview *DeleteTemplatePreview) []string {
	var blockers []string
	if preview == nil {
		return []string{"删除预览为空"}
	}
	if preview.ParentTemplate == nil {
		blockers = append(blockers, "根模板没有上级节点，无法热提升子节点")
	}
	if len(preview.PromotedTemplates) == 0 && len(preview.RebasedVMs) == 0 {
		blockers = append(blockers, "当前节点没有可热提升的子模板或可热重定向的直接 VM")
	}
	for _, vm := range preview.RelatedVMs {
		if !isVMStateShutoff(vm.Status) && !strings.EqualFold(strings.TrimSpace(vm.Status), "running") {
			blockers = append(blockers, fmt.Sprintf("关联虚拟机 %s 当前状态为 %s，热提升仅支持 running 或 shut off", vm.Name, firstNonEmpty(vm.Status, "unknown")))
		}
	}
	return blockers
}

func isVMStateShutoff(state string) bool {
	normalized := strings.ToLower(strings.TrimSpace(state))
	return normalized == "shut off" || normalized == "shutoff"
}

func DeleteTemplate(templateName string) error {
	preview, err := GetDeleteTemplatePreview(templateName)
	if err != nil {
		return err
	}
	if len(preview.RelatedVMs) > 0 {
		return fmt.Errorf("模板链路下仍存在 %d 台虚拟机，请确认联动删除后再试", len(preview.RelatedVMs))
	}
	for i := len(preview.Templates) - 1; i >= 0; i-- {
		if err := deleteTemplateFiles(preview.Templates[i].Name); err != nil {
			return err
		}
	}
	return nil
}

func normalizeTemplateDeleteMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case TemplateDeleteModePromoteHot:
		return TemplateDeleteModePromoteHot
	case TemplateDeleteModePromote:
		return TemplateDeleteModePromote
	default:
		return TemplateDeleteModeCascade
	}
}

func DeleteTemplateWithVMs(params *DeleteTemplateParams, progressFn func(int, string)) (*DeleteTemplateResult, error) {
	if progressFn == nil {
		progressFn = func(int, string) {}
	}
	if params == nil || strings.TrimSpace(params.TemplateName) == "" {
		return nil, fmt.Errorf("模板名称不能为空")
	}

	progressFn(5, "正在检查模板链路...")
	preview, err := GetDeleteTemplatePreview(params.TemplateName)
	if err != nil {
		return nil, err
	}
	deleteMode := normalizeTemplateDeleteMode(params.DeleteMode)

	currentVMNames := make([]string, 0, len(preview.RelatedVMs))
	for _, vm := range preview.RelatedVMs {
		currentVMNames = append(currentVMNames, vm.Name)
	}
	if deleteMode == TemplateDeleteModeCascade && len(currentVMNames) > 0 && !params.DeleteVMs {
		return nil, fmt.Errorf("模板链路下仍存在 %d 台虚拟机，请确认联动删除后再试", len(currentVMNames))
	}
	if params.DeleteVMs && len(params.ExpectedVMs) > 0 && !sameStringSet(currentVMNames, params.ExpectedVMs) {
		return nil, fmt.Errorf("模板关联虚拟机列表已发生变化，请刷新页面后重新确认")
	}
	if deleteMode == TemplateDeleteModePromote || deleteMode == TemplateDeleteModePromoteHot {
		if len(params.ExpectedVMs) > 0 && !sameStringSet(currentVMNames, params.ExpectedVMs) {
			return nil, fmt.Errorf("模板关联虚拟机列表已发生变化，请刷新页面后重新确认")
		}
		if deleteMode == TemplateDeleteModePromoteHot {
			return deleteTemplatePromoteChildrenHot(params, preview, progressFn)
		}
		return deleteTemplatePromoteChildren(params, preview, progressFn)
	}

	deletedVMs := make([]string, 0, len(currentVMNames))
	affectedUsers := make(map[string]bool)
	totalVMs := len(preview.RelatedVMs)
	for i, vm := range preview.RelatedVMs {
		progress := 10 + (i * 55 / maxInt(totalVMs, 1))
		progressFn(progress, fmt.Sprintf("正在删除关联虚拟机 %s (%d/%d)...", vm.Name, i+1, totalVMs))
		owner := FindVMOwner(vm.Name)
		if err := DeleteVM(vm.Name); err != nil {
			return nil, fmt.Errorf("删除关联虚拟机 %s 失败: %w", vm.Name, err)
		}
		if owner != "" {
			if err := RemoveVMFromUser(owner, vm.Name); err != nil {
				fmt.Printf("[警告] 从用户 %s 的访问列表移除虚拟机 %s 失败: %v\n", owner, vm.Name, err)
			}
			affectedUsers[owner] = true
		}
		deletedVMs = append(deletedVMs, vm.Name)
	}

	deletedTemplates := make([]string, 0, len(preview.Templates))
	progressFn(75, "正在删除模板链路文件...")
	for i := len(preview.Templates) - 1; i >= 0; i-- {
		name := preview.Templates[i].Name
		if err := deleteTemplateFiles(name); err != nil {
			return nil, err
		}
		deletedTemplates = append(deletedTemplates, name)
	}

	for username := range affectedUsers {
		if err := RebalanceUserBandwidth(username); err != nil {
			fmt.Printf("[警告] 删除模板后重新分配用户 %s 带宽失败: %v\n", username, err)
		}
	}

	progressFn(100, "模板链路已删除")
	return &DeleteTemplateResult{
		TemplateName:     params.TemplateName,
		DeletedTemplates: deletedTemplates,
		DeletedVMs:       deletedVMs,
	}, nil
}

func deleteTemplatePromoteChildren(params *DeleteTemplateParams, preview *DeleteTemplatePreview, progressFn func(int, string)) (*DeleteTemplateResult, error) {
	if preview == nil {
		return nil, fmt.Errorf("删除预览为空")
	}
	if len(preview.PromoteBlockers) > 0 {
		return nil, fmt.Errorf("无法提升子节点: %s", strings.Join(preview.PromoteBlockers, "；"))
	}
	parent := preview.ParentTemplate
	if parent == nil {
		return nil, fmt.Errorf("根模板没有上级节点，无法提升子节点")
	}
	deleteTpl, err := GetTemplateInfoByName(params.TemplateName)
	if err != nil {
		return nil, err
	}
	parentPath, err := ensureTemplatePath(parent.Name)
	if err != nil {
		return nil, fmt.Errorf("父模板不可用: %w", err)
	}
	deletePath, err := ensureTemplatePath(deleteTpl.Name)
	if err != nil {
		return nil, err
	}

	totalWork := len(preview.PromotedTemplates) + len(preview.RebasedVMs)
	if totalWork == 0 {
		return nil, fmt.Errorf("当前节点没有可提升的子模板或可重定向的直接 VM")
	}
	vmDiskPaths, err := validateTemplatePromoteRebaseTargets(preview, deletePath)
	if err != nil {
		return nil, err
	}

	promotedTemplates := make([]string, 0, len(preview.PromotedTemplates))
	rebasedVMs := make([]string, 0, len(preview.RebasedVMs))
	progressFn(10, "正在安全改写模板链路 backing...")
	for i, child := range preview.PromotedTemplates {
		progress := 10 + (i * 45 / maxInt(totalWork, 1))
		progressFn(progress, fmt.Sprintf("正在提升子模板 %s ...", child.AdminName))
		if err := rebaseQcow2BackingToParent(child.Path, deletePath, parentPath); err != nil {
			return nil, fmt.Errorf("提升子模板 %s 失败: %w", child.AdminName, err)
		}
		if err := updatePromotedTemplateMeta(child, *parent); err != nil {
			return nil, err
		}
		promotedTemplates = append(promotedTemplates, child.Name)
	}

	progressFn(55, "正在安全改写直接关联 VM backing...")
	for i, vm := range preview.RebasedVMs {
		progress := 55 + (i * 25 / maxInt(len(preview.RebasedVMs), 1))
		progressFn(progress, fmt.Sprintf("正在重定向虚拟机 %s 的链式硬盘...", vm.Name))
		if err := rebaseQcow2BackingToParent(vmDiskPaths[vm.Name], deletePath, parentPath); err != nil {
			return nil, fmt.Errorf("重定向虚拟机 %s 失败: %w", vm.Name, err)
		}
		if err := WriteVMTemplateSource(vm.Name, parent.Name, "linked"); err != nil {
			return nil, err
		}
		rebasedVMs = append(rebasedVMs, vm.Name)
	}

	progressFn(85, "正在删除当前模板节点文件...")
	if err := deleteTemplateFiles(deleteTpl.Name); err != nil {
		return nil, err
	}

	progressFn(100, "模板节点已删除，子节点已提升")
	return &DeleteTemplateResult{
		TemplateName:      params.TemplateName,
		DeletedTemplates:  []string{deleteTpl.Name},
		DeletedVMs:        []string{},
		PromotedTemplates: promotedTemplates,
		RebasedVMs:        rebasedVMs,
	}, nil
}

func deleteTemplatePromoteChildrenHot(params *DeleteTemplateParams, preview *DeleteTemplatePreview, progressFn func(int, string)) (*DeleteTemplateResult, error) {
	if preview == nil {
		return nil, fmt.Errorf("删除预览为空")
	}
	if len(preview.PromoteHotBlockers) > 0 {
		return nil, fmt.Errorf("无法热提升子节点: %s", strings.Join(preview.PromoteHotBlockers, "；"))
	}
	parent := preview.ParentTemplate
	if parent == nil {
		return nil, fmt.Errorf("根模板没有上级节点，无法热提升子节点")
	}
	deleteTpl, err := GetTemplateInfoByName(params.TemplateName)
	if err != nil {
		return nil, err
	}
	parentPath, err := ensureTemplatePath(parent.Name)
	if err != nil {
		return nil, fmt.Errorf("父模板不可用: %w", err)
	}
	deletePath, err := ensureTemplatePath(deleteTpl.Name)
	if err != nil {
		return nil, err
	}
	if len(preview.PromotedTemplates)+len(preview.RebasedVMs) == 0 {
		return nil, fmt.Errorf("当前节点没有可热提升的子模板或可热重定向的直接 VM")
	}
	if err := validateTemplatePromoteHotTargets(preview, deletePath); err != nil {
		return nil, err
	}

	progressFn(10, "正在热提升子模板 backing...")
	for i, child := range preview.PromotedTemplates {
		progressFn(10+(i*40/maxInt(len(preview.PromotedTemplates), 1)), fmt.Sprintf("正在热提升子模板 %s ...", child.AdminName))
		vms := runningVMsForTemplateSubtree(preview, child.NodeID)
		vmBackingPaths := runningVMBackingPaths(preview, vms, child.Path)
		if err := hotSwapPromotedTemplate(child, *parent, deletePath, parentPath, vms, vmBackingPaths); err != nil {
			return nil, err
		}
	}

	rebasedVMs := make([]string, 0, len(preview.RebasedVMs))
	progressFn(55, "正在热重定向直接关联 VM backing...")
	for i, vm := range preview.RebasedVMs {
		progressFn(55+(i*25/maxInt(len(preview.RebasedVMs), 1)), fmt.Sprintf("正在处理虚拟机 %s ...", vm.Name))
		diskInfo := getVMDiskInfo(vm.Name)
		if strings.TrimSpace(diskInfo.path) == "" || strings.TrimSpace(diskInfo.device) == "" {
			return nil, fmt.Errorf("无法获取虚拟机 %s 的系统盘路径或设备名", vm.Name)
		}
		if strings.EqualFold(strings.TrimSpace(vm.Status), "running") {
			if err := blockPullRunningVMToBase(vm.Name, diskInfo.device, diskInfo.path, parentPath); err != nil {
				return nil, err
			}
		} else {
			if err := rebaseQcow2BackingToParent(diskInfo.path, deletePath, parentPath); err != nil {
				return nil, fmt.Errorf("重定向虚拟机 %s 失败: %w", vm.Name, err)
			}
		}
		if err := WriteVMTemplateSource(vm.Name, parent.Name, "linked"); err != nil {
			return nil, err
		}
		rebasedVMs = append(rebasedVMs, vm.Name)
	}

	progressFn(85, "正在删除当前模板节点文件...")
	if err := deleteTemplateFiles(deleteTpl.Name); err != nil {
		return nil, err
	}

	promotedTemplates := make([]string, 0, len(preview.PromotedTemplates))
	for _, child := range preview.PromotedTemplates {
		promotedTemplates = append(promotedTemplates, child.Name)
	}
	progressFn(100, "模板节点已热删除，子节点已提升")
	return &DeleteTemplateResult{
		TemplateName:      params.TemplateName,
		DeletedTemplates:  []string{deleteTpl.Name},
		DeletedVMs:        []string{},
		PromotedTemplates: promotedTemplates,
		RebasedVMs:        rebasedVMs,
	}, nil
}

func validateTemplatePromoteHotTargets(preview *DeleteTemplatePreview, deletePath string) error {
	for _, child := range preview.PromotedTemplates {
		if strings.TrimSpace(child.Path) == "" {
			return fmt.Errorf("子模板 %s 缺少磁盘路径", child.AdminName)
		}
		if err := ensureDiskCanLeaveOldBacking(child.Path, deletePath); err != nil {
			return fmt.Errorf("子模板 %s 不满足热提升条件: %w", child.AdminName, err)
		}
	}
	for _, vm := range preview.RebasedVMs {
		diskInfo := getVMDiskInfo(vm.Name)
		if strings.TrimSpace(diskInfo.path) == "" || strings.TrimSpace(diskInfo.device) == "" {
			return fmt.Errorf("无法获取虚拟机 %s 的系统盘路径或设备名", vm.Name)
		}
		if err := ensureDiskCanLeaveOldBacking(diskInfo.path, deletePath); err != nil {
			return fmt.Errorf("虚拟机 %s 不满足热重定向条件: %w", vm.Name, err)
		}
	}
	for _, vm := range preview.RelatedVMs {
		if hasExternal, names, err := CheckVMSnapshotSafety(vm.Name); err != nil {
			return fmt.Errorf("检查虚拟机 %s 快照状态失败: %w", vm.Name, err)
		} else if hasExternal {
			return fmt.Errorf("虚拟机 %s 存在外部快照（%s），请先删除这些快照后再热提升", vm.Name, strings.Join(names, "、"))
		}
		if strings.EqualFold(strings.TrimSpace(vm.Status), "running") {
			diskInfo := getVMDiskInfo(vm.Name)
			if strings.TrimSpace(diskInfo.path) == "" || strings.TrimSpace(diskInfo.device) == "" {
				return fmt.Errorf("无法获取运行中虚拟机 %s 的系统盘路径或设备名", vm.Name)
			}
		}
	}
	return nil
}

func hotSwapPromotedTemplate(child TemplateInfo, parent TemplateInfo, deletePath, parentPath string, runningVMs []TemplateRelatedVM, vmBackingPaths map[string]string) error {
	tempPath := child.Path + ".promote-new-" + time.Now().Format("20060102150405")
	backupPath := child.Path + ".promote-old-" + time.Now().Format("20060102150405")
	if err := copyDiskFileSparse(context.Background(), child.Path, tempPath); err != nil {
		return fmt.Errorf("复制子模板临时文件失败: %w", err)
	}
	if err := rebaseQcow2BackingToParent(tempPath, deletePath, parentPath); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("改写子模板临时 backing 失败: %w", err)
	}
	if err := os.Rename(child.Path, backupPath); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("备份原子模板文件失败: %w", err)
	}
	if err := os.Rename(tempPath, child.Path); err != nil {
		_ = os.Rename(backupPath, child.Path)
		_ = os.Remove(tempPath)
		return fmt.Errorf("替换子模板文件失败: %w", err)
	}
	_ = setLibvirtDiskFileOwner(child.Path)
	if err := updatePromotedTemplateMeta(child, parent); err != nil {
		return err
	}

	for _, vm := range runningVMs {
		backingPath := strings.TrimSpace(vmBackingPaths[vm.Name])
		if backingPath == "" {
			backingPath = child.Path
		}
		if err := pivotRunningVMToTemplateBacking(vm.Name, backingPath); err != nil {
			return fmt.Errorf("运行中虚拟机 %s 切换到新子模板 backing 失败: %w", vm.Name, err)
		}
	}
	if err := os.Remove(backupPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除子模板旧 backing 文件失败: %w", err)
	}
	return nil
}

func runningVMBackingPaths(preview *DeleteTemplatePreview, vms []TemplateRelatedVM, fallbackPath string) map[string]string {
	templateByNode := make(map[string]TemplateInfo, len(preview.Templates))
	for _, tpl := range preview.Templates {
		templateByNode[tpl.NodeID] = tpl
	}
	paths := make(map[string]string, len(vms))
	for _, vm := range vms {
		if tpl, ok := templateByNode[vm.NodeID]; ok && strings.TrimSpace(tpl.Path) != "" {
			paths[vm.Name] = tpl.Path
		} else {
			paths[vm.Name] = fallbackPath
		}
	}
	return paths
}

func runningVMsForTemplateSubtree(preview *DeleteTemplatePreview, nodeID string) []TemplateRelatedVM {
	templateByNode := make(map[string]TemplateInfo, len(preview.Templates))
	for _, tpl := range preview.Templates {
		templateByNode[tpl.NodeID] = tpl
	}
	var vms []TemplateRelatedVM
	for _, vm := range preview.RelatedVMs {
		if strings.EqualFold(strings.TrimSpace(vm.Status), "running") && templateNodeInSubtree(vm.NodeID, nodeID, templateByNode) {
			vms = append(vms, vm)
		}
	}
	return vms
}

func templateNodeInSubtree(nodeID, rootNodeID string, templateByNode map[string]TemplateInfo) bool {
	nodeID = strings.TrimSpace(nodeID)
	rootNodeID = strings.TrimSpace(rootNodeID)
	for nodeID != "" {
		if nodeID == rootNodeID {
			return true
		}
		tpl, ok := templateByNode[nodeID]
		if !ok {
			return false
		}
		nodeID = strings.TrimSpace(tpl.ParentNodeID)
	}
	return false
}

func pivotRunningVMToTemplateBacking(vmName, backingPath string) error {
	diskInfo := getVMDiskInfo(vmName)
	if strings.TrimSpace(diskInfo.path) == "" || strings.TrimSpace(diskInfo.device) == "" {
		return fmt.Errorf("无法获取系统盘路径或设备名")
	}
	chain, err := qemuInfoChain(diskInfo.path)
	if err != nil {
		return fmt.Errorf("无法读取活动硬盘容量: %w", err)
	}
	if len(chain) == 0 || chain[0].VirtualSize <= 0 {
		return fmt.Errorf("无法读取活动硬盘容量")
	}
	targetPath, err := nextHotPromoteDiskPath(diskInfo.path)
	if err != nil {
		return err
	}
	createCmd := "qemu-img create -f qcow2 -F qcow2 -b " + utils.ShellSingleQuote(backingPath) + " " + utils.ShellSingleQuote(targetPath) + " " + strconv.FormatInt(chain[0].VirtualSize, 10)
	createResult := utils.ExecShellContextWithTimeout(context.Background(), createCmd, 10*time.Minute)
	if createResult.Error != nil {
		return fmt.Errorf("创建热切换目标 overlay 失败: %s", firstNonEmpty(createResult.Stderr, createResult.Error.Error()))
	}
	_ = setLibvirtDiskFileOwner(targetPath)
	cmd := strings.Join([]string{
		"virsh blockcopy",
		utils.ShellSingleQuote(vmName),
		utils.ShellSingleQuote(diskInfo.device),
		"--dest", utils.ShellSingleQuote(targetPath),
		"--format qcow2",
		"--wait --verbose --pivot --transient-job --shallow --reuse-external",
	}, " ")
	result := utils.ExecShellContextWithTimeout(context.Background(), cmd, 8*time.Hour)
	if result.Error != nil {
		_ = utils.ExecCommand("virsh", "blockjob", vmName, diskInfo.device, "--abort", "--async")
		_ = os.Remove(targetPath)
		return fmt.Errorf("热切换运行中 VM backing 失败: %s", firstNonEmpty(result.Stderr, result.Error.Error()))
	}
	if err := updateInactiveDomainDiskPath(vmName, diskInfo.path, targetPath); err != nil {
		return err
	}
	if err := os.Remove(diskInfo.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("热切换已完成，但删除旧活动 overlay 失败: %w", err)
	}
	return nil
}

func blockPullRunningVMToBase(vmName, device, diskPath, basePath string) error {
	cmd := strings.Join([]string{
		"virsh blockpull",
		utils.ShellSingleQuote(vmName),
		utils.ShellSingleQuote(device),
		"--base", utils.ShellSingleQuote(basePath),
		"--wait --verbose",
	}, " ")
	result := utils.ExecShellContextWithTimeout(context.Background(), cmd, 8*time.Hour)
	if result.Error != nil {
		return fmt.Errorf("运行中 VM 在线拉平到上级模板失败: %s", firstNonEmpty(result.Stderr, result.Error.Error()))
	}
	if err := ensureDiskBackingMatches(diskPath, basePath); err != nil {
		return err
	}
	return nil
}

func nextHotPromoteDiskPath(sourcePath string) (string, error) {
	dir := filepath.Dir(sourcePath)
	base := filepath.Base(sourcePath)
	ext := filepath.Ext(base)
	nameOnly := strings.TrimSuffix(base, ext)
	if ext == "" {
		ext = ".qcow2"
	}
	stamp := time.Now().Format("20060102150405")
	for i := 0; i < 100; i++ {
		suffix := stamp
		if i > 0 {
			suffix += "-" + strconv.Itoa(i)
		}
		candidate := filepath.Join(dir, nameOnly+"_hotpromote_"+suffix+ext)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate, nil
		} else if err != nil {
			return "", fmt.Errorf("检查热删除目标硬盘路径失败: %w", err)
		}
	}
	return "", fmt.Errorf("无法生成不冲突的热删除目标硬盘路径")
}

func validateTemplatePromoteRebaseTargets(preview *DeleteTemplatePreview, deletePath string) (map[string]string, error) {
	for _, child := range preview.PromotedTemplates {
		if strings.TrimSpace(child.Path) == "" {
			return nil, fmt.Errorf("子模板 %s 缺少磁盘路径", child.AdminName)
		}
		if err := ensureDiskCanLeaveOldBacking(child.Path, deletePath); err != nil {
			return nil, fmt.Errorf("子模板 %s 不满足 rebase 条件: %w", child.AdminName, err)
		}
	}

	vmDiskPaths := make(map[string]string, len(preview.RebasedVMs))
	for _, vm := range preview.RebasedVMs {
		diskInfo := getVMDiskInfo(vm.Name)
		if strings.TrimSpace(diskInfo.path) == "" {
			return nil, fmt.Errorf("无法获取虚拟机 %s 的系统盘路径", vm.Name)
		}
		if err := ensureDiskCanLeaveOldBacking(diskInfo.path, deletePath); err != nil {
			return nil, fmt.Errorf("虚拟机 %s 不满足 rebase 条件: %w", vm.Name, err)
		}
		vmDiskPaths[vm.Name] = diskInfo.path
	}
	return vmDiskPaths, nil
}

func rebaseQcow2BackingToParent(diskPath, oldParentPath, newParentPath string) error {
	diskPath = strings.TrimSpace(diskPath)
	if diskPath == "" {
		return fmt.Errorf("磁盘路径为空")
	}
	if _, err := os.Stat(diskPath); err != nil {
		return fmt.Errorf("磁盘文件不可读: %w", err)
	}
	if _, err := os.Stat(newParentPath); err != nil {
		return fmt.Errorf("目标父级模板不可读: %w", err)
	}
	if err := ensureDiskCanLeaveOldBacking(diskPath, oldParentPath); err != nil {
		return err
	}
	result := utils.ExecCommandWithTimeout(
		"qemu-img",
		6*time.Hour,
		"rebase",
		"-f", "qcow2",
		"-F", "qcow2",
		"-b", newParentPath,
		diskPath,
	)
	if result.Error != nil {
		return fmt.Errorf("安全 rebase 失败: %s", firstNonEmpty(result.Stderr, result.Error.Error()))
	}
	if err := ensureDiskBackingMatches(diskPath, newParentPath); err != nil {
		return err
	}
	_ = utils.ExecCommand("chown", "libvirt-qemu:kvm", diskPath)
	return nil
}

func ensureDiskCanLeaveOldBacking(diskPath, oldParentPath string) error {
	chain, err := qemuInfoChain(diskPath)
	if err != nil {
		return err
	}
	if len(chain) < 2 {
		return nil
	}
	currentBacking := firstNonEmpty(chain[0].FullBackingFilename, chain[0].BackingFilename, chain[1].Filename)
	if sameCleanPath(currentBacking, oldParentPath) {
		return nil
	}
	return fmt.Errorf("当前 backing 为 %s，不是即将删除的模板 %s，已拒绝自动改写", currentBacking, oldParentPath)
}

func ensureDiskBackingMatches(diskPath, expectedBacking string) error {
	chain, err := qemuInfoChain(diskPath)
	if err != nil {
		return err
	}
	if len(chain) < 2 {
		return fmt.Errorf("rebase 后磁盘未形成链式 backing")
	}
	currentBacking := firstNonEmpty(chain[0].FullBackingFilename, chain[0].BackingFilename, chain[1].Filename)
	if !sameCleanPath(currentBacking, expectedBacking) {
		return fmt.Errorf("rebase 后 backing 不匹配，当前为 %s，期望为 %s", currentBacking, expectedBacking)
	}
	return nil
}

func updatePromotedTemplateMeta(child TemplateInfo, parent TemplateInfo) error {
	meta := GetTemplateMeta(child.Name)
	meta.ParentNodeID = parent.NodeID
	meta.TemplateUID = parent.TemplateUID
	meta.RootNodeID = parent.RootNodeID
	if strings.TrimSpace(meta.RootNodeID) == "" {
		meta.RootNodeID = parent.NodeID
	}
	hash, err := CalculateFileHashes(child.Path)
	if err != nil {
		return err
	}
	meta.MD5 = hash.MD5
	meta.SHA256 = hash.SHA256
	meta.FileSize = hash.FileSize
	if err := saveTemplateMeta(child.Path, meta); err != nil {
		return err
	}
	_ = utils.ExecCommand("chown", "libvirt-qemu:kvm", getMetaPath(child.Path))
	return nil
}

func deleteTemplateFiles(templateName string) error {
	templatePath, err := ensureTemplatePath(templateName)
	if err != nil {
		return err
	}
	if err := os.Remove(templatePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除模板文件失败: %w", err)
	}
	metaPath := getMetaPath(templatePath)
	if err := os.Remove(metaPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除模板元数据失败: %w", err)
	}
	return nil
}

func ensureTemplatePath(templateName string) (string, error) {
	templateDir := config.GlobalConfig.TemplateDir
	templatePath := filepath.Join(templateDir, templateName+".qcow2")
	if _, err := os.Stat(templatePath); err != nil {
		return "", fmt.Errorf("模板不存在: %s", templateName)
	}
	return templatePath, nil
}

func GetTemplateMinDiskSizeGB(templateName string) (int, error) {
	templatePath, err := ensureTemplatePath(templateName)
	if err != nil {
		return 0, err
	}
	result := utils.ExecCommand("qemu-img", "info", "--output=json", "-U", templatePath)
	if result.Error != nil {
		return 0, fmt.Errorf("获取模板磁盘信息失败: %s", result.Stderr)
	}
	var info struct {
		VirtualSize int64 `json:"virtual-size"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &info); err != nil {
		return 0, fmt.Errorf("解析模板磁盘信息失败: %v", err)
	}
	if info.VirtualSize <= 0 {
		return 0, fmt.Errorf("模板磁盘大小无效: %s", templateName)
	}
	return int(math.Ceil(float64(info.VirtualSize) / float64(1<<30))), nil
}

func NormalizeRequestedDiskSize(requestedDiskSize, minDiskSize int) int {
	if minDiskSize <= 0 {
		return requestedDiskSize
	}
	if requestedDiskSize <= 0 || requestedDiskSize < minDiskSize {
		return minDiskSize
	}
	return requestedDiskSize
}

func ResolveCloneDiskSizeGB(templateName string, requestedDiskSize int) (int, error) {
	minDiskSize, err := GetTemplateMinDiskSizeGB(templateName)
	if err != nil {
		return 0, err
	}
	return NormalizeRequestedDiskSize(requestedDiskSize, minDiskSize), nil
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	counter := make(map[string]int, len(left))
	for _, item := range left {
		counter[item]++
	}
	for _, item := range right {
		counter[item]--
		if counter[item] < 0 {
			return false
		}
	}
	for _, count := range counter {
		if count != 0 {
			return false
		}
	}
	return true
}

func UpdateTemplatePublish(templateName string, params *UpdateTemplatePublishParams) error {
	templatePath, err := ensureTemplatePath(templateName)
	if err != nil {
		return err
	}
	meta := loadTemplateMeta(templatePath)
	if meta == nil {
		normalized := normalizeLoadedTemplateMeta(templateName, templatePath, nil, false)
		meta = &normalized
	}
	if err := ValidateTemplateCategory(meta.Type, params.Category); err != nil {
		return err
	}
	meta.AdminName = strings.TrimSpace(params.AdminName)
	if meta.AdminName == "" {
		meta.AdminName = templateName
	}
	meta.DisplayName = strings.TrimSpace(params.DisplayName)
	if meta.DisplayName == "" {
		meta.DisplayName = meta.AdminName
	}
	meta.CloneVisible = params.CloneVisible
	meta.Disabled = params.Disabled
	meta.Category = normalizeTemplateCategoryForName(meta.Type, params.Category, templateName)
	meta.DefaultConfig = normalizeTemplateDefaultConfig(&TemplateDefaultConfig{
		VCPU:                params.VCPU,
		RAM:                 params.RAM,
		DiskSize:            params.DiskSize,
		DiskBus:             params.DiskBus,
		NicModel:            params.NicModel,
		VideoModel:          params.VideoModel,
		CPUTopologyMode:     params.CPUTopologyMode,
		FirstBootRebootMode: params.FirstBootRebootMode,
	})
	return saveTemplateMeta(templatePath, meta)
}

// UpdateTemplateMeta 保留旧接口兼容，允许更新管理员可维护字段和默认创建配置。
func UpdateTemplateMeta(templateName string, params *UpdateTemplateMetaParams) error {
	return UpdateTemplatePublish(templateName, &UpdateTemplatePublishParams{
		AdminName:           params.AdminName,
		DisplayName:         params.DisplayName,
		CloneVisible:        params.CloneVisible,
		Disabled:            params.Disabled,
		Category:            params.Category,
		VCPU:                params.VCPU,
		RAM:                 params.RAM,
		DiskSize:            params.DiskSize,
		DiskBus:             params.DiskBus,
		NicModel:            params.NicModel,
		VideoModel:          params.VideoModel,
		CPUTopologyMode:     params.CPUTopologyMode,
		FirstBootRebootMode: params.FirstBootRebootMode,
	})
}

type UpdateTemplateMetaParams struct {
	AdminName           string `json:"admin_name,omitempty"`
	DisplayName         string `json:"display_name,omitempty"`
	CloneVisible        bool   `json:"clone_visible"`
	Disabled            bool   `json:"disabled"`
	Category            string `json:"category,omitempty"`
	VCPU                int    `json:"vcpu,omitempty"`
	RAM                 int    `json:"ram,omitempty"`
	DiskSize            int    `json:"disk_size,omitempty"`
	DiskBus             string `json:"disk_bus,omitempty"`
	NicModel            string `json:"nic_model,omitempty"`
	VideoModel          string `json:"video_model,omitempty"`
	CPUTopologyMode     string `json:"cpu_topology_mode,omitempty"`
	FirstBootRebootMode string `json:"first_boot_reboot_mode,omitempty"`
}

type TemplateFileHash struct {
	MD5      string `json:"md5"`
	SHA256   string `json:"sha256"`
	FileSize int64  `json:"file_size"`
}

func CalculateFileHashes(path string) (*TemplateFileHash, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("读取模板磁盘失败: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("读取模板磁盘信息失败: %w", err)
	}
	md5Hash := md5.New()
	sha256Hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(md5Hash, sha256Hash), file); err != nil {
		return nil, fmt.Errorf("计算模板哈希失败: %w", err)
	}
	return &TemplateFileHash{
		MD5:      hex.EncodeToString(md5Hash.Sum(nil)),
		SHA256:   hex.EncodeToString(sha256Hash.Sum(nil)),
		FileSize: info.Size(),
	}, nil
}

func VerifyTemplateFileIntegrity(tpl TemplateInfo) error {
	if tpl.MD5 == "" || tpl.SHA256 == "" || tpl.FileSize <= 0 {
		return fmt.Errorf("模板 %s 缺少完整性元数据", tpl.AdminName)
	}
	hash, err := CalculateFileHashes(tpl.Path)
	if err != nil {
		return err
	}
	if hash.FileSize != tpl.FileSize || !strings.EqualFold(hash.MD5, tpl.MD5) || !strings.EqualFold(hash.SHA256, tpl.SHA256) {
		return fmt.Errorf("模板 %s 完整性校验失败", tpl.AdminName)
	}
	return nil
}

func WriteVMTemplateSource(vmName, templateName, cloneMode string) error {
	tpl, err := GetTemplateInfoByName(templateName)
	if err != nil {
		return err
	}
	wrapper := vmTemplateSource{
		XMLNS:        vmTemplateSourceMetadataURI,
		TemplateName: tpl.Name,
		TemplateUID:  tpl.TemplateUID,
		NodeID:       tpl.NodeID,
		CloneMode:    cloneMode,
	}
	xmlBytes, err := xml.Marshal(wrapper)
	if err != nil {
		return err
	}
	result := utils.ExecCommand(
		"virsh", "metadata", vmName, vmTemplateSourceMetadataURI,
		"--config", "--key", vmTemplateSourceMetadataKey, "--set", string(xmlBytes),
	)
	if result.Error != nil {
		return fmt.Errorf("写入虚拟机模板来源失败: %s", result.Stderr)
	}
	return nil
}

func ReadVMTemplateSource(vmName string) *vmTemplateSource {
	result := utils.ExecCommand("virsh", "metadata", vmName, vmTemplateSourceMetadataURI, "--config")
	if result.Error != nil || strings.TrimSpace(result.Stdout) == "" {
		return nil
	}
	var source vmTemplateSource
	if err := xml.Unmarshal([]byte(result.Stdout), &source); err != nil {
		return nil
	}
	return &source
}

func EnsureTemplateVisibleForClone(templateName string, isAdmin bool) error {
	if isAdmin {
		tpl, err := GetTemplateInfoByName(templateName)
		if err != nil {
			return err
		}
		if tpl.Disabled {
			return fmt.Errorf("该模板已禁用，无法克隆")
		}
		return nil
	}
	tpl, err := GetTemplateInfoByName(templateName)
	if err != nil {
		return err
	}
	if tpl.Disabled {
		return fmt.Errorf("该模板已禁用，无法克隆")
	}
	if !tpl.CloneVisible {
		return fmt.Errorf("该模板当前未开放克隆")
	}
	return nil
}
