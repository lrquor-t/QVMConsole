import request from '@/utils/request'

// 容器列表
export function getLXCList() {
  return request({ url: '/lxc/list', method: 'get' })
}

// 容器详情
export function getLXCDetail(name) {
  return request({ url: `/lxc/${name}/detail`, method: 'get' })
}

// 创建容器（异步任务）
export function createLXC(data) {
  return request({ url: '/lxc/create', method: 'post', data })
}

// 批量创建
export function batchCreateLXC(data) {
  return request({ url: '/lxc/batch-create', method: 'post', data })
}

// 启动/停止/重启
export function operateLXC(name, action) {
  return request({ url: `/lxc/${name}/operate`, method: 'post', data: { action } })
}

// 删除容器（异步任务）
export function deleteLXC(name) {
  return request({ url: `/lxc/${name}`, method: 'delete' })
}

// 批量操作
export function batchOperateLXC(names, action) {
  return request({ url: '/lxc/batch', method: 'post', data: { names, action } })
}

// 更新配置（cgroup/autostart/remark/group）
export function updateLXCConfig(name, data) {
  return request({ url: `/lxc/${name}/config`, method: 'put', data })
}

// 容器 IP
export function getLXCIP(name) {
  return request({ url: `/lxc/${name}/ip`, method: 'get' })
}

// 快照
export function listLXCSnapshots(name) {
  return request({ url: `/lxc/${name}/snapshots`, method: 'get' })
}
export function createLXCSnapshot(name, comment = '') {
  return request({ url: `/lxc/${name}/snapshot`, method: 'post', data: { comment } })
}
export function restoreLXCSnapshot(name, snap) {
  return request({ url: `/lxc/${name}/snapshot/${snap}/restore`, method: 'post' })
}
export function deleteLXCSnapshot(name, snap) {
  return request({ url: `/lxc/${name}/snapshot/${snap}`, method: 'delete' })
}

// 读取容器磁盘配额（GB，0=不限）
export function getLXCDiskLimit(name) {
  return request({ url: `/lxc/${name}/disk-limit`, method: 'get' })
}

// 设置/取消容器磁盘配额（gb>0 设上限，0 取消）
export function setLXCDiskLimit(name, gb) {
  return request({ url: `/lxc/${name}/disk-limit`, method: 'put', data: { gb } })
}

// 模板
export function getLXCTemplateList() {
  return request({ url: '/lxc/template/list', method: 'get' })
}
export function finalizeLXCTemplate(data) {
  return request({ url: '/lxc/template/finalize', method: 'post', data })
}
export function deleteLXCTemplate(name) {
  return request({ url: `/lxc/template/${name}`, method: 'delete' })
}

// 更新模板展示/管理元数据（管理员）
export function updateLXCTemplate(name, data) {
  return request({ url: `/lxc/template/${name}`, method: 'put', data })
}

// 构造终端 WS 地址（与 utils/vnc.js 的 buildVncWsUrl 风格一致）
export function buildLXCConsoleWsUrl(name, token) {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const host = window.location.host
  return `${protocol}//${host}/api/lxc/${name}/console/ws?token=${encodeURIComponent(token)}`
}

// ==================== LXC 模板 rootfs tarball 分片上传 ====================
export function lxcTemplateUploadInit(data) {
  return request({ url: '/lxc/template/upload/init', method: 'post', data })
}
export function lxcTemplateUploadChunk(formData) {
  return request({
    url: '/lxc/template/upload/chunk',
    method: 'post',
    data: formData,
    timeout: 0,
    maxContentLength: Infinity,
    maxBodyLength: Infinity
  })
}
export function lxcTemplateUploadComplete(data) {
  return request({ url: '/lxc/template/upload/complete', method: 'post', data })
}
// POST（非 DELETE）：后端 lxcTmpl 组已有 DELETE /:name，DELETE /upload 会通配冲突
export function lxcTemplateUploadCancel(path) {
  return request({ url: '/lxc/template/upload/cancel', method: 'post', params: { path } })
}

// 探测 tarball 结构 + 解析 os-release，回填 distro/release
// 大包定向探测通常秒级，但缺成员时需扫到底；取消前端 60s 超时兜底
export function probeLXCTemplate(data) {
  return request({ url: '/lxc/template/probe', method: 'post', data, timeout: 0 })
}

// LXC 存储目录迁移/切换
// data: { new_lxc_path: string, migrate: boolean }
export function relocateLXCStorage(data) {
  return request({ url: '/lxc/storage/relocate', method: 'post', data })
}

// lxc 目录是否在 zfs 上 + 默认 backing（导入页提示用）
export function getLXCBackingInfo() {
  return request({ url: '/lxc/storage/backing-info', method: 'get' })
}

// lxc-create -t download 官方镜像清单（distro/release/arch）
export function getLXCDownloadList() {
  return request({ url: '/lxc/download/list', method: 'get' })
}

// ==================== LXC 多网卡管理 ====================
export function listLXCInterfaces(name) {
  return request({ url: `/lxc/${name}/interfaces`, method: 'get' })
}
export function addLXCInterface(name, data) {
  return request({ url: `/lxc/${name}/interfaces`, method: 'post', data })
}
export function updateLXCInterface(name, order, data) {
  return request({ url: `/lxc/${name}/interfaces/${order}`, method: 'put', data })
}
export function removeLXCInterface(name, order, data) {
  return request({ url: `/lxc/${name}/interfaces/${order}`, method: 'delete', data })
}

// 从容器制作 LXC 模板（异步任务，仅管理员）
export function makeLXCTemplateFromContainer(srcName, data) {
  return request({
    url: '/lxc/template/from-container',
    method: 'post',
    data: { src_name: srcName, ...data }
  })
}

// 从源容器快照克隆出新容器（异步任务；仅 zfs 后端）
export function cloneLXCFromSnapshot(srcName, data) {
  return request({
    url: `/lxc/${srcName}/clone`,
    method: 'post',
    data
  })
}

// ==================== LXC 容器资源监控 ====================
// LXC 容器实时资源监控（CPU/内存/网络/磁盘用量）
export function getLXCStats(name) {
  return request({ url: `/lxc/${name}/stats`, method: 'get' })
}

// LXC 容器资源历史（按日期范围）
export function getLXCStatsHistory(name, start, end) {
  return request({ url: `/lxc/${name}/stats/history`, method: 'get', params: { start, end } })
}

// ==================== LXC 定时任务 ====================
export function getLXCSchedules(name) {
  return request({
    url: `/lxc/${name}/schedules`,
    method: 'get'
  })
}

export function createLXCSchedule(name, data) {
  return request({
    url: `/lxc/${name}/schedules`,
    method: 'post',
    data
  })
}

export function updateLXCSchedule(name, id, data) {
  return request({
    url: `/lxc/${name}/schedules/${id}`,
    method: 'put',
    data
  })
}

export function deleteLXCSchedule(name, id) {
  return request({
    url: `/lxc/${name}/schedules/${id}`,
    method: 'delete'
  })
}

// ==================== LXC 目录挂载（管理员）====================
// 列出容器目录挂载（返回 {status, restart_required, mounts[]}）
export function getLXCMounts(name) {
  return request({ url: `/lxc/${name}/mounts`, method: 'get' })
}

// 添加目录挂载 data: { host_path, target, read_only }
export function addLXCMount(name, data) {
  return request({ url: `/lxc/${name}/mounts`, method: 'post', data })
}

// 删除目录挂载（按容器内挂载点 target）
export function deleteLXCMount(name, target) {
  return request({ url: `/lxc/${name}/mounts`, method: 'delete', params: { target } })
}

// 执行单条命令（owner，同步返回 stdout/stderr/exit_code/truncated/timed_out）
export function execLXC(name, data) {
  return request({ url: `/lxc/${name}/exec`, method: 'post', data })
}

// CPU 硬限制 + 绑核（管理员；cores 核数支持小数，0=不限；cpuset 如 0-3,^2）
export function getLXCCPULimit(name) {
  return request({ url: `/lxc/${name}/cpu-limit`, method: 'get' })
}
export function setLXCCPULimit(name, data) {
  return request({ url: `/lxc/${name}/cpu-limit`, method: 'put', data })
}

// config 配置文件（管理员）
export function getLXCConfigFile(name) {
  return request({ url: `/lxc/${name}/config-file`, method: 'get' })
}
export function setLXCConfigFile(name, content) {
  return request({ url: `/lxc/${name}/config-file`, method: 'put', data: { content } })
}
export function getLXCConfigFileBackups(name) {
  return request({ url: `/lxc/${name}/config-file/backups`, method: 'get' })
}
export function restoreLXCConfigFileBackup(name, bak) {
  return request({ url: `/lxc/${name}/config-file/backups/${bak}/restore`, method: 'post' })
}
export function deleteLXCConfigFileBackup(name, bak) {
  return request({ url: `/lxc/${name}/config-file/backups/${bak}`, method: 'delete' })
}
// 读取某份历史备份的内容（管理员，只读查看）
export function getLXCConfigFileBackup(name, bak) {
  return request({ url: `/lxc/${name}/config-file/backups/${bak}`, method: 'get' })
}

// ==================== LXC 端口映射 ====================
// 列表（按容器所有可能 IP 过滤全局规则；后端自动回填 vm_name/owner）
export function getLXCPortForwards(name) {
  return request({ url: `/lxc/${name}/port-forwards`, method: 'get' })
}

// 新增（host_port 留空自动分配；data: { host_port, vm_port, protocol, comment? }）
export function addLXCPortForward(name, data) {
  return request({ url: `/lxc/${name}/port-forwards`, method: 'post', data })
}

// 删除（后端 requireHighRiskVerification，二次验证由全局 axios 拦截器触发）
export function deleteLXCPortForward(name, id) {
  return request({ url: `/lxc/${name}/port-forwards/${id}`, method: 'delete' })
}

// ==================== LXC 健康检查 ====================
// 列出容器全部健康检查规则
export function getLXCHealthChecks(name) {
  return request({ url: `/lxc/${name}/health-checks`, method: 'get' })
}

// 新增（data: { check_name, type, target, expected_code, critical, enabled }；script 仅管理员）
export function addLXCHealthCheck(name, data) {
  return request({ url: `/lxc/${name}/health-checks`, method: 'post', data })
}

// 更新
export function updateLXCHealthCheck(name, id, data) {
  return request({ url: `/lxc/${name}/health-checks/${id}`, method: 'put', data })
}

// 删除
export function deleteLXCHealthCheck(name, id) {
  return request({ url: `/lxc/${name}/health-checks/${id}`, method: 'delete' })
}

// 容器聚合健康状态 + 各规则最新探测结果（返回 { status, checks, checked_at }）
export function getLXCHealth(name) {
  return request({ url: `/lxc/${name}/health`, method: 'get' })
}

// 手动立即探测一次（返回 { status }）
export function probeLXCHealth(name) {
  return request({ url: `/lxc/${name}/health/probe`, method: 'post' })
}
