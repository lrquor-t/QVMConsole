package service

import (
	"context"

	"kvm_console/model"
	"kvm_console/service/lxc"
	"kvm_console/service/lxc/zfsbacking"
	netpkg "kvm_console/service/network"
)

// init 注入 service 根包实现给 service/lxc 包的 Hook 变量，
// 打破反向依赖：service/lxc 不能 import service 根包。
func init() {
	lxc.HookIsMaintenanceModeEnabled = IsMaintenanceModeEnabled
}

// StartLXCHealthCheckScheduler 启动 LXC 健康检查后台调度器。
func StartLXCHealthCheckScheduler() {
	lxc.StartLXCHealthCheckScheduler()
}

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
func LXCStartContainer(name string) error    { return lxc.StartContainer(name) }
func LXCStopContainer(name string) error     { return lxc.StopContainer(name) }
func LXCRestartContainer(name string) error  { return lxc.RestartContainer(name) }
func LXCFreezeContainer(name string) error   { return lxc.FreezeContainer(name) }
func LXCUnfreezeContainer(name string) error { return lxc.UnfreezeContainer(name) }
func LXCDestroyContainer(name string) error  { return lxc.DestroyContainer(name) }

// LXCContainerConfigUpdate 透出 lxc.ContainerConfigUpdate，便于 handler 只依赖 service 包。
type LXCContainerConfigUpdate = lxc.ContainerConfigUpdate

// LXCUpdateContainerConfig 更新容器配置（cgroup/autostart/remark/group）。
func LXCUpdateContainerConfig(name string, u lxc.ContainerConfigUpdate) error {
	return lxc.UpdateContainerConfig(name, u)
}

// LXCConfigFileContent 透出 lxc.ConfigFileContent。
type LXCConfigFileContent = lxc.ConfigFileContent

// LXCConfigBackup 透出 lxc.ConfigBackup。
type LXCConfigBackup = lxc.ConfigBackup

// LXCReadContainerConfigFile 读取容器原始 config 文件内容 + 元信息。
func LXCReadContainerConfigFile(name string) (LXCConfigFileContent, error) {
	return lxc.ReadContainerConfigFile(name)
}

// LXCWriteContainerConfigFile 备份+原子写入容器 config（仅 STOPPED）。
func LXCWriteContainerConfigFile(name, content string) error {
	return lxc.WriteContainerConfigFile(name, content)
}

// LXCListContainerConfigBackups 列出容器 config 的历史备份（新→旧）。
func LXCListContainerConfigBackups(name string) ([]LXCConfigBackup, error) {
	return lxc.ListContainerConfigBackups(name)
}

// LXCReadContainerConfigBackup 读取一份历史备份的内容 + 元信息（任意状态可读）。
func LXCReadContainerConfigBackup(name, bakName string) (LXCConfigFileContent, error) {
	return lxc.ReadContainerConfigBackup(name, bakName)
}

// LXCRestoreContainerConfigFile 用备份覆盖当前 config（STOPPED-only，复用写入主路径）。
func LXCRestoreContainerConfigFile(name, bakName string) error {
	return lxc.RestoreContainerConfigFile(name, bakName)
}

// LXCDeleteContainerConfigFileBackup 删除一份备份（不要求 STOPPED）。
func LXCDeleteContainerConfigFileBackup(name, bakName string) error {
	return lxc.DeleteContainerConfigFileBackup(name, bakName)
}

// LXCGetDiskLimit 读取容器磁盘配额（refquota，GB；0=不限）。非 zfs 容器返回错误。
func LXCGetDiskLimit(name string) (int, error) {
	return lxc.LXCGetDiskLimit(name)
}

// LXCSetDiskLimit 设置容器磁盘配额（gb>0 设上限；0 取消）。非 zfs 容器返回错误。
func LXCSetDiskLimit(name string, gb int) error {
	return lxc.LXCSetDiskLimit(name, gb)
}

// LXC 一次性命令执行（owner，与 console 同级）
type LXCExecResult = lxc.ExecResult

func LXCExecContainer(name, command string, timeoutSec int) (lxc.ExecResult, error) {
	return lxc.ExecContainer(name, command, timeoutSec)
}

// LXC CPU 硬限制 + 绑核（管理员，config 即真相源、不入库）
type LXCCPULimit = lxc.CPULimit

func LXCGetCPULimit(name string) (lxc.CPULimit, error) { return lxc.GetCPULimit(name) }
func LXCSetCPULimit(name string, lim lxc.CPULimit) error {
	return lxc.SetCPULimit(name, lim)
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

// LXC 多网卡管理
type LXCAddInterfaceRequest = lxc.AddLXCInterfaceRequest
type LXCInterfaceInfo = lxc.LXCInterfaceInfo

func LXCListContainerInterfaces(name string) ([]lxc.LXCInterfaceInfo, error) {
	return lxc.ListContainerInterfaces(name)
}
func LXCAddContainerInterface(name string, req lxc.AddLXCInterfaceRequest) error {
	return lxc.AddContainerInterface(name, req)
}
func LXCUpdateContainerInterface(name string, order int, req lxc.AddLXCInterfaceRequest) error {
	return lxc.UpdateContainerInterface(name, order, req)
}
func LXCRemoveContainerInterface(name string, order int, force bool) error {
	return lxc.RemoveContainerInterface(name, order, force)
}

// LXC 克隆（从快照）
type LXCCloneParams = lxc.CloneParams

// LXCParseCloneParams 反序列化克隆任务参数。
func LXCParseCloneParams(s string) (*LXCCloneParams, error) {
	return lxc.ParseCloneParams(s)
}

// LXCValidateCloneFromSnapshot 同步预校验克隆参数。
func LXCValidateCloneFromSnapshot(srcName, snap, dstName string) error {
	return lxc.ValidateCloneFromSnapshot(srcName, snap, dstName)
}

// LXCCloneFromSnapshot 执行从快照克隆容器（后台任务）。
func LXCCloneFromSnapshot(p *lxc.CloneParams, progress func(int, string)) error {
	return lxc.CloneFromSnapshot(p, progress)
}

// LXCContainerSpecForQuota 取容器 CPU/Mem 规格（克隆配额校验用）。
func LXCContainerSpecForQuota(name string) (cpu, memMB int, err error) {
	return lxc.ContainerSpecForQuota(name)
}

// LXCSourcePrimarySwitchID 取源容器主卡（order0）所属交换机 ID（克隆预检固定 IP 用）。
func LXCSourcePrimarySwitchID(src string) uint {
	return lxc.SourcePrimarySwitchID(src)
}

// LXC 定时任务
type LXCScheduleInput = lxc.LXCScheduleInput
type LXCScheduleItem = lxc.LXCScheduleItem

func LXCListSchedules(name string) ([]LXCScheduleItem, error) {
	return lxc.ListLXCSchedules(name)
}
func LXCCreateLXCSchedule(name, createdBy string, in lxc.LXCScheduleInput) (*lxc.LXCScheduleItem, error) {
	return lxc.CreateLXCSchedule(name, createdBy, in)
}
func LXCUpdateLXCSchedule(name string, id uint, in lxc.LXCScheduleInput) (*lxc.LXCScheduleItem, error) {
	return lxc.UpdateLXCSchedule(name, id, in)
}
func LXCDeleteLXCSchedule(name string, id uint) error {
	return lxc.DeleteLXCSchedule(name, id)
}
func StartLXCScheduleRunner() {
	lxc.StartLXCScheduleRunner()
}
func RunLXCScheduledAction(ctx context.Context, task *model.Task, progress func(int, string)) (string, error) {
	return lxc.RunLXCScheduledAction(ctx, task, progress)
}

// ── 批量创建 ---

// LXCBatchCreateContainerParams 批量创建容器参数（透出 lxc.BatchCreateContainerParams）。
type LXCBatchCreateContainerParams = lxc.BatchCreateContainerParams

// LXCBatchResult 单容器批量创建结果。
type LXCBatchResult = lxc.LXCBatchResult

// LXCBatchName 生成批量容器名（prefix-NN），handler 预检/前端语义与创建共用。
func LXCBatchName(prefix string, n int) string { return lxc.BatchName(prefix, n) }

// LXCNameExists 容器名是否已占用（DB 行 或 lxc-info）。
func LXCNameExists(name string) bool { return lxc.NameExists(name) }

// LXCValidateName 容器名校验（正则 + 保留前缀）。
func LXCValidateName(name string) error { return lxc.ValidateName(name) }

// LXCParseBatchCreateContainerParams 反序列化批量创建任务参数。
func LXCParseBatchCreateContainerParams(s string) (*lxc.BatchCreateContainerParams, error) {
	return lxc.ParseBatchCreateContainerParams(s)
}

// LXCBatchCreateContainer 并发批量创建容器（部分成功，支持取消）。
func LXCBatchCreateContainer(ctx context.Context, params *lxc.BatchCreateContainerParams, progress func(int, string)) ([]lxc.LXCBatchResult, error) {
	return lxc.BatchCreateContainer(ctx, params, progress)
}

// LXCCheckQuotaForBatch 批量创建配额校验（×n）。
func LXCCheckQuotaForBatch(username string, cpu, ramMB, n int) error {
	return lxc.CheckLXCQuotaForBatch(username, cpu, ramMB, n)
}

// ── LXC 目录挂载（管理员）——

// LXCMount 透出 lxc.LXCMount，便于 handler 只依赖 service 包。
type LXCMount = lxc.LXCMount

// LXCMountListResult 透出 lxc.LXCMountListResult。
type LXCMountListResult = lxc.LXCMountListResult

// LXCListMounts 列出容器目录挂载（附状态/是否需重启）。
func LXCListMounts(name string) (LXCMountListResult, error) {
	return lxc.ListMounts(name)
}

// LXCAddMount 添加一条目录挂载（全量重写 config）。
func LXCAddMount(name string, m lxc.LXCMount) error {
	return lxc.AddMount(name, m)
}

// LXCDeleteMount 按 target 删除一条目录挂载。
func LXCDeleteMount(name, target string) error {
	return lxc.DeleteMount(name, target)
}

// ── LXC 端口映射（复用 VM iptables DNAT 链路）──

// LXCPortForwardRequest 透出 lxc.PortForwardRequest，便于 handler 只依赖 service 包。
type LXCPortForwardRequest = lxc.PortForwardRequest

// LXCListPortForwards 列出容器关联的端口转发规则（按容器所有可能 IP 过滤）。
func LXCListPortForwards(name string) ([]netpkg.PortForwardRule, error) {
	return lxc.ListContainerPortForwards(name)
}

// LXCAddPortForward 给容器新增端口转发（自动填容器 IP；HostPort 留空自动分配）。
func LXCAddPortForward(name string, req lxc.PortForwardRequest, createdBy string, isAdmin bool) error {
	return lxc.AddContainerPortForward(name, req, createdBy, isAdmin)
}

// LXCDeletePortForward 删除容器的一条端口转发（校验归属，避免误删其他容器/VM 规则）。
func LXCDeletePortForward(name string, id int) error {
	return lxc.DeleteContainerPortForward(name, id)
}

// ── LXC 健康检查（规则 CRUD + 手动探测 + 聚合状态查询）──

// LXCListHealthChecks 列出容器全部健康检查规则。
func LXCListHealthChecks(name string) ([]model.LXCHealthCheck, error) {
	return model.ListLXCHealthChecksByContainer(name)
}

// LXCCreateHealthCheck 创建健康检查规则。
func LXCCreateHealthCheck(h *model.LXCHealthCheck) error {
	return model.CreateLXCHealthCheck(h)
}

// LXCGetHealthCheck 取容器下指定规则（按 id+container 校验归属）。
func LXCGetHealthCheck(id uint, name string) (*model.LXCHealthCheck, error) {
	return model.GetLXCHealthCheckByIDAndContainer(id, name)
}

// LXCUpdateHealthCheck 保存健康检查规则。
func LXCUpdateHealthCheck(h *model.LXCHealthCheck) error {
	return model.UpdateLXCHealthCheck(h)
}

// LXCDeleteHealthCheck 删除单条规则（按 id+container 校验归属，避免误删他者规则）。
func LXCDeleteHealthCheck(id uint, name string) error {
	h, err := model.GetLXCHealthCheckByIDAndContainer(id, name)
	if err != nil {
		return err
	}
	return model.DeleteLXCHealthCheck(h.ID)
}

// LXCRunHealthProbe 立即探测一次容器健康（写各项结果 + 聚合状态），返回聚合状态。
func LXCRunHealthProbe(name string) (string, error) {
	return lxc.RunHealthCheckForContainer(name)
}

// LXCGetCacheByName 取容器缓存行（含聚合 HealthStatus）。GetLXCHealth handler 用。
func LXCGetCacheByName(name string) (*model.LXCCache, error) {
	return model.GetLXCCacheByName(name)
}
