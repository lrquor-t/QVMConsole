package lxc

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

const cpuPeriod = 100000 // cgroup2 cpu.max period 固定

// CPULimit 容器 CPU 硬限制视图。
type CPULimit struct {
	Cores  float64 `json:"cores"`  // 核数上限（支持小数，≤3 位）；0 = 不限
	CPUSet string  `json:"cpuset"` // 物理核绑定，如 "0-3,^2"；空 = 不绑
}

var cpusetRe = regexp.MustCompile(`^[0-9,\-^]*$`)

// coresToCPUMax 核数 → cgroup2 cpu.max 值。cores<=0 → "max"。
func coresToCPUMax(cores float64) string {
	if cores <= 0 {
		return "max"
	}
	quota := int64(math.Round(cores * float64(cpuPeriod)))
	return fmt.Sprintf("%d %d", quota, cpuPeriod)
}

// parseCPUMax 解析 cpu.max 值回核数。"max"/"" → 0；"quota period" → quota/period。非法 → (0,false)。
func parseCPUMax(val string) (float64, bool) {
	val = strings.TrimSpace(val)
	if val == "" || val == "max" {
		return 0, true
	}
	f := strings.Fields(val)
	if len(f) != 2 {
		return 0, false
	}
	quota, err1 := strconv.ParseFloat(f[0], 64)
	period, err2 := strconv.ParseFloat(f[1], 64)
	if err1 != nil || err2 != nil || period <= 0 {
		return 0, false
	}
	return quota / period, true
}

// validateCPULimit 校验核数（非负、≤3 位小数、≤nproc）与 cpuset 字符集。
func validateCPULimit(cores float64, cpuset string, nproc int) error {
	if cores < 0 {
		return errors.New("CPU 核数不能为负")
	}
	if math.Round(cores*1000) != cores*1000 {
		return errors.New("CPU 核数最多支持 3 位小数")
	}
	if nproc > 0 && cores > float64(nproc) {
		return fmt.Errorf("CPU 核数不能超过宿主机核数 %d", nproc)
	}
	if cpuset != "" && !cpusetRe.MatchString(cpuset) {
		return errors.New("CPU 绑核格式非法（仅允许数字、逗号、连字符、^）")
	}
	return nil
}

// renderCPULimit 渲染 config 键值文本（rewriteConfigKeys 的 appendText）。
// cores 始终输出 cpu.max（0 → "max" 显式不限）；cpuset 为空则不输出该行。
func renderCPULimit(lim CPULimit) string {
	var b strings.Builder
	fmt.Fprintf(&b, "lxc.cgroup2.cpu.max = %s\n", coresToCPUMax(lim.Cores))
	if cs := strings.TrimSpace(lim.CPUSet); cs != "" {
		fmt.Fprintf(&b, "lxc.cgroup2.cpuset.cpus = %s\n", cs)
	}
	return b.String()
}
