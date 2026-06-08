package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"kvm_console/config"
	"kvm_console/model"
	"kvm_console/utils"
)

const hostStorageRoot = "/var/lib/kvm-storage"

var storageIDSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

// HostStoragePoolInfo 是管理员侧存储池页面展示的宿主机块设备信息。
type HostStoragePoolInfo struct {
	ID           string                `json:"id"`
	Name         string                `json:"name"`
	DisplayName  string                `json:"display_name"`
	DevicePath   string                `json:"device_path"`
	KName        string                `json:"kname"`
	Type         string                `json:"type"`
	Size         int64                 `json:"size"`
	FSType       string                `json:"fstype"`
	FSVersion    string                `json:"fsver"`
	Label        string                `json:"label"`
	UUID         string                `json:"uuid"`
	Mountpoints  []string              `json:"mountpoints"`
	MountPath    string                `json:"mount_path"`
	VMDir        string                `json:"vm_dir"`
	Model        string                `json:"model"`
	Serial       string                `json:"serial"`
	Rota         bool                  `json:"rota"`
	Removable    bool                  `json:"removable"`
	Readonly     bool                  `json:"readonly"`
	Tran         string                `json:"tran"`
	PKName       string                `json:"pkname"`
	Used         int64                 `json:"used"`
	Available    int64                 `json:"available"`
	UsePercent   int                   `json:"use_percent"`
	Enabled      bool                  `json:"enabled"`
	IsDefault    bool                  `json:"is_default"`
	Configured   bool                  `json:"configured"`
	CanFormat    bool                  `json:"can_format"`
	CanUseForVM  bool                  `json:"can_use_for_vm"`
	SystemDisk   bool                  `json:"system_disk"`
	StatusReason string                `json:"status_reason"`
	Children     []HostStoragePoolInfo `json:"children,omitempty"`
}

// VMStorageTarget 是创建虚拟机时可选择的落盘位置。
type VMStorageTarget struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	DevicePath  string `json:"device_path"`
	MountPath   string `json:"mount_path"`
	VMDir       string `json:"vm_dir"`
	Size        int64  `json:"size"`
	Used        int64  `json:"used"`
	Available   int64  `json:"available"`
	Enabled     bool   `json:"enabled"`
	IsDefault   bool   `json:"is_default"`
}

// UpdateHostStoragePoolConfigRequest 更新硬盘显示和启用配置。
type UpdateHostStoragePoolConfigRequest struct {
	DisplayName string `json:"display_name"`
	Enabled     bool   `json:"enabled"`
}

type flexibleMountpoints []string

func (m *flexibleMountpoints) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*m = nil
		return nil
	}
	var arr []string
	if err := json.Unmarshal(data, &arr); err == nil {
		*m = arr
		return nil
	}
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		if strings.TrimSpace(single) == "" {
			*m = nil
		} else {
			*m = []string{single}
		}
		return nil
	}
	return nil
}

type lsblkOutput struct {
	BlockDevices []lsblkDevice `json:"blockdevices"`
}

type lsblkDevice struct {
	Name        string              `json:"name"`
	KName       string              `json:"kname"`
	Path        string              `json:"path"`
	Type        string              `json:"type"`
	Size        int64               `json:"size"`
	FSType      string              `json:"fstype"`
	FSVersion   string              `json:"fsver"`
	Label       string              `json:"label"`
	UUID        string              `json:"uuid"`
	Mountpoints flexibleMountpoints `json:"mountpoints"`
	Model       string              `json:"model"`
	Serial      string              `json:"serial"`
	Rota        bool                `json:"rota"`
	Removable   bool                `json:"rm"`
	Readonly    bool                `json:"ro"`
	Tran        string              `json:"tran"`
	PKName      string              `json:"pkname"`
	Children    []lsblkDevice       `json:"children"`
}

type findmntOutput struct {
	Filesystems []findmntInfo `json:"filesystems"`
}

type findmntInfo struct {
	Target   string        `json:"target"`
	Source   string        `json:"source"`
	FSType   string        `json:"fstype"`
	Options  string        `json:"options"`
	Size     int64         `json:"size"`
	Used     int64         `json:"used"`
	Avail    int64         `json:"avail"`
	Children []findmntInfo `json:"children"`
}

type mountUsage struct {
	Source    string
	Target    string
	Size      int64
	Used      int64
	Available int64
}

// ListStoragePools 列出宿主机所有块设备，并合并管理员存储池配置。
func ListStoragePools() ([]HostStoragePoolInfo, error) {
	devices, err := readLSBLKDevices()
	if err != nil {
		return nil, err
	}
	mounts := readFindmntMap()
	dfUsage := readDFUsage()
	aliases := readDeviceAliasMap()
	configs := loadHostStoragePoolConfigs()
	return buildStoragePoolTree(devices, mounts, dfUsage, aliases, configs), nil
}

// GetStoragePool 获取单个宿主机存储池设备。
func GetStoragePool(id string) (*HostStoragePoolInfo, error) {
	pools, err := ListStoragePools()
	if err != nil {
		return nil, err
	}
	if pool := findStoragePoolByID(pools, id); pool != nil {
		return pool, nil
	}
	return nil, fmt.Errorf("存储池设备不存在: %s", id)
}

// UpdateHostStoragePoolConfig 更新显示名与启用状态。
func UpdateHostStoragePoolConfig(id string, req UpdateHostStoragePoolConfigRequest) error {
	pool, err := GetStoragePool(id)
	if err != nil {
		return err
	}
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = defaultStorageDisplayName(*pool)
	}
	mountPath := pool.MountPath
	if mountPath == "" {
		mountPath = defaultStorageMountPath(id)
	}
	if req.Enabled && !pool.CanUseForVM {
		return fmt.Errorf("该硬盘当前不能启用为虚拟机存储位置: %s", pool.StatusReason)
	}
	cfg := model.HostStoragePool{DeviceID: id}
	return model.DB.Where("device_id = ?", id).Assign(map[string]interface{}{
		"display_name": displayName,
		"enabled":      req.Enabled,
		"is_default":   pool.IsDefault && req.Enabled,
		"mount_path":   mountPath,
	}).FirstOrCreate(&cfg).Error
}

// SetDefaultHostStoragePool 将指定硬盘设为默认存储位置。
func SetDefaultHostStoragePool(id string) error {
	pool, err := GetStoragePool(id)
	if err != nil {
		return err
	}
	if !pool.CanUseForVM {
		return fmt.Errorf("该硬盘当前不能设为默认存储位置: %s", pool.StatusReason)
	}
	displayName := strings.TrimSpace(pool.DisplayName)
	if displayName == "" {
		displayName = defaultStorageDisplayName(*pool)
	}
	mountPath := pool.MountPath
	if mountPath == "" {
		mountPath = defaultStorageMountPath(id)
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.HostStoragePool{}).Where("is_default = ?", true).Update("is_default", false).Error; err != nil {
			return err
		}
		cfg := model.HostStoragePool{DeviceID: id}
		return tx.Where("device_id = ?", id).Assign(map[string]interface{}{
			"display_name": displayName,
			"enabled":      true,
			"is_default":   true,
			"mount_path":   mountPath,
		}).FirstOrCreate(&cfg).Error
	})
}

// ListVMStorageTargets 返回创建虚拟机可选择的落盘位置。
func ListVMStorageTargets(isAdmin bool) ([]VMStorageTarget, error) {
	pools, err := ListStoragePools()
	if err != nil {
		return nil, err
	}
	var targets []VMStorageTarget
	walkStoragePools(pools, func(pool HostStoragePoolInfo) {
		if !pool.CanUseForVM {
			return
		}
		if !isAdmin && !pool.Enabled {
			return
		}
		targets = append(targets, VMStorageTarget{
			ID:          pool.ID,
			DisplayName: pool.DisplayName,
			DevicePath:  pool.DevicePath,
			MountPath:   pool.MountPath,
			VMDir:       pool.VMDir,
			Size:        pool.Size,
			Used:        pool.Used,
			Available:   pool.Available,
			Enabled:     pool.Enabled || isAdmin,
			IsDefault:   pool.IsDefault,
		})
	})
	sort.SliceStable(targets, func(i, j int) bool {
		if targets[i].IsDefault != targets[j].IsDefault {
			return targets[i].IsDefault
		}
		if targets[i].Enabled != targets[j].Enabled {
			return targets[i].Enabled
		}
		return targets[i].DisplayName < targets[j].DisplayName
	})
	return targets, nil
}

// ResolveVMStorageDir 解析创建虚拟机时使用的磁盘目录。
func ResolveVMStorageDir(poolID string, isAdmin bool) (string, string, error) {
	poolID = strings.TrimSpace(poolID)
	if poolID == "" {
		if cfg, ok := getDefaultHostStoragePoolConfig(); ok {
			poolID = cfg.DeviceID
		} else {
			return config.GlobalConfig.CloneDir, "", nil
		}
	}

	pool, err := GetStoragePool(poolID)
	if err != nil {
		return "", "", err
	}
	if !pool.CanUseForVM {
		return "", "", fmt.Errorf("存储池不可用于创建虚拟机: %s", pool.StatusReason)
	}
	if !isAdmin && !pool.Enabled {
		return "", "", fmt.Errorf("该存储池未启用，普通用户不能使用")
	}
	if err := ensureVMStorageDir(pool.VMDir); err != nil {
		return "", "", err
	}
	return pool.VMDir, pool.ID, nil
}

// FormatAndMountStoragePool 格式化指定块设备并挂载为虚拟机存储位置。
func FormatAndMountStoragePool(ctx context.Context, id string, progress func(int, string)) error {
	pool, err := GetStoragePool(id)
	if err != nil {
		return err
	}
	if err := validateFormatTarget(*pool); err != nil {
		return err
	}
	mountPath := defaultStorageMountPath(id)
	devicePath := pool.DevicePath

	progress(10, "正在清理旧文件系统标记...")
	utils.ExecCommandContextWithTimeout(ctx, "wipefs", 2*time.Minute, "-a", devicePath)
	if ctx.Err() != nil {
		return ctx.Err()
	}

	progress(30, "正在格式化为 ext4...")
	mkfs := utils.ExecCommandContextWithTimeout(ctx, "mkfs.ext4", 10*time.Minute, "-F", devicePath)
	if mkfs.Error != nil {
		return fmt.Errorf("格式化硬盘失败: %s", mkfs.Stderr)
	}

	progress(55, "正在读取文件系统 UUID...")
	blkid := utils.ExecCommandContextWithTimeout(ctx, "blkid", 30*time.Second, "-s", "UUID", "-o", "value", devicePath)
	if blkid.Error != nil || strings.TrimSpace(blkid.Stdout) == "" {
		return fmt.Errorf("读取新文件系统 UUID 失败: %s", blkid.Stderr)
	}
	uuid := strings.TrimSpace(blkid.Stdout)

	progress(65, "正在写入开机自动挂载配置...")
	if err := os.MkdirAll(mountPath, 0755); err != nil {
		return fmt.Errorf("创建挂载目录失败: %w", err)
	}
	if err := ensureFstabEntry(uuid, mountPath); err != nil {
		return err
	}

	progress(75, "正在挂载硬盘...")
	mount := utils.ExecCommandContextWithTimeout(ctx, "mount", 2*time.Minute, mountPath)
	if mount.Error != nil {
		return fmt.Errorf("挂载硬盘失败: %s", mount.Stderr)
	}

	progress(85, "正在创建虚拟机磁盘目录...")
	vmDir := filepath.Join(mountPath, "vm-disks")
	if err := ensureVMStorageDir(vmDir); err != nil {
		return err
	}

	progress(92, "正在保存存储池配置...")
	displayName := pool.DisplayName
	if strings.TrimSpace(displayName) == "" {
		displayName = defaultStorageDisplayName(*pool)
	}
	cfg := model.HostStoragePool{DeviceID: id}
	if err := model.DB.Where("device_id = ?", id).Assign(map[string]interface{}{
		"display_name": displayName,
		"enabled":      true,
		"mount_path":   mountPath,
	}).FirstOrCreate(&cfg).Error; err != nil {
		return fmt.Errorf("保存存储池配置失败: %w", err)
	}

	progress(100, "硬盘已格式化并挂载")
	return nil
}

func readLSBLKDevices() ([]lsblkDevice, error) {
	result := utils.ExecCommand("lsblk", "-J", "-b", "-o", "NAME,KNAME,PATH,TYPE,SIZE,FSTYPE,FSVER,LABEL,UUID,MOUNTPOINTS,MODEL,SERIAL,ROTA,RM,RO,TRAN,PKNAME")
	if result.Error != nil {
		return nil, fmt.Errorf("读取宿主机硬盘列表失败: %s", result.Stderr)
	}
	var out lsblkOutput
	if err := json.Unmarshal([]byte(result.Stdout), &out); err != nil {
		return nil, fmt.Errorf("解析硬盘列表失败: %w", err)
	}
	return out.BlockDevices, nil
}

func readFindmntMap() map[string]findmntInfo {
	result := utils.ExecCommand("findmnt", "-J", "-b", "-o", "TARGET,SOURCE,FSTYPE,OPTIONS,SIZE,USED,AVAIL")
	if result.Error != nil {
		return map[string]findmntInfo{}
	}
	var out findmntOutput
	if err := json.Unmarshal([]byte(result.Stdout), &out); err != nil {
		return map[string]findmntInfo{}
	}
	mounts := make(map[string]findmntInfo)
	var walk func([]findmntInfo)
	walk = func(items []findmntInfo) {
		for _, item := range items {
			mounts[item.Target] = item
			walk(item.Children)
		}
	}
	walk(out.Filesystems)
	return mounts
}

func readDFUsage() map[string]mountUsage {
	result := utils.ExecCommand("df", "-B1", "--output=source,size,used,avail,pcent,target")
	if result.Error != nil {
		return map[string]mountUsage{}
	}
	usage := make(map[string]mountUsage)
	for i, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if i == 0 || line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		size, _ := strconv.ParseInt(fields[1], 10, 64)
		used, _ := strconv.ParseInt(fields[2], 10, 64)
		avail, _ := strconv.ParseInt(fields[3], 10, 64)
		target := fields[5]
		usage[target] = mountUsage{
			Source:    fields[0],
			Target:    target,
			Size:      size,
			Used:      used,
			Available: avail,
		}
	}
	return usage
}

func readDeviceAliasMap() map[string]string {
	aliases := make(map[string]string)
	fill := func(pattern string, allowUUID bool) {
		script := fmt.Sprintf("for p in %s; do [ -e \"$p\" ] || continue; t=$(readlink -f \"$p\" 2>/dev/null || true); [ -n \"$t\" ] && echo \"$t|$p\"; done", pattern)
		result := utils.ExecShell(script)
		if result.Error != nil {
			return
		}
		for _, line := range strings.Split(result.Stdout, "\n") {
			parts := strings.SplitN(strings.TrimSpace(line), "|", 2)
			if len(parts) != 2 {
				continue
			}
			if !allowUUID && strings.Contains(parts[1], "/by-uuid/") {
				continue
			}
			if aliases[parts[0]] == "" {
				aliases[parts[0]] = parts[1]
			}
		}
	}
	fill("/dev/disk/by-id/*", false)
	fill("/dev/disk/by-uuid/*", true)
	return aliases
}

func buildStoragePoolTree(devices []lsblkDevice, mounts map[string]findmntInfo, dfUsage map[string]mountUsage, aliases map[string]string, configs map[string]model.HostStoragePool) []HostStoragePoolInfo {
	result := make([]HostStoragePoolInfo, 0, len(devices))
	for _, dev := range devices {
		result = append(result, buildStoragePoolNode(dev, mounts, dfUsage, aliases, configs))
	}
	return result
}

func buildStoragePoolNode(dev lsblkDevice, mounts map[string]findmntInfo, dfUsage map[string]mountUsage, aliases map[string]string, configs map[string]model.HostStoragePool) HostStoragePoolInfo {
	devicePath := dev.Path
	if devicePath == "" && dev.KName != "" {
		devicePath = "/dev/" + dev.KName
	}
	idSource := aliases[devicePath]
	if idSource == "" {
		idSource = devicePath
	}
	id := normalizeStorageDeviceID(idSource)
	cfg, configured := configs[id]
	mountpoints := normalizeMountpoints([]string(dev.Mountpoints))

	node := HostStoragePoolInfo{
		ID:          id,
		Name:        dev.Name,
		DevicePath:  devicePath,
		KName:       dev.KName,
		Type:        dev.Type,
		Size:        dev.Size,
		FSType:      dev.FSType,
		FSVersion:   dev.FSVersion,
		Label:       dev.Label,
		UUID:        dev.UUID,
		Mountpoints: mountpoints,
		Model:       strings.TrimSpace(dev.Model),
		Serial:      strings.TrimSpace(dev.Serial),
		Rota:        dev.Rota,
		Removable:   dev.Removable,
		Readonly:    dev.Readonly,
		Tran:        dev.Tran,
		PKName:      dev.PKName,
		Configured:  configured,
	}
	if configured {
		node.DisplayName = cfg.DisplayName
		node.Enabled = cfg.Enabled
		node.IsDefault = cfg.IsDefault
		node.MountPath = cfg.MountPath
	}
	if node.DisplayName == "" {
		node.DisplayName = defaultStorageDisplayName(node)
	}
	if node.MountPath == "" && len(mountpoints) > 0 {
		node.MountPath = mountpoints[0]
	}
	if node.MountPath != "" {
		node.VMDir = filepath.Join(node.MountPath, "vm-disks")
		if cloneDir := configuredCloneDir(); isPathUnderMount(cloneDir, node.MountPath) {
			node.VMDir = cloneDir
		}
	}
	applyUsage(&node, mounts, dfUsage)

	for _, child := range dev.Children {
		node.Children = append(node.Children, buildStoragePoolNode(child, mounts, dfUsage, aliases, configs))
	}
	node.SystemDisk = isSystemStorageNode(node)
	node.CanFormat, node.StatusReason = canFormatStorageNode(node)
	node.CanUseForVM = canUseStorageNode(node)
	if !node.CanUseForVM && node.StatusReason == "" {
		node.StatusReason = "硬盘未挂载，无法作为虚拟机存储位置"
	}
	if node.Enabled && !node.CanUseForVM {
		node.Enabled = false
	}
	return node
}

func applyUsage(node *HostStoragePoolInfo, mounts map[string]findmntInfo, dfUsage map[string]mountUsage) {
	for _, mp := range node.Mountpoints {
		if usage, ok := dfUsage[mp]; ok {
			node.Size = usage.Size
			node.Used = usage.Used
			node.Available = usage.Available
			break
		}
		if info, ok := mounts[mp]; ok {
			if info.Size > 0 {
				node.Size = info.Size
			}
			node.Used = info.Used
			node.Available = info.Avail
			break
		}
	}
	if node.Size > 0 && node.Used > 0 {
		node.UsePercent = int(float64(node.Used) / float64(node.Size) * 100)
	}
}

func canFormatStorageNode(node HostStoragePoolInfo) (bool, string) {
	if node.Readonly {
		return false, "设备为只读状态"
	}
	if node.Type != "disk" && node.Type != "part" {
		return false, "只支持格式化整块硬盘或分区"
	}
	if node.Type == "loop" || node.Type == "rom" || node.Removable {
		return false, "不支持格式化 loop、光驱或可移动设备"
	}
	if len(node.Mountpoints) > 0 || hasMountedChild(node) {
		return false, "设备或其分区当前已挂载"
	}
	if node.SystemDisk {
		return false, "系统关键磁盘禁止格式化"
	}
	return true, ""
}

func canUseStorageNode(node HostStoragePoolInfo) bool {
	if node.Readonly || node.Type == "rom" || node.Type == "loop" {
		return false
	}
	return node.MountPath != "" && len(node.Mountpoints) > 0
}

func validateFormatTarget(pool HostStoragePoolInfo) error {
	if ok, reason := canFormatStorageNode(pool); !ok {
		return fmt.Errorf("该硬盘不能格式化: %s", reason)
	}
	return nil
}

func isSystemStorageNode(node HostStoragePoolInfo) bool {
	for _, mp := range node.Mountpoints {
		switch mp {
		case "/", "/boot", "/boot/efi", "/usr", "/var", "/home":
			return true
		}
	}
	for _, child := range node.Children {
		if isSystemStorageNode(child) {
			return true
		}
	}
	return false
}

func hasMountedChild(node HostStoragePoolInfo) bool {
	for _, child := range node.Children {
		if len(child.Mountpoints) > 0 || hasMountedChild(child) {
			return true
		}
	}
	return false
}

func normalizeMountpoints(items []string) []string {
	var result []string
	seen := make(map[string]bool)
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" || item == "[SWAP]" || seen[item] {
			continue
		}
		seen[item] = true
		result = append(result, item)
	}
	return result
}

func normalizeStorageDeviceID(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "/dev/disk/by-id/")
	raw = strings.TrimPrefix(raw, "/dev/disk/by-uuid/")
	raw = strings.TrimPrefix(raw, "/dev/")
	raw = storageIDSanitizer.ReplaceAllString(raw, "-")
	raw = strings.Trim(raw, "-")
	if raw == "" {
		raw = "unknown"
	}
	return raw
}

func defaultStorageMountPath(id string) string {
	return filepath.Join(hostStorageRoot, normalizeStorageDeviceID(id))
}

func defaultStorageDisplayName(pool HostStoragePoolInfo) string {
	if pool.Label != "" {
		return pool.Label
	}
	if pool.Model != "" {
		return strings.TrimSpace(fmt.Sprintf("%s %s", pool.Model, pool.Name))
	}
	if pool.DevicePath != "" {
		return pool.DevicePath
	}
	return pool.Name
}

func isPathUnderMount(pathValue, mountPath string) bool {
	pathValue = filepath.Clean(strings.TrimSpace(pathValue))
	mountPath = filepath.Clean(strings.TrimSpace(mountPath))
	if pathValue == "." || mountPath == "." {
		return false
	}
	if mountPath == string(os.PathSeparator) {
		return filepath.IsAbs(pathValue)
	}
	return pathValue == mountPath || strings.HasPrefix(pathValue, mountPath+string(os.PathSeparator))
}

func configuredCloneDir() string {
	if config.GlobalConfig == nil || strings.TrimSpace(config.GlobalConfig.CloneDir) == "" {
		return ""
	}
	return config.GlobalConfig.CloneDir
}

func loadHostStoragePoolConfigs() map[string]model.HostStoragePool {
	configs := make(map[string]model.HostStoragePool)
	if model.DB == nil {
		return configs
	}
	var rows []model.HostStoragePool
	if err := model.DB.Find(&rows).Error; err != nil {
		return configs
	}
	for _, row := range rows {
		configs[row.DeviceID] = row
	}
	return configs
}

func getDefaultHostStoragePoolConfig() (model.HostStoragePool, bool) {
	var cfg model.HostStoragePool
	if model.DB == nil {
		return cfg, false
	}
	if err := model.DB.Where("is_default = ?", true).First(&cfg).Error; err != nil {
		return cfg, false
	}
	return cfg, true
}

func findStoragePoolByID(pools []HostStoragePoolInfo, id string) *HostStoragePoolInfo {
	for _, pool := range pools {
		if pool.ID == id {
			cp := pool
			return &cp
		}
		if found := findStoragePoolByID(pool.Children, id); found != nil {
			return found
		}
	}
	return nil
}

func walkStoragePools(pools []HostStoragePoolInfo, fn func(HostStoragePoolInfo)) {
	for _, pool := range pools {
		fn(pool)
		walkStoragePools(pool.Children, fn)
	}
}

func ensureVMStorageDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("虚拟机磁盘目录为空")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建虚拟机磁盘目录失败: %w", err)
	}
	if err := ensureLibvirtStorageAppArmorAccessForPaths([]string{dir}); err != nil {
		return fmt.Errorf("配置 libvirt 自定义存储访问规则失败: %w", err)
	}
	utils.ExecCommand("chown", "libvirt-qemu:kvm", dir)
	return nil
}

func ensureFstabEntry(uuid, mountPath string) error {
	data, err := os.ReadFile("/etc/fstab")
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("读取 /etc/fstab 失败: %w", err)
	}
	line := fmt.Sprintf("UUID=%s %s ext4 defaults,nofail 0 2", uuid, mountPath)
	var lines []string
	found := false
	for _, existing := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(existing)
		if trimmed == "" {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) >= 2 && (fields[0] == "UUID="+uuid || fields[1] == mountPath) {
			lines = append(lines, line)
			found = true
			continue
		}
		lines = append(lines, existing)
	}
	if !found {
		lines = append(lines, line)
	}
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile("/etc/fstab", []byte(content), 0644); err != nil {
		return fmt.Errorf("写入 /etc/fstab 失败: %w", err)
	}
	return nil
}

// ===== ISO 聚合能力：保留给创建虚拟机表单使用 =====

// ISOFileInfo ISO 文件信息（带自动推断的系统类型）
type ISOFileInfo struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Size      string `json:"size"`
	SizeBytes int64  `json:"size_bytes"`
	Pool      string `json:"pool"`
	OSType    string `json:"os_type"`
	OSVariant string `json:"os_variant"`
	MinDisk   int    `json:"min_disk"`
}

// GetAllISOs 扫描系统设置中配置的全局 ISO 目录。
func GetAllISOs() ([]ISOFileInfo, error) {
	paths := collectISOScanPaths()
	seen := make(map[string]bool)
	var all []ISOFileInfo
	for label, dir := range paths {
		result := utils.ExecShell(fmt.Sprintf("find %s -maxdepth 1 -name '*.iso' -type f 2>/dev/null", utils.ShellSingleQuote(dir)))
		if result.Error != nil || strings.TrimSpace(result.Stdout) == "" {
			continue
		}
		for _, line := range strings.Split(result.Stdout, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || seen[line] {
				continue
			}
			seen[line] = true
			all = append(all, buildISOInfo(line, label))
		}
	}
	return all, nil
}

func collectISOScanPaths() map[string]string {
	return map[string]string{
		"系统设置 ISO": configuredISODir(),
	}
}

func configuredISODir() string {
	if config.GlobalConfig == nil {
		return config.DefaultISODir
	}
	isoDir := strings.TrimSpace(config.GlobalConfig.ISODir)
	if isoDir == "" {
		return config.DefaultISODir
	}
	return isoDir
}

func buildISOInfo(filePath, poolName string) ISOFileInfo {
	name := filepath.Base(filePath)
	nameLower := strings.ToLower(name)
	iso := ISOFileInfo{Name: name, Path: filePath, Pool: poolName}
	sizeResult := utils.ExecShell(fmt.Sprintf("du -h %s | awk '{print $1}'", utils.ShellSingleQuote(filePath)))
	if sizeResult.Error == nil {
		iso.Size = strings.TrimSpace(sizeResult.Stdout)
	}
	bytesResult := utils.ExecShell(fmt.Sprintf("stat -c '%%s' %s 2>/dev/null", utils.ShellSingleQuote(filePath)))
	if bytesResult.Error == nil {
		iso.SizeBytes, _ = strconv.ParseInt(strings.TrimSpace(bytesResult.Stdout), 10, 64)
	}
	iso.OSType, iso.OSVariant = inferOSFromISO(nameLower)
	if iso.OSType == "windows" {
		iso.MinDisk = 20
	} else {
		iso.MinDisk = 10
	}
	return iso
}

func inferOSFromISO(nameLower string) (osType, osVariant string) {
	if strings.Contains(nameLower, "win11") || strings.Contains(nameLower, "windows11") || strings.Contains(nameLower, "windows_11") || strings.Contains(nameLower, "win_11") {
		return "windows", "win11"
	}
	if strings.Contains(nameLower, "win10") || strings.Contains(nameLower, "windows10") || strings.Contains(nameLower, "windows_10") || strings.Contains(nameLower, "win_10") {
		return "windows", "win10"
	}
	if strings.Contains(nameLower, "win2k25") || strings.Contains(nameLower, "server2025") || strings.Contains(nameLower, "2025") && strings.Contains(nameLower, "server") {
		return "windows", "win2k25"
	}
	if strings.Contains(nameLower, "win2k22") || strings.Contains(nameLower, "server2022") || strings.Contains(nameLower, "2022") && strings.Contains(nameLower, "server") {
		return "windows", "win2k22"
	}
	if strings.Contains(nameLower, "win2k19") || strings.Contains(nameLower, "server2019") || strings.Contains(nameLower, "2019") && strings.Contains(nameLower, "server") {
		return "windows", "win2k19"
	}
	if strings.Contains(nameLower, "win2k16") || strings.Contains(nameLower, "server2016") {
		return "windows", "win2k16"
	}
	if strings.Contains(nameLower, "win") || strings.Contains(nameLower, "windows") {
		return "windows", ""
	}
	if strings.Contains(nameLower, "ubuntu") {
		if strings.Contains(nameLower, "24.04") || strings.Contains(nameLower, "noble") {
			return "linux", "ubuntu24.04"
		}
		if strings.Contains(nameLower, "22.04") || strings.Contains(nameLower, "jammy") {
			return "linux", "ubuntu22.04"
		}
		if strings.Contains(nameLower, "20.04") || strings.Contains(nameLower, "focal") {
			return "linux", "ubuntu20.04"
		}
		return "linux", "ubuntu24.04"
	}
	if strings.Contains(nameLower, "debian") {
		if strings.Contains(nameLower, "12") || strings.Contains(nameLower, "bookworm") {
			return "linux", "debian12"
		}
		if strings.Contains(nameLower, "11") || strings.Contains(nameLower, "bullseye") {
			return "linux", "debian11"
		}
		return "linux", "debian12"
	}
	if strings.Contains(nameLower, "centos") {
		return "linux", "centos-stream9"
	}
	if strings.Contains(nameLower, "rocky") {
		return "linux", "rocky9"
	}
	if strings.Contains(nameLower, "alma") {
		return "linux", "almalinux9"
	}
	if strings.Contains(nameLower, "rhel") || strings.Contains(nameLower, "redhat") {
		return "linux", "rhel9-unknown"
	}
	if strings.Contains(nameLower, "fedora") {
		return "linux", "fedora-unknown"
	}
	if strings.Contains(nameLower, "arch") {
		return "linux", "archlinux"
	}
	if strings.Contains(nameLower, "alpine") {
		return "linux", "alpinelinux3.21"
	}
	if strings.Contains(nameLower, "opensuse") || strings.Contains(nameLower, "suse") {
		return "linux", "opensuse-unknown"
	}
	return "linux", "generic"
}
