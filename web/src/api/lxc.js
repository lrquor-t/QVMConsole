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
export function createLXCSnapshot(name) {
  return request({ url: `/lxc/${name}/snapshot`, method: 'post' })
}
export function restoreLXCSnapshot(name, snap) {
  return request({ url: `/lxc/${name}/snapshot/${snap}/restore`, method: 'post' })
}
export function deleteLXCSnapshot(name, snap) {
  return request({ url: `/lxc/${name}/snapshot/${snap}`, method: 'delete' })
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
