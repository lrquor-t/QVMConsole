package lxc

import (
	"fmt"
	"path/filepath"
	"strings"

	"kvm_console/config"
	"kvm_console/utils"
)

// zfs backing：lxc-create -t none 不设 rootfs，lxc-copy -B zfs 是全量拷贝，故走手动 zfs clone（CoW）。
// 布局（每个容器一个 dataset，rootfs 是子目录；parent = 挂载在 LXCLxcPath 的 zfs dataset）：
//   <parent>/<base>            模板容器 dataset（含 config + rootfs/）
//   <parent>/<base>@base       模板快照（导入末尾打一次，所有容器克隆源）
//   <parent>/<name>            容器 dataset（clone of @base），mountpoint <lxcpath>/<name>

const zfsBaseSnap = "@base"

// —— 纯函数：dataset 名构造（便于单测）——
func zfsBaseDataset(parent, base string) string          { return parent + "/" + base }
func zfsBaseSnapshot(parent, base string) string         { return zfsBaseDataset(parent, base) + zfsBaseSnap }
func zfsContainerDataset(parent, name string) string     { return parent + "/" + name }
func zfsContainerMountpoint(lxcpath, name string) string { return filepath.Join(lxcpath, name) }

// rewriteRootfsPathForClone 把克隆 config 里继承自基底的 rootfs 路径替换为容器自己的（纯函数）。
func rewriteRootfsPathForClone(cfg, oldRootfsPath, newRootfsPath string) string {
	return strings.ReplaceAll(cfg, oldRootfsPath, newRootfsPath)
}

// —— zfs 命令封装（实测命令；非单测，靠真机手测 Task 7）——

// ZfsResolveParent 返回挂载在 lxcpath 的 zfs dataset 名（如 /zp01/lxc → zp01/lxc）。跨包用→导出。
func ZfsResolveParent(lxcpath string) (string, error) {
	res := utils.ExecCommand("zfs", "list", "-Ho", "name", lxcpath)
	if res.Error != nil {
		return "", fmt.Errorf("解析 lxcpath 的 zfs dataset 失败（%s 不是 zfs 挂载点？backing=zfs 仅支持 lxc 目录在 zfs 上）: %w", lxcpath, res.Error)
	}
	parent := strings.TrimSpace(res.Stdout)
	if parent == "" {
		return "", fmt.Errorf("lxcpath %s 未对应任何 zfs dataset", lxcpath)
	}
	return parent, nil
}

// ZfsCreateBase 创建模板容器 dataset <parent>/<base>（rootfs 是其子目录，不再单独建 dataset）。跨包用→导出。
func ZfsCreateBase(parent, base string) error {
	if res := utils.ExecCommand("zfs", "create", "-p", zfsBaseDataset(parent, base)); res.Error != nil {
		return fmt.Errorf("zfs create 模板 dataset 失败: %w", res.Error)
	}
	return nil
}

// ZfsSnapshotBase 给模板 dataset 打 @base 快照（导入末尾一次；克隆源）。跨包用→导出。
func ZfsSnapshotBase(parent, base string) error {
	if res := utils.ExecCommand("zfs", "snapshot", zfsBaseSnapshot(parent, base)); res.Error != nil {
		return fmt.Errorf("zfs snapshot @base 失败: %w", res.Error)
	}
	return nil
}

// ZfsDestroyBase 销毁模板 dataset（-r 连带 @base 快照；有克隆时 zfs 会拒绝）。跨包用→导出。
func ZfsDestroyBase(parent, base string) error {
	if res := utils.ExecCommand("zfs", "destroy", "-r", zfsBaseDataset(parent, base)); res.Error != nil {
		return fmt.Errorf("zfs destroy 模板失败: %w", res.Error)
	}
	return nil
}

// zfsCloneContainer 从 <parent>/<base>@base 克隆出 <parent>/<name>，mountpoint 设到 <lxcpath>/<name>。
// 克隆继承基底 config + rootfs（CoW）；调用方随后改写 config 的 rootfs.path。
func zfsCloneContainer(parent, base, name string) error {
	ds := zfsContainerDataset(parent, name)
	if res := utils.ExecCommand("zfs", "clone", zfsBaseSnapshot(parent, base), ds); res.Error != nil {
		return fmt.Errorf("zfs clone 失败: %w", res.Error)
	}
	if res := utils.ExecCommand("zfs", "set", "mountpoint="+zfsContainerMountpoint(config.GlobalConfig.LXCLxcPath, name), ds); res.Error != nil {
		return fmt.Errorf("zfs set mountpoint 失败: %w", res.Error)
	}
	return nil
}

// zfsDestroyContainer 销毁容器 dataset <parent>/<name>（调用方再 rm 残留空目录）。
func zfsDestroyContainer(parent, name string) error {
	if res := utils.ExecCommand("zfs", "destroy", zfsContainerDataset(parent, name)); res.Error != nil {
		return fmt.Errorf("zfs destroy 容器失败: %w", res.Error)
	}
	return nil
}

// isZfsContainer 判断 <lxcpath>/<name> 是否本身就是 zfs dataset 挂载点（zfs 容器），
// 还是父 dataset 上的普通子目录（dir/overlay 容器）。查 zfs 该路径的 mountpoint：
// zfs 容器 → mountpoint == 该路径；dir 容器 → mountpoint 是父（如 /zp01/lxc）≠ 该路径。
// 比 DB Backing 更稳：不受孤儿/手工篡改影响（看真实文件系统状态）。
func isZfsContainer(name string) bool {
	p := filepath.Join(config.GlobalConfig.LXCLxcPath, name)
	res := utils.ExecCommand("zfs", "list", "-Ho", "mountpoint", p)
	if res.Error != nil {
		return false
	}
	return strings.TrimSpace(res.Stdout) == p
}
