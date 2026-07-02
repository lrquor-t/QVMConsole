<template>
  <div class="lxc-tpl-page">
    <el-card>
      <template #header>
        <div class="card-header">
          <span>LXC 模板</span>
          <el-button type="primary" size="small" @click="openImport">导入模板</el-button>
        </div>
      </template>
      <el-table :data="tableData" v-loading="loading" border>
        <el-table-column prop="name" label="名称" />
        <el-table-column label="系统">
          <template #default="{ row }">{{ [row.distro, row.release].filter(Boolean).join(' ') }}</template>
        </el-table-column>
        <el-table-column prop="arch" label="架构" width="80" />
        <el-table-column prop="backing" label="后端" width="90" />
        <el-table-column label="rootfs 大小" width="120">
          <template #default="{ row }">{{ formatSize(row.rootfs_size_bytes) }}</template>
        </el-table-column>
        <el-table-column label="启用" width="80">
          <template #default="{ row }">
            <el-tag :type="row.disabled ? 'info' : 'success'">{{ row.disabled ? '禁用' : '启用' }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="created_at" label="创建时间" width="180" />
        <el-table-column label="操作" width="100" fixed="right">
          <template #default="{ row }">
            <el-button size="small" type="danger" link @click="handleDelete(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

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
.lxc-tpl-page { padding: 16px; }
.card-header { display: flex; align-items: center; justify-content: space-between; }
</style>
