package service

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"kvm_console/model"
	"kvm_console/utils"
)

// ==================== 用户存储池管理 ====================

// 存储池类别常量
const (
	StorageCategoryISO   = "iso"   // ISO 镜像
	StorageCategoryShare = "share" // 文件共享
	StorageCategoryDisk  = "disk"  // 虚拟磁盘
)

// UserStorageInfo 用户存储池信息
type UserStorageInfo struct {
	Initialized bool   `json:"initialized"`  // 是否已初始化
	UsedBytes   int64  `json:"used_bytes"`   // 已用空间（字节）
	UsedDisplay string `json:"used_display"` // 已用空间（人类可读）
	MaxStorage  int    `json:"max_storage"`  // 配额上限（GB），0=不限
	MaxBytes    int64  `json:"max_bytes"`    // 配额上限（字节）
	Readonly    bool   `json:"readonly"`     // 是否只读（超出配额）
	ISODir      string `json:"iso_dir"`      // ISO 目录路径
	ShareDir    string `json:"share_dir"`    // 文件共享目录路径
	DiskDir     string `json:"disk_dir"`     // 虚拟磁盘目录路径
}

// UserFileInfo 用户文件信息
type UserFileInfo struct {
	Name     string `json:"name"`      // 文件名
	Size     int64  `json:"size"`      // 文件大小（字节）
	SizeText string `json:"size_text"` // 文件大小（人类可读）
	ModTime  string `json:"mod_time"`  // 修改时间
	Path     string `json:"path"`      // 完整路径（仅内部使用，不返回给前端）
	// ISO 专属字段
	OSType    string `json:"os_type,omitempty"`    // 系统类型 linux/windows
	OSVariant string `json:"os_variant,omitempty"` // 系统变体
}

// GetUserISODir 获取用户 ISO 目录路径
func GetUserISODir(username string) string {
	return fmt.Sprintf("%s/%s/iso", GetStorageMountPoint(), username)
}

// GetUserShareDir 获取用户文件共享目录路径
func GetUserShareDir(username string) string {
	return fmt.Sprintf("%s/%s/share", GetStorageMountPoint(), username)
}

// GetUserDiskDir 获取用户虚拟磁盘目录路径
func GetUserDiskDir(username string) string {
	return fmt.Sprintf("%s/%s/disk", GetStorageMountPoint(), username)
}

// InitUserStorage 初始化用户存储池目录
func InitUserStorage(username string) error {
	// 确保存储文件系统已挂载
	if err := EnsureStorageFilesystem(); err != nil {
		return fmt.Errorf("存储文件系统未就绪: %w", err)
	}

	isoDir := GetUserISODir(username)
	shareDir := GetUserShareDir(username)
	diskDir := GetUserDiskDir(username)

	// 创建用户目录
	for _, dir := range []string{isoDir, shareDir, diskDir} {
		result := utils.ExecCommand("mkdir", "-p", dir)
		if result.Error != nil {
			return fmt.Errorf("创建目录 %s 失败: %s", dir, result.Stderr)
		}
	}

	// 设置目录权限（project quota 不依赖文件 owner，保持 libvirt-qemu:kvm 确保 VM 可访问）
	for _, dir := range []string{isoDir, shareDir, diskDir} {
		utils.ExecCommand("chown", "libvirt-qemu:kvm", dir)
		utils.ExecCommand("chmod", "775", dir)
	}

	// 设置 project 配额映射（将所有目录绑定到同一个 project ID）
	if err := SetupUserProject(username, []string{isoDir, shareDir, diskDir}); err != nil {
		fmt.Printf("[警告] 设置用户 %s 的 project 映射失败: %v\n", username, err)
	}

	// 设置配额限制
	var user model.User
	if err := model.DB.Where("username = ?", username).First(&user).Error; err == nil {
		if user.MaxStorage > 0 {
			if err := SetUserStorageQuota(username, user.MaxStorage); err != nil {
				fmt.Printf("[警告] 设置用户 %s 存储配额失败: %v\n", username, err)
			}
		}
	}

	return nil
}

// IsStorageInitialized 检查用户存储池是否已初始化
func IsStorageInitialized(username string) bool {
	isoDir := GetUserISODir(username)
	shareDir := GetUserShareDir(username)
	diskDir := GetUserDiskDir(username)
	// 三个目录都存在才算初始化（兼容旧用户：disk 目录不存在时自动创建）
	result1 := utils.ExecShell(fmt.Sprintf("test -d %s && echo yes || echo no", utils.ShellSingleQuote(isoDir)))
	result2 := utils.ExecShell(fmt.Sprintf("test -d %s && echo yes || echo no", utils.ShellSingleQuote(shareDir)))
	result3 := utils.ExecShell(fmt.Sprintf("test -d %s && echo yes || echo no", utils.ShellSingleQuote(diskDir)))

	isoOK := strings.TrimSpace(result1.Stdout) == "yes"
	shareOK := strings.TrimSpace(result2.Stdout) == "yes"
	diskOK := strings.TrimSpace(result3.Stdout) == "yes"

	if !isoOK || !shareOK {
		return false
	}

	// 如果 disk 目录不存在，自动补建
	if !diskOK {
		utils.ExecCommand("mkdir", "-p", diskDir)
		utils.ExecCommand("chown", "libvirt-qemu:kvm", diskDir)
		utils.ExecCommand("chmod", "775", diskDir)
		diskOK = true
	}

	// 确保 disk 目录在 project quota mapping 中（兼容旧用户升级场景）
	if diskOK {
		checkResult := utils.ExecShell(fmt.Sprintf("grep -q %s /etc/projects 2>/dev/null", utils.ShellSingleQuote(diskDir)))
		if checkResult.Error != nil {
			// disk 目录不在 project mapping 中，自动加入
			_ = SetupUserProject(username, []string{diskDir})
			// 对已有文件追溯设置 project ID
			projectID, err := getProjectID(username)
			if err == nil {
				utils.ExecShell(fmt.Sprintf("find %s -exec chattr -p %d {} \\; 2>/dev/null", utils.ShellSingleQuote(diskDir), projectID))
			}
		}
	}

	return true
}

// GetUserStorageInfo 获取用户存储池信息
func GetUserStorageInfo(username string) (*UserStorageInfo, error) {
	// 获取用户信息
	var user model.User
	if err := model.DB.Where("username = ?", username).First(&user).Error; err != nil {
		return nil, fmt.Errorf("用户不存在: %w", err)
	}

	info := &UserStorageInfo{
		Initialized: IsStorageInitialized(username),
		MaxStorage:  user.MaxStorage,
		ISODir:      GetUserISODir(username),
		ShareDir:    GetUserShareDir(username),
		DiskDir:     GetUserDiskDir(username),
	}

	if user.MaxStorage > 0 {
		info.MaxBytes = int64(user.MaxStorage) * 1024 * 1024 * 1024
	}

	if info.Initialized {
		// 通过文件系统配额获取使用量
		quotaInfo, err := GetUserStorageUsage(username)
		if err == nil && quotaInfo != nil {
			info.UsedBytes = quotaInfo.UsedBytes
			// 如果文件系统配额有限额信息，优先使用（与数据库同步）
			if quotaInfo.LimitBytes > 0 {
				info.MaxBytes = quotaInfo.LimitBytes
			}
		} else {
			// 回退：使用 du 统计
			info.UsedBytes = getDirSizeBytes(info.ISODir) + getDirSizeBytes(info.ShareDir) + getDirSizeBytes(info.DiskDir)
		}
		info.UsedDisplay = formatBytes(info.UsedBytes)

		// 检查是否超出配额
		if info.MaxBytes > 0 && info.UsedBytes >= info.MaxBytes {
			info.Readonly = true
		}
	}

	return info, nil
}

// CheckStorageQuota 检查存储配额是否允许写入
// 文件系统配额已在内核层强制限制，此函数用于提前警告用户
func CheckStorageQuota(username string, additionalBytes int64) error {
	var user model.User
	if err := model.DB.Where("username = ?", username).First(&user).Error; err != nil {
		return fmt.Errorf("用户不存在: %w", err)
	}

	// 不限制
	if user.MaxStorage <= 0 {
		return nil
	}

	// 通过文件系统配额检查
	quotaInfo, err := GetUserStorageUsage(username)
	if err == nil && quotaInfo != nil {
		maxBytes := quotaInfo.LimitBytes
		if maxBytes <= 0 {
			maxBytes = int64(user.MaxStorage) * 1024 * 1024 * 1024
		}
		if quotaInfo.UsedBytes+additionalBytes > maxBytes {
			return fmt.Errorf("存储空间不足（已用 %s / 上限 %dGB），请先删除部分文件",
				formatBytes(quotaInfo.UsedBytes), user.MaxStorage)
		}
		return nil
	}

	// 回退：使用 du 统计
	maxBytes := int64(user.MaxStorage) * 1024 * 1024 * 1024
	isoDir := GetUserISODir(username)
	shareDir := GetUserShareDir(username)
	diskDir := GetUserDiskDir(username)
	usedBytes := getDirSizeBytes(isoDir) + getDirSizeBytes(shareDir) + getDirSizeBytes(diskDir)

	if usedBytes+additionalBytes > maxBytes {
		return fmt.Errorf("存储空间不足（已用 %s / 上限 %dGB），请先删除部分文件",
			formatBytes(usedBytes), user.MaxStorage)
	}

	return nil
}

// IsStorageReadonly 判断用户存储是否只读
func IsStorageReadonly(username string) bool {
	var user model.User
	if err := model.DB.Where("username = ?", username).First(&user).Error; err != nil {
		return false
	}
	if user.MaxStorage <= 0 {
		return false
	}

	// 通过文件系统配额检查
	quotaInfo, err := GetUserStorageUsage(username)
	if err == nil && quotaInfo != nil {
		maxBytes := quotaInfo.LimitBytes
		if maxBytes <= 0 {
			maxBytes = int64(user.MaxStorage) * 1024 * 1024 * 1024
		}
		return quotaInfo.UsedBytes >= maxBytes
	}

	// 回退：使用 du 统计
	maxBytes := int64(user.MaxStorage) * 1024 * 1024 * 1024
	isoDir := GetUserISODir(username)
	shareDir := GetUserShareDir(username)
	diskDir := GetUserDiskDir(username)
	usedBytes := getDirSizeBytes(isoDir) + getDirSizeBytes(shareDir) + getDirSizeBytes(diskDir)
	return usedBytes >= maxBytes
}

// ListUserFiles 列出用户指定类别的文件
func ListUserFiles(username, category string) ([]UserFileInfo, error) {
	var dir string
	switch category {
	case StorageCategoryISO:
		dir = GetUserISODir(username)
	case StorageCategoryShare:
		dir = GetUserShareDir(username)
	case StorageCategoryDisk:
		dir = GetUserDiskDir(username)
	default:
		return nil, fmt.Errorf("未知的存储类别: %s", category)
	}

	// 检查目录是否存在
	checkResult := utils.ExecShell(fmt.Sprintf("test -d %s && echo yes || echo no", utils.ShellSingleQuote(dir)))
	if strings.TrimSpace(checkResult.Stdout) != "yes" {
		return []UserFileInfo{}, nil
	}

	// 列出文件
	result := utils.ExecShell(fmt.Sprintf(
		"find '%s' -maxdepth 1 -type f -printf '%%f|%%s|%%T@\\n' 2>/dev/null | sort", dir))
	if result.Error != nil || result.Stdout == "" {
		return []UserFileInfo{}, nil
	}

	var files []UserFileInfo
	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) < 3 {
			continue
		}

		name := parts[0]
		sizeBytes, _ := strconv.ParseInt(parts[1], 10, 64)
		modTimeUnix, _ := strconv.ParseFloat(parts[2], 64)

		file := UserFileInfo{
			Name:     name,
			Size:     sizeBytes,
			SizeText: formatBytes(sizeBytes),
			ModTime:  time.Unix(int64(modTimeUnix), 0).Format("2006-01-02 15:04:05"),
			Path:     filepath.Join(dir, name),
		}

		// ISO 类别额外推断系统类型
		if category == StorageCategoryISO && strings.HasSuffix(strings.ToLower(name), ".iso") {
			file.OSType, file.OSVariant = inferOSFromISO(strings.ToLower(name))
		}

		files = append(files, file)
	}

	return files, nil
}

// DeleteUserFile 删除用户的指定文件
func DeleteUserFile(username, category, filename string) error {
	var dir string
	switch category {
	case StorageCategoryISO:
		dir = GetUserISODir(username)
	case StorageCategoryShare:
		dir = GetUserShareDir(username)
	case StorageCategoryDisk:
		dir = GetUserDiskDir(username)
	default:
		return fmt.Errorf("未知的存储类别: %s", category)
	}

	// 安全检查：文件名不能包含路径分隔符
	if strings.Contains(filename, "/") || strings.Contains(filename, "..") {
		return fmt.Errorf("非法文件名: %s", filename)
	}

	filePath := filepath.Join(dir, filename)

	// 检查文件是否存在
	checkResult := utils.ExecShell(fmt.Sprintf("test -f %s && echo yes || echo no", utils.ShellSingleQuote(filePath)))
	if strings.TrimSpace(checkResult.Stdout) != "yes" {
		return fmt.Errorf("文件不存在: %s", filename)
	}

	result := utils.ExecShell(fmt.Sprintf("rm -f %s", utils.ShellSingleQuote(filePath)))
	if result.Error != nil {
		return fmt.Errorf("删除文件失败: %s", result.Stderr)
	}

	return nil
}

// GetUserFilePath 获取用户文件的完整路径（用于下载）
func GetUserFilePath(username, category, filename string) (string, error) {
	var dir string
	switch category {
	case StorageCategoryISO:
		dir = GetUserISODir(username)
	case StorageCategoryShare:
		dir = GetUserShareDir(username)
	case StorageCategoryDisk:
		dir = GetUserDiskDir(username)
	default:
		return "", fmt.Errorf("未知的存储类别: %s", category)
	}

	// 安全检查
	if strings.Contains(filename, "/") || strings.Contains(filename, "..") {
		return "", fmt.Errorf("非法文件名: %s", filename)
	}

	filePath := filepath.Join(dir, filename)
	checkResult := utils.ExecShell(fmt.Sprintf("test -f %s && echo yes || echo no", utils.ShellSingleQuote(filePath)))
	if strings.TrimSpace(checkResult.Stdout) != "yes" {
		return "", fmt.Errorf("文件不存在: %s", filename)
	}

	return filePath, nil
}

// GetUserISOs 获取用户的 ISO 列表（给 VM 创建用，格式兼容 ISOFileInfo）
func GetUserISOs(username string) []ISOFileInfo {
	dir := GetUserISODir(username)

	result := utils.ExecShell(fmt.Sprintf(
		"find %s -maxdepth 1 -name '*.iso' -type f 2>/dev/null", utils.ShellSingleQuote(dir)))
	if result.Error != nil || result.Stdout == "" {
		return []ISOFileInfo{}
	}

	var isos []ISOFileInfo
	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		iso := buildISOInfo(line, fmt.Sprintf("user:%s", username))
		isos = append(isos, iso)
	}

	return isos
}

// MountStorageToVM 挂载用户存储池目录到虚拟机
func MountStorageToVM(username, vmName, category string, readonly bool) error {
	var hostPath string
	var tag string

	switch category {
	case StorageCategoryISO:
		hostPath = GetUserISODir(username)
		tag = fmt.Sprintf("user_%s_iso", username)
	case StorageCategoryShare:
		hostPath = GetUserShareDir(username)
		tag = fmt.Sprintf("user_%s_share", username)
	default:
		return fmt.Errorf("未知的存储类别: %s", category)
	}

	// 检查目录是否存在
	checkResult := utils.ExecShell(fmt.Sprintf("test -d %s && echo yes || echo no", utils.ShellSingleQuote(hostPath)))
	if strings.TrimSpace(checkResult.Stdout) != "yes" {
		return fmt.Errorf("存储池目录不存在，请先初始化存储池")
	}

	// 调用已有的共享目录服务
	securityModel := "mapped"
	return AddShare(vmName, hostPath, tag, securityModel, readonly)
}

// UnmountStorageFromVM 卸载用户存储池目录
func UnmountStorageFromVM(vmName, tag string) error {
	return RemoveShare(vmName, tag)
}

// ==================== 辅助函数 ====================

// getDirSizeBytes 获取目录总大小（字节）
func getDirSizeBytes(dir string) int64 {
	result := utils.ExecShell(fmt.Sprintf("du -sb %s 2>/dev/null | awk '{print $1}'", utils.ShellSingleQuote(dir)))
	if result.Error != nil || result.Stdout == "" {
		return 0
	}
	size, _ := strconv.ParseInt(strings.TrimSpace(result.Stdout), 10, 64)
	return size
}

// formatBytes 格式化字节为人类可读
func formatBytes(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
		TB = GB * 1024
	)
	switch {
	case bytes >= TB:
		return fmt.Sprintf("%.2f TB", float64(bytes)/float64(TB))
	case bytes >= GB:
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.2f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.2f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// FormatBytesPublic 格式化字节数为人类可读字符串（公开方法）
func FormatBytesPublic(bytes int64) string {
	return formatBytes(bytes)
}
