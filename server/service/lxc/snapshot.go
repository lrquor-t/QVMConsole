package lxc

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"kvm_console/config"
	"kvm_console/utils"
)

// LXCSnapshot 是前端展示的快照（list 返回）。
type LXCSnapshot struct {
	Name      string `json:"name"`
	Comment   string `json:"comment"`
	CreatedAt string `json:"created_at"`
}

// SnapshotParams 是异步创建快照任务的参数。
type SnapshotParams struct {
	Name    string `json:"name"`
	Comment string `json:"comment"`
}

// ParseSnapshotParams 反序列化创建快照任务参数。
func ParseSnapshotParams(s string) (*SnapshotParams, error) {
	var p SnapshotParams
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// ListSnapshots 列出容器快照（按 backing 分支；返回新→旧）。
func ListSnapshots(name string) ([]LXCSnapshot, error) {
	if isZfsContainer(name) {
		return listZfsSnapshots(name)
	}
	return listDirSnapshots(name)
}

func listZfsSnapshots(name string) ([]LXCSnapshot, error) {
	parent, err := ZfsResolveParent(config.GlobalConfig.LXCLxcPath)
	if err != nil {
		return nil, err
	}
	zs, err := zfsListContainerSnapshots(parent, name)
	if err != nil {
		return nil, err
	}
	out := make([]LXCSnapshot, 0, len(zs))
	for i := len(zs) - 1; i >= 0; i-- { // zfs 默认旧→新，反转为新→旧
		out = append(out, LXCSnapshot{Name: zs[i].Name, Comment: zs[i].Comment, CreatedAt: zs[i].CreatedAt})
	}
	return out, nil
}

func listDirSnapshots(name string) ([]LXCSnapshot, error) {
	res := utils.ExecCommand("lxc-snapshot", "-n", name, "-L")
	if res.ExitCode != 0 {
		if strings.Contains(res.Stderr, "no snapshot") || strings.Contains(res.Stderr, "not supported") {
			return []LXCSnapshot{}, nil
		}
		return nil, errors.New("列出快照失败: " + res.Stderr)
	}
	snaps := parseSnapshotList(res.Stdout)
	for i, j := 0, len(snaps)-1; i < j; i, j = i+1, j-1 { // lxc-snapshot -L 旧→新(snap0..)，反转为新→旧
		snaps[i], snaps[j] = snaps[j], snaps[i]
	}
	return snaps, nil
}

// parseSnapshotList 解析 lxc-snapshot -L 的 stdout（Name/Comment/Creation time 三列，空格对齐）。
// name=首段；creation=末两段(日期 时间)；comment=中间段(可含空格)；Comment 为 "-" 视为空。
func parseSnapshotList(stdout string) []LXCSnapshot {
	var out []LXCSnapshot
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Name") {
			continue
		}
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		snap := LXCSnapshot{Name: f[0]}
		if len(f) >= 4 {
			snap.CreatedAt = f[len(f)-2] + " " + f[len(f)-1]
			snap.Comment = strings.Join(f[1:len(f)-2], " ")
		}
		if snap.Comment == "-" {
			snap.Comment = ""
		}
		out = append(out, snap)
	}
	return out
}

// CreateSnapshot 对容器创建新快照（异步任务调用；可能耗时）。
func CreateSnapshot(name, comment string) error {
	if isZfsContainer(name) {
		return createZfsSnapshot(name, comment)
	}
	return createDirSnapshot(name, comment)
}

func createZfsSnapshot(name, comment string) error {
	parent, err := ZfsResolveParent(config.GlobalConfig.LXCLxcPath)
	if err != nil {
		return err
	}
	snap := "snap-" + time.Now().Format("20060102150405")
	if err := zfsSnapshotContainer(parent, name, snap); err != nil {
		return err
	}
	return zfsSetSnapshotComment(parent, name, snap, comment)
}

func createDirSnapshot(name, comment string) error {
	args := []string{"-n", name}
	if comment != "" {
		tmp, err := os.CreateTemp("", "lxc-snap-comment-*")
		if err != nil {
			return fmt.Errorf("创建备注临时文件失败: %w", err)
		}
		if _, err := tmp.WriteString(comment); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return fmt.Errorf("写入备注临时文件失败: %w", err)
		}
		tmp.Close()
		defer os.Remove(tmp.Name())
		args = append(args, "-c", tmp.Name())
	}
	res := utils.ExecCommandLongRunning("lxc-snapshot", args...)
	return res.Error
}

// RestoreSnapshot 从指定快照恢复（先防御性关机；zfs 回滚会销毁更新快照——前端已二次确认）。
func RestoreSnapshot(name, snap string) error {
	// 防御性关机：zfs 回滚不应作用于运行中容器的 live rootfs；dir 路径也更安全。
	_ = utils.ExecCommandQuiet("lxc-stop", "-n", name).Error
	if isZfsContainer(name) {
		parent, err := ZfsResolveParent(config.GlobalConfig.LXCLxcPath)
		if err != nil {
			return err
		}
		return zfsRollbackContainer(parent, name, snap)
	}
	res := utils.ExecCommandLongRunning("lxc-snapshot", "-n", name, "-r", snap)
	return res.Error
}

// DeleteSnapshot 删除指定快照。
func DeleteSnapshot(name, snap string) error {
	if isZfsContainer(name) {
		parent, err := ZfsResolveParent(config.GlobalConfig.LXCLxcPath)
		if err != nil {
			return err
		}
		return zfsDestroyContainerSnapshot(parent, name, snap)
	}
	res := utils.ExecCommandLongRunning("lxc-snapshot", "-n", name, "-d", snap)
	return res.Error
}
