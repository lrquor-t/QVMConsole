package pool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"kvm_console/utils"
)

// BtrfsPropertyInfo 池级可改属性。
type BtrfsPropertyInfo struct {
	Label       string   `json:"label"`
	Mount       string   `json:"mount"`
	Compression string   `json:"compression"` // zstd/lzo/zlib/off（挂载选项 compress=）
	NoCow       bool     `json:"nocow"`       // vm-disks 是否 nodatacow（chattr C）
	Algos       []string `json:"algos"`       // 可选压缩算法
}

// ── 读 ──

// GetBtrfsProperty 读当前压缩挂载选项 + vm-disks 的 CoW 标志。
func GetBtrfsProperty(mount string) (BtrfsPropertyInfo, error) {
	info := BtrfsPropertyInfo{Mount: mount, Compression: "off", Algos: []string{"off", "zstd", "lzo", "zlib"}}
	// 压缩：findmnt -no OPTIONS
	r := utils.ExecCommand("findmnt", "-no", "OPTIONS", mount)
	if r.Error == nil {
		for _, opt := range strings.Split(strings.TrimSpace(r.Stdout), ",") {
			opt = strings.TrimSpace(opt)
			if strings.HasPrefix(opt, "compress=") {
				info.Compression = strings.TrimPrefix(opt, "compress=")
				break
			}
		}
	}
	// CoW：lsattr -d <vmdir>
	vmDir := filepath.Join(mount, "vm-disks")
	if _, err := os.Stat(vmDir); err == nil {
		ra := utils.ExecCommand("lsattr", "-d", vmDir)
		if ra.Error == nil && strings.Contains(ra.Stdout, "C") {
			info.NoCow = true
		}
	}
	return info, nil
}

// ── 写：压缩（remount + fstab）──

// updateBtrfsFstabOptions 按 mountPath 定位 /etc/fstab 行，只替换第 4 字段（options），不动其余。
func updateBtrfsFstabOptions(mount, options string) error {
	data, err := os.ReadFile("/etc/fstab")
	if err != nil {
		return fmt.Errorf("读取 /etc/fstab 失败: %w", err)
	}
	var lines []string
	changed := false
	for _, line := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			lines = append(lines, line)
			continue
		}
		fields := strings.Fields(t)
		if len(fields) >= 4 && fields[1] == mount && fields[2] == "btrfs" {
			fields[3] = options
			lines = append(lines, strings.Join(fields, "\t"))
			changed = true
		} else {
			lines = append(lines, line)
		}
	}
	if !changed {
		return fmt.Errorf("未在 /etc/fstab 找到 %s 的 btrfs 条目，跳过持久化", mount)
	}
	content := strings.Join(lines, "\n")
	if err := os.WriteFile("/etc/fstab", []byte(content), 0644); err != nil {
		return fmt.Errorf("写入 /etc/fstab 失败: %w", err)
	}
	return nil
}

// SetBtrfsCompression remount 改压缩 + 同步 fstab。algo=off 用 nocompress。
// 仅对新写入数据生效。
func SetBtrfsCompression(mount, algo string) error {
	algo = strings.ToLower(strings.TrimSpace(algo))
	var remountOpt, fstabOpt string
	if algo == "off" {
		remountOpt = "nocompress"
		fstabOpt = "defaults,nofail"
	} else {
		remountOpt = "compress=" + algo
		fstabOpt = "defaults,nofail,compress=" + algo
	}
	if r := utils.ExecCommand("mount", "-o", "remount,"+remountOpt, mount); r.Error != nil {
		return fmt.Errorf("remount 失败: %s", strings.TrimSpace(r.Stderr))
	}
	if err := updateBtrfsFstabOptions(mount, fstabOpt); err != nil {
		return err
	}
	return nil
}

// ── 写：CoW（chattr）──

// SetBtrfsNoCow 切换 vm-disks 的 nodatacow。仅对目录内新创建文件生效。
func SetBtrfsNoCow(mount string, enabled bool) error {
	vmDir := filepath.Join(mount, "vm-disks")
	if _, err := os.Stat(vmDir); err != nil {
		return fmt.Errorf("vm-disks 目录不存在: %w", err)
	}
	flag := "-C"
	if enabled {
		flag = "+C"
	}
	if r := utils.ExecCommand("chattr", flag, vmDir); r.Error != nil {
		return fmt.Errorf("chattr 失败: %s", strings.TrimSpace(r.Stderr))
	}
	return nil
}
