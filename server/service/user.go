package service

import (
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"kvm_console/config"
	"kvm_console/model"
	"kvm_console/utils"
)

func isExistingVMAccessUser(username string) bool {
	username = strings.TrimSpace(username)
	if username == "" {
		return false
	}
	if model.DB == nil {
		return true
	}
	var user model.User
	return model.DB.Select("id").Where("username = ?", username).First(&user).Error == nil
}

// VMUserInfo 用户信息（含分配的虚拟机和配额）
type VMUserInfo struct {
	ID                         uint                            `json:"id"`
	Username                   string                          `json:"username"`
	Email                      string                          `json:"email"`
	Role                       string                          `json:"role"`
	CloudType                  string                          `json:"cloud_type"`
	DedicatedVPCSwitchID       uint                            `json:"dedicated_vpc_switch_id"`
	Status                     string                          `json:"status"`
	MaxCPU                     int                             `json:"max_cpu"`
	MaxMemory                  int                             `json:"max_memory"`
	MaxDisk                    int                             `json:"max_disk"`
	MaxVM                      int                             `json:"max_vm"`
	MaxStorage                 int                             `json:"max_storage"`
	MaxRuntimeHours            int                             `json:"max_runtime_hours"`
	EnablePortForward          bool                            `json:"enable_port_forward"`
	MaxPortForwards            int                             `json:"max_port_forwards"`
	MaxSnapshots               int                             `json:"max_snapshots"`
	MaxBandwidthUp             float64                         `json:"max_bandwidth_up"`
	MaxBandwidthDown           float64                         `json:"max_bandwidth_down"`
	MaxTrafficDown             float64                         `json:"max_traffic_down"`
	MaxTrafficUp               float64                         `json:"max_traffic_up"`
	MaxPublicIPs               int                             `json:"max_public_ips"`
	SSHEnabled                 bool                            `json:"ssh_enabled"`
	VMs                        []string                        `json:"vms"`
	Quota                      *QuotaUsage                     `json:"quota"`
	LightweightVMQuotas        []model.LightweightVMQuota      `json:"lightweight_quotas,omitempty"`
	LightweightVMRegistrations []LightweightVMRegistrationView `json:"lightweight_vm_registrations,omitempty"`
}

// UserStatusChangeResult 用户状态变更结果
type UserStatusChangeResult struct {
	Username   string   `json:"username"`
	Status     string   `json:"status"`
	StoppedVMs []string `json:"stopped_vms,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
}

// ListUsers 获取用户列表（含配额信息）
func ListUsers() ([]VMUserInfo, error) {
	var users []model.User
	if err := model.DB.Find(&users).Error; err != nil {
		return nil, err
	}

	var result []VMUserInfo
	for _, u := range users {
		info := VMUserInfo{
			ID:                   u.ID,
			Username:             u.Username,
			Email:                u.Email,
			Role:                 u.Role,
			CloudType:            NormalizeCloudType(u.CloudType),
			DedicatedVPCSwitchID: u.DedicatedVPCSwitchID,
			Status:               u.Status,
			MaxCPU:               u.MaxCPU,
			MaxMemory:            u.MaxMemory,
			MaxDisk:              u.MaxDisk,
			MaxVM:                u.MaxVM,
			MaxStorage:           u.MaxStorage,
			MaxRuntimeHours:      u.MaxRuntimeHours,
			EnablePortForward:    u.EnablePortForward,
			MaxPortForwards:      u.MaxPortForwards,
			MaxSnapshots:         u.MaxSnapshots,
			MaxBandwidthUp:       u.MaxBandwidthUp,
			MaxBandwidthDown:     u.MaxBandwidthDown,
			MaxTrafficDown:       u.MaxTrafficDown,
			MaxTrafficUp:         u.MaxTrafficUp,
			MaxPublicIPs:         u.MaxPublicIPs,
			SSHEnabled:           u.SSHEnabled,
		}

		// 读取用户的虚拟机分配列表
		if u.Role != "admin" {
			info.VMs = GetUserVMList(u.Username)

			// 弹性云用户显示用户级配额，轻量云改为单 VM 配额。
			if !IsLightweightCloudType(u.CloudType) {
				if quota, err := GetUserQuotaUsage(u.Username); err == nil {
					info.Quota = quota
				}
			} else {
				model.DB.Where("username = ?", u.Username).Find(&info.LightweightVMQuotas)
				for i := range info.LightweightVMQuotas {
					fillLightweightVMQuotaRuntime(&info.LightweightVMQuotas[i])
				}
				if regs, err := ListLightweightVMRegistrations(u.Username, true); err == nil {
					info.LightweightVMRegistrations = regs
				}
			}
		} else {
			// 管理员也填充存储配额使用情况
			if quota, err := GetUserQuotaUsage(u.Username); err == nil {
				info.Quota = quota
			}
		}

		result = append(result, info)
	}

	return result, nil
}

// CreateSystemUser 创建系统用户并添加到数据库
func CreateSystemUser(username, password, role string, maxCPU, maxMemory, maxDisk, maxVM, maxStorage, maxRuntimeHours int, enablePortForward bool, maxPortForwards, maxSnapshots int, maxBandwidthUp, maxBandwidthDown, maxTrafficDown, maxTrafficUp float64) error {
	if role == "" {
		role = "user"
	}

	// 检查用户名是否已存在（未删除的）
	var count int64
	if err := model.DB.Model(&model.User{}).Where("username = ?", username).Count(&count).Error; err != nil {
		return fmt.Errorf("检查用户名失败: %w", err)
	}
	if count > 0 {
		return fmt.Errorf("用户名 %s 已存在", username)
	}

	// 清理同名的软删除记录（避免 UNIQUE 约束冲突）
	model.DB.Unscoped().Where("username = ? AND deleted_at IS NOT NULL", username).Delete(&model.User{})

	// 密码加密
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("密码加密失败: %w", err)
	}

	// 创建数据库记录
	user := model.User{
		Username:          username,
		PasswordHash:      string(hashedPassword),
		Email:             "",
		Role:              role,
		CloudType:         CloudTypeElastic,
		Status:            UserStatusActive,
		MaxCPU:            maxCPU,
		MaxMemory:         maxMemory,
		MaxDisk:           maxDisk,
		MaxVM:             maxVM,
		MaxStorage:        maxStorage,
		MaxRuntimeHours:   maxRuntimeHours,
		EnablePortForward: enablePortForward,
		MaxPortForwards:   maxPortForwards,
		MaxSnapshots:      maxSnapshots,
		MaxBandwidthUp:    maxBandwidthUp,
		MaxBandwidthDown:  maxBandwidthDown,
		MaxTrafficDown:    maxTrafficDown,
		MaxTrafficUp:      maxTrafficUp,
	}
	if err := model.DB.Create(&user).Error; err != nil {
		return fmt.Errorf("创建用户失败: %w", err)
	}

	if err := provisionSystemUserResources(&user, password); err != nil {
		return err
	}
	if user.Role == "user" && !IsLightweightCloudType(user.CloudType) {
		if _, err := EnsureDefaultSecurityGroup(user.Username); err != nil {
			return err
		}
		if _, err := EnsureDefaultVPCSwitch(user.Username); err != nil {
			return err
		}
	}
	return nil
}

func provisionSystemUserResources(user *model.User, password string) error {
	// 根据角色创建系统用户
	if user.Role == "user" {
		// 确保 vmoperator 组存在
		utils.ExecCommand("groupadd", "-f", "vmoperator")

		// 创建系统用户（加入 kvm 组以便 sudo -u 执行导出时有存储目录写权限）
		// 默认 shell 设为 nologin，禁止 SSH 登录；管理员可通过面板 SSH 开关切换为 /bin/bash
		result := utils.ExecCommand("useradd", "-m", "-s", sshShellNologin,
			"-G", "vmoperator,libvirt,kvm", user.Username)
		if result.Error != nil {
			// 用户可能已存在，更新组和 shell
			utils.ExecCommand("usermod", "-aG", "vmoperator,libvirt,kvm",
				"-s", sshShellNologin, user.Username)
		}

		// 设置系统密码
		utils.ExecShell(fmt.Sprintf("echo '%s:%s' | chpasswd", user.Username, password))

		// 创建 VM 访问配置目录
		utils.ExecCommand("mkdir", "-p", config.GlobalConfig.VMAccessDir)

		// 初始化空的 VM 分配文件
		utils.ExecShell(fmt.Sprintf("touch '%s/%s'", config.GlobalConfig.VMAccessDir, user.Username))

		// 创建用户后同步 SSH 拒绝配置（默认禁止 SSH）
		regenerateSSHDenyConfig()
	}

	// 设置文件系统存储配额（所有角色都支持）
	if user.MaxStorage > 0 {
		if err := SetUserStorageQuota(user.Username, user.MaxStorage); err != nil {
			fmt.Printf("[警告] 设置用户 %s 文件系统配额失败: %v\n", user.Username, err)
		}
	}

	return nil
}

// FindVMOwner 根据 VM 名称查找归属用户
func FindVMOwner(vmName string) string {
	vmAccessDir := config.GlobalConfig.VMAccessDir
	lsResult := utils.ExecShell(fmt.Sprintf("ls '%s' 2>/dev/null", vmAccessDir))
	if lsResult.Error != nil || lsResult.Stdout == "" {
		return ""
	}
	for _, username := range strings.Split(lsResult.Stdout, "\n") {
		username = strings.TrimSpace(username)
		if username == "" {
			continue
		}
		if !isExistingVMAccessUser(username) {
			continue
		}
		vms := GetUserVMList(username)
		for _, vm := range vms {
			if vm == vmName {
				return username
			}
		}
	}
	return ""
}

// AssignVMsToUser 分配虚拟机给用户
func AssignVMsToUser(username string, vmNames []string) error {
	return AssignVMsToUserWithQuotas(username, vmNames, nil)
}

func AssignVMsToUserWithQuotas(username string, vmNames []string, lightweightQuotas []LightweightVMQuotaRequest) error {
	var user model.User
	if err := model.DB.Where("username = ?", username).First(&user).Error; err != nil {
		return fmt.Errorf("用户不存在: %w", err)
	}

	// 写入分配文件
	content := strings.Join(vmNames, "\n")
	utils.ExecShell(fmt.Sprintf("echo '%s' > '%s/%s'",
		content, config.GlobalConfig.VMAccessDir, username))

	// 重新生成 polkit 规则
	if err := regeneratePolkitRules(); err != nil {
		return err
	}
	defer SyncVMCacheOwnersForAssignment(username, vmNames)
	if !IsLightweightCloudType(user.CloudType) {
		for _, vmName := range vmNames {
			if IsLightweightCloudVM(vmName) {
				CleanupVMVPCBinding(vmName)
				CleanupLightweightVMResources(vmName)
			}
		}
		return nil
	}

	quotaByVM := make(map[string]LightweightVMQuotaRequest)
	for _, req := range lightweightQuotas {
		req = NormalizeLightweightVMQuotaRequest(req)
		if req.VMName != "" {
			quotaByVM[req.VMName] = req
		}
	}
	vmSet := make(map[string]bool)
	for _, vmName := range vmNames {
		vmName = strings.TrimSpace(vmName)
		if vmName == "" {
			continue
		}
		vmSet[vmName] = true
		req, ok := quotaByVM[vmName]
		if !ok {
			req = defaultLightweightVMQuota(vmName)
		}
		req.VMName = vmName
		if _, err := UpsertLightweightVMQuota(username, req); err != nil {
			return err
		}
		if err := EnsureLightweightVMNetwork(username, vmName); err != nil {
			return err
		}
	}

	var existing []model.LightweightVMQuota
	model.DB.Where("username = ?", username).Find(&existing)
	for _, quota := range existing {
		if !vmSet[quota.VMName] {
			CleanupLightweightVMResources(quota.VMName)
		}
	}
	return nil
}

// UpdateUserStatus 更新用户状态
func UpdateUserStatus(username, targetStatus string) error {
	targetStatus = strings.TrimSpace(targetStatus)
	if targetStatus != UserStatusActive && targetStatus != UserStatusDisabled {
		return fmt.Errorf("不支持的用户状态: %s", targetStatus)
	}

	var user model.User
	if err := model.DB.Where("username = ?", username).First(&user).Error; err != nil {
		return fmt.Errorf("用户 %s 不存在", username)
	}
	if user.Role == "admin" {
		return fmt.Errorf("不能修改管理员状态")
	}
	if user.Status == UserStatusPendingInvite {
		return fmt.Errorf("待激活用户不支持封禁或解封")
	}

	now := time.Now()
	updates := map[string]interface{}{
		"status":                   targetStatus,
		"login_verified_until":     nil,
		"high_risk_verified_until": nil,
		"security_updated_at":      &now,
	}
	if err := model.DB.Model(&model.User{}).Where("id = ?", user.ID).Updates(updates).Error; err != nil {
		return fmt.Errorf("更新用户状态失败: %w", err)
	}
	return nil
}

// DisableUserAccount 封禁用户并关闭其运行中的资源
func DisableUserAccount(username string, progressFn func(int, string)) (*UserStatusChangeResult, error) {
	if progressFn == nil {
		progressFn = func(int, string) {}
	}

	var user model.User
	if err := model.DB.Where("username = ?", username).First(&user).Error; err != nil {
		return nil, fmt.Errorf("用户 %s 不存在", username)
	}
	if user.Role == "admin" {
		return nil, fmt.Errorf("不能封禁管理员用户")
	}
	if user.Status == UserStatusPendingInvite {
		return nil, fmt.Errorf("待激活用户不支持封禁")
	}

	result := &UserStatusChangeResult{
		Username: username,
		Status:   UserStatusDisabled,
	}

	progressFn(5, fmt.Sprintf("正在封禁用户 %s ...", username))
	if err := UpdateUserStatus(username, UserStatusDisabled); err != nil {
		return nil, err
	}

	progressFn(15, "正在关闭用户 SSH 访问...")
	if err := SetUserSSH(username, false); err != nil {
		result.Warnings = append(result.Warnings, "关闭 SSH 访问失败: "+err.Error())
	}

	progressFn(35, "正在关闭用户虚拟机...")
	result.StoppedVMs, result.Warnings = stopUserVMsForDisable(username, result.Warnings)

	progressFn(100, "用户已封禁，运行中的虚拟机已关闭")
	return result, nil
}

func stopUserVMsForDisable(username string, warnings []string) ([]string, []string) {
	vmNames := GetUserVMList(username)
	if len(vmNames) == 0 {
		return nil, warnings
	}

	var stopped []string
	for _, vmName := range vmNames {
		stateResult := utils.ExecCommand("virsh", "domstate", vmName)
		if stateResult.Error != nil {
			warnings = append(warnings, fmt.Sprintf("获取虚拟机 %s 状态失败: %s", vmName, stateResult.Stderr))
			continue
		}

		state := strings.ToLower(strings.TrimSpace(stateResult.Stdout))
		if state == "" || strings.Contains(state, "shut off") {
			continue
		}

		needForceOff := strings.Contains(state, "paused")
		if !needForceOff {
			if err := ShutdownVM(vmName); err != nil {
				needForceOff = true
			} else if !waitVMShutdownForDisable(vmName, 40*time.Second) {
				needForceOff = true
			}
		}

		if needForceOff {
			if err := DestroyVM(vmName); err != nil {
				warnings = append(warnings, fmt.Sprintf("关闭虚拟机 %s 失败: %s", vmName, err.Error()))
				continue
			}
		}

		stopped = append(stopped, vmName)
	}

	return stopped, warnings
}

func waitVMShutdownForDisable(vmName string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		stateResult := utils.ExecCommand("virsh", "domstate", vmName)
		if stateResult.Error == nil {
			state := strings.ToLower(strings.TrimSpace(stateResult.Stdout))
			if state == "" || strings.Contains(state, "shut off") {
				return true
			}
		}
		time.Sleep(2 * time.Second)
	}
	return false
}

// DeleteSystemUser 删除用户及其所有资产（虚拟机、存储池等）
// progressFn 可选，用于在异步任务中上报进度
func DeleteSystemUser(username string, progressFn func(int, string)) error {
	if progressFn == nil {
		progressFn = func(int, string) {} // 空实现，避免 nil panic
	}

	// 第一步：删除用户的所有虚拟机
	progressFn(5, "正在获取用户虚拟机列表...")
	userVMs := GetUserVMList(username)
	if len(userVMs) > 0 {
		totalVMs := len(userVMs)
		for i, vmName := range userVMs {
			vmProgress := 5 + (i*35)/totalVMs
			progressFn(vmProgress, fmt.Sprintf("正在删除虚拟机 %s (%d/%d)...", vmName, i+1, totalVMs))
			if err := DeleteVM(vmName); err != nil {
				fmt.Printf("[警告] 删除用户 %s 的虚拟机 %s 失败: %v\n", username, vmName, err)
				// 继续删除其他虚拟机，不中断流程
			}
		}
		progressFn(40, fmt.Sprintf("已删除 %d 台虚拟机", totalVMs))
	} else {
		progressFn(40, "用户没有虚拟机")
	}

	// 第二步：清理用户流量记录
	progressFn(50, "正在清理用户流量记录...")
	cleanupUserTrafficRecords(username)

	// 第三步：清理用户网络资源
	progressFn(55, "正在清理用户 VPC/OVS 网络资源...")
	if err := CleanupUserNetworkResources(username, userVMs); err != nil {
		fmt.Printf("[警告] 清理用户 %s 网络资源失败: %v\n", username, err)
	}

	// 第四步：删除用户存储池目录
	progressFn(60, "正在清理用户存储池...")
	userStorageDir := fmt.Sprintf("%s/%s", GetStorageMountPoint(), username)
	checkResult := utils.ExecShell(fmt.Sprintf("test -d '%s' && echo yes || echo no", userStorageDir))
	if strings.TrimSpace(checkResult.Stdout) == "yes" {
		result := utils.ExecShell(fmt.Sprintf("rm -rf '%s'", userStorageDir))
		if result.Error != nil {
			fmt.Printf("[警告] 删除用户 %s 存储池目录失败: %s\n", username, result.Stderr)
		}
	}

	// 第五步：清除文件系统配额
	progressFn(70, "正在清理文件系统配额...")
	if err := RemoveUserStorageQuota(username); err != nil {
		fmt.Printf("[警告] 清除用户 %s 文件系统配额失败: %v\n", username, err)
	}

	// 第六步：删除数据库记录
	progressFn(80, "正在删除用户数据库记录...")
	if err := model.DB.Where("username = ?", username).Delete(&model.User{}).Error; err != nil {
		return fmt.Errorf("删除用户数据库记录失败: %w", err)
	}

	// 第七步：删除 VM 分配文件
	progressFn(85, "正在清理 VM 访问配置...")
	utils.ExecShell(fmt.Sprintf("rm -f '%s/%s'", config.GlobalConfig.VMAccessDir, username))

	// 第八步：删除系统用户
	progressFn(90, "正在删除系统用户...")
	utils.ExecCommand("userdel", "-r", username)

	// 第九步：重新生成 polkit 规则
	progressFn(95, "正在更新权限规则...")
	if err := regeneratePolkitRules(); err != nil {
		fmt.Printf("[警告] 重新生成 polkit 规则失败: %v\n", err)
	}

	// 第十步：同步 SSH 拒绝配置
	regenerateSSHDenyConfig()

	progressFn(100, "用户及所有资产已删除")
	return nil
}

// cleanupUserTrafficRecords 清理用户的流量记录
func cleanupUserTrafficRecords(username string) {
	result := model.DB.Where("username = ?", username).Delete(&model.UserTrafficDaily{})
	if result.RowsAffected > 0 {
		fmt.Printf("[流量] 已清理用户 %s 的流量记录（共 %d 条）\n", username, result.RowsAffected)
	}
}

// regeneratePolkitRules 重新生成 polkit 权限规则
func regeneratePolkitRules() error {
	vmAccessDir := config.GlobalConfig.VMAccessDir

	// 读取所有用户的 VM 映射
	lsResult := utils.ExecShell(fmt.Sprintf("ls '%s' 2>/dev/null", vmAccessDir))
	if lsResult.Error != nil || lsResult.Stdout == "" {
		return nil
	}

	var mappings []string
	for _, username := range strings.Split(lsResult.Stdout, "\n") {
		username = strings.TrimSpace(username)
		if username == "" {
			continue
		}
		if !isExistingVMAccessUser(username) {
			continue
		}

		// 读取该用户的 VM 列表
		vmsResult := utils.ExecShell(fmt.Sprintf("cat '%s/%s' 2>/dev/null", vmAccessDir, username))
		if vmsResult.Error != nil || vmsResult.Stdout == "" {
			continue
		}

		var jsArr []string
		for _, vm := range strings.Split(vmsResult.Stdout, "\n") {
			vm = strings.TrimSpace(vm)
			if vm != "" {
				jsArr = append(jsArr, fmt.Sprintf(`"%s"`, vm))
			}
		}
		if len(jsArr) > 0 {
			mappings = append(mappings, fmt.Sprintf(`        "%s": [%s],`, username, strings.Join(jsArr, ", ")))
		}
	}

	mappingStr := strings.Join(mappings, "\n")

	polkitRules := fmt.Sprintf(`// 自动生成 - 请勿手动编辑
// 规则1: root 和 libvirt-dbus 拥有全部权限
polkit.addRule(function(action, subject) {
    if (action.id.indexOf("org.libvirt.") === 0) {
        if (subject.user === "root" || subject.user === "libvirtdbus") {
            return polkit.Result.YES;
        }
    }
    return polkit.Result.NOT_HANDLED;
});

// 规则2: vmoperator 用户按虚拟机分配权限
polkit.addRule(function(action, subject) {
    if (!subject.isInGroup("vmoperator")) return polkit.Result.NOT_HANDLED;
    var vmAccessMap = {
%s
    };
    var userVMs = vmAccessMap[subject.user];
    if (!userVMs || userVMs.length === 0) {
        if (action.id.indexOf("org.libvirt.") === 0) return polkit.Result.NO;
        return polkit.Result.NOT_HANDLED;
    }
    var connectActions = ["org.libvirt.unix.manage","org.libvirt.unix.monitor","org.libvirt.api.connect.getattr","org.libvirt.api.connect.read","org.libvirt.api.connect.search-domains","org.libvirt.api.connect.search-networks"];
    for (var i = 0; i < connectActions.length; i++) { if (action.id === connectActions[i]) return polkit.Result.YES; }
    var infraActions = ["org.libvirt.api.network.getattr","org.libvirt.api.network.read","org.libvirt.api.network-port.create","org.libvirt.api.network-port.delete"];
    for (var i = 0; i < infraActions.length; i++) { if (action.id === infraActions[i]) return polkit.Result.YES; }
    var domainName = action.lookup("domain_name");
    if (domainName) {
        var hasAccess = false;
        for (var i = 0; i < userVMs.length; i++) { if (domainName === userVMs[i]) { hasAccess = true; break; } }
        if (!hasAccess) return polkit.Result.NO;
        var domainActions = ["org.libvirt.api.domain.getattr","org.libvirt.api.domain.read","org.libvirt.api.domain.start","org.libvirt.api.domain.stop","org.libvirt.api.domain.reset","org.libvirt.api.domain.snapshot"];
        for (var i = 0; i < domainActions.length; i++) { if (action.id === domainActions[i]) return polkit.Result.YES; }
    }
    if (action.id.indexOf("org.libvirt.") === 0) return polkit.Result.NO;
    return polkit.Result.NOT_HANDLED;
});`, mappingStr)

	// 写入规则文件
	polkitPath := "/etc/polkit-1/rules.d/10-vmoperator.rules"
	writeResult := utils.ExecShell(fmt.Sprintf("cat > '%s' << 'POLKITEOF'\n%s\nPOLKITEOF", polkitPath, polkitRules))
	if writeResult.Error != nil {
		return fmt.Errorf("写入 polkit 规则失败: %s", writeResult.Stderr)
	}

	// 重启 polkit
	utils.ExecCommand("systemctl", "restart", "polkit")

	return nil
}
