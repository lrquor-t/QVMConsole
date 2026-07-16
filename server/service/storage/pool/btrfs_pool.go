package pool

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

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
	case "single", "raid0":
		return 1
	case "raid1":
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

	// 成员盘路径 → label
	memberToLabel := make(map[string]string)
	for _, bp := range bPools {
		for _, m := range bp.Devices {
			memberToLabel[m] = bp.Label
		}
	}
	markBtrfsMemberNodes(pools, memberToLabel)

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

// markBtrfsMemberNodes 标记属于受管 btrfs 池的成员盘节点：只读、容量清零。
func markBtrfsMemberNodes(pools []HostStoragePoolInfo, memberToLabel map[string]string) {
	for i := range pools {
		if label, ok := memberToLabel[pools[i].DevicePath]; ok {
			pools[i].Readonly = true
			pools[i].CanFormat = false
			pools[i].CanUseForVM = false
			pools[i].StatusReason = "已加入 Btrfs 存储池 " + label
			pools[i].Size = 0
			pools[i].Used = 0
			pools[i].Available = 0
			pools[i].UsePercent = 0
		}
		markBtrfsMemberNodes(pools[i].Children, memberToLabel)
	}
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
		Size:         0,
		Readonly:     true,
		StatusReason: "Btrfs 存储池 " + label + " 成员盘",
	}
}
