<template>
  <div class="lxc-tpl-container">
    <!-- 页面头 -->
    <div class="lxc-tpl-header">
      <div class="lxc-tpl-header-left">
        <h2 class="lxc-tpl-title">LXC 模板</h2>
        <span class="lxc-tpl-sub">共 {{ tableData.length }} 个模板</span>
      </div>
      <div class="lxc-tpl-header-right">
        <el-button type="success" :icon="Refresh" :loading="loading" @click="fetchData">刷新</el-button>
        <el-button type="primary" :icon="Plus" @click="openImport">导入模板</el-button>
      </div>
    </div>

    <!-- 表格 -->
    <div class="lxc-tpl-wrap" v-loading="loading">
      <el-table :data="tableData" style="width: 100%" v-if="tableData.length">
        <el-table-column label="名称" min-width="150" show-overflow-tooltip>
          <template #default="{ row }">
            <span class="tpl-name">{{ row.name }}</span>
          </template>
        </el-table-column>
        <el-table-column label="系统" min-width="150">
          <template #default="{ row }">
            <span class="distro-tag" :class="distroClass(row.distro)">
              {{ [row.distro, row.release].filter(Boolean).join(' ') || '-' }}
            </span>
          </template>
        </el-table-column>
        <el-table-column prop="arch" label="架构" width="90" align="center" />
        <el-table-column prop="backing" label="后端" width="100" align="center" />
        <el-table-column label="rootfs 大小" width="120" align="center">
          <template #default="{ row }">{{ formatSize(row.rootfs_size_bytes) }}</template>
        </el-table-column>
        <el-table-column label="状态" width="90" align="center">
          <template #default="{ row }">
            <el-tag :type="row.disabled ? 'info' : 'success'" size="small" effect="light">{{ row.disabled ? '禁用' : '启用' }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="created_at" label="创建时间" width="180" align="center" />
        <el-table-column label="操作" width="100" fixed="right" align="center">
          <template #default="{ row }">
            <el-tooltip content="删除模板" placement="top">
              <el-button size="small" type="danger" plain circle :icon="Delete" @click="handleDelete(row)" />
            </el-tooltip>
          </template>
        </el-table-column>
      </el-table>

      <!-- 空状态 -->
      <div v-else class="tpl-empty">
        <div class="tpl-empty-icon">📦</div>
        <div class="tpl-empty-text">暂无模板</div>
        <div class="tpl-empty-hint">点击右上角「导入模板」导入宿主机上的 rootfs tarball</div>
      </div>
    </div>

    <el-dialog v-model="importVisible" title="导入 LXC 模板" width="520px">
      <el-form :model="importForm" label-width="110px">
        <el-form-item label="模板名称" required>
          <el-input v-model="importForm.name" placeholder="如 ubuntu22（小写字母/数字/连字符）" />
        </el-form-item>
        <el-form-item label="发行版">
          <el-input v-model="importForm.distro" placeholder="ubuntu / debian / ..." />
        </el-form-item>
        <el-form-item label="版本">
          <el-input v-model="importForm.release" placeholder="22.04 / bookworm / ..." />
        </el-form-item>
        <el-form-item label="架构">
          <el-select v-model="importForm.arch" style="width:100%">
            <el-option label="amd64" value="amd64" />
            <el-option label="arm64" value="arm64" />
          </el-select>
        </el-form-item>
        <el-form-item label="主机 tarball 路径" required>
          <el-input v-model="importForm.host_path" placeholder="宿主机上 rootfs tarball 的绝对路径" />
        </el-form-item>
        <el-form-item label="创建后命令">
          <el-input v-model="importForm.post_create_command" type="textarea" :rows="2" placeholder="可选：首次创建容器后 lxc-attach 执行" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="importVisible = false">取消</el-button>
        <el-button type="primary" :loading="importing" @click="handleImport">导入</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus, Refresh, Delete } from '@element-plus/icons-vue'
import { getLXCTemplateList, finalizeLXCTemplate, deleteLXCTemplate } from '@/api/lxc'

const tableData = ref([])
const loading = ref(false)
const importVisible = ref(false)
const importing = ref(false)
const importForm = ref({ name: '', distro: '', release: '', arch: 'amd64', host_path: '', post_create_command: '' })

const formatSize = (b) => {
  if (!b) return '-'
  const mb = b / 1024 / 1024
  if (mb < 1024) return mb.toFixed(0) + ' MB'
  return (mb / 1024).toFixed(2) + ' GB'
}

const distroClass = (distro) => {
  const d = (distro || '').toLowerCase()
  if (d.includes('ubuntu')) return 'd-ubuntu'
  if (d.includes('debian')) return 'd-debian'
  if (d.includes('alpine')) return 'd-alpine'
  if (d.includes('centos')) return 'd-centos'
  if (d.includes('rocky')) return 'd-rocky'
  if (d.includes('alma')) return 'd-alma'
  if (d.includes('fedora')) return 'd-fedora'
  if (d.includes('arch')) return 'd-arch'
  if (d.includes('suse') || d.includes('opensuse')) return 'd-suse'
  return 'd-other'
}

const fetchData = async () => {
  loading.value = true
  try {
    const res = await getLXCTemplateList()
    tableData.value = res.data || []
  } catch (e) {
    ElMessage.error('获取模板列表失败')
  } finally {
    loading.value = false
  }
}

const openImport = () => {
  importForm.value = { name: '', distro: '', release: '', arch: 'amd64', host_path: '', post_create_command: '' }
  importVisible.value = true
}

const handleImport = async () => {
  if (!importForm.value.name || !importForm.value.host_path) {
    ElMessage.warning('请填写模板名称与主机 tarball 路径')
    return
  }
  importing.value = true
  try {
    await finalizeLXCTemplate({ ...importForm.value, host_path: importForm.value.host_path })
    ElMessage.success('导入成功')
    importVisible.value = false
    fetchData()
  } catch (e) {
    // 错误由 request 拦截器提示
  } finally {
    importing.value = false
  }
}

const handleDelete = async (row) => {
  await ElMessageBox.confirm(`确认删除模板 ${row.name}？其金基底容器将一并销毁。`, '删除模板', { type: 'warning' })
  try {
    await deleteLXCTemplate(row.name)
    ElMessage.success('已删除')
    fetchData()
  } catch (e) {}
}

onMounted(fetchData)
</script>

<style scoped>
.lxc-tpl-container {
  padding: 10px;
}

/* 页面头 */
.lxc-tpl-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 16px;
}
.lxc-tpl-header-left {
  display: flex;
  align-items: baseline;
  gap: 10px;
  flex-shrink: 0;
}
.lxc-tpl-title {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  white-space: nowrap;
}
.lxc-tpl-sub {
  font-size: 13px;
  color: var(--el-text-color-secondary);
}
.lxc-tpl-header-right {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-shrink: 0;
}

/* 表格容器 */
.lxc-tpl-wrap {
  background: var(--app-bg-card);
  border-radius: var(--app-radius-md, 10px);
  box-shadow: var(--app-shadow-sm);
  border: 1px solid var(--app-border-light);
  padding: 2px;
  overflow: hidden;
}

.tpl-name {
  font-weight: 600;
  color: var(--el-text-color-primary);
}

/* 发行版标签 */
.distro-tag {
  display: inline-flex;
  align-items: center;
  padding: 2px 10px;
  border-radius: 6px;
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.2px;
  border: 1px solid transparent;
  white-space: nowrap;
}
.d-ubuntu { background: #e8f5e9; color: #2e7d32; border-color: #c8e6c9; }
.d-debian { background: #fce4ec; color: #c62828; border-color: #f8bbd0; }
.d-alpine { background: #e3f2fd; color: #1565c0; border-color: #bbdefb; }
.d-centos { background: #fff3e0; color: #e65100; border-color: #ffe0b2; }
.d-rocky  { background: #fff3e0; color: #e65100; border-color: #ffe0b2; }
.d-alma   { background: #fff3e0; color: #e65100; border-color: #ffe0b2; }
.d-fedora { background: #ede7f6; color: #4527a0; border-color: #d1c4e9; }
.d-arch   { background: #e0f7fa; color: #00838f; border-color: #b2ebf2; }
.d-suse   { background: #e8eaf6; color: #283593; border-color: #c5cae9; }
.d-other  { background: #f5f5f5; color: #666; border-color: #e0e0e0; }

/* 空状态 */
.tpl-empty {
  text-align: center;
  padding: 64px 20px;
  color: var(--el-text-color-secondary);
}
.tpl-empty-icon {
  font-size: 48px;
  margin-bottom: 12px;
}
.tpl-empty-text {
  font-size: 15px;
  color: var(--el-text-color-regular);
}
.tpl-empty-hint {
  font-size: 12px;
  margin-top: 8px;
  color: var(--el-text-color-placeholder);
}

/* ===== 深色模式 ===== */
html.dark .d-ubuntu { background: rgba(46, 125, 50, 0.2); color: #81c784; border-color: rgba(46, 125, 50, 0.3); }
html.dark .d-debian { background: rgba(198, 40, 40, 0.2); color: #e57373; border-color: rgba(198, 40, 40, 0.3); }
html.dark .d-alpine { background: rgba(21, 101, 192, 0.2); color: #64b5f6; border-color: rgba(21, 101, 192, 0.3); }
html.dark .d-centos,
html.dark .d-rocky,
html.dark .d-alma { background: rgba(230, 81, 0, 0.2); color: #ffb74d; border-color: rgba(230, 81, 0, 0.3); }
html.dark .d-fedora { background: rgba(69, 39, 160, 0.2); color: #b39ddb; border-color: rgba(69, 39, 160, 0.3); }
html.dark .d-arch { background: rgba(0, 131, 143, 0.2); color: #80cbc4; border-color: rgba(0, 131, 143, 0.3); }
html.dark .d-suse { background: rgba(40, 53, 147, 0.2); color: #9fa8da; border-color: rgba(40, 53, 147, 0.3); }
html.dark .d-other { background: rgba(255, 255, 255, 0.06); color: var(--el-text-color-secondary); border-color: var(--app-border-light); }

/* ===== 移动端 ===== */
@media (max-width: 768px) {
  .lxc-tpl-header {
    flex-wrap: wrap;
    gap: 10px;
  }
  .lxc-tpl-title {
    font-size: 18px;
  }
}
</style>
