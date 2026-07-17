package pool

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"kvm_console/utils"
)

// ── 解析 btrfs filesystem show 的 per-device used ──
//
// 样例：devid    1 size 10.00GiB used 5.00GiB path /dev/sda
var btrfsDevidRe = regexp.MustCompile(`devid\s+\d+\s+size\s+\S+\s+used\s+(\S+)\s+path\s+(\S+)`)

// scanBtrfsDeviceUsage 解析 `btrfs filesystem show <mount>`，返回 path→usedBytes。
func scanBtrfsDeviceUsage(mount string) map[string]int64 {
	out := map[string]int64{}
	r := utils.ExecCommandQuiet("btrfs", "filesystem", "show", mount)
	if r.Error != nil {
		return out
	}
	for _, line := range strings.Split(r.Stdout, "\n") {
		if m := btrfsDevidRe.FindStringSubmatch(strings.TrimSpace(line)); len(m) >= 3 {
			out[m[2]] = parseBtrfsBytes(m[1])
		}
	}
	return out
}

// ── 严格护栏 ──

// PreflightBtrfsShrink 移盘前硬拦截：
//  1. 目标盘必须是当前成员盘
//  2. 剩余盘数 ≥ 当前 data profile 最小
//  3. 剩余盘可用空间 ≥ 被移盘数据量（启发式，保守）
//  4. 未在 balance/scrub 中
func PreflightBtrfsShrink(label, mount string, deviceIDs []string) ([]string, error) {
	if len(deviceIDs) == 0 {
		return nil, fmt.Errorf("至少选择一块要移除的盘")
	}
	members := scanBtrfsDevices(mount)
	memberSet := map[string]bool{}
	for _, m := range members {
		memberSet[m] = true
	}
	// 成员盘校验由下方 memberSet 兜底：缩容操作的是已存在成员盘（只读引用节点），
	// 不能用 validateAndCollectPVTargets（它校验的是新 PV 候选，且按归一化 pool.ID 解析会与
	// 前端发来的裸 /dev/sdX 路径对不上）。这里只做 /dev/ 前缀净化，保持裸路径与
	// scanBtrfsDevices/scanBtrfsDeviceUsage 的 key 一致。
	devicePaths := make([]string, 0, len(deviceIDs))
	for _, d := range deviceIDs {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if !strings.HasPrefix(d, "/dev/") {
			return nil, fmt.Errorf("无效的设备路径: %s", d)
		}
		devicePaths = append(devicePaths, d)
	}
	if len(devicePaths) == 0 {
		return nil, fmt.Errorf("至少选择一块要移除的盘")
	}
	for _, dp := range devicePaths {
		if !memberSet[dp] {
			return nil, fmt.Errorf("%s 不是该存储池的成员盘", dp)
		}
	}
	remain := len(members) - len(devicePaths)
	curProfile, _ := readBtrfsDataProfileUsed(mount)
	min := btrfsProfileMinDisks(curProfile)
	if curProfile == "" {
		min = 1
	}
	if remain < min {
		return nil, fmt.Errorf("移除后仅剩 %d 块成员盘，低于 %s 所需的 %d 块", remain, btrfsProfileLabel(curProfile), min)
	}
	// 空间估算
	usage := scanBtrfsDeviceUsage(mount)
	var need int64
	for _, dp := range devicePaths {
		need += usage[dp]
	}
	free := freeBytesOfMount(mount)
	if need > 0 && free < need {
		return nil, fmt.Errorf("剩余空间不足：需迁移约 %s 数据，当前可用 %s（请先加盘或清理数据）", humanBtrfsBytes(need), humanBtrfsBytes(free))
	}
	if balanceAlreadyRunning(mount) {
		return nil, fmt.Errorf("该存储池有 balance/scrub 在运行，请先完成或取消")
	}
	return devicePaths, nil
}

// ShrinkBtrfsPool 移除成员盘（btrfs device delete，阻塞型，无增量进度）。
func ShrinkBtrfsPool(ctx context.Context, label, mount string, deviceIDs []string, progress func(int, string)) error {
	progress(10, "正在校验与迁移前置检查...")
	devicePaths, err := PreflightBtrfsShrink(label, mount, deviceIDs)
	if err != nil {
		return err
	}
	progress(30, fmt.Sprintf("正在迁移 %d 块盘上的数据，期间请勿关机...", len(devicePaths)))
	args := append([]string{"device", "delete"}, devicePaths...)
	args = append(args, mount)
	if r := utils.ExecCommandContextWithTimeout(ctx, "btrfs", 60*time.Minute, args...); r.Error != nil {
		return fmt.Errorf("btrfs device delete 失败: %s（数据未完成迁移，池保持原状）", strings.TrimSpace(r.Stderr))
	}
	progress(100, "缩容完成")
	return nil
}
