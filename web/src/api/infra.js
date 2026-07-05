import request from '@/utils/request'

// ==================== 存储池管理 ====================

// 获取存储池列表
export function getStoragePoolList() {
  return request({
    url: '/storage-pool/list',
    method: 'get'
  })
}

// 获取存储池详情
export function getStoragePoolDetail(id) {
  return request({
    url: `/storage-pool/${encodeURIComponent(id)}`,
    method: 'get'
  })
}

// 更新存储池配置
export function updateStoragePoolConfig(id, data) {
  return request({
    url: `/storage-pool/${encodeURIComponent(id)}/config`,
    method: 'put',
    data
  })
}

// 设置默认存储池
export function setDefaultStoragePool(id) {
  return request({
    url: `/storage-pool/${encodeURIComponent(id)}/default`,
    method: 'post',
  })
}

// 格式化并挂载存储池
export function formatMountStoragePool(id, fstype) {
  return request({
    url: `/storage-pool/${encodeURIComponent(id)}/format-mount`,
    method: 'post',
    data: { fstype }
  })
}

// 创建磁盘分区
export function createStoragePartition(id, data) {
  return request({
    url: `/storage-pool/${encodeURIComponent(id)}/create-partition`,
    method: 'post',
    data
  })
}

// 删除磁盘所有分区
export function deleteStoragePartitions(id) {
  return request({
    url: `/storage-pool/${encodeURIComponent(id)}/delete-partitions`,
    method: 'post'
  })
}

// 获取创建虚拟机可选存储位置
export function getVMStorageTargets() {
  return request({
    url: '/storage-pool/vm-targets',
    method: 'get'
  })
}

// 获取所有存储池中的 ISO（聚合）
export function getAllISOs() {
  return request({
    url: '/storage-pool/all-isos',
    method: 'get'
  })
}

// 获取可供 LVM 使用的磁盘列表
export function getAvailablePVTargets() {
  return request({
    url: '/storage-pool/pv-targets',
    method: 'get'
  })
}

// 创建 LVM 存储卷
export function createLVMVolume(data) {
  return request({
    url: '/storage-pool/create-volume',
    method: 'post',
    data
  })
}

// 删除 LVM 存储卷
export function deleteLVMVolume(vgName) {
  return request({
    url: '/storage-pool/delete-volume',
    method: 'post',
    data: { vg_name: vgName }
  })
}

// 检测宿主机 ZFS 可用性
export function getZFSStatus() {
  return request({
    url: '/storage-pool/zfs-status',
    method: 'get'
  })
}

// 创建 ZFS 存储池
export function createZFSPool(data) {
  return request({
    url: '/storage-pool/create-zfs-pool',
    method: 'post',
    data
  })
}

// 在已有 ZFS 存储池下创建数据集
export function createZFSDataset(data) {
  return request({
    url: '/storage-pool/zfs-dataset',
    method: 'post',
    data
  })
}

// 删除 ZFS 数据集
export function deleteZFSDataset(data) {
  return request({
    url: '/storage-pool/zfs-dataset',
    method: 'delete',
    data
  })
}

// 销毁 ZFS 存储池
export function deleteZFSPool(poolName) {
  return request({
    url: '/storage-pool/delete-zfs-pool',
    method: 'post',
    data: { pool_name: poolName }
  })
}
