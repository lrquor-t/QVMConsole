package lxc

import (
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"kvm_console/model"
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
	if math.Abs(math.Round(cores*1000)-cores*1000) > 1e-6 {
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

// readConfigKeyValues 读取 config 文本中指定 key 的值（每个 key 取最后一次出现）。纯函数。
func readConfigKeyValues(text string, keys []string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		for _, k := range keys {
			if v, ok := cutConfigKey(t, k); ok {
				out[k] = v
			}
		}
	}
	return out
}

// cutConfigKey 若行 t 形如 "k = v" 或 "k=v" 返回 (v,true)。
func cutConfigKey(t, k string) (string, bool) {
	if v, ok := strings.CutPrefix(t, k+" = "); ok {
		return strings.TrimSpace(v), true
	}
	if v, ok := strings.CutPrefix(t, k+"="); ok {
		return strings.TrimSpace(v), true
	}
	return "", false
}

// GetCPULimit 读容器 config 解析当前 CPU 硬限制。
func GetCPULimit(name string) (CPULimit, error) {
	data, err := os.ReadFile(containerConfigPath(name))
	if err != nil {
		return CPULimit{}, fmt.Errorf("读取容器配置失败: %w", err)
	}
	kv := readConfigKeyValues(string(data), []string{"lxc.cgroup2.cpu.max", "lxc.cgroup2.cpuset.cpus"})
	lim := CPULimit{}
	if cores, ok := parseCPUMax(kv["lxc.cgroup2.cpu.max"]); ok {
		lim.Cores = math.Round(cores*1000) / 1000 // 归整到 3 位小数
	}
	lim.CPUSet = kv["lxc.cgroup2.cpuset.cpus"]
	return lim, nil
}

// SetCPULimit 校验并写入 CPU 硬限制；运行中热应用 cgroup。
func SetCPULimit(name string, lim CPULimit) error {
	var row model.LXCCache
	if err := model.DB.Where("name = ?", name).First(&row).Error; err != nil {
		return errors.New("容器不存在")
	}
	if err := validateCPULimit(lim.Cores, lim.CPUSet, runtime.NumCPU()); err != nil {
		return err
	}
	cfg := containerConfigPath(name)
	if err := rewriteConfigKeys(cfg, []string{"lxc.cgroup2.cpu.max", "lxc.cgroup2.cpuset.cpus"}, renderCPULimit(lim)); err != nil {
		return fmt.Errorf("更新配置文件失败: %w", err)
	}
	if strings.ToUpper(strings.TrimSpace(row.Status)) == "RUNNING" {
		applyCgroup(row.Name, "cpu.max", coresToCPUMax(lim.Cores))
		if cs := strings.TrimSpace(lim.CPUSet); cs != "" {
			applyCgroup(row.Name, "cpuset.cpus", cs)
		}
	}
	return nil
}
