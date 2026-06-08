package service

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"kvm_console/utils"
)

// ==================== Linux 文件系统 Project 配额 ====================
//
// 使用专用的回环挂载文件系统 + ext4 project quota 实现按目录的配额统计。
// 每个用户分配一个唯一的 project ID（基于系统 UID），
// 用户的 ISO 和文件共享目录都设置相同的 project ID，
// 配额在内核层强制限制写入。
//
// 依赖：quota 工具包（setquota, repquota, quotaon）
// 文件系统要求：ext4 with project,quota feature + prjquota 挂载选项

// 默认存储挂载点
const defaultStorageMountPoint = "/var/lib/kvm-user-storage"

// 默认存储镜像文件路径
const defaultStorageImagePath = "/var/lib/kvm-user-storage.img"

// StorageQuotaInfo 存储配额信息
type StorageQuotaInfo struct {
	UsedBytes  int64 // 已用空间（字节）
	LimitBytes int64 // 硬限制（字节），0 = 不限
}

// GetStorageMountPoint 获取用户存储挂载点
func GetStorageMountPoint() string {
	return defaultStorageMountPoint
}

// GetStorageImagePath 获取存储镜像文件路径
func GetStorageImagePath() string {
	return defaultStorageImagePath
}

// getProjectID 获取用户的 project ID（基于系统 UID）
func getProjectID(username string) (int, error) {
	result := utils.ExecShell(fmt.Sprintf("id -u %s 2>/dev/null", utils.ShellSingleQuote(username)))
	if result.Error != nil || strings.TrimSpace(result.Stdout) == "" {
		return 0, fmt.Errorf("获取用户 %s 的 UID 失败", username)
	}
	uid, err := strconv.Atoi(strings.TrimSpace(result.Stdout))
	if err != nil {
		return 0, fmt.Errorf("解析 UID 失败: %w", err)
	}
	return uid, nil
}

// getProjectName 生成 project 名称
func getProjectName(username string) string {
	return fmt.Sprintf("kvmstore_%s", username)
}

// SetupUserProject 为用户设置 project 配额映射
// 将用户的 ISO 和文件共享目录都绑定到同一个 project ID
func SetupUserProject(username string, dirs []string) error {
	projectID, err := getProjectID(username)
	if err != nil {
		return err
	}
	projectName := getProjectName(username)

	// 更新 /etc/projects（project_id:directory）
	for _, dir := range dirs {
		// 检查是否已存在该映射
		checkResult := utils.ExecShell(fmt.Sprintf("grep -q %d:%s /etc/projects 2>/dev/null", projectID, utils.ShellSingleQuote(dir)))
		if checkResult.Error != nil {
			// 不存在，追加
			utils.ExecShell(fmt.Sprintf("echo %d:%s >> /etc/projects", projectID, utils.ShellSingleQuote(dir)))
		}
	}

	// 更新 /etc/projid（project_name:project_id）
	checkResult := utils.ExecShell(fmt.Sprintf("grep -q %s:%d /etc/projid 2>/dev/null", utils.ShellSingleQuote(projectName), projectID))
	if checkResult.Error != nil {
		utils.ExecShell(fmt.Sprintf("echo %s:%d >> /etc/projid", utils.ShellSingleQuote(projectName), projectID))
	}

	// 对每个目录设置 project ID 和继承属性
	for _, dir := range dirs {
		result := utils.ExecShell(fmt.Sprintf("chattr +P -p %d %s 2>/dev/null", projectID, utils.ShellSingleQuote(dir)))
		if result.Error != nil {
			return fmt.Errorf("设置目录 %s 的 project ID 失败: %s", dir, result.Stderr)
		}
	}

	return nil
}

// SetUserStorageQuota 设置用户的存储配额（project quota）
// limitGB 为 0 表示取消配额限制
func SetUserStorageQuota(username string, limitGB int) error {
	projectID, err := getProjectID(username)
	if err != nil {
		return err
	}

	mountPoint := GetStorageMountPoint()

	if limitGB <= 0 {
		// 取消配额限制
		return clearProjectQuota(projectID, mountPoint)
	}

	// 将 GB 转换为 KB（setquota 使用 KB 为单位）
	limitKB := int64(limitGB) * 1024 * 1024

	// setquota -P <project_id> <block-soft> <block-hard> <inode-soft> <inode-hard> <filesystem>
	result := utils.ExecCommand("setquota", "-P",
		strconv.Itoa(projectID),
		"0",                                  // block soft limit
		strconv.FormatInt(limitKB, 10),        // block hard limit
		"0",                                   // inode soft limit
		"0",                                   // inode hard limit
		mountPoint,
	)
	if result.Error != nil {
		return fmt.Errorf("设置 project 配额失败: %s", result.Stderr)
	}

	return nil
}

// RemoveUserStorageQuota 清除用户的存储配额
func RemoveUserStorageQuota(username string) error {
	projectID, err := getProjectID(username)
	if err != nil {
		// 用户可能已被删除，忽略
		return nil
	}

	mountPoint := GetStorageMountPoint()

	// 清除配额
	clearProjectQuota(projectID, mountPoint)

	// 清理 /etc/projects 和 /etc/projid 中的条目
	projectName := getProjectName(username)
	utils.ExecShell(fmt.Sprintf("sed -i '/^%d:/d' /etc/projects 2>/dev/null", projectID))
	utils.ExecShell(fmt.Sprintf("sed -i '/^%s:/d' /etc/projid 2>/dev/null", utils.ShellSingleQuote(projectName)))

	return nil
}

// clearProjectQuota 清除指定 project 的配额
func clearProjectQuota(projectID int, mountPoint string) error {
	result := utils.ExecCommand("setquota", "-P",
		strconv.Itoa(projectID),
		"0", "0", "0", "0",
		mountPoint,
	)
	if result.Error != nil {
		return fmt.Errorf("清除 project 配额失败: %s", result.Stderr)
	}
	return nil
}

// GetUserStorageUsage 获取用户的存储配额使用情况（通过 project quota）
func GetUserStorageUsage(username string) (*StorageQuotaInfo, error) {
	projectID, err := getProjectID(username)
	if err != nil {
		return nil, err
	}

	mountPoint := GetStorageMountPoint()

	// 使用 repquota -P 获取 project 配额
	result := utils.ExecCommand("repquota", "-Ps", mountPoint)
	if result.Error != nil {
		return nil, fmt.Errorf("repquota 执行失败: %s", result.Stderr)
	}

	// 解析输出，查找目标 project ID
	projectIDStr := strconv.Itoa(projectID)
	projectName := getProjectName(username)
	lines := strings.Split(result.Stdout, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "*") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}

		// 第一个字段是 project 名称或 ID
		if fields[0] != projectName && fields[0] != projectIDStr {
			continue
		}

		// fields[1] = 状态标记（--、+-、-+、++）
		// fields[2] = 已用 blocks（KB）
		// fields[3] = soft limit（KB）
		// fields[4] = hard limit（KB）
		usedKB, err := parseQuotaNumber(fields[2])
		if err != nil {
			return nil, fmt.Errorf("解析已用空间失败: %w", err)
		}

		hardLimitKB, err := parseQuotaNumber(fields[4])
		if err != nil {
			return nil, fmt.Errorf("解析硬限制失败: %w", err)
		}

		return &StorageQuotaInfo{
			UsedBytes:  usedKB * 1024,
			LimitBytes: hardLimitKB * 1024,
		}, nil
	}

	// 用户不在 repquota 输出中
	return &StorageQuotaInfo{UsedBytes: 0, LimitBytes: 0}, nil
}

// parseQuotaNumber 解析配额数值（可能带 * 号表示超出配额）
func parseQuotaNumber(s string) (int64, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "*")

	// repquota -s 模式下可能输出人类可读格式（如 10M、1G）
	re := regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*([KMGT]?)$`)
	matches := re.FindStringSubmatch(strings.ToUpper(s))
	if matches != nil {
		val, err := strconv.ParseFloat(matches[1], 64)
		if err != nil {
			return 0, err
		}
		switch matches[2] {
		case "K":
			return int64(val), nil
		case "M":
			return int64(val * 1024), nil
		case "G":
			return int64(val * 1024 * 1024), nil
		case "T":
			return int64(val * 1024 * 1024 * 1024), nil
		default:
			return int64(val), nil
		}
	}

	return strconv.ParseInt(s, 10, 64)
}

// ==================== 存储文件系统管理 ====================

// IsStorageFilesystemMounted 检查专用存储文件系统是否已挂载
func IsStorageFilesystemMounted() bool {
	mountPoint := GetStorageMountPoint()
	result := utils.ExecShell(fmt.Sprintf("mount | grep -q %s", utils.ShellSingleQuote(" "+mountPoint+" ")))
	return result.Error == nil
}

// InitStorageFilesystem 初始化专用存储文件系统
// 创建镜像文件、格式化、挂载并启用 project 配额
func InitStorageFilesystem(sizeGB int) error {
	imgPath := GetStorageImagePath()
	mountPoint := GetStorageMountPoint()

	// 检查是否已挂载
	if IsStorageFilesystemMounted() {
		return nil
	}

	// 检查镜像文件是否已存在
	checkResult := utils.ExecShell(fmt.Sprintf("test -f %s && echo yes || echo no", utils.ShellSingleQuote(imgPath)))
	if strings.TrimSpace(checkResult.Stdout) != "yes" {
		if sizeGB <= 0 {
			// 默认与根分区大小相同
			rootSizeResult := utils.ExecShell("df -k / | awk 'NR==2{print $2}'")
			if rootSizeResult.Error == nil && strings.TrimSpace(rootSizeResult.Stdout) != "" {
				rootKB, err := strconv.ParseInt(strings.TrimSpace(rootSizeResult.Stdout), 10, 64)
				if err == nil && rootKB > 0 {
					sizeGB = int(rootKB / 1024 / 1024)
				}
			}
			if sizeGB <= 0 {
				sizeGB = 100
			}
		}

		// 创建稀疏文件（不实际占用磁盘空间）
		result := utils.ExecCommand("truncate", "-s", fmt.Sprintf("%dG", sizeGB), imgPath)
		if result.Error != nil {
			return fmt.Errorf("创建存储镜像文件失败: %s", result.Stderr)
		}

		// 格式化为 ext4，启用 project 和 quota 特性
		result = utils.ExecShell(fmt.Sprintf("mkfs.ext4 -O project,quota %s", utils.ShellSingleQuote(imgPath)))
		if result.Error != nil {
			return fmt.Errorf("格式化存储文件系统失败: %s", result.Stderr)
		}
	}

	// 创建挂载点
	utils.ExecCommand("mkdir", "-p", mountPoint)

	// 挂载
	result := utils.ExecCommand("mount", "-o", "loop,prjquota", imgPath, mountPoint)
	if result.Error != nil {
		return fmt.Errorf("挂载存储文件系统失败: %s", result.Stderr)
	}

	// 启用 project 配额
	utils.ExecShell(fmt.Sprintf("quotaon -P %s 2>/dev/null || true", utils.ShellSingleQuote(mountPoint)))

	// 确保 /etc/projects 和 /etc/projid 文件存在
	utils.ExecShell("touch /etc/projects /etc/projid")

	return nil
}

// EnsureStorageFilesystem 确保存储文件系统已挂载
// 如果未挂载但镜像文件存在，自动挂载
func EnsureStorageFilesystem() error {
	if IsStorageFilesystemMounted() {
		return nil
	}

	imgPath := GetStorageImagePath()
	mountPoint := GetStorageMountPoint()

	// 检查镜像文件是否存在
	checkResult := utils.ExecShell(fmt.Sprintf("test -f %s && echo yes || echo no", utils.ShellSingleQuote(imgPath)))
	if strings.TrimSpace(checkResult.Stdout) != "yes" {
		// 镜像不存在，初始化
		return InitStorageFilesystem(0)
	}

	// 挂载
	utils.ExecCommand("mkdir", "-p", mountPoint)
	result := utils.ExecCommand("mount", "-o", "loop,prjquota", imgPath, mountPoint)
	if result.Error != nil {
		return fmt.Errorf("挂载存储文件系统失败: %s", result.Stderr)
	}

	// 启用配额
	utils.ExecShell(fmt.Sprintf("quotaon -P %s 2>/dev/null || true", utils.ShellSingleQuote(mountPoint)))

	return nil
}

// CheckQuotaToolsAvailable 检查配额工具是否可用
func CheckQuotaToolsAvailable() error {
	result := utils.ExecShell("which setquota repquota 2>/dev/null")
	if result.Error != nil || result.Stdout == "" {
		return fmt.Errorf("配额工具未安装，请执行: apt install quota")
	}
	return nil
}

// SyncAllUserQuotas 同步所有用户的配额到文件系统
func SyncAllUserQuotas() error {
	if err := CheckQuotaToolsAvailable(); err != nil {
		return err
	}

	if err := EnsureStorageFilesystem(); err != nil {
		return err
	}

	users, err := ListUsers()
	if err != nil {
		return fmt.Errorf("获取用户列表失败: %w", err)
	}

	var errs []string
	for _, user := range users {
		if err := SetUserStorageQuota(user.Username, user.MaxStorage); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", user.Username, err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("部分用户配额同步失败: %s", strings.Join(errs, "; "))
	}

	return nil
}
