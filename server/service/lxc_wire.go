package service

import (
	"kvm_console/service/lxc"
	"kvm_console/service/lxc/zfsbacking"
)

// LXCSyncContainerCache 同步 LXC 容器缓存。
func LXCSyncContainerCache() error { return lxc.SyncContainerCache() }

// LXCListContainers 列出可见容器。
func LXCListContainers(username string, isAdmin bool) ([]any, error) {
	rows, err := lxc.ListContainers(username, isAdmin)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, r)
	}
	return out, nil
}

// LXCGetContainerDetail 取容器详情。
func LXCGetContainerDetail(name string) (lxc.ContainerDetail, error) {
	return lxc.GetContainerDetail(name)
}

// LXCCreateContainerParams 创建容器参数（透出 lxc.CreateContainerParams，便于 handler
// 只依赖 service 包而无需直接 import service/lxc）。
type LXCCreateContainerParams = lxc.CreateContainerParams

// LXCCreateContainer 由模板克隆创建容器。
func LXCCreateContainer(p *lxc.CreateContainerParams, progress func(int, string)) error {
	return lxc.CreateContainer(p, progress)
}

// LXCParseCreateContainerParams 反序列化创建容器任务参数。
func LXCParseCreateContainerParams(s string) (*lxc.CreateContainerParams, error) {
	return lxc.ParseCreateContainerParams(s)
}

// LXC 生命周期封装
func LXCStartContainer(name string) error   { return lxc.StartContainer(name) }
func LXCStopContainer(name string) error    { return lxc.StopContainer(name) }
func LXCRestartContainer(name string) error { return lxc.RestartContainer(name) }
func LXCDestroyContainer(name string) error { return lxc.DestroyContainer(name) }

// LXCContainerConfigUpdate 透出 lxc.ContainerConfigUpdate，便于 handler 只依赖 service 包。
type LXCContainerConfigUpdate = lxc.ContainerConfigUpdate

// LXCUpdateContainerConfig 更新容器配置（cgroup/autostart/remark/group）。
func LXCUpdateContainerConfig(name string, u lxc.ContainerConfigUpdate) error {
	return lxc.UpdateContainerConfig(name, u)
}

// LXCCheckQuota 校验用户 LXC 配额（admin 不限）。
func LXCCheckQuota(username string, cpu, ramMB int) error {
	return lxc.CheckLXCQuota(username, cpu, ramMB)
}

// LXC 快照封装
type LXCSnapshot = lxc.LXCSnapshot

func LXCListSnapshots(name string) ([]LXCSnapshot, error) {
	return lxc.ListSnapshots(name)
}
func LXCCreateSnapshot(name, comment string) error { return lxc.CreateSnapshot(name, comment) }
func LXCRestoreSnapshot(name, snap string) error   { return lxc.RestoreSnapshot(name, snap) }
func LXCDeleteSnapshot(name, snap string) error    { return lxc.DeleteSnapshot(name, snap) }

// LXC 快照任务参数（透出 lxc.SnapshotParams，便于 handler/main 只依赖 service 包）。
type LXCSnapshotParams = lxc.SnapshotParams

func LXCParseSnapshotParams(s string) (*LXCSnapshotParams, error) {
	return lxc.ParseSnapshotParams(s)
}

// LXCRelocateParams 透出 lxc.RelocateParams，便于 handler/main 只依赖 service 包。
type LXCRelocateParams = lxc.RelocateParams

// LXCRelocate 执行完整 LXC 存储迁移（后台任务）。
func LXCRelocate(p lxc.RelocateParams, progress func(int, string)) error {
	return lxc.Relocate(p, progress)
}

// LXCSwitchLxcPath 无容器时的轻量切换。
func LXCSwitchLxcPath(newLxcPath, newImportDir string) error {
	return lxc.SwitchLxcPath(newLxcPath, newImportDir)
}

// LXCEstimateRelocateTargets 探测迁移规模（用户容器数、模板数、待搬目录数）。
func LXCEstimateRelocateTargets() (containers, templates, totalDirs int, err error) {
	return lxc.EstimateRelocateTargets()
}

// LXCCascadeImportDir 计算迁移后模板导入临时目录的级联值。
func LXCCascadeImportDir(oldLxcPath, newLxcPath, curImportDir string) string {
	return lxc.CascadeImportDir(oldLxcPath, newLxcPath, curImportDir)
}

// LXCIsLxcpathZfs 报告 lxcpath 是否在 zfs 上（前端据此给"dir on zfs 用 zfs 更优"提示）。
func LXCIsLxcpathZfs(lxcpath string) bool {
	return zfsbacking.IsLxcpathZfs(lxcpath)
}

// LXCDownloadImageEntry 透出 lxc.DownloadImageEntry。
type LXCDownloadImageEntry = lxc.DownloadImageEntry

// LXCDownloadList 拉取官方镜像清单（带缓存）。
func LXCDownloadList() ([]lxc.DownloadImageEntry, error) {
	return lxc.DownloadImageList()
}
