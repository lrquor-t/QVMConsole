package lxc

// zfs backing 命令/纯函数已下沉到 service/lxc/zfsbacking 子包，以打破 import 环：
// service/lxc/create.go 依赖 service/lxc/template，而 template 也要调 zfs 命令。
// 本文件保留 lxc 包内的同名封装（既有 lxc.Zfs* 调用方与小写内部调用方继续可用），
// 实现全部转发到 zfsbacking。纯函数单测见 zfs_test.go（此处仅保持测试用的小写别名）。

import "kvm_console/service/lxc/zfsbacking"

const zfsBaseSnap = zfsbacking.BaseSnap

// —— 纯函数：dataset 名构造（便于单测；转发到 zfsbacking）——

func zfsBaseDataset(parent, base string) string  { return zfsbacking.BaseDataset(parent, base) }
func zfsBaseSnapshot(parent, base string) string { return zfsbacking.BaseSnapshot(parent, base) }
func zfsContainerDataset(parent, name string) string {
	return zfsbacking.ContainerDataset(parent, name)
}
func zfsContainerMountpoint(lxcpath, name string) string {
	return zfsbacking.ContainerMountpoint(lxcpath, name)
}
func rewriteRootfsPathForClone(cfg, oldRootfsPath, newRootfsPath string) string {
	return zfsbacking.RewriteRootfsPathForClone(cfg, oldRootfsPath, newRootfsPath)
}

// —— 跨包用→导出（转发到 zfsbacking）——

func ZfsResolveParent(lxcpath string) (string, error) { return zfsbacking.ResolveParent(lxcpath) }
func ZfsCreateBase(parent, base string) error         { return zfsbacking.CreateBase(parent, base) }
func ZfsSnapshotBase(parent, base string) error       { return zfsbacking.SnapshotBase(parent, base) }
func ZfsDestroyBase(parent, base string) error        { return zfsbacking.DestroyBase(parent, base) }
func zfsCloneContainer(parent, base, name string) error {
	return zfsbacking.CloneContainer(parent, base, name)
}
func zfsDestroyContainer(parent, name string) error { return zfsbacking.DestroyContainer(parent, name) }
func isZfsContainer(name string) bool               { return zfsbacking.IsZfsContainer(name) }
