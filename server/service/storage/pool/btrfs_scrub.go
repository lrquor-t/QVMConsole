package pool

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"kvm_console/utils"
)

// ── 类型 ──

// BtrfsScrubStatus `btrfs scrub status` 解析结果。
type BtrfsScrubStatus struct {
	Label        string `json:"label"`
	Mount        string `json:"mount"`
	State        string `json:"state"`      // none/running/finished/canceled
	Pct          int    `json:"pct"`        // 运行中：bytes_scrubbed/total
	ScannedBytes int64  `json:"scanned"`    // Bytes scrubbed
	TotalBytes   int64  `json:"total"`      // Total to scrub
	Duration     string `json:"duration"`   // "0:01:23"
	StartedAt    string `json:"started_at"` // "Scrub started: ..."
	ReadErr      int64  `json:"read_err"`
	WriteErr     int64  `json:"write_err"`
	CsumErr      int64  `json:"csum_err"`
}

// ── 纯函数：btrfs 字节量解析 ──
//
// btrfs-progs 输出形如 "4.00GiB"/"16.00KiB"，base-1024。剥掉 "iB" 后缀再查表。
// 复用思路同 parseZFSBytes，但因 btrfs 带 "iB" 后缀，单独实现。
func parseBtrfsBytes(s string) int64 {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "/s")
	if s == "" || s == "-" {
		return 0
	}
	var numStr, unit string
	for i, r := range s {
		if (r >= '0' && r <= '9') || r == '.' {
			continue
		}
		numStr = s[:i]
		unit = strings.TrimSpace(s[i:])
		break
	}
	if numStr == "" {
		numStr = s
	}
	f, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0
	}
	// "GiB" -> "G"，"MiB" -> "M"，"B" -> "B"
	unit = strings.TrimSuffix(strings.ToUpper(unit), "IB")
	mul := map[string]float64{
		"": 1, "B": 1,
		"K": 1024, "M": 1024 * 1024, "G": 1024 * 1024 * 1024,
		"T": 1024 * 1024 * 1024 * 1024, "P": 1024 * 1024 * 1024 * 1024 * 1024,
		"E": 1024 * 1024 * 1024 * 1024 * 1024 * 1024,
	}
	return int64(f * mul[unit])
}

// ── 纯函数：解析 btrfs scrub status ──
//
// 目标 btrfs-progs v6.x 表格化输出样例（finished）：
//
//	Status:           finished
//	Duration:         0:01:23
//	Total to scrub:   4.00GiB
//	Bytes scrubbed:   4.00GiB
//	Error summary:    no errors found
//
// running 样例：Status: running，多出 Rate 行；错误时 Error summary: read=N write=M csum=K
//
//	read errors:    N
//	write errors:   M
//	csum errors:    K
var (
	btrfsScrubStatusRe = regexp.MustCompile(`(?i)^\s*Status:\s+(\w+)`)
	btrfsScrubDurRe    = regexp.MustCompile(`(?i)^\s*Duration:\s+(\S+)`)
	btrfsScrubTotalRe  = regexp.MustCompile(`(?i)^\s*Total to scrub:\s+(\S+)`)
	btrfsScrubDoneRe   = regexp.MustCompile(`(?i)^\s*Bytes scrubbed:\s+(\S+)`)
	btrfsScrubStartRe  = regexp.MustCompile(`(?i)^\s*Scrub started:\s+(.+)$`)
	btrfsScrubErrRe    = regexp.MustCompile(`(?i)^\s*(read|write|csum) errors:\s+(\d+)`)
)

// parseBtrfsScrubStatus 解析 `btrfs scrub status <mount>` 输出。无法识别时 State="none"。
func parseBtrfsScrubStatus(raw string) BtrfsScrubStatus {
	st := BtrfsScrubStatus{State: "none"}
	for _, line := range strings.Split(raw, "\n") {
		if m := btrfsScrubStatusRe.FindStringSubmatch(line); len(m) > 1 {
			switch strings.ToLower(m[1]) {
			case "running":
				st.State = "running"
			case "finished":
				st.State = "finished"
			case "aborted":
				st.State = "canceled"
			}
		}
		if m := btrfsScrubDurRe.FindStringSubmatch(line); len(m) > 1 {
			st.Duration = m[1]
		}
		if m := btrfsScrubTotalRe.FindStringSubmatch(line); len(m) > 1 {
			st.TotalBytes = parseBtrfsBytes(m[1])
		}
		if m := btrfsScrubDoneRe.FindStringSubmatch(line); len(m) > 1 {
			st.ScannedBytes = parseBtrfsBytes(m[1])
		}
		if m := btrfsScrubStartRe.FindStringSubmatch(line); len(m) > 1 {
			st.StartedAt = strings.TrimSpace(m[1])
		}
		if m := btrfsScrubErrRe.FindStringSubmatch(line); len(m) > 2 {
			n, _ := strconv.ParseInt(m[2], 10, 64)
			switch strings.ToLower(m[1]) {
			case "read":
				st.ReadErr = n
			case "write":
				st.WriteErr = n
			case "csum":
				st.CsumErr = n
			}
		}
	}
	if st.State == "running" && st.TotalBytes > 0 {
		st.Pct = int(float64(st.ScannedBytes) / float64(st.TotalBytes) * 100)
		if st.Pct > 100 {
			st.Pct = 100
		}
	}
	return st
}

// ── 命令封装 ──

// GetBtrfsScrubStatus 读取某 btrfs 池（按挂载点）的 scrub 状态。
// 命令失败/从未 scrub 时返回 State="none"（不当硬错误，对齐 ZFS 语义）。
func GetBtrfsScrubStatus(mount string) (BtrfsScrubStatus, error) {
	r := utils.ExecCommand("btrfs", "scrub", "status", mount)
	if r.Error != nil {
		// 从未 scrub 或池未挂载：返回 none，不抛错
		return BtrfsScrubStatus{Mount: mount, State: "none"}, nil
	}
	st := parseBtrfsScrubStatus(r.Stdout)
	st.Mount = mount
	return st, nil
}

// StartBtrfsScrub 启动 scrub（后台运行，立即返回）。
func StartBtrfsScrub(mount string) error {
	if r := utils.ExecCommand("btrfs", "scrub", "start", mount); r.Error != nil {
		return fmt.Errorf("启动 scrub 失败: %s", strings.TrimSpace(r.Stderr))
	}
	return nil
}

// CancelBtrfsScrub 取消正在进行的 scrub。
func CancelBtrfsScrub(mount string) error {
	if r := utils.ExecCommand("btrfs", "scrub", "cancel", mount); r.Error != nil {
		return fmt.Errorf("取消 scrub 失败: %s", strings.TrimSpace(r.Stderr))
	}
	return nil
}
