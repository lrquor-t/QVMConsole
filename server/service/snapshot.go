package service

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"kvm_console/config"
	"kvm_console/model"
	"kvm_console/utils"
)

var snapshotNameRegexp = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,63}$`)

const (
	appArmorManagedBlockBegin       = "# BEGIN kvm_console managed storage access"
	appArmorManagedBlockEnd         = "# END kvm_console managed storage access"
	appArmorVirtAAHelperLocalPath   = "/etc/apparmor.d/local/usr.lib.libvirt.virt-aa-helper"
	appArmorLibvirtQemuStoragePath  = "/etc/apparmor.d/abstractions/libvirt-qemu.d/kvm-console-storage"
	appArmorVirtAAHelperProfilePath = "/etc/apparmor.d/usr.lib.libvirt.virt-aa-helper"
)

// SnapshotInfo 快照信息
type SnapshotInfo struct {
	Name        string `json:"name"`
	CreatedAt   string `json:"created_at"`
	State       string `json:"state"`
	Description string `json:"description"`
	IsCurrent   bool   `json:"is_current"`
	Location    string `json:"location"` // internal / external
	Children    int    `json:"children"`
	Descendants int    `json:"descendants"`
}

// SnapshotQuotaInfo 快照配额信息。
type SnapshotQuotaInfo struct {
	Scope              string `json:"scope"`
	UsedSnapshots      int    `json:"used_snapshots"`
	MaxSnapshots       int    `json:"max_snapshots"`
	RemainingSnapshots int    `json:"remaining_snapshots"`
}

type snapshotXMLDescription struct {
	Description string `xml:"description"`
}

type snapshotInfoOutput struct {
	State       string
	Location    string
	Children    int
	Descendants int
}

type vmDiskSource struct {
	Target string
	Source string
}

type externalSnapshotDiskXML struct {
	Source struct {
		File string `xml:"file,attr"`
	} `xml:"source"`
}

type externalSnapshotDomainDiskXML struct {
	Source struct {
		File string `xml:"file,attr"`
	} `xml:"source"`
	Target struct {
		Dev string `xml:"dev,attr"`
	} `xml:"target"`
}

type externalSnapshotXML struct {
	Disks  []externalSnapshotDiskXML `xml:"disks>disk"`
	Domain struct {
		Devices struct {
			Disks []externalSnapshotDomainDiskXML `xml:"disk"`
		} `xml:"devices"`
	} `xml:"domain"`
}

// ValidateSnapshotName 校验快照名称，避免 libvirt/QEMU 内部任务 ID 与文件名不兼容。
func ValidateSnapshotName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("请输入快照名称")
	}
	if !snapshotNameRegexp.MatchString(name) {
		return fmt.Errorf("快照名称只能包含英文字母、数字、下划线、点和短横线，且必须以英文字母、数字或下划线开头，最长 64 个字符")
	}
	return nil
}

// GenerateSnapshotName 生成兼容 libvirt/QEMU 内部任务 ID 的快照名称。
func GenerateSnapshotName() string {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return "snap_" + time.Now().Format("20060102_150405")
	}
	return "snap_" + time.Now().Format("20060102_150405") + "_" + hex.EncodeToString(buf)
}

func NormalizeSnapshotName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = GenerateSnapshotName()
	}
	if err := ValidateSnapshotName(name); err != nil {
		return "", err
	}
	return name, nil
}

// ListSnapshots 列出快照
func ListSnapshots(vmName string) ([]SnapshotInfo, error) {
	result := utils.ExecCommand("virsh", "snapshot-list", vmName, "--tree")
	if result.Error != nil {
		// 没有快照时也可能返回错误
		return []SnapshotInfo{}, nil
	}

	// 使用详细命令获取每个快照信息
	listResult := utils.ExecCommand("virsh", "snapshot-list", vmName)
	if listResult.Error != nil {
		return []SnapshotInfo{}, nil
	}

	var snapshots []SnapshotInfo
	lines := strings.Split(listResult.Stdout, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Name") || strings.HasPrefix(line, "-") {
			continue
		}

		// 解析：Name  Creation Time  State
		re := regexp.MustCompile(`^(\S+)\s+(.+?\d{4}.*?\d{2}:\d{2}:\d{2})\s+(\S+.*)$`)
		matches := re.FindStringSubmatch(line)
		if len(matches) >= 4 {
			snap := SnapshotInfo{
				Name:      matches[1],
				CreatedAt: strings.TrimSpace(matches[2]),
				State:     strings.TrimSpace(matches[3]),
			}

			// 获取描述和位置信息
			infoResult := utils.ExecCommand("virsh", "snapshot-info", vmName, snap.Name)
			if infoResult.Error == nil {
				for _, infoLine := range strings.Split(infoResult.Stdout, "\n") {
					infoLine = strings.TrimSpace(infoLine)
					if strings.HasPrefix(infoLine, "Description:") {
						snap.Description = strings.TrimSpace(strings.TrimPrefix(infoLine, "Description:"))
					}
					if strings.HasPrefix(infoLine, "Location:") {
						snap.Location = strings.TrimSpace(strings.TrimPrefix(infoLine, "Location:"))
					}
					if strings.HasPrefix(infoLine, "Children:") {
						snap.Children = parseSnapshotInfoInt(infoLine, "Children:")
					}
					if strings.HasPrefix(infoLine, "Descendants:") {
						snap.Descendants = parseSnapshotInfoInt(infoLine, "Descendants:")
					}
				}
			}
			if snap.Description == "" {
				snap.Description = getSnapshotDescriptionFromXML(vmName, snap.Name)
			}

			snapshots = append(snapshots, snap)
		}
	}

	// 获取当前快照
	currentResult := utils.ExecCommand("virsh", "snapshot-current", vmName, "--name")
	if currentResult.Error == nil {
		currentName := strings.TrimSpace(currentResult.Stdout)
		for i := range snapshots {
			if snapshots[i].Name == currentName {
				snapshots[i].IsCurrent = true
			}
		}
	}

	return snapshots, nil
}

func getSnapshotDescriptionFromXML(vmName, snapName string) string {
	result := utils.ExecCommand("virsh", "snapshot-dumpxml", vmName, snapName)
	if result.Error != nil {
		return ""
	}
	var doc snapshotXMLDescription
	if err := xml.Unmarshal([]byte(result.Stdout), &doc); err == nil {
		return strings.TrimSpace(doc.Description)
	}
	matches := regexp.MustCompile(`(?s)<description>\s*(.*?)\s*</description>`).FindStringSubmatch(result.Stdout)
	if len(matches) < 2 {
		return ""
	}
	return strings.TrimSpace(matches[1])
}

// CountVMSnapshots 统计单台虚拟机的快照数量。
func CountVMSnapshots(vmName string) int {
	result := utils.ExecCommand("virsh", "snapshot-list", vmName, "--name")
	if result.Error != nil {
		return 0
	}
	count := 0
	for _, line := range strings.Split(result.Stdout, "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

// CountUserSnapshots 统计用户名下所有虚拟机的快照总数。
func CountUserSnapshots(username string) int {
	total := 0
	for _, vmName := range GetUserVMList(username) {
		total += CountVMSnapshots(vmName)
	}
	return total
}

func BuildVMSnapshotQuotaInfo(username, role, vmName string, currentCount int) *SnapshotQuotaInfo {
	username = strings.TrimSpace(username)
	if strings.TrimSpace(role) == "admin" || username == "" || strings.TrimSpace(vmName) == "" {
		return nil
	}
	if currentCount < 0 {
		currentCount = CountVMSnapshots(vmName)
	}
	if quota, err := GetLightweightVMQuota(vmName); err == nil && quota != nil {
		info := &SnapshotQuotaInfo{
			Scope:         CloudTypeLightweight,
			UsedSnapshots: currentCount,
			MaxSnapshots:  quota.MaxSnapshots,
		}
		if info.MaxSnapshots > 0 {
			info.RemainingSnapshots = info.MaxSnapshots - info.UsedSnapshots
			if info.RemainingSnapshots < 0 {
				info.RemainingSnapshots = 0
			}
		}
		return info
	}

	var user model.User
	if err := model.DB.Where("username = ?", username).First(&user).Error; err != nil {
		return nil
	}
	info := &SnapshotQuotaInfo{
		Scope:         CloudTypeElastic,
		UsedSnapshots: CountUserSnapshots(username),
		MaxSnapshots:  user.MaxSnapshots,
	}
	if info.MaxSnapshots > 0 {
		info.RemainingSnapshots = info.MaxSnapshots - info.UsedSnapshots
		if info.RemainingSnapshots < 0 {
			info.RemainingSnapshots = 0
		}
	}
	return info
}

func CheckUserSnapshotQuota(username string, delta int) error {
	if delta <= 0 {
		return nil
	}
	var user model.User
	if err := model.DB.Where("username = ?", strings.TrimSpace(username)).First(&user).Error; err != nil {
		return fmt.Errorf("用户不存在: %w", err)
	}
	if user.Role == "admin" || user.MaxSnapshots <= 0 {
		return nil
	}
	used := CountUserSnapshots(username)
	if used+delta > user.MaxSnapshots {
		return fmt.Errorf("当前快照数量超出配额限制（已用 %d / 上限 %d）", used, user.MaxSnapshots)
	}
	return nil
}

func CheckVMSnapshotQuota(username, role, vmName string, delta int) error {
	if delta <= 0 || strings.TrimSpace(role) == "admin" {
		return nil
	}
	if IsLightweightCloudVM(vmName) || IsLightweightCloudUser(username) {
		return CheckLightweightVMSnapshotQuota(username, vmName, delta)
	}
	return CheckUserSnapshotQuota(username, delta)
}

func CheckInternalSnapshotVirtFSUnsupported(vmName string) (bool, string, error) {
	stateResult := utils.ExecCommand("virsh", "domstate", vmName)
	if stateResult.Error != nil {
		return false, "", fmt.Errorf("获取虚拟机状态失败: %s", stateResult.Stderr)
	}
	if strings.TrimSpace(stateResult.Stdout) != "running" {
		return false, "", nil
	}
	shares, err := ListShares(vmName)
	if err != nil {
		return false, "", err
	}
	if len(shares) == 0 {
		return false, "", nil
	}
	parts := make([]string, 0, len(shares))
	for _, share := range shares {
		label := strings.TrimSpace(share.Tag)
		if label == "" {
			label = strings.TrimSpace(share.Target)
		}
		if share.Source != "" {
			label += " -> " + share.Source
		}
		parts = append(parts, label)
	}
	return true, "当前虚拟机正在挂载 9p/VirtFS 共享目录（" + strings.Join(parts, "，") + "），libvirt 禁止在这种状态创建包含内存的内部快照。请先在虚拟机内卸载共享目录并关机移除共享挂载，或取消勾选“保存虚拟机内存状态”创建仅磁盘快照。", nil
}

// getSnapshotType 获取快照的类型/位置信息
// 返回: state(running/shutoff/disk-snapshot), location(internal/external)
func getSnapshotType(vmName, snapName string) (state string, location string, err error) {
	info, err := getSnapshotInfo(vmName, snapName)
	if err != nil {
		return "", "", err
	}
	return info.State, info.Location, nil
}

func getSnapshotInfo(vmName, snapName string) (snapshotInfoOutput, error) {
	infoResult := utils.ExecCommand("virsh", "snapshot-info", vmName, snapName)
	if infoResult.Error != nil {
		return snapshotInfoOutput{}, fmt.Errorf("获取快照信息失败: %s", infoResult.Stderr)
	}
	return parseSnapshotInfoOutput(infoResult.Stdout), nil
}

func parseSnapshotInfoOutput(output string) snapshotInfoOutput {
	var info snapshotInfoOutput
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "State:") {
			info.State = strings.TrimSpace(strings.TrimPrefix(line, "State:"))
		}
		if strings.HasPrefix(line, "Location:") {
			info.Location = strings.TrimSpace(strings.TrimPrefix(line, "Location:"))
		}
		if strings.HasPrefix(line, "Children:") {
			info.Children = parseSnapshotInfoInt(line, "Children:")
		}
		if strings.HasPrefix(line, "Descendants:") {
			info.Descendants = parseSnapshotInfoInt(line, "Descendants:")
		}
	}
	return info
}

func parseSnapshotInfoInt(line, prefix string) int {
	value := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return n
}

// getExternalSnapshotDiskFiles 获取外部快照关联的磁盘文件列表
func getExternalSnapshotDiskFiles(vmName, snapName string) ([]string, error) {
	result := utils.ExecCommand("virsh", "snapshot-dumpxml", vmName, snapName)
	if result.Error != nil {
		return nil, fmt.Errorf("获取快照XML失败: %s", result.Stderr)
	}

	files, err := parseExternalSnapshotDiskFiles(result.Stdout)
	if err != nil {
		return nil, err
	}
	return files, nil
}

func parseExternalSnapshotDiskFiles(snapshotXML string) ([]string, error) {
	var doc externalSnapshotXML
	if err := xml.Unmarshal([]byte(snapshotXML), &doc); err != nil {
		return nil, fmt.Errorf("解析快照XML失败: %w", err)
	}

	var files []string
	seen := make(map[string]struct{})
	for _, disk := range doc.Disks {
		file := strings.TrimSpace(disk.Source.File)
		if file == "" {
			continue
		}
		if _, ok := seen[file]; ok {
			continue
		}
		seen[file] = struct{}{}
		files = append(files, file)
	}
	return files, nil
}

func parseExternalSnapshotOriginalDiskFiles(snapshotXML string) (map[string]string, error) {
	var doc externalSnapshotXML
	if err := xml.Unmarshal([]byte(snapshotXML), &doc); err != nil {
		return nil, fmt.Errorf("解析快照XML失败: %w", err)
	}

	files := make(map[string]string)
	for _, disk := range doc.Domain.Devices.Disks {
		target := strings.TrimSpace(disk.Target.Dev)
		file := strings.TrimSpace(disk.Source.File)
		if target == "" || file == "" {
			continue
		}
		files[target] = file
	}
	return files, nil
}

// CreateSnapshot 创建快照
// includeMemory 为 true 时保存内存状态（仅运行中的虚拟机有效），为 false 时仅保存磁盘状态
func CreateSnapshot(vmName, snapName, description string, includeMemory bool) error {
	return CreateSnapshotWithOptions(vmName, snapName, description, includeMemory, false, true, nil)
}

// CreateSnapshotWithOptions 创建快照，可在用户确认后自动修复 UEFI pflash NVRAM 格式。
// pauseForMemorySnapshot 控制运行中内存快照是否先主动暂停虚拟机。
func CreateSnapshotWithOptions(vmName, snapName, description string, includeMemory, autoFixNVRAM, pauseForMemorySnapshot bool, progressFn func(int, string)) error {
	if err := EnsureVMNotMigrating(vmName, "创建快照"); err != nil {
		return err
	}
	normalizedName, err := NormalizeSnapshotName(snapName)
	if err != nil {
		return err
	}
	snapName = normalizedName
	args := []string{"snapshot-create-as", vmName, "--name", snapName}
	if description != "" {
		args = append(args, "--description", description)
	}

	// 检测虚拟机当前状态
	stateResult := utils.ExecCommand("virsh", "domstate", vmName)
	isRunning := stateResult.Error == nil && strings.TrimSpace(stateResult.Stdout) == "running"

	if isRunning {
		if includeMemory {
			if unsupported, message, err := CheckInternalSnapshotVirtFSUnsupported(vmName); err != nil {
				return err
			} else if unsupported {
				return fmt.Errorf("%s", message)
			}
			if err := ensureInternalSnapshotNVRAMCompatible(vmName, true); err != nil {
				required, _, checkErr := CheckInternalSnapshotNVRAMRepairRequired(vmName)
				if !autoFixNVRAM || checkErr != nil || !required {
					return err
				}
				if err := autoRepairRunningVMNVRAMForInternalSnapshot(vmName, progressFn); err != nil {
					return err
				}
				stateResult = utils.ExecCommand("virsh", "domstate", vmName)
				isRunning = stateResult.Error == nil && strings.TrimSpace(stateResult.Stdout) == "running"
				if !isRunning {
					return fmt.Errorf("UEFI NVRAM 已修复，但虚拟机重新开机后状态不是 running，请检查虚拟机状态后重试")
				}
				if err := ensureInternalSnapshotNVRAMCompatible(vmName, true); err != nil {
					return err
				}
			}
			if progressFn != nil {
				if pauseForMemorySnapshot {
					progressFn(60, "NVRAM 兼容性检查完成，正在暂停虚拟机并创建内存快照...")
				} else {
					progressFn(60, "NVRAM 兼容性检查完成，正在直接创建内存快照...")
				}
			}
			if pauseForMemorySnapshot {
				// 运行中 + 包含内存：先暂停 VM → 创建内部快照 → 恢复运行。
				// 这样更容易得到一致的内部快照，但快照写入期间业务会处于暂停状态。
				fmt.Printf("[快照] 暂停虚拟机 %s 以创建内部快照...\n", vmName)
				pauseResult := utils.ExecCommand("virsh", "suspend", vmName)
				if pauseResult.Error != nil {
					return fmt.Errorf("暂停虚拟机失败，无法创建内部快照: %s", pauseResult.Stderr)
				}
				if progressFn != nil {
					progressFn(70, "虚拟机已暂停，正在写入内部快照...")
				}

				// 暂停后创建快照（不加 --disk-only，关机/暂停状态下默认创建内部快照）。
				result := utils.ExecCommand("virsh", args...)

				// 无论成功失败，都恢复 VM 运行。
				fmt.Printf("[快照] 恢复虚拟机 %s 运行...\n", vmName)
				resumeResult := utils.ExecCommand("virsh", "resume", vmName)
				if resumeResult.Error != nil {
					fmt.Printf("[警告] 恢复虚拟机运行失败: %s\n", resumeResult.Stderr)
				}

				if result.Error != nil {
					return formatSnapshotCreateError(result.Stderr)
				}
			} else {
				// 实验模式：不主动 suspend，交给 libvirt/QEMU 自行处理运行中内存快照。
				// 该模式能减少面板主动暂停时间，但不同宿主机/libvirt 版本的行为可能不同。
				fmt.Printf("[快照] 不主动暂停虚拟机 %s，直接创建内存快照...\n", vmName)
				result := utils.ExecCommand("virsh", args...)
				if result.Error != nil {
					return formatSnapshotCreateError(result.Stderr)
				}
			}
		} else {
			// 运行中 + 不包含内存：仅磁盘快照（外部）
			// 注意：外部快照不支持 virsh snapshot-revert
			fmt.Printf("[警告] 虚拟机 %s 运行中创建仅磁盘快照，该快照为外部快照，恢复时需要特殊处理\n", vmName)
			args = append(args, "--disk-only")
			result := utils.ExecCommand("virsh", args...)
			if result.Error != nil {
				return formatSnapshotCreateError(result.Stderr)
			}

			// 修正外部快照创建后 overlay 文件的权限
			// libvirt 创建的 overlay 文件默认归 root:root 且权限 600，
			// 需要修改为 libvirt-qemu:kvm，否则 QEMU 进程无法读取导致开机失败
			fixSnapshotDiskPermissions(vmName)
		}
	} else {
		if err := ensureInternalSnapshotNVRAMCompatible(vmName, false); err != nil {
			return err
		}
		// 关机状态下不加额外参数，使用默认的内部快照
		result := utils.ExecCommand("virsh", args...)
		if result.Error != nil {
			return formatSnapshotCreateError(result.Stderr)
		}
	}

	// 创建完成后，检查实际创建的快照类型，向调用者报告
	state, location, err := getSnapshotType(vmName, snapName)
	if err == nil && location == "external" {
		fmt.Printf("[警告] 快照 %s 被创建为外部快照(state=%s, location=%s)，恢复时需要特殊处理\n", snapName, state, location)
	}

	return nil
}

func formatSnapshotCreateError(stderr string) error {
	message := strings.TrimSpace(stderr)
	if message == "" {
		message = "未知错误"
	}
	lower := strings.ToLower(message)
	if strings.Contains(lower, "invalid job id") {
		return fmt.Errorf("创建快照失败: 快照名称包含 libvirt/QEMU 不支持的字符，请使用英文、数字、下划线、点或短横线")
	}
	if strings.Contains(lower, "migration is disabled") && strings.Contains(lower, "virtfs") {
		return fmt.Errorf("创建快照失败: 当前虚拟机正在挂载 9p/VirtFS 共享目录，libvirt 禁止在这种状态创建包含内存的内部快照。请先卸载并移除共享目录，或取消勾选“保存虚拟机内存状态”创建仅磁盘快照")
	}
	return fmt.Errorf("创建快照失败: %s", message)
}

func ensureInternalSnapshotNVRAMCompatible(vmName string, isRunning bool) error {
	xmlContent, err := GetVMInactiveDomainXML(vmName)
	if err != nil {
		return err
	}
	if !domainUsesPflashNVRAM(xmlContent) {
		return nil
	}
	nvramPath := extractDomainNVRAMPath(xmlContent)
	if nvramPath == "" {
		return nil
	}
	xmlFormat := extractDomainNVRAMFormat(xmlContent)
	fileFormat := detectQemuImageFormat(nvramPath)
	if xmlFormat == "qcow2" && (fileFormat == "" || fileFormat == "qcow2") {
		return nil
	}
	if fileFormat == "qcow2" {
		if isRunning {
			return fmt.Errorf("当前虚拟机的 UEFI NVRAM 文件已是 qcow2，但虚拟机配置仍未声明 qcow2。请先正常关机后重新创建内存快照，系统会自动修正配置；修正完成后再开机创建内存快照即可")
		}
		updatedXML := setDomainNVRAMFormat(xmlContent, "qcow2")
		if updatedXML != xmlContent {
			return SetVMInactiveDomainXML(vmName, updatedXML)
		}
		return nil
	}
	if isRunning {
		return fmt.Errorf("当前虚拟机使用 UEFI pflash 且 NVRAM 仍是 raw 格式，libvirt 不支持为这种配置创建内部内存快照。请先正常关机后重新创建内存快照，系统会自动将 NVRAM 转换为 qcow2；转换完成后再开机创建内存快照即可")
	}
	if err := convertExistingNVRAMToQCOW2(nvramPath); err != nil {
		return fmt.Errorf("转换 UEFI NVRAM 为 qcow2 失败: %w", err)
	}
	updatedXML := setDomainNVRAMFormat(xmlContent, "qcow2")
	if updatedXML == xmlContent {
		return nil
	}
	if err := SetVMInactiveDomainXML(vmName, updatedXML); err != nil {
		return fmt.Errorf("更新虚拟机 NVRAM 格式配置失败: %w", err)
	}
	return nil
}

// CheckInternalSnapshotNVRAMRepairRequired 检查运行中内存快照是否需要先修复 UEFI NVRAM。
func CheckInternalSnapshotNVRAMRepairRequired(vmName string) (bool, string, error) {
	stateResult := utils.ExecCommand("virsh", "domstate", vmName)
	if stateResult.Error != nil {
		return false, "", fmt.Errorf("获取虚拟机状态失败: %s", stateResult.Stderr)
	}
	if strings.TrimSpace(stateResult.Stdout) != "running" {
		return false, "", nil
	}

	xmlContent, err := GetVMInactiveDomainXML(vmName)
	if err != nil {
		return false, "", err
	}
	if !domainUsesPflashNVRAM(xmlContent) {
		return false, "", nil
	}
	nvramPath := extractDomainNVRAMPath(xmlContent)
	if nvramPath == "" {
		return false, "", nil
	}
	xmlFormat := extractDomainNVRAMFormat(xmlContent)
	fileFormat := detectQemuImageFormat(nvramPath)
	if xmlFormat == "qcow2" && (fileFormat == "" || fileFormat == "qcow2") {
		return false, "", nil
	}

	message := "当前虚拟机使用 UEFI pflash，NVRAM 不是快照兼容的 qcow2 格式。若继续，系统将正常关机，转换 NVRAM，重新开机，然后继续创建内存快照。关机过程会中断当前业务，确定要立即修复吗？"
	return true, message, nil
}

func autoRepairRunningVMNVRAMForInternalSnapshot(vmName string, progressFn func(int, string)) error {
	if progressFn != nil {
		progressFn(20, "检测到 UEFI NVRAM 需要修复，正在正常关机...")
	}
	if err := ShutdownVM(vmName); err != nil {
		return err
	}
	if err := waitVMShutoff(vmName, 180*time.Second); err != nil {
		return err
	}

	if progressFn != nil {
		progressFn(40, "虚拟机已关机，正在转换 UEFI NVRAM 为 qcow2...")
	}
	if err := ensureInternalSnapshotNVRAMCompatible(vmName, false); err != nil {
		return err
	}

	if progressFn != nil {
		progressFn(50, "NVRAM 修复完成，正在重新开机...")
	}
	if err := StartVM(vmName); err != nil {
		return fmt.Errorf("NVRAM 修复完成，但重新开机失败: %w", err)
	}
	if err := waitVMRunning(vmName, 120*time.Second); err != nil {
		return err
	}
	return nil
}

func waitVMShutoff(vmName string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		stateResult := utils.ExecCommand("virsh", "domstate", vmName)
		if stateResult.Error != nil {
			return fmt.Errorf("获取虚拟机状态失败: %s", stateResult.Stderr)
		}
		state := strings.ToLower(strings.TrimSpace(stateResult.Stdout))
		if state == "shut off" || state == "shutoff" {
			UpdateVMRuntimeState(vmName, "shut off", time.Now())
			return nil
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("已发送正常关机指令，但虚拟机在 180 秒内未关机。为避免强制断电造成数据丢失，请先在系统内关机后再重试")
}

func waitVMRunning(vmName string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		stateResult := utils.ExecCommand("virsh", "domstate", vmName)
		if stateResult.Error != nil {
			return fmt.Errorf("获取虚拟机状态失败: %s", stateResult.Stderr)
		}
		if strings.TrimSpace(stateResult.Stdout) == "running" {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("虚拟机已执行开机，但在 120 秒内未进入 running 状态，请检查虚拟机状态后重试")
}

// RevertSnapshot 恢复快照
func RevertSnapshot(vmName, snapName string) error {
	if err := EnsureVMNotMigrating(vmName, "恢复快照"); err != nil {
		return err
	}
	// 先检查快照类型
	state, location, err := getSnapshotType(vmName, snapName)
	if err != nil {
		return fmt.Errorf("获取快照信息失败: %w", err)
	}

	// 外部快照（disk-snapshot）不能直接使用 virsh snapshot-revert
	if state == "disk-snapshot" || location == "external" {
		return revertExternalSnapshot(vmName, snapName)
	}

	if err := ensureInternalSnapshotRestoreDiskAccess(vmName, snapName); err != nil {
		return err
	}

	// 内部快照可以直接恢复
	result := utils.ExecCommand("virsh", "snapshot-revert", vmName, snapName)
	if result.Error != nil {
		return fmt.Errorf("恢复快照失败: %s", result.Stderr)
	}

	// 恢复后检查 VM 状态，如果是暂停状态（快照在暂停时创建的）则自动恢复运行
	vmState := utils.ExecCommand("virsh", "domstate", vmName)
	if vmState.Error == nil && strings.TrimSpace(vmState.Stdout) == "paused" {
		fmt.Printf("[快照恢复] 虚拟机 %s 处于暂停状态，自动恢复运行\n", vmName)
		resumeResult := utils.ExecCommand("virsh", "resume", vmName)
		if resumeResult.Error != nil {
			fmt.Printf("[警告] 自动恢复运行失败: %s\n", resumeResult.Stderr)
		}
	}

	return nil
}

func ensureInternalSnapshotRestoreDiskAccess(vmName, snapName string) error {
	diskPaths, err := getCurrentVMDiskSourcePaths(vmName)
	if err != nil {
		return err
	}
	snapshotDiskPaths, err := getSnapshotDomainDiskPaths(vmName, snapName)
	if err != nil {
		log.Printf("[快照权限修正] 获取快照 %s 磁盘配置失败: %v", snapName, err)
	}
	diskPaths = append(diskPaths, snapshotDiskPaths...)
	if err := ensureSnapshotDiskAccessForPaths(diskPaths); err != nil {
		return fmt.Errorf("修复快照磁盘访问权限失败: %w", err)
	}
	return nil
}

func getSnapshotDomainDiskPaths(vmName, snapName string) ([]string, error) {
	result := utils.ExecCommand("virsh", "snapshot-dumpxml", vmName, snapName)
	if result.Error != nil {
		return nil, fmt.Errorf("获取快照 XML 失败: %s", result.Stderr)
	}
	files, err := parseExternalSnapshotOriginalDiskFiles(result.Stdout)
	if err != nil {
		return nil, err
	}
	var diskPaths []string
	for _, file := range files {
		diskPaths = append(diskPaths, file)
	}
	return diskPaths, nil
}

// revertExternalSnapshot 恢复外部快照
// 外部快照不能直接用 virsh snapshot-revert，需要手动操作 qcow2 链
func revertExternalSnapshot(vmName, snapName string) error {
	// 1. 获取要恢复的快照的磁盘文件（这些是快照创建时的 overlay 文件）
	snapFiles, err := getExternalSnapshotDiskFiles(vmName, snapName)
	if err != nil {
		return fmt.Errorf("获取快照磁盘信息失败: %w", err)
	}
	if len(snapFiles) == 0 {
		return fmt.Errorf("快照 %s 没有关联的磁盘文件", snapName)
	}

	// 2. 获取快照 XML 中记录的原始磁盘信息（快照创建时 VM 使用的磁盘）
	// 快照 XML 的 <domain> 部分记录了创建快照时 VM 的配置
	snapXmlResult := utils.ExecCommand("virsh", "snapshot-dumpxml", vmName, snapName)
	if snapXmlResult.Error != nil {
		return fmt.Errorf("获取快照XML失败: %s", snapXmlResult.Stderr)
	}

	originalDisks, err := parseExternalSnapshotOriginalDiskFiles(snapXmlResult.Stdout)
	if err != nil {
		return err
	}
	if len(originalDisks) == 0 {
		return fmt.Errorf("无法从快照XML域配置中提取原始磁盘路径")
	}

	// 3. 检查虚拟机是否在运行，需要先关机
	vmState := utils.ExecCommand("virsh", "domstate", vmName)
	if vmState.Error == nil && strings.TrimSpace(vmState.Stdout) == "running" {
		destroyResult := utils.ExecCommand("virsh", "destroy", vmName)
		if destroyResult.Error != nil {
			return fmt.Errorf("关闭虚拟机失败: %s", destroyResult.Stderr)
		}
	}

	// 4. 获取当前 VM 的磁盘列表（当前指向的文件）
	currentDisks := utils.ExecCommand("virsh", "domblklist", vmName, "--details")
	if currentDisks.Error != nil {
		return fmt.Errorf("获取当前磁盘列表失败: %s", currentDisks.Stderr)
	}

	// 解析当前磁盘: type  device  target  source
	type diskInfo struct {
		target string
		source string
	}
	var currentDiskList []diskInfo
	for _, line := range strings.Split(currentDisks.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Type") || strings.HasPrefix(line, "-") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 4 && fields[1] == "disk" {
			currentDiskList = append(currentDiskList, diskInfo{target: fields[2], source: fields[3]})
		}
	}

	// 5. 恢复外部快照的语义是切回快照创建时的磁盘状态。
	// 不能执行 qemu-img commit，否则会把快照后的写入合并回 backing，导致没有真正回滚。
	for _, snapFile := range snapFiles {
		if _, err := os.Stat(snapFile); os.IsNotExist(err) {
			return fmt.Errorf("快照增量文件不存在，无法确认恢复链: %s", snapFile)
		}
	}

	// 6. 为每块恢复目标盘创建新的可写 overlay。
	// 外部快照的恢复点必须只作为 backing 使用，不能让 VM 直接写入恢复点文件，
	// 否则从早期快照恢复后再创建分支快照时，会污染这个早期快照。
	restoreDisks := make(map[string]string, len(originalDisks))
	createdRestoreOverlays := []string{}
	for _, disk := range currentDiskList {
		originalDisk, ok := originalDisks[disk.target]
		if !ok {
			continue
		}
		if _, err := os.Stat(originalDisk); os.IsNotExist(err) {
			return fmt.Errorf("原始磁盘文件不存在: %s", originalDisk)
		}
		restoreOverlay := generateExternalSnapshotRestoreOverlayPath(originalDisk, vmName, disk.target, snapName)
		if err := createExternalSnapshotRestoreOverlay(originalDisk, restoreOverlay); err != nil {
			for _, created := range createdRestoreOverlays {
				_ = os.Remove(created)
			}
			return err
		}
		restoreDisks[disk.target] = restoreOverlay
		createdRestoreOverlays = append(createdRestoreOverlays, restoreOverlay)
		fmt.Printf("[快照恢复] 磁盘 %s 将以 %s 为 backing，切换到新的恢复 overlay %s\n", disk.target, originalDisk, restoreOverlay)
	}

	// 使用 sed 方式批量修改 XML（通过 EDITOR 环境变量）
	// 构建 sed 命令来替换磁盘路径
	sedParts := []string{}
	for _, disk := range currentDiskList {
		restoreDisk, ok := restoreDisks[disk.target]
		if ok && disk.source != restoreDisk {
			// 转义路径中的特殊字符
			escapedOld := strings.ReplaceAll(disk.source, "/", "\\/")
			escapedOld = strings.ReplaceAll(escapedOld, ".", "\\.")
			escapedNew := strings.ReplaceAll(restoreDisk, "/", "\\/")
			sedParts = append(sedParts, fmt.Sprintf("s|%s|%s|g", escapedOld, escapedNew))
		}
	}

	if len(sedParts) > 0 {
		sedCmd := strings.Join(sedParts, "; ")
		shellCmd := fmt.Sprintf("EDITOR=\"sed -i '%s'\" virsh edit %s", sedCmd, utils.ShellSingleQuote(vmName))
		editResult := utils.ExecShell(shellCmd)
		if editResult.Error != nil {
			for _, created := range createdRestoreOverlays {
				_ = os.Remove(created)
			}
			return fmt.Errorf("修改虚拟机磁盘配置失败: %s", editResult.Stderr)
		}
	}

	// 7. 清理 VM XML 中可能残留的 backingStore 自引用
	// 检查并移除 backingStore 中指向自己的情况
	dumpResult := utils.ExecCommand("virsh", "dumpxml", vmName)
	if dumpResult.Error == nil {
		hasBackingStore := strings.Contains(dumpResult.Stdout, "<backingStore")
		if hasBackingStore {
			shellCmd := fmt.Sprintf("EDITOR=\"sed -i '/<backingStore type/,/<\\/backingStore>/d'\" virsh edit %s", utils.ShellSingleQuote(vmName))
			cleanResult := utils.ExecShell(shellCmd)
			if cleanResult.Error != nil {
				fmt.Printf("[警告] 清理 backingStore 失败: %s\n", cleanResult.Stderr)
			}
		}
	}

	// 8. 启动前主动修复恢复后磁盘和快照 overlay 的访问权限。
	// 自定义存储池路径还需要 AppArmor 允许 virt-aa-helper 读取 backing chain。
	restoredDiskPaths := make([]string, 0, len(originalDisks)+len(restoreDisks)+len(snapFiles))
	for _, diskPath := range originalDisks {
		restoredDiskPaths = append(restoredDiskPaths, diskPath)
	}
	for _, diskPath := range restoreDisks {
		restoredDiskPaths = append(restoredDiskPaths, diskPath)
	}
	restoredDiskPaths = append(restoredDiskPaths, snapFiles...)
	if err := ensureSnapshotDiskAccessForPaths(restoredDiskPaths); err != nil {
		return fmt.Errorf("修复快照磁盘访问权限失败: %w", err)
	}

	// 9. 恢复外部快照不自动清理快照元数据或 overlay 文件，避免恢复成功后快照列表被清空。
	// 同步 libvirt 当前快照指针，否则从内部快照恢复到外部快照后，快照树仍会显示父级内部快照为当前。
	currentResult := utils.ExecCommand("virsh", "snapshot-current", vmName, snapName)
	if currentResult.Error != nil {
		return fmt.Errorf("设置当前快照标记失败: %s", currentResult.Stderr)
	}

	// 10. 启动虚拟机
	if err := StartVM(vmName); err != nil {
		return fmt.Errorf("恢复快照后启动虚拟机失败: %s，请检查虚拟机配置", err.Error())
	}

	fmt.Printf("[快照恢复] 虚拟机 %s 已成功恢复到快照 %s 之前的状态并启动\n", vmName, snapName)
	return nil
}

func createExternalSnapshotRestoreOverlay(backingPath, overlayPath string) error {
	if strings.TrimSpace(backingPath) == "" || strings.TrimSpace(overlayPath) == "" {
		return fmt.Errorf("恢复 overlay 或 backing 路径为空")
	}
	if err := os.MkdirAll(path.Dir(overlayPath), 0755); err != nil {
		return fmt.Errorf("创建恢复 overlay 目录失败: %w", err)
	}
	backingFormat := detectQemuImageFormat(backingPath)
	if backingFormat == "" {
		backingFormat = "qcow2"
	}
	result := utils.ExecCommandLongRunning("qemu-img", "create", "-f", "qcow2", "-F", backingFormat, "-b", backingPath, overlayPath)
	if result.Error != nil {
		_ = os.Remove(overlayPath)
		return fmt.Errorf("创建恢复 overlay 失败: %s", result.Stderr)
	}
	if err := ensureSnapshotDiskAccessForPaths([]string{backingPath, overlayPath}); err != nil {
		_ = os.Remove(overlayPath)
		return err
	}
	return nil
}

func generateExternalSnapshotRestoreOverlayPath(backingPath, vmName, target, snapName string) string {
	dir := path.Dir(backingPath)
	base := path.Base(backingPath)
	ext := path.Ext(base)
	name := base
	if ext != "" {
		name = strings.TrimSuffix(base, ext)
	}
	token := make([]byte, 4)
	if _, err := rand.Read(token); err != nil {
		token = []byte(time.Now().Format("150405"))
	}
	return path.Join(dir, fmt.Sprintf(
		"%s.snap_restore_%s_%s_%s_%s.qcow2",
		name,
		sanitizeSnapshotPathPart(vmName),
		sanitizeSnapshotPathPart(target),
		sanitizeSnapshotPathPart(snapName),
		hex.EncodeToString(token),
	))
}

func sanitizeSnapshotPathPart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return regexp.MustCompile(`[^A-Za-z0-9_.-]+`).ReplaceAllString(value, "_")
}

// DeleteSnapshot 删除快照
func DeleteSnapshot(vmName, snapName string) error {
	if err := deleteSnapshot(vmName, snapName, true); err != nil {
		return err
	}
	if err := consolidateActiveSnapshotResidualOverlays(vmName, snapName, true); err != nil {
		return fmt.Errorf("清理当前快照残留 overlay 失败: %w", err)
	}
	if err := cleanupSnapshotResidualFiles(vmName, snapName, false); err != nil {
		return fmt.Errorf("清理快照残留文件失败: %w", err)
	}
	return nil
}

func deleteSnapshot(vmName, snapName string, preserveOtherSnapshots bool) error {
	if err := EnsureVMNotMigrating(vmName, "删除快照"); err != nil {
		return err
	}
	// 先检查快照类型
	info, err := getSnapshotInfo(vmName, snapName)
	if err != nil {
		// 如果获取失败，尝试直接删除
		result := utils.ExecCommand("virsh", "snapshot-delete", vmName, snapName)
		if result.Error != nil {
			if !preserveOtherSnapshots && isInternalSnapshotDiskMismatchDeleteError(result.Stderr) {
				return deleteSnapshotMetadataOnly(vmName, snapName, "删除全部快照时无法读取完整快照信息，且内部快照所在磁盘已不在当前活动链，仅清理 libvirt 元数据")
			}
			return formatSnapshotDeleteError(result.Stderr)
		}
		return nil
	}

	if info.Children > 0 {
		return fmt.Errorf("删除快照失败: 当前快照还有 %d 个子快照，不能直接删除父级快照。请先从快照树最末端的子快照开始处理；如果该快照是外部快照链的父节点，请先恢复/切回目标快照并确认子快照链已清理后再删除", info.Children)
	}

	if info.State == "disk-snapshot" || info.Location == "external" {
		return deleteExternalSnapshot(vmName, snapName, preserveOtherSnapshots)
	}

	// 内部快照直接删除
	result := utils.ExecCommand("virsh", "snapshot-delete", vmName, snapName)
	if result.Error != nil {
		if isInternalSnapshotDiskMismatchDeleteError(result.Stderr) {
			consolidated, err := consolidateActiveExternalOverlays(vmName, preserveOtherSnapshots)
			if err != nil {
				return fmt.Errorf("删除快照失败: 当前 VM 正在使用外部快照 overlay，自动合并并切回原始磁盘失败: %w", err)
			}
			if consolidated {
				retryResult := utils.ExecCommand("virsh", "snapshot-delete", vmName, snapName)
				if retryResult.Error == nil {
					return nil
				}
				if !preserveOtherSnapshots && isInternalSnapshotDiskMismatchDeleteError(retryResult.Stderr) {
					return deleteSnapshotMetadataOnly(vmName, snapName, "已尝试折叠当前外部 overlay，但 libvirt 仍认为该内部快照所在磁盘与当前活动磁盘不一致，删除全部快照时仅清理 libvirt 元数据")
				}
				return formatSnapshotDeleteError(retryResult.Stderr)
			}
			if !preserveOtherSnapshots {
				return deleteSnapshotMetadataOnly(vmName, snapName, "当前活动磁盘已经不再包含该内部快照，删除全部快照时仅清理 libvirt 元数据")
			}
			return fmt.Errorf("删除快照失败: 当前活动磁盘与内部快照所在磁盘不一致，但没有发现可自动合并的外部快照 overlay。请确认当前磁盘链后再删除该内部快照")
		}
		return formatSnapshotDeleteError(result.Stderr)
	}
	return nil
}

// DeleteAllSnapshots 删除虚拟机的全部快照，按快照树叶子节点逐步清理。
func DeleteAllSnapshots(vmName string, progressFn func(int, string)) (int, error) {
	if err := EnsureVMNotMigrating(vmName, "删除全部快照"); err != nil {
		return 0, err
	}

	deleted := 0
	for step := 0; step < 1000; step++ {
		snapshots, err := ListSnapshots(vmName)
		if err != nil {
			return deleted, err
		}
		if len(snapshots) == 0 {
			if _, err := consolidateActiveExternalOverlays(vmName, false); err != nil {
				return deleted, fmt.Errorf("清理当前快照 overlay 失败: %w", err)
			}
			if err := cleanupSnapshotResidualFiles(vmName, "", true); err != nil {
				return deleted, fmt.Errorf("清理快照残留文件失败: %w", err)
			}
			if progressFn != nil {
				progressFn(100, "全部快照已删除")
			}
			return deleted, nil
		}

		leaf := findSnapshotLeafForDelete(snapshots)
		if leaf == nil {
			return deleted, fmt.Errorf("删除全部快照失败: 未找到可删除的叶子快照，请检查快照树状态")
		}
		if progressFn != nil {
			percent := 10 + min(85, deleted*10)
			progressFn(percent, fmt.Sprintf("正在删除快照 %s，剩余 %d 个...", leaf.Name, len(snapshots)))
		}
		if err := deleteSnapshot(vmName, leaf.Name, false); err != nil {
			return deleted, err
		}
		if err := cleanupSnapshotResidualFiles(vmName, leaf.Name, false); err != nil {
			return deleted, fmt.Errorf("清理快照 %s 残留文件失败: %w", leaf.Name, err)
		}
		deleted++
	}
	return deleted, fmt.Errorf("删除全部快照失败: 快照数量或链条状态异常，已达到最大处理轮次")
}

func findSnapshotLeafForDelete(snapshots []SnapshotInfo) *SnapshotInfo {
	for i := range snapshots {
		if snapshots[i].Children == 0 {
			return &snapshots[i]
		}
	}
	return nil
}

func formatSnapshotDeleteError(stderr string) error {
	message := strings.TrimSpace(stderr)
	if message == "" {
		message = "未知错误"
	}
	lower := strings.ToLower(message)
	if strings.Contains(lower, "internal snapshot") && strings.Contains(lower, "not the same as disk image currently used by vm") {
		return fmt.Errorf("删除快照失败: 当前 VM 正在使用的磁盘与目标内部快照所在磁盘不一致，libvirt 不能直接删除该内部快照。请先合并当前外部 overlay，或确认当前磁盘链后再重试")
	}
	return fmt.Errorf("删除快照失败: %s", message)
}

func isInternalSnapshotDiskMismatchDeleteError(stderr string) bool {
	lower := strings.ToLower(strings.TrimSpace(stderr))
	return strings.Contains(lower, "internal snapshot") && strings.Contains(lower, "not the same as disk image currently used by vm")
}

func deleteSnapshotMetadataOnly(vmName, snapName, reason string) error {
	if strings.TrimSpace(reason) != "" {
		fmt.Printf("[快照删除] %s: %s\n", snapName, reason)
	}
	result := utils.ExecCommand("virsh", "snapshot-delete", vmName, snapName, "--metadata")
	if result.Error != nil {
		return fmt.Errorf("删除快照元数据失败: %s", result.Stderr)
	}
	return nil
}

// deleteExternalSnapshot 删除外部快照。
// preserveOtherSnapshots 为 true 时，不能把 overlay commit 回仍被其他外部快照用作恢复点的 backing。
func deleteExternalSnapshot(vmName, snapName string, preserveOtherSnapshots bool) error {
	// 获取快照关联的 overlay 文件
	snapFiles, err := getExternalSnapshotDiskFiles(vmName, snapName)
	if err != nil {
		fmt.Printf("[警告] 获取外部快照文件列表失败: %v\n", err)
	}

	// 获取当前 VM 正在使用的磁盘文件，当前活动 overlay 必须先 blockcommit + pivot，不能只删元数据。
	currentDiskList, diskErr := getCurrentVMDiskSources(vmName)
	if diskErr != nil {
		fmt.Printf("[警告] 获取当前磁盘列表失败: %v\n", diskErr)
	}
	activeDiskTargets := make(map[string]string)
	for _, disk := range currentDiskList {
		if disk.Source != "" && disk.Source != "-" {
			activeDiskTargets[disk.Source] = disk.Target
		}
	}
	stateResult := utils.ExecCommand("virsh", "domstate", vmName)
	isRunning := stateResult.Error == nil && strings.TrimSpace(stateResult.Stdout) == "running"
	protectedBacking, err := getProtectedExternalSnapshotRestoreFiles(vmName, snapName)
	if err != nil {
		return err
	}

	// 当前活动 overlay 删除时要保留当前状态；非活动 leaf overlay 不应该 commit 回 backing，
	// 否则会污染仍然依赖该 backing 的早期外部快照恢复点。
	for _, f := range snapFiles {
		if target, ok := activeDiskTargets[f]; ok {
			chain, err := qemuInfoChain(f)
			if err != nil {
				return fmt.Errorf("读取外部快照磁盘链失败 (%s): %w", f, err)
			}
			backingPath := ""
			if len(chain) >= 2 {
				backingPath = chain[1].Filename
			}
			if isRunning {
				if preserveOtherSnapshots && protectedBacking[backingPath] {
					fmt.Printf("[快照删除] 文件 %s 正在被 VM 使用，且 backing 是其他快照恢复点，执行 blockcopy 独立当前盘\n", f)
					if err := copyActiveExternalOverlayToStandalone(vmName, target, f); err != nil {
						return fmt.Errorf("复制当前活动磁盘失败 (%s): %w", f, err)
					}
					continue
				}
				fmt.Printf("[快照删除] 文件 %s 正在被 VM 使用，执行在线 blockcommit 并 pivot\n", f)
				if err := commitActiveExternalOverlay(vmName, target, f); err != nil {
					return fmt.Errorf("合并当前正在使用的外部快照失败 (%s): %w", f, err)
				}
			} else {
				if len(chain) < 2 {
					return fmt.Errorf("外部快照 %s 没有 backing 文件，无法合并", f)
				}
				if preserveOtherSnapshots && protectedBacking[backingPath] {
					fmt.Printf("[快照删除] 文件 %s 的 backing 是其他快照恢复点，删除 overlay 文件但不 commit\n", f)
					_ = os.Remove(f)
					continue
				}
				if err := commitInactiveExternalOverlay(vmName, f, chain[1].Filename); err != nil {
					return fmt.Errorf("合并关机状态外部快照失败 (%s): %w", f, err)
				}
			}
			continue
		}
		if _, err := os.Stat(f); err == nil {
			if preserveOtherSnapshots {
				fmt.Printf("[快照删除] 文件 %s 不是当前活动磁盘，删除 overlay 文件但不 commit，避免污染早期恢复点\n", f)
				_ = os.Remove(f)
			} else {
				commitResult := utils.ExecCommandLongRunning("qemu-img", "commit", f)
				if commitResult.Error != nil {
					fmt.Printf("[警告] 合并overlay文件 %s 失败: %s\n", f, commitResult.Stderr)
				} else {
					if err := os.Remove(f); err != nil {
						fmt.Printf("[警告] 删除overlay文件 %s 失败: %v\n", f, err)
					} else {
						fmt.Printf("[快照删除] 已合并并删除overlay文件: %s\n", f)
					}
				}
			}
		}
	}

	// 删除快照元数据（对外部快照只能用 --metadata）
	return deleteSnapshotMetadataOnly(vmName, snapName, "外部快照文件已按当前删除策略处理")
}

func consolidateActiveExternalOverlays(vmName string, preserveOtherSnapshots bool) (bool, error) {
	diskList, err := getCurrentVMDiskSources(vmName)
	if err != nil {
		return false, err
	}
	protectedBacking, err := getProtectedExternalSnapshotRestoreFiles(vmName, "")
	if err != nil {
		return false, err
	}

	stateResult := utils.ExecCommand("virsh", "domstate", vmName)
	isRunning := stateResult.Error == nil && strings.TrimSpace(stateResult.Stdout) == "running"

	consolidated := false
	for _, disk := range diskList {
		if disk.Source == "" || disk.Source == "-" {
			continue
		}
		chain, err := qemuInfoChain(disk.Source)
		if err != nil {
			return consolidated, err
		}
		if len(chain) < 2 || strings.TrimSpace(chain[0].BackingFilename) == "" {
			continue
		}
		if !isLikelyExternalSnapshotOverlay(disk.Source) {
			continue
		}
		backingPath := chain[1].Filename
		if isRunning {
			if preserveOtherSnapshots && protectedBacking[backingPath] {
				if err := copyActiveExternalOverlayToStandalone(vmName, disk.Target, disk.Source); err != nil {
					return consolidated, err
				}
			} else {
				if err := commitActiveExternalOverlay(vmName, disk.Target, disk.Source); err != nil {
					return consolidated, err
				}
			}
		} else {
			if preserveOtherSnapshots && protectedBacking[backingPath] {
				return consolidated, fmt.Errorf("当前关机磁盘 %s 的 backing 仍是其他外部快照恢复点，不能 commit 以免污染早期快照", disk.Source)
			}
			if err := commitInactiveExternalOverlay(vmName, disk.Source, chain[1].Filename); err != nil {
				return consolidated, err
			}
		}
		consolidated = true
	}
	return consolidated, nil
}

func consolidateActiveSnapshotResidualOverlays(vmName, snapName string, preserveOtherSnapshots bool) error {
	snapName = strings.TrimSpace(snapName)
	if snapName == "" {
		return nil
	}
	diskList, err := getCurrentVMDiskSources(vmName)
	if err != nil {
		return err
	}
	protectedBacking, err := getProtectedExternalSnapshotRestoreFiles(vmName, snapName)
	if err != nil {
		return err
	}

	stateResult := utils.ExecCommand("virsh", "domstate", vmName)
	isRunning := stateResult.Error == nil && strings.TrimSpace(stateResult.Stdout) == "running"

	for _, disk := range diskList {
		if disk.Source == "" || disk.Source == "-" {
			continue
		}
		if !isLikelyExternalSnapshotOverlay(disk.Source) || !strings.Contains(path.Base(disk.Source), snapName) {
			continue
		}
		chain, err := qemuInfoChain(disk.Source)
		if err != nil {
			return err
		}
		if len(chain) < 2 || strings.TrimSpace(chain[0].BackingFilename) == "" {
			continue
		}
		backingPath := chain[1].Filename
		if isRunning {
			if preserveOtherSnapshots && protectedBacking[backingPath] {
				if err := copyActiveExternalOverlayToStandalone(vmName, disk.Target, disk.Source); err != nil {
					return err
				}
			} else {
				if err := commitActiveExternalOverlay(vmName, disk.Target, disk.Source); err != nil {
					return err
				}
			}
			continue
		}
		if preserveOtherSnapshots && protectedBacking[backingPath] {
			return fmt.Errorf("当前关机磁盘 %s 的 backing 仍是其他外部快照恢复点，不能 commit 以免污染早期快照", disk.Source)
		}
		if err := commitInactiveExternalOverlay(vmName, disk.Source, backingPath); err != nil {
			return err
		}
	}
	return nil
}

func isLikelyExternalSnapshotOverlay(diskPath string) bool {
	name := path.Base(strings.TrimSpace(diskPath))
	return strings.Contains(name, ".snap_") || strings.Contains(name, "-snap_")
}

func cleanupSnapshotResidualFiles(vmName, snapName string, allSnapshots bool) error {
	vmName = strings.TrimSpace(vmName)
	if vmName == "" {
		return nil
	}
	protected := collectSnapshotCleanupProtectedPaths(vmName)
	var removeErrors []string
	for _, root := range snapshotCleanupRoots() {
		if _, err := os.Stat(root); err != nil {
			continue
		}
		err := filepath.WalkDir(root, func(filePath string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			base := path.Base(filepath.ToSlash(filePath))
			if !isManagedSnapshotResidualFileName(vmName, snapName, base, allSnapshots) {
				return nil
			}
			cleanPath := normalizeSnapshotFilePath(filepath.ToSlash(filePath))
			if protected[cleanPath] {
				log.Printf("[快照残留清理] 跳过仍被引用的文件: %s", cleanPath)
				return nil
			}
			if err := os.Remove(filePath); err != nil {
				removeErrors = append(removeErrors, fmt.Sprintf("%s: %v", cleanPath, err))
				return nil
			}
			log.Printf("[快照残留清理] 已删除残留文件: %s", cleanPath)
			return nil
		})
		if err != nil {
			removeErrors = append(removeErrors, fmt.Sprintf("%s: %v", root, err))
		}
	}
	if len(removeErrors) > 0 {
		return fmt.Errorf("%s", strings.Join(removeErrors, "; "))
	}
	return nil
}

func snapshotCleanupRoots() []string {
	return uniqueNonEmptyStrings([]string{
		"/var/lib/libvirt/images",
		hostStorageRoot,
	})
}

func isManagedSnapshotResidualFileName(vmName, snapName, fileName string, allSnapshots bool) bool {
	if !strings.Contains(fileName, vmName) {
		return false
	}
	if !strings.Contains(fileName, ".snap_") && !strings.Contains(fileName, "-snap_") {
		return false
	}
	if allSnapshots || strings.TrimSpace(snapName) == "" {
		return true
	}
	return strings.Contains(fileName, snapName)
}

func collectSnapshotCleanupProtectedPaths(vmName string) map[string]bool {
	protected := make(map[string]bool)
	addProtectedPaths(protected, getCurrentVMDiskSourcePathsOrEmpty(vmName))
	addProtectedPaths(protected, extractCurrentQEMUBlockPaths(vmName))

	dumpResult := utils.ExecCommand("virsh", "dumpxml", vmName)
	if dumpResult.Error == nil {
		addProtectedPaths(protected, extractSourceFilePathsFromXML(dumpResult.Stdout))
	}

	snapshots, err := ListSnapshots(vmName)
	if err == nil {
		for _, snap := range snapshots {
			result := utils.ExecCommand("virsh", "snapshot-dumpxml", vmName, snap.Name)
			if result.Error != nil {
				continue
			}
			addProtectedPaths(protected, extractSourceFilePathsFromXML(result.Stdout))
		}
	}
	return protected
}

func getCurrentVMDiskSourcePathsOrEmpty(vmName string) []string {
	diskPaths, err := getCurrentVMDiskSourcePaths(vmName)
	if err != nil {
		log.Printf("[快照残留清理] 获取当前磁盘路径失败: %v", err)
		return nil
	}
	return diskPaths
}

func addProtectedPaths(protected map[string]bool, diskPaths []string) {
	for _, diskPath := range expandDiskPathsWithBackingChain(diskPaths) {
		diskPath = normalizeSnapshotFilePath(diskPath)
		if diskPath == "" {
			continue
		}
		protected[diskPath] = true
	}
}

func extractCurrentQEMUBlockPaths(vmName string) []string {
	result := utils.ExecCommand("virsh", "qemu-monitor-command", vmName, "--hmp", "info block")
	if result.Error != nil {
		return nil
	}
	return extractAbsoluteFilePaths(result.Stdout)
}

func extractSourceFilePathsFromXML(content string) []string {
	matches := regexp.MustCompile(`<source\b[^>]*\bfile=['"]([^'"]+)['"]`).FindAllStringSubmatch(content, -1)
	files := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) >= 2 {
			files = append(files, strings.TrimSpace(match[1]))
		}
	}
	return uniqueNonEmptyStrings(files)
}

func extractAbsoluteFilePaths(content string) []string {
	matches := regexp.MustCompile(`/[A-Za-z0-9._~+%:=,@/-]+`).FindAllString(content, -1)
	return uniqueNonEmptyStrings(matches)
}

func normalizeSnapshotFilePath(filePath string) string {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" || filePath == "-" {
		return ""
	}
	return path.Clean(filepath.ToSlash(filePath))
}

func getProtectedExternalSnapshotRestoreFiles(vmName, excludeSnapName string) (map[string]bool, error) {
	protected := make(map[string]bool)
	snapshots, err := ListSnapshots(vmName)
	if err != nil {
		return protected, err
	}
	for _, snap := range snapshots {
		if snap.Name == excludeSnapName {
			continue
		}
		if snap.State != "disk-snapshot" && snap.Location != "external" {
			continue
		}
		result := utils.ExecCommand("virsh", "snapshot-dumpxml", vmName, snap.Name)
		if result.Error != nil {
			return protected, fmt.Errorf("获取快照 %s XML 失败: %s", snap.Name, result.Stderr)
		}
		files, err := parseExternalSnapshotOriginalDiskFiles(result.Stdout)
		if err != nil {
			return protected, err
		}
		for _, file := range files {
			if strings.TrimSpace(file) != "" {
				protected[file] = true
			}
		}
	}
	return protected, nil
}

func commitActiveExternalOverlay(vmName, target, overlayPath string) error {
	if err := ensureSnapshotDiskAccessForPaths([]string{overlayPath}); err != nil {
		return err
	}
	result := utils.ExecCommandLongRunning(
		"virsh",
		"blockcommit",
		vmName,
		target,
		"--shallow",
		"--active",
		"--pivot",
		"--verbose",
		"--delete",
	)
	if result.Error != nil {
		return fmt.Errorf("blockcommit 失败: %s", result.Stderr)
	}
	current, err := getCurrentVMDiskSources(vmName)
	if err != nil {
		return err
	}
	for _, disk := range current {
		if disk.Target == target && disk.Source == overlayPath {
			return fmt.Errorf("blockcommit 已完成但磁盘 %s 仍指向 overlay: %s", target, overlayPath)
		}
	}
	_ = os.Remove(overlayPath)
	return nil
}

func copyActiveExternalOverlayToStandalone(vmName, target, overlayPath string) error {
	destPath := generateStandaloneDiskPath(overlayPath)
	if err := ensureSnapshotDiskAccessForPaths([]string{overlayPath, destPath}); err != nil {
		return err
	}
	result := utils.ExecCommandLongRunning(
		"virsh",
		"blockcopy",
		vmName,
		target,
		"--dest",
		destPath,
		"--format",
		"qcow2",
		"--wait",
		"--verbose",
		"--pivot",
	)
	if result.Error != nil {
		_ = os.Remove(destPath)
		return fmt.Errorf("blockcopy 失败: %s", result.Stderr)
	}
	_ = utils.ExecCommand("chown", "libvirt-qemu:kvm", destPath)
	current, err := getCurrentVMDiskSources(vmName)
	if err != nil {
		return err
	}
	for _, disk := range current {
		if disk.Target == target && disk.Source == overlayPath {
			return fmt.Errorf("blockcopy 已完成但磁盘 %s 仍指向 overlay: %s", target, overlayPath)
		}
	}
	_ = os.Remove(overlayPath)
	return nil
}

func generateStandaloneDiskPath(sourcePath string) string {
	dir := path.Dir(sourcePath)
	base := path.Base(sourcePath)
	name := base
	if strings.HasSuffix(name, ".qcow2") {
		name = strings.TrimSuffix(name, ".qcow2")
	}
	return path.Join(dir, fmt.Sprintf("%s.consolidated_%s.qcow2", name, time.Now().Format("20060102_150405")))
}

func commitInactiveExternalOverlay(vmName, overlayPath, backingPath string) error {
	if strings.TrimSpace(backingPath) == "" {
		return fmt.Errorf("overlay %s 的 backing 为空", overlayPath)
	}
	if err := ensureSnapshotDiskAccessForPaths([]string{overlayPath, backingPath}); err != nil {
		return err
	}
	commitResult := utils.ExecCommandLongRunning("qemu-img", "commit", overlayPath)
	if commitResult.Error != nil {
		return fmt.Errorf("qemu-img commit 失败: %s", commitResult.Stderr)
	}
	if err := replaceVMDiskSource(vmName, overlayPath, backingPath); err != nil {
		return err
	}
	_ = os.Remove(overlayPath)
	return nil
}

func replaceVMDiskSource(vmName, oldPath, newPath string) error {
	if oldPath == "" || newPath == "" || oldPath == newPath {
		return nil
	}
	escapedOld := strings.ReplaceAll(oldPath, "/", "\\/")
	escapedOld = strings.ReplaceAll(escapedOld, ".", "\\.")
	escapedNew := strings.ReplaceAll(newPath, "/", "\\/")
	shellCmd := fmt.Sprintf("EDITOR=\"sed -i 's|%s|%s|g'\" virsh edit %s", escapedOld, escapedNew, utils.ShellSingleQuote(vmName))
	editResult := utils.ExecShell(shellCmd)
	if editResult.Error != nil {
		return fmt.Errorf("修改虚拟机磁盘配置失败: %s", editResult.Stderr)
	}
	return nil
}

// fixSnapshotDiskPermissions 修正外部快照创建后的权限和 XML 配置问题
// 1. 修正 overlay 文件权限（libvirt 创建的默认归 root:root，QEMU 无法读取）
// 2. 清理 VM XML 中不完整的 backingStore 标签（避免 AppArmor 无法遍历完整 backing chain）
func fixSnapshotDiskPermissions(vmName string) {
	diskPaths, err := getCurrentVMDiskSourcePaths(vmName)
	if err != nil {
		log.Printf("[快照权限修正] %v", err)
		return
	}
	if err := ensureSnapshotDiskAccessForPaths(diskPaths); err != nil {
		log.Printf("[快照权限修正] 修复磁盘访问权限失败: %v", err)
	}

	// 清理 VM XML 中不完整的 backingStore 标签
	// 创建外部快照后 libvirt 会在 XML 中写入一层 backingStore，但 backing chain 可能更深，
	// 导致 virt-aa-helper 只为第一层 backing file 生成 AppArmor 规则，
	// 最深层的模板文件不在白名单中，QEMU 启动时访问被 AppArmor 拒绝。
	// 清理后让 libvirt 在下次启动时自动检测并生成完整的 backing chain 权限
	dumpResult := utils.ExecCommand("virsh", "dumpxml", vmName)
	if dumpResult.Error == nil && strings.Contains(dumpResult.Stdout, "<backingStore") {
		shellCmd := fmt.Sprintf("EDITOR=\"sed -i '/<backingStore type/,/<\\/backingStore>/d'\" virsh edit %s", utils.ShellSingleQuote(vmName))
		cleanResult := utils.ExecShell(shellCmd)
		if cleanResult.Error != nil {
			log.Printf("[快照权限修正] 清理 backingStore XML 失败: %s", cleanResult.Stderr)
		} else {
			log.Printf("[快照权限修正] 已清理 VM %s 的 backingStore XML", vmName)
		}
	}
}

func getCurrentVMDiskSources(vmName string) ([]vmDiskSource, error) {
	blkResult := utils.ExecCommand("virsh", "domblklist", vmName, "--details")
	if blkResult.Error != nil {
		return nil, fmt.Errorf("获取磁盘列表失败: %s", blkResult.Stderr)
	}

	var diskSources []vmDiskSource
	for _, line := range strings.Split(blkResult.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Type") || strings.HasPrefix(line, "-") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 4 && fields[1] == "disk" {
			diskSources = append(diskSources, vmDiskSource{Target: fields[2], Source: fields[3]})
		}
	}
	return diskSources, nil
}

func getCurrentVMDiskSourcePaths(vmName string) ([]string, error) {
	diskSources, err := getCurrentVMDiskSources(vmName)
	if err != nil {
		return nil, err
	}
	var diskPaths []string
	for _, disk := range diskSources {
		if disk.Source == "" || disk.Source == "-" {
			continue
		}
		diskPaths = append(diskPaths, disk.Source)
	}
	return diskPaths, nil
}

func ensureSnapshotDiskAccessForPaths(diskPaths []string) error {
	diskPaths = expandDiskPathsWithBackingChain(diskPaths)
	if err := ensureLibvirtStorageAppArmorAccessForPaths(diskPaths); err != nil {
		return err
	}

	for _, diskPath := range uniqueNonEmptyStrings(diskPaths) {
		if diskPath == "" || diskPath == "-" {
			continue
		}
		if _, err := os.Stat(diskPath); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("检查磁盘文件失败 %s: %w", diskPath, err)
		}
		// 修正文件权限为 libvirt-qemu:kvm，避免切回原始盘或 overlay 后 QEMU 无法访问。
		chownResult := utils.ExecCommand("chown", "libvirt-qemu:kvm", diskPath)
		if chownResult.Error != nil {
			return fmt.Errorf("chown %s 失败: %s", diskPath, chownResult.Stderr)
		} else {
			log.Printf("[快照权限修正] 已修正 %s 的权限为 libvirt-qemu:kvm", diskPath)
		}
	}
	return nil
}

func expandDiskPathsWithBackingChain(diskPaths []string) []string {
	expanded := append([]string{}, uniqueNonEmptyStrings(diskPaths)...)
	for _, diskPath := range uniqueNonEmptyStrings(diskPaths) {
		if _, err := os.Stat(diskPath); err != nil {
			continue
		}
		chain, err := qemuInfoChain(diskPath)
		if err != nil {
			log.Printf("[快照权限修正] 读取磁盘 backing chain 失败 %s: %v", diskPath, err)
			continue
		}
		for _, item := range chain {
			expanded = append(expanded, item.Filename)
			expanded = append(expanded, item.FullBackingFilename)
			if path.IsAbs(item.BackingFilename) {
				expanded = append(expanded, item.BackingFilename)
			}
		}
	}
	return uniqueNonEmptyStrings(expanded)
}

func ensureLibvirtStorageAppArmorAccessForPaths(diskPaths []string) error {
	roots := managedLibvirtAccessRootsForPaths(diskPaths)
	if len(roots) == 0 {
		return nil
	}
	return ensureLibvirtStorageAppArmorAccess(roots)
}

func containsManagedHostStoragePath(paths []string) bool {
	for _, item := range paths {
		if isManagedHostStoragePath(item) {
			return true
		}
	}
	return false
}

func isManagedHostStoragePath(item string) bool {
	item = strings.TrimSpace(item)
	if item == "" {
		return false
	}
	return isPathWithinRoot(item, hostStorageRoot)
}

func managedLibvirtAccessRootsForPaths(paths []string) []string {
	var roots []string
	for _, root := range managedLibvirtAccessRoots() {
		for _, item := range paths {
			if isPathWithinRoot(item, root) {
				roots = append(roots, root)
				break
			}
		}
	}
	return uniqueNonEmptyStrings(roots)
}

func managedLibvirtAccessRoots() []string {
	roots := []string{hostStorageRoot}
	if config.GlobalConfig != nil && strings.TrimSpace(config.GlobalConfig.TemplateDir) != "" {
		roots = append(roots, config.GlobalConfig.TemplateDir)
	} else {
		roots = append(roots, "/var/lib/libvirt/images/templates")
	}
	return uniqueNonEmptyStrings(roots)
}

func isPathWithinRoot(item, root string) bool {
	item = strings.TrimSpace(item)
	root = strings.TrimSpace(root)
	if item == "" || root == "" {
		return false
	}
	cleanPath := path.Clean(item)
	cleanRoot := path.Clean(root)
	return cleanPath == cleanRoot || strings.HasPrefix(cleanPath, cleanRoot+"/")
}

func ensureLibvirtStorageAppArmorAccess(roots []string) error {
	if _, err := os.Stat("/sys/module/apparmor"); err != nil {
		return nil
	}
	if _, err := os.Stat("/etc/apparmor.d"); err != nil {
		return nil
	}

	changed := false
	var helperRules []string
	var qemuRules []string
	for _, root := range uniqueNonEmptyStrings(roots) {
		storagePath := strings.TrimRight(path.Clean(root), "/")
		helperRules = append(helperRules,
			fmt.Sprintf("%s/ r,", storagePath),
			fmt.Sprintf("%s/**/ r,", storagePath),
			fmt.Sprintf("%s/** r,", storagePath),
		)
		qemuRules = append(qemuRules,
			fmt.Sprintf("%s/ r,", storagePath),
			fmt.Sprintf("%s/**/ r,", storagePath),
			fmt.Sprintf("%s/** rwk,", storagePath),
		)
	}
	helperBlock := buildManagedAppArmorBlock(helperRules)
	qemuBlock := buildManagedAppArmorBlock(qemuRules)

	helperChanged, err := upsertManagedAppArmorBlock(appArmorVirtAAHelperLocalPath, helperBlock)
	if err != nil {
		return fmt.Errorf("写入 virt-aa-helper AppArmor 规则失败: %w", err)
	}
	changed = changed || helperChanged

	qemuChanged, err := upsertManagedAppArmorBlock(appArmorLibvirtQemuStoragePath, qemuBlock)
	if err != nil {
		return fmt.Errorf("写入 libvirt-qemu AppArmor 规则失败: %w", err)
	}
	changed = changed || qemuChanged

	if changed {
		if err := reloadVirtAAHelperAppArmorProfile(); err != nil {
			return err
		}
		log.Printf("[快照权限修正] 已更新 libvirt AppArmor 规则: %s", strings.Join(uniqueNonEmptyStrings(roots), ", "))
	}
	return nil
}

func buildManagedAppArmorBlock(rules []string) string {
	return appArmorManagedBlockBegin + "\n" + strings.Join(rules, "\n") + "\n" + appArmorManagedBlockEnd + "\n"
}

func upsertManagedAppArmorBlock(filePath, block string) (bool, error) {
	if err := os.MkdirAll(path.Dir(filePath), 0755); err != nil {
		return false, err
	}

	existingBytes, err := os.ReadFile(filePath)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	existing := string(existingBytes)
	updated := upsertManagedBlock(existing, block)
	if updated == existing {
		return false, nil
	}
	if err := os.WriteFile(filePath, []byte(updated), 0644); err != nil {
		return false, err
	}
	return true, nil
}

func upsertManagedBlock(existing, block string) string {
	start := strings.Index(existing, appArmorManagedBlockBegin)
	end := strings.Index(existing, appArmorManagedBlockEnd)
	if start >= 0 && end >= start {
		end += len(appArmorManagedBlockEnd)
		if end < len(existing) && existing[end] == '\n' {
			end++
		}
		return existing[:start] + block + existing[end:]
	}

	trimmed := strings.TrimRight(existing, "\n")
	if trimmed == "" {
		return block
	}
	return trimmed + "\n\n" + block
}

func reloadVirtAAHelperAppArmorProfile() error {
	parser, err := exec.LookPath("apparmor_parser")
	if err != nil {
		return nil
	}
	if _, err := os.Stat(appArmorVirtAAHelperProfilePath); err != nil {
		return nil
	}
	result := utils.ExecCommandWithTimeout(parser, 30*time.Second, "-r", appArmorVirtAAHelperProfilePath)
	if result.Error != nil {
		return fmt.Errorf("重载 virt-aa-helper AppArmor 规则失败: %s", result.Stderr)
	}
	return nil
}

func uniqueNonEmptyStrings(values []string) []string {
	var result []string
	seen := make(map[string]struct{})
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
