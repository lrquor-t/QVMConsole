package pool

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"kvm_console/utils"
)

// ── 类型 ──

// BtrfsBalanceStatus `btrfs balance status` 解析结果。
type BtrfsBalanceStatus struct {
	Label       string `json:"label"`
	Mount       string `json:"mount"`
	State       string `json:"state"` // idle/running/paused
	Pct         int    `json:"pct"`   // (N% complete)
	ChunksDone  int64  `json:"chunks_done"`
	ChunksTotal int64  `json:"chunks_total"`
	LeftBytes   int64  `json:"left"` // "X left"
}

// BtrfsBalanceStartReq 启动 balance 请求。
type BtrfsBalanceStartReq struct {
	Label         string `json:"label"`
	Mode          string `json:"mode"`           // reclaim|convert
	Usage         int    `json:"usage"`          // mode=reclaim：0~50
	TargetProfile string `json:"target_profile"` // mode=convert：single|raid1|raid10
}

// ── 纯函数：解析 balance status ──
//
// 样例（running）：
//
//	Balance on '/mnt/btank' is running
//	273 out of 5671 chunks balanced (4% complete), total 200.00GiB, 192.00GiB left
//
// 样例（paused）：
//
//	Balance on '/mnt/btank' is paused
//	273 out of 5671 chunks balanced (4% complete)
//
// 样例（idle）：
//
//	No balance in progress, 273 chunks balanced
var (
	btrfsBalRunningRe = regexp.MustCompile(`Balance on '.*' is running`)
	btrfsBalPausedRe  = regexp.MustCompile(`Balance on '.*' is paused`)
	btrfsBalIdleRe    = regexp.MustCompile(`No balance in progress`)
	btrfsBalChunkRe   = regexp.MustCompile(`(\d+) out of (\d+) chunks balanced \((\d+)% complete\)`)
	btrfsBalLeftRe    = regexp.MustCompile(`,\s+([0-9.]+[KMGTPE]?i?B?)\s+left`)
)

// parseBtrfsBalanceStatus 解析 `btrfs balance status`。
func parseBtrfsBalanceStatus(raw string) BtrfsBalanceStatus {
	st := BtrfsBalanceStatus{State: "idle"}
	for _, line := range strings.Split(raw, "\n") {
		c := strings.TrimSpace(line)
		if btrfsBalRunningRe.MatchString(c) {
			st.State = "running"
		} else if btrfsBalPausedRe.MatchString(c) {
			st.State = "paused"
		} else if btrfsBalIdleRe.MatchString(c) {
			st.State = "idle"
		}
		if m := btrfsBalChunkRe.FindStringSubmatch(c); len(m) >= 4 {
			st.ChunksDone, _ = strconv.ParseInt(m[1], 10, 64)
			st.ChunksTotal, _ = strconv.ParseInt(m[2], 10, 64)
			pct, _ := strconv.Atoi(m[3])
			st.Pct = pct
		}
		if m := btrfsBalLeftRe.FindStringSubmatch(c); len(m) > 1 {
			st.LeftBytes = parseBtrfsBytes(m[1])
		}
	}
	return st
}

// ── 纯函数：解析 btrfs filesystem df 的 Data 行（取 profile + used）──
//
// 样例：Data, single: total=8.00GiB, used=4.00GiB
var btrfsFsDfDataRe = regexp.MustCompile(`^Data,\s+(\w+):\s+total=([\d.]+[KMGTPE]?i?B?),\s+used=([\d.]+[KMGTPE]?i?B?)`)

// readBtrfsDataProfileUsed 解析 `btrfs filesystem df <mount>`，返回 (dataProfile, usedBytes)。
func readBtrfsDataProfileUsed(mount string) (string, int64) {
	r := utils.ExecCommand("btrfs", "filesystem", "df", mount)
	if r.Error != nil {
		return "", 0
	}
	for _, line := range strings.Split(r.Stdout, "\n") {
		if m := btrfsFsDfDataRe.FindStringSubmatch(strings.TrimSpace(line)); len(m) >= 4 {
			return m[1], parseBtrfsBytes(m[3])
		}
	}
	return "", 0
}

// ── 严格护栏 ──

// balanceAlreadyRunning balance/scrub 是否在运行中。
func balanceAlreadyRunning(mount string) bool {
	if s := parseBtrfsBalanceStatus(utils.ExecCommand("btrfs", "balance", "status", mount).Stdout); s.State == "running" || s.State == "paused" {
		return true
	}
	if s, _ := GetBtrfsScrubStatus(mount); s.State == "running" {
		return true
	}
	return false
}

// PreflightBtrfsBalance 严格护栏：不满足任一条返回 error（启动前硬拦截）。
//  1. 未在 balance/scrub 中
//  2. convert：盘数 ≥ 目标 profile 最小
//  3. convert 且为升级型（single→raid1/raid10）：可用空间 ≥ 已用 data（启发式，保守）
func PreflightBtrfsBalance(label, mount, mode, target string, usage int) error {
	if mode != "reclaim" && mode != "convert" {
		return fmt.Errorf("未知 balance 模式: %s", mode)
	}
	if balanceAlreadyRunning(mount) {
		return fmt.Errorf("该存储池已有 balance/scrub 在运行，请先完成或取消")
	}
	if mode == "convert" {
		if !isValidBtrfsProfile(target) {
			return fmt.Errorf("不支持的目标 profile: %s", target)
		}
		members := scanBtrfsDevices(mount)
		min := btrfsProfileMinDisks(target)
		if len(members) < min {
			return fmt.Errorf("%s 至少需要 %d 块盘，当前成员盘 %d 块", btrfsProfileLabel(target), min, len(members))
		}
		// 升级型转换空间估算
		curProfile, used := readBtrfsDataProfileUsed(mount)
		if (curProfile == "single") && (target == "raid1" || target == "raid10") && used > 0 {
			free := freeBytesOfMount(mount)
			need := used // raid1/raid10 冗余倍数≈2，约需 1×已用 的额外空间
			if free < need {
				return fmt.Errorf("可用空间不足：转换约需 %s 可用空间，当前仅 %s（请先加盘或清理数据）",
					humanBtrfsBytes(need), humanBtrfsBytes(free))
			}
		}
	}
	if mode == "reclaim" && (usage < 0 || usage > 50) {
		return fmt.Errorf("usage 取值范围 0~50")
	}
	return nil
}

// ── 命令封装 ──

// GetBtrfsBalanceStatus 读 balance 状态。
func GetBtrfsBalanceStatus(mount string) (BtrfsBalanceStatus, error) {
	r := utils.ExecCommand("btrfs", "balance", "status", mount)
	if r.Error != nil {
		return BtrfsBalanceStatus{Mount: mount, State: "idle"}, nil
	}
	st := parseBtrfsBalanceStatus(r.Stdout)
	st.Mount = mount
	return st, nil
}

// StartBtrfsBalance 启动 balance（后台运行）。调用方须先过 PreflightBtrfsBalance。
func StartBtrfsBalance(mount, mode, target string, usage int) error {
	var args []string
	if mode == "reclaim" {
		args = []string{"start", "-dusage=" + strconv.Itoa(usage), mount}
	} else { // convert
		args = []string{"start", "-dconvert=" + target, mount}
		if target == "raid1" || target == "raid10" {
			args = []string{"start", "-dconvert=" + target, "-mconvert=" + target, mount}
		}
	}
	if r := utils.ExecCommand("btrfs", append([]string{"balance"}, args...)...); r.Error != nil {
		return fmt.Errorf("启动 balance 失败: %s", strings.TrimSpace(r.Stderr))
	}
	return nil
}

// CancelBtrfsBalance 取消 balance。
func CancelBtrfsBalance(mount string) error {
	if r := utils.ExecCommand("btrfs", "balance", "cancel", mount); r.Error != nil {
		return fmt.Errorf("取消 balance 失败: %s", strings.TrimSpace(r.Stderr))
	}
	return nil
}

// PauseBtrfsBalance 暂停 balance。
func PauseBtrfsBalance(mount string) error {
	if r := utils.ExecCommand("btrfs", "balance", "pause", mount); r.Error != nil {
		return fmt.Errorf("暂停 balance 失败: %s", strings.TrimSpace(r.Stderr))
	}
	return nil
}

// ResumeBtrfsBalance 恢复 balance。
func ResumeBtrfsBalance(mount string) error {
	if r := utils.ExecCommand("btrfs", "balance", "resume", mount); r.Error != nil {
		return fmt.Errorf("恢复 balance 失败: %s", strings.TrimSpace(r.Stderr))
	}
	return nil
}
