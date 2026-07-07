package lxc

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"kvm_console/config"
)

// parseUnifiedCgroupPath 从 /proc/<pid>/cgroup 内容取 cgroup2 unified 路径。
// 输入形如 "1:cpuset:/\n0::/lxc.payload.foo\n"；返回 "/lxc.payload.foo"，无则 ""。
func parseUnifiedCgroupPath(procCgroup string) string {
	for _, line := range strings.Split(procCgroup, "\n") {
		line = strings.TrimSpace(line)
		i := strings.Index(line, "::")
		if i < 0 {
			continue
		}
		rest := strings.TrimSpace(line[i+2:])
		if rest != "" {
			return rest
		}
	}
	return ""
}

// parseUsageUsec 从 cpu.stat 内容取 usage_usec 值。无则 0。
func parseUsageUsec(cpuStat string) int64 {
	sc := bufio.NewScanner(strings.NewReader(cpuStat))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 2 && f[0] == "usage_usec" {
			if v, err := strconv.ParseInt(f[1], 10, 64); err == nil {
				return v
			}
		}
	}
	return 0
}

// effCPUFromCPUMax 解析 cpu.max "quota period" → eff CPU 数；"max period" 或异常 → fallback。
func effCPUFromCPUMax(cpuMax string, fallback int) int {
	f := strings.Fields(strings.TrimSpace(cpuMax))
	if len(f) >= 2 && f[0] != "max" {
		quota, e1 := strconv.ParseInt(f[0], 10, 64)
		period, e2 := strconv.ParseInt(f[1], 10, 64)
		if e1 == nil && e2 == nil && period > 0 && quota > 0 {
			return int(quota / period)
		}
	}
	return fallback
}

// cpuPercentFromDelta 由两次累计 usage_usec 与墙钟间隔算 CPU%，封顶 100。
func cpuPercentFromDelta(prevUsec, curUsec int64, dtSeconds float64, effCPU int) float64 {
	if dtSeconds <= 0 || effCPU <= 0 || curUsec <= prevUsec {
		return 0
	}
	usedSec := float64(curUsec-prevUsec) / 1e6
	pct := usedSec / dtSeconds / float64(effCPU) * 100
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return pct
}

// diskUsageBytes 用 statfs 取 path 的 (已用字节, 总字节)。失败返回 (_, _, false)。
func diskUsageBytes(path string) (used, total int64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, false
	}
	total = int64(st.Blocks) * int64(st.Bsize)
	free := int64(st.Bfree) * int64(st.Bsize)
	return total - free, total, true
}

// rootfsPath 取容器 rootfs 绝对路径：解析 config 的 lxc.rootfs.path（剥 "dir:"/"zfs:" 等前缀），
// 无则兜底 <lxcpath>/<name>/rootfs。
func rootfsPath(name string) string {
	if data, err := os.ReadFile(configPath(name)); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			l := strings.TrimSpace(line)
			if !strings.HasPrefix(l, "lxc.rootfs.path") {
				continue
			}
			idx := strings.Index(l, "=")
			if idx < 0 {
				continue
			}
			v := strings.TrimSpace(l[idx+1:])
			if c := strings.Index(v, ":"); c >= 0 {
				v = v[c+1:]
			}
			if v != "" {
				return v
			}
		}
	}
	return filepath.Join(config.GlobalConfig.LXCLxcPath, name, "rootfs")
}

// 以下为采集编排用的小工具，Task 3 实现 GetContainerStats 时复用。
func readFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func readIntFile(dir, name string) int64 {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return 0
	}
	v, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return v
}

func hostMemTotalKB() int64 {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fld := strings.Fields(sc.Text())
		if len(fld) >= 2 && fld[0] == "MemTotal:" {
			if v, err := strconv.ParseInt(fld[1], 10, 64); err == nil {
				return v // /proc/meminfo 单位已是 KB
			}
		}
	}
	return 0
}

// 占位：避免 runtime 在 Task 3 之前未使用导致编译错误（Task 3 会用 runtime.NumCPU）。
var _ = runtime.NumCPU
