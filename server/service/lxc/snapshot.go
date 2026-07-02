package lxc

import (
	"errors"
	"strings"

	"kvm_console/utils"
)

// ListSnapshots 列出指定容器的快照名。
func ListSnapshots(name string) ([]string, error) {
	res := utils.ExecCommand("lxc-snapshot", "-n", name, "-L")
	if res.ExitCode != 0 {
		// 无快照/不支持时 lxc-snapshot 可能非零；stderr 提示
		if strings.Contains(res.Stderr, "no snapshot") || strings.Contains(res.Stderr, "not supported") {
			return []string{}, nil
		}
		return nil, errors.New("列出快照失败: " + res.Stderr)
	}
	return parseSnapshotList(res.Stdout), nil
}

// parseSnapshotList 是 ListSnapshots 的纯解析函数（便于单测，无 shell 副作用）。
// 输入为 `lxc-snapshot -L` 的 stdout：首行表头（Name ...），其余行每行一个快照。
func parseSnapshotList(stdout string) []string {
	var snaps []string
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Name") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 {
			snaps = append(snaps, fields[0])
		}
	}
	return snaps
}

// CreateSnapshot 对容器创建新快照（可能耗时，使用长任务执行器）。
func CreateSnapshot(name string) error {
	res := utils.ExecCommandLongRunning("lxc-snapshot", "-n", name)
	return res.Error
}

// RestoreSnapshot 从指定快照恢复容器。
func RestoreSnapshot(name, snap string) error {
	res := utils.ExecCommandLongRunning("lxc-snapshot", "-n", name, "-r", snap)
	return res.Error
}

// DeleteSnapshot 删除指定快照。
func DeleteSnapshot(name, snap string) error {
	res := utils.ExecCommandLongRunning("lxc-snapshot", "-n", name, "-d", snap)
	return res.Error
}
