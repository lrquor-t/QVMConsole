package pool

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
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
