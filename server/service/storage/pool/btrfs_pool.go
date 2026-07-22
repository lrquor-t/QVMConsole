package pool

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"kvm_console/model"
	"kvm_console/utils"
)

// ── Btrfs 存储池类型定义 ──

// BtrfsPoolRequest 创建 Btrfs 存储池的请求参数。
type BtrfsPoolRequest struct {
	DeviceIDs    []string `json:"device_ids"`     // 选中的磁盘设备 ID 列表（1~N）
	Label        string   `json:"label"`          // 卷标/池名，mkfs.btrfs -L
	DataProfile  string   `json:"data_profile"`   // single/raid0/raid1/raid10
	Compression  string   `json:"compression"`    // zstd/off
	MountPath    string   `json:"mount_path"`     // 挂载点，留空自动生成
	NoCowVMDisks bool     `json:"nocow_vm_disks"` // vm-disks 关闭 CoW（nodatacow），默认 true
	AddFstab     bool     `json:"add_fstab"`      // 是否写入 fstab，默认 true
}

// BtrfsPoolInfo 扫描得到的受管 Btrfs 存储池信息。
type BtrfsPoolInfo struct {
	Label     string   `json:"label"`
	MountPath string   `json:"mount_path"`
	Devices   []string `json:"devices"` // 成员盘设备路径
}

// ── 纯逻辑 ──

var btrfsLabelRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_.\-]{0,254}$`)

// validateBtrfsLabel 校验 btrfs 卷标合法性（mkfs.btrfs -L 最多 255 字符）。
func validateBtrfsLabel(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("存储池名称不能为空")
	}
	if len(name) > 255 {
		return fmt.Errorf("存储池名称过长（最多 255 字符）")
	}
	if !btrfsLabelRe.MatchString(name) {
		return fmt.Errorf("名称只能以字母开头，包含字母、数字、下划线、点或短横线")
	}
	return nil
}

// isValidBtrfsProfile 判断是否为支持的 data profile。
func isValidBtrfsProfile(p string) bool {
	switch p {
	case "single", "raid0", "raid1", "raid10":
		return true
	}
	return false
}

// normalizeBtrfsProfile 规范化 data profile，空/非法值降级为 single。
func normalizeBtrfsProfile(p string) string {
	p = strings.TrimSpace(p)
	if !isValidBtrfsProfile(p) {
		return "single"
	}
	return p
}

// normalizeBtrfsCompression 规范化压缩算法，非法值降级为 off。
func normalizeBtrfsCompression(c string) string {
	c = strings.ToLower(strings.TrimSpace(c))
	switch c {
	case "zstd", "off":
		return c
	}
	return "off"
}

// btrfsProfileMinDisks 返回各 profile 所需的最少磁盘数。
func btrfsProfileMinDisks(profile string) int {
	switch profile {
	case "single":
		return 1
	case "raid0", "raid1":
		return 2
	case "raid10":
		return 4
	}
	return 1
}

// btrfsProfileLabel 返回 profile 的中文标签。
func btrfsProfileLabel(p string) string {
	switch p {
	case "single":
		return "单盘/单副本"
	case "raid0":
		return "RAID0（条带）"
	case "raid1":
		return "RAID1（镜像）"
	case "raid10":
		return "RAID10"
	}
	return p
}

// buildMkfsBtrfsArgs 拼装 mkfs.btrfs 参数（纯函数，假定输入已校验）。
// metadata profile 由 mkfs.btrfs 按盘数自动选取（单盘默认 dup），故只显式指定 -d。
func buildMkfsBtrfsArgs(label, profile string, devicePaths []string) []string {
	args := []string{"-f", "-L", label, "-d", profile}
	args = append(args, devicePaths...)
	return args
}

// ── Btrfs 可用性检测 ──

// BtrfsAvailable 检测宿主机是否安装了 mkfs.btrfs / btrfs / chattr 命令。
func BtrfsAvailable() bool {
	_, e1 := exec.LookPath("mkfs.btrfs")
	_, e2 := exec.LookPath("btrfs")
	_, e3 := exec.LookPath("chattr")
	return e1 == nil && e2 == nil && e3 == nil
}

// ── Btrfs 池扫描与层级注入 ──

// scanBtrfsDevices 解析 `btrfs filesystem show <mountPath>`，返回成员盘设备路径列表。
// 挂载点失效/未挂载时返回 nil。
func scanBtrfsDevices(mountPath string) []string {
	if strings.TrimSpace(mountPath) == "" {
		return nil
	}
	r := utils.ExecCommandQuiet("btrfs", "filesystem", "show", mountPath)
	if r.Error != nil {
		return nil
	}
	var devs []string
	for _, line := range strings.Split(r.Stdout, "\n") {
		t := strings.TrimSpace(line)
		// 匹配 "devid N size ... used ... path /dev/sdX"
		if idx := strings.Index(t, "path "); idx >= 0 {
			dev := strings.TrimSpace(t[idx+len("path "):])
			if dev != "" {
				devs = append(devs, dev)
			}
		}
	}
	return devs
}

// ListBtrfsPools 采用 DB 驱动：只返回 DB 中 deviceID 以 "btrfs-" 开头的受管 btrfs 池。
// 对每个池用其挂载点取成员盘（btrfs filesystem show），容量在 injectBtrfsTree 中用 df 补全。
func ListBtrfsPools(configs map[string]model.HostStoragePool) []BtrfsPoolInfo {
	if !BtrfsAvailable() {
		return nil
	}
	var pools []BtrfsPoolInfo
	for deviceID, cfg := range configs {
		if !strings.HasPrefix(deviceID, "btrfs-") {
			continue
		}
		label := strings.TrimPrefix(deviceID, "btrfs-")
		pools = append(pools, BtrfsPoolInfo{
			Label:     label,
			MountPath: cfg.MountPath,
			Devices:   scanBtrfsDevices(cfg.MountPath),
		})
	}
	sort.Slice(pools, func(i, j int) bool { return pools[i].Label < pools[j].Label })
	return pools
}

// injectBtrfsTree 将受管 Btrfs 池注入存储池树，并把成员盘容量清零避免重复计入。
func injectBtrfsTree(pools []HostStoragePoolInfo, bPools []BtrfsPoolInfo,
	dfUsage map[string]mountUsage, configs map[string]model.HostStoragePool) []HostStoragePoolInfo {

	// 成员盘路径集合：从顶层树移除（btrfs 成员盘 lsblk fstype 可能为空或带 label，
	// 保留在顶层会与池节点重名或被误判为未分配）。成员盘仍作为池节点子节点展示。
	memberPaths := make(map[string]bool)
	for _, bp := range bPools {
		for _, m := range bp.Devices {
			memberPaths[m] = true
		}
	}
	pools = removeBtrfsMemberNodes(pools, memberPaths)

	for _, bp := range bPools {
		deviceID := normalizeStorageDeviceID("btrfs-" + bp.Label)
		cfg, configured := configs[deviceID]

		node := HostStoragePoolInfo{
			ID:           deviceID,
			Name:         bp.Label,
			DisplayName:  "Btrfs: " + bp.Label,
			Type:         "btrfs",
			FSType:       "btrfs",
			IsBTRFSPool:  true,
			BTRFSLabel:   bp.Label,
			BTRFSDevices: len(bp.Devices),
			Readonly:     true,
			CanFormat:    false,
			CanUseForVM:  false,
			StatusReason: fmt.Sprintf("Btrfs 存储池（%d 块成员盘）", len(bp.Devices)),
		}
		if configured {
			node.DisplayName = cfg.DisplayName
			node.Enabled = cfg.Enabled
			node.IsDefault = cfg.IsDefault
			node.MountPath = cfg.MountPath
		}
		// 池容量（df）
		if cfg.MountPath != "" {
			if u, ok := dfUsage[cfg.MountPath]; ok {
				node.Size = u.Size
				node.Used = u.Used
				node.Available = u.Available
				if u.Size > 0 {
					node.UsePercent = int(float64(u.Used) / float64(u.Size) * 100)
				}
			}
			// vm-disks 子节点（可落盘，容量随池）
			vmDir := filepath.Join(cfg.MountPath, "vm-disks")
			node.Children = append(node.Children, HostStoragePoolInfo{
				ID:          normalizeStorageDeviceID("btrfs-" + bp.Label + "-vm-disks"),
				Name:        "vm-disks",
				DisplayName: "vm-disks",
				Type:        "dir",
				FSType:      "btrfs",
				MountPath:   vmDir,
				VMDir:       vmDir,
				Mountpoints: []string{cfg.MountPath},
				Size:        node.Size,
				Used:        node.Used,
				Available:   node.Available,
				UsePercent:  node.UsePercent,
				CanUseForVM: true,
				Configured:  configured,
				IsBTRFSPool: true,
			})
		}
		// 成员盘引用子节点
		for _, m := range bp.Devices {
			node.Children = append(node.Children, *buildBtrfsMemberRefNode(bp.Label, m))
		}
		pools = append(pools, node)
	}
	return pools
}

// removeBtrfsMemberNodes 从树中移除受管 btrfs 池的成员盘节点。
// btrfs 成员盘在 lsblk 中 fstype 可能为空（非主成员，如扩容加入的盘）或带 label（主成员），
// 若保留在顶层会与池节点重名（label 冲突）或被误判为未分配（fstype 空），故移除；
// 成员盘仍作为池节点的子节点（buildBtrfsMemberRefNode）展示。
func removeBtrfsMemberNodes(pools []HostStoragePoolInfo, memberPaths map[string]bool) []HostStoragePoolInfo {
	result := make([]HostStoragePoolInfo, 0, len(pools))
	for _, p := range pools {
		if memberPaths[p.DevicePath] {
			continue
		}
		p.Children = removeBtrfsMemberNodes(p.Children, memberPaths)
		result = append(result, p)
	}
	return result
}

// buildBtrfsMemberRefNode 构建 Btrfs 成员盘引用节点（仅展示层级，不可操作）。
func buildBtrfsMemberRefNode(label, devPath string) *HostStoragePoolInfo {
	base := filepath.Base(devPath)
	return &HostStoragePoolInfo{
		ID:           normalizeStorageDeviceID("btrfsmember-" + label + "-" + base),
		Name:         base,
		DisplayName:  devPath,
		DevicePath:   devPath,
		Type:         "pv",
		FSType:       "btrfs",
		IsBTRFSPool:  true,
		Size:         0,
		Readonly:     true,
		StatusReason: "Btrfs 存储池 " + label + " 成员盘",
	}
}

// ── 创建 Btrfs 存储池 ──

// CreateBtrfsPool 创建 Btrfs 存储池，按顺序执行：
// 校验 → wipefs → mkfs.btrfs → device scan → blkid → fstab → mount → vm-disks(+chattr +C) → 权限 → 存库。
func CreateBtrfsPool(ctx context.Context, req BtrfsPoolRequest, progress func(int, string)) error {
	if err := validateBtrfsLabel(req.Label); err != nil {
		return err
	}
	profile := normalizeBtrfsProfile(req.DataProfile)
	if len(req.DeviceIDs) == 0 {
		return fmt.Errorf("至少需要选择一个物理磁盘")
	}
	minDisks := btrfsProfileMinDisks(profile)
	if len(req.DeviceIDs) < minDisks {
		return fmt.Errorf("%s 至少需要 %d 块磁盘，当前选择 %d 块", btrfsProfileLabel(profile), minDisks, len(req.DeviceIDs))
	}
	compression := normalizeBtrfsCompression(req.Compression)
	mountPath := strings.TrimSpace(req.MountPath)
	if mountPath == "" {
		mountPath = defaultStorageMountPath("btrfs-" + req.Label)
	}
	noCow := req.NoCowVMDisks
	addFstab := req.AddFstab

	// 1) 校验并收集设备路径（已拒绝 lvm/zfs/raid member 盘）
	progress(5, "正在校验选中的磁盘...")
	devicePaths, err := validateAndCollectPVTargets(req.DeviceIDs)
	if err != nil {
		return fmt.Errorf("校验磁盘失败: %w", err)
	}
	devicePaths = resolveStableDevicePaths(devicePaths, readStableDeviceAliases())

	// 2) 名称冲突检查
	progress(10, "检查存储池名称冲突...")
	deviceID := normalizeStorageDeviceID("btrfs-" + req.Label)
	if btrfsPoolConfigExists(deviceID) {
		return fmt.Errorf("Btrfs 存储池 %s 已存在", req.Label)
	}

	// 3) 清旧标记
	progress(20, "正在清理旧文件系统标记...")
	for _, dp := range devicePaths {
		utils.ExecCommandContextWithTimeout(ctx, "wipefs", 2*time.Minute, "-a", dp)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}

	// 4) mkfs.btrfs
	progress(35, fmt.Sprintf("正在创建 Btrfs 文件系统（%s）...", btrfsProfileLabel(profile)))
	mkfsArgs := buildMkfsBtrfsArgs(req.Label, profile, devicePaths)
	if r := utils.ExecCommandContextWithTimeout(ctx, "mkfs.btrfs", 10*time.Minute, mkfsArgs...); r.Error != nil {
		return fmt.Errorf("mkfs.btrfs 失败: %s", r.Stderr)
	}

	// 5) device scan（多盘挂载前置；单盘也无害）
	progress(45, "正在扫描 Btrfs 设备...")
	scanArgs := append([]string{"device", "scan"}, devicePaths...)
	utils.ExecCommandContextWithTimeout(ctx, "btrfs", 1*time.Minute, scanArgs...)

	// 6) blkid 取 UUID（用首盘）
	progress(55, "正在读取文件系统 UUID...")
	blkid := utils.ExecCommandContextWithTimeout(ctx, "blkid", 30*time.Second, "-s", "UUID", "-o", "value", devicePaths[0])
	if blkid.Error != nil || strings.TrimSpace(blkid.Stdout) == "" {
		rollbackBtrfsPool(ctx, devicePaths, mountPath)
		return fmt.Errorf("读取 Btrfs UUID 失败: %s", blkid.Stderr)
	}
	uuid := strings.TrimSpace(blkid.Stdout)

	// 7) 挂载目录 + fstab
	if err := os.MkdirAll(mountPath, 0755); err != nil {
		rollbackBtrfsPool(ctx, devicePaths, mountPath)
		return fmt.Errorf("创建挂载目录失败: %w", err)
	}
	if addFstab {
		progress(65, "正在写入开机自动挂载配置...")
		if err := ensureBtrfsFstabEntry(uuid, mountPath, compression); err != nil {
			rollbackBtrfsPool(ctx, devicePaths, mountPath)
			return err
		}
	}

	// 8) mount（多盘池挂首盘即可，内核自动接入其余成员盘）
	progress(75, "正在挂载 Btrfs 文件系统...")
	var mountArgs []string
	if compression != "off" {
		mountArgs = []string{"-o", "compress=" + compression, devicePaths[0], mountPath}
	} else {
		mountArgs = []string{devicePaths[0], mountPath}
	}
	if r := utils.ExecCommandContextWithTimeout(ctx, "mount", 2*time.Minute, mountArgs...); r.Error != nil {
		rollbackBtrfsPool(ctx, devicePaths, mountPath)
		return fmt.Errorf("挂载失败: %s", r.Stderr)
	}

	// 9) vm-disks 目录 + chattr +C（目录为空时设置才生效）
	progress(85, "正在创建虚拟机磁盘目录...")
	vmDir := filepath.Join(mountPath, "vm-disks")
	if err := ensureVMStorageDir(vmDir); err != nil {
		rollbackBtrfsPool(ctx, devicePaths, mountPath)
		return err
	}
	if noCow {
		progress(88, "正在为 vm-disks 关闭 CoW（nodatacow）...")
		if r := utils.ExecCommandContextWithTimeout(ctx, "chattr", 30*time.Second, "+C", vmDir); r.Error != nil {
			// 非致命：仅性能影响
			progress(88, fmt.Sprintf("警告: 设置 nodatacow 失败: %s", strings.TrimSpace(r.Stderr)))
		}
	}

	// 10) 存库
	progress(95, "正在保存存储池配置...")
	cfg := model.HostStoragePool{DeviceID: deviceID}
	if err := model.DB.Where("device_id = ?", deviceID).Assign(map[string]interface{}{
		"display_name": req.Label,
		"enabled":      true,
		"mount_path":   mountPath,
	}).FirstOrCreate(&cfg).Error; err != nil {
		rollbackBtrfsPool(ctx, devicePaths, mountPath)
		return fmt.Errorf("保存存储池配置失败: %w", err)
	}

	progress(100, "Btrfs 存储池创建完成")
	return nil
}

// ensureBtrfsFstabEntry 写入/更新 btrfs 的 fstab 条目（支持 compress 挂载选项）。
func ensureBtrfsFstabEntry(uuid, mountPath, compression string) error {
	data, err := os.ReadFile("/etc/fstab")
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("读取 /etc/fstab 失败: %w", err)
	}
	options := "defaults,nofail"
	if compression != "off" {
		options = "defaults,nofail,compress=" + compression
	}
	line := fmt.Sprintf("UUID=%s %s btrfs %s 0 0", uuid, mountPath, options)
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

// removeFstabEntryByMount 按 mountPath 删除 fstab 条目。
func removeFstabEntryByMount(mountPath string) {
	data, err := os.ReadFile("/etc/fstab")
	if err != nil {
		return
	}
	var lines []string
	for _, existing := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(existing)
		if trimmed == "" {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) >= 2 && fields[1] == mountPath {
			continue
		}
		lines = append(lines, existing)
	}
	content := strings.Join(lines, "\n") + "\n"
	_ = os.WriteFile("/etc/fstab", []byte(content), 0644)
}

// rollbackBtrfsPool 创建失败回滚：umount + 删 fstab + wipefs 成员盘。
func rollbackBtrfsPool(ctx context.Context, devicePaths []string, mountPath string) {
	utils.ExecCommandContextWithTimeout(ctx, "umount", 1*time.Minute, mountPath)
	removeFstabEntryByMount(mountPath)
	for _, dp := range devicePaths {
		utils.ExecCommandQuietWithTimeout("wipefs", 1*time.Minute, "-a", dp)
	}
}

// btrfsPoolConfigExists 检查 DB 中是否已存在该 deviceID 的配置。
func btrfsPoolConfigExists(deviceID string) bool {
	if model.DB == nil {
		return false
	}
	var count int64
	model.DB.Model(&model.HostStoragePool{}).Where("device_id = ?", deviceID).Count(&count)
	return count > 0
}

// getConfigByDeviceID 从 DB 取单个 HostStoragePool 配置。
func getConfigByDeviceID(deviceID string) (model.HostStoragePool, bool) {
	var cfg model.HostStoragePool
	if model.DB == nil {
		return cfg, false
	}
	if err := model.DB.Where("device_id = ?", deviceID).First(&cfg).Error; err != nil {
		return cfg, false
	}
	return cfg, true
}

// ── 删除 Btrfs 存储池 ──

// DeleteBtrfsPool 销毁指定 Btrfs 存储池：umount → 删 fstab → wipefs 成员盘 → 删 DB。
func DeleteBtrfsPool(ctx context.Context, label string, progress func(int, string)) error {
	label = strings.TrimSpace(label)
	if label == "" {
		return fmt.Errorf("存储池名称不能为空")
	}
	deviceID := normalizeStorageDeviceID("btrfs-" + label)
	cfg, ok := getConfigByDeviceID(deviceID)
	if !ok {
		return fmt.Errorf("Btrfs 存储池 %s 不存在", label)
	}
	mountPath := cfg.MountPath
	switch strings.TrimSpace(mountPath) {
	case "/", "/boot", "/boot/efi", "/usr", "/var", "/home":
		return fmt.Errorf("Btrfs 存储池挂载于系统关键路径 %s，禁止删除", mountPath)
	}

	// 卸载前先拿到成员盘（卸载后按挂载点查不到）
	devs := scanBtrfsDevices(mountPath)

	progress(20, fmt.Sprintf("正在卸载 %s ...", mountPath))
	if r := utils.ExecCommandContextWithTimeout(ctx, "umount", 1*time.Minute, mountPath); r.Error != nil {
		return fmt.Errorf("卸载失败: %s", r.Stderr)
	}

	progress(50, "正在清理开机挂载配置...")
	removeFstabEntryByMount(mountPath)

	progress(75, "正在擦除成员盘文件系统签名...")
	for _, dp := range devs {
		utils.ExecCommandContextWithTimeout(ctx, "wipefs", 2*time.Minute, "-a", dp)
	}

	progress(90, "正在清理存储池配置...")
	if model.DB != nil {
		model.DB.Where("device_id = ?", deviceID).Delete(&model.HostStoragePool{})
	}

	progress(100, "Btrfs 存储池已删除")
	return nil
}

// ── 扩容 Btrfs 存储池 ──

// ExpandBtrfsPoolRequest 扩容请求。
type ExpandBtrfsPoolRequest struct {
	Label     string   `json:"label"`
	DeviceIDs []string `json:"device_ids"` // 新加入的磁盘
}

// ExpandBtrfsPool 给已存在的 btrfs 池加盘扩容（btrfs device add）。同步执行。
func ExpandBtrfsPool(label string, deviceIDs []string) error {
	label = strings.TrimSpace(label)
	if label == "" {
		return fmt.Errorf("存储池名称不能为空")
	}
	if len(deviceIDs) == 0 {
		return fmt.Errorf("至少需要选择一个物理磁盘")
	}
	deviceID := normalizeStorageDeviceID("btrfs-" + label)
	cfg, ok := getConfigByDeviceID(deviceID)
	if !ok {
		return fmt.Errorf("Btrfs 存储池 %s 不存在", label)
	}
	if cfg.MountPath == "" {
		return fmt.Errorf("Btrfs 存储池 %s 未配置挂载点", label)
	}
	devicePaths, err := validateAndCollectPVTargets(deviceIDs)
	if err != nil {
		return fmt.Errorf("校验磁盘失败: %w", err)
	}
	devicePaths = resolveStableDevicePaths(devicePaths, readStableDeviceAliases())

	// btrfs device add <dev>... <path>
	args := append([]string{"device", "add"}, devicePaths...)
	args = append(args, cfg.MountPath)
	ctx := context.Background()
	if r := utils.ExecCommandContextWithTimeout(ctx, "btrfs", 2*time.Minute, args...); r.Error != nil {
		return fmt.Errorf("btrfs device add 失败: %s", strings.TrimSpace(r.Stderr))
	}
	return nil
}

// ── Scrub 用导出 helper ──

// ValidateBtrfsLabelExported 导出版本，供 handler 包校验 label。
func ValidateBtrfsLabelExported(name string) error { return validateBtrfsLabel(name) }

// GetBtrfsConfigByLabel 按 label 取受管 btrfs 池的配置。
func GetBtrfsConfigByLabel(label string) (model.HostStoragePool, bool) {
	return getConfigByDeviceID(normalizeStorageDeviceID("btrfs-" + label))
}
