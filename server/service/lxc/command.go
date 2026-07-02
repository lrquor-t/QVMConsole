package lxc

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"strings"

	"kvm_console/model"
	"kvm_console/utils"
)

// IsLxcAvailable 报告 PATH 中是否存在 lxc-create（用于集成测试跳过判定）。
func IsLxcAvailable() bool {
	_, err := exec.LookPath("lxc-create")
	return err == nil
}

// LxcLsFancy 执行 lxc-ls --fancy。
func LxcLsFancy() *utils.CmdResult {
	return utils.ExecCommand("lxc-ls", "--fancy")
}

// LxcInfo 执行 lxc-info -n <name>。
func LxcInfo(name string) *utils.CmdResult {
	return utils.ExecCommand("lxc-info", "-n", name)
}

// ParseLxcLsFancy 解析 `lxc-ls --fancy` 输出。
// 表头行形如：NAME STATE IPV4 IPV6 AUTOSTART TYPE GROUP
func ParseLxcLsFancy(stdout string) ([]ContainerListItem, error) {
	stdout = strings.TrimSpace(stdout)
	if stdout == "" {
		return []ContainerListItem{}, nil
	}
	lines := strings.Split(stdout, "\n")
	if len(lines) < 2 {
		return nil, errors.New("lxc-ls 输出格式异常：缺少表头")
	}
	header := strings.Fields(lines[0])
	idx := map[string]int{}
	for i, h := range header {
		idx[strings.ToUpper(h)] = i
	}
	// 至少要有 NAME 与 STATE 列
	if _, ok := idx["NAME"]; !ok {
		return nil, errors.New("lxc-ls 输出缺少 NAME 列")
	}
	out := make([]ContainerListItem, 0, len(lines)-1)
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		item := ContainerListItem{}
		if i, ok := idx["NAME"]; ok && i < len(fields) {
			item.Name = fields[i]
		}
		if i, ok := idx["STATE"]; ok && i < len(fields) {
			item.Status = fields[i]
			item.Running = strings.EqualFold(item.Status, "RUNNING")
		}
		if i, ok := idx["IPV4"]; ok && i < len(fields) {
			v := fields[i]
			if v != "-" {
				item.IPv4 = v
			}
		}
		if i, ok := idx["AUTOSTART"]; ok && i < len(fields) {
			item.Autostart = strings.ToUpper(fields[i])
		}
		out = append(out, item)
	}
	return out, nil
}

// ParseLxcInfo 解析 `lxc-info -n <name>` 的 "Key: Value" 输出。
func ParseLxcInfo(stdout string) (ContainerDetail, error) {
	d := ContainerDetail{}
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		idx := strings.Index(line, ":")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		switch strings.ToLower(key) {
		case "name":
			d.Name = val
		case "state":
			d.Status = val
		case "ip":
			d.IP = val
		case "pid":
			d.PID = val
		case "arch", "architecture":
			d.Arch = val
		}
	}
	return d, nil
}

// genMacByName 由名称派生本地管理 MAC（02: 前缀，避免与 VM 段冲突）。
func genMacByName(seed string) string {
	h := sha1.Sum([]byte(seed))
	hx := hex.EncodeToString(h[:5])
	return "02:" + hx[0:2] + ":" + hx[2:4] + ":" + hx[4:6] + ":" + hx[6:8] + ":" + hx[8:10]
}

// openForAppend 以追加写方式打开文件。
func openForAppend(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
}

// RefreshRuntimeFields 启动后解析 host veth（按容器 MAC 匹配 ip link）与 IP（lxc-info）回填 LXCCache。
func RefreshRuntimeFields(name string) error {
	var row model.LXCCache
	if err := model.DB.Where("name = ?", name).First(&row).Error; err != nil {
		return err
	}
	// IP / Status
	res := LxcInfo(name)
	if res.ExitCode == 0 {
		if d, err := ParseLxcInfo(res.Stdout); err == nil {
			row.CachedIP = d.IP
			row.Status = d.Status
		}
	}
	// veth by MAC
	if row.MacAddress != "" {
		if veth := findVethByMAC(row.MacAddress); veth != "" {
			row.VethName = veth
		}
	}
	return model.DB.Save(&row).Error
}

// findVethByMAC 在 host 上按 MAC 查找容器对应 veth 名。
func findVethByMAC(mac string) string {
	if mac == "" {
		return ""
	}
	res := utils.ExecShell("ip -o link | grep -i '" + normalizeMAC(mac) + "'")
	// 行形如: "5: vethX@if4: ... link/ether 02:xx ..."
	for _, line := range strings.Split(res.Stdout, "\n") {
		if !strings.Contains(line, "@") {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 2 {
			continue
		}
		field := strings.TrimSpace(parts[1])
		if i := strings.Index(field, "@"); i > 0 {
			return field[:i]
		}
	}
	return ""
}

// normalizeMAC 统一 MAC 大小写与分隔，便于在 ip link 输出中匹配。
func normalizeMAC(mac string) string {
	return strings.ToLower(strings.TrimSpace(mac))
}
