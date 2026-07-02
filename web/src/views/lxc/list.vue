<template>
  <div class="lxc-list-page">
    <el-card>
      <template #header>
        <div class="card-header">
          <span>LXC 容器</span>
          <div class="actions">
            <el-button size="small" @click="fetchData">刷新</el-button>
            <el-button size="small" @click="autoRefresh = !autoRefresh">{{ autoRefresh ? '停止自动刷新' : '自动刷新' }}</el-button>
            <el-select v-model="batchAction" size="small" placeholder="批量操作" style="width:120px" :disabled="!selected.length">
              <el-option label="启动" value="start" />
              <el-option label="停止" value="stop" />
              <el-option label="重启" value="restart" />
              <el-option label="删除" value="delete" />
            </el-select>
            <el-button size="small" type="primary" :disabled="!selected.length || !batchAction" @click="handleBatch">执行</el-button>
            <el-button type="primary" size="small" @click="openCreate">创建容器</el-button>
          </div>
        </div>
      </template>
      <el-table :data="tableData" v-loading="loading" border @selection-change="selected = $event">
        <el-table-column type="selection" width="42" />
        <el-table-column prop="name" label="名称" />
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag :type="row.status === 'RUNNING' ? 'success' : 'info'">{{ row.status }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="template" label="模板" />
        <el-table-column label="CPU/内存" width="140">
          <template #default="{ row }">{{ row.cpu_shares || '-' }} / {{ row.memory_mb ? row.memory_mb + 'MB' : '-' }}</template>
        </el-table-column>
        <el-table-column prop="cached_ip" label="IP" width="130" />
        <el-table-column prop="group_name" label="分组" width="110" />
        <el-table-column prop="remark" label="备注" />
        <el-table-column label="操作" width="320" fixed="right">
          <template #default="{ row }">
            <el-button size="small" link :disabled="row.status === 'RUNNING'" @click="operate(row, 'start')">启动</el-button>
            <el-button size="small" link :disabled="row.status !== 'RUNNING'" @click="operate(row, 'stop')">停止</el-button>
            <el-button size="small" link @click="operate(row, 'restart')">重启</el-button>
            <el-button size="small" link @click="openConfig(row)">配置</el-button>
            <el-button size="small" link @click="openSnapshots(row)">快照</el-button>
            <el-button size="small" link @click="openConsole(row)">终端</el-button>
            <el-button size="small" link type="danger" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-card>

    <!-- 创建对话框 -->
    <el-dialog v-model="createVisible" title="创建 LXC 容器" width="520px">
      <el-form :model="createForm" label-width="100px">
        <el-form-item label="名称" required><el-input v-model="createForm.name" /></el-form-item>
        <el-form-item label="模板" required>
          <el-select v-model="createForm.template" style="width:100%">
            <el-option v-for="t in templates" :key="t.name" :label="t.display_name || t.name" :value="t.name" :disabled="t.disabled" />
          </el-select>
        </el-form-item>
        <el-form-item label="CPU 权重"><el-input-number v-model="createForm.cpu_shares" :min="0" /></el-form-item>
        <el-form-item label="内存(MB)"><el-input-number v-model="createForm.memory_mb" :min="0" /></el-form-item>
        <el-form-item label="自动启动"><el-switch v-model="createForm.autostart" /></el-form-item>
        <el-form-item label="分组"><el-input v-model="createForm.group_name" /></el-form-item>
        <el-form-item label="备注"><el-input v-model="createForm.remark" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="createVisible = false">取消</el-button>
        <el-button type="primary" :loading="creating" @click="handleCreate">创建</el-button>
      </template>
    </el-dialog>

    <!-- 配置对话框 -->
    <el-dialog v-model="configVisible" title="容器配置" width="460px">
      <el-form :model="configForm" label-width="100px">
        <el-form-item label="CPU 权重"><el-input-number v-model="configForm.cpu_shares" :min="0" /></el-form-item>
        <el-form-item label="内存(MB)"><el-input-number v-model="configForm.memory_mb" :min="0" /></el-form-item>
        <el-form-item label="自动启动"><el-switch v-model="configForm.autostart" /></el-form-item>
        <el-form-item label="分组"><el-input v-model="configForm.group_name" /></el-form-item>
        <el-form-item label="备注"><el-input v-model="configForm.remark" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="configVisible = false">取消</el-button>
        <el-button type="primary" :loading="configSaving" @click="handleConfigSave">保存</el-button>
      </template>
    </el-dialog>

    <!-- 快照对话框 -->
    <el-dialog v-model="snapVisible" :title="`快照 · ${snapName}`" width="560px">
      <div style="margin-bottom:8px">
        <el-button size="small" type="primary" :loading="snapCreating" @click="handleSnapCreate">新建快照</el-button>
        <el-button size="small" @click="fetchSnapshots">刷新</el-button>
      </div>
      <el-table :data="snapshots" border>
        <el-table-column prop="name" label="快照名" />
        <el-table-column label="操作" width="160">
          <template #default="{ row }">
            <el-button size="small" link @click="handleSnapRestore(row)">恢复</el-button>
            <el-button size="small" link type="danger" @click="handleSnapDelete(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </el-dialog>
  </div>
</template>

<script setup>
import { ref, onMounted, onBeforeUnmount } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useUserStore } from '@/store/user'
import {
  getLXCList, createLXC, operateLXC, deleteLXC, batchOperateLXC,
  updateLXCConfig, getLXCTemplateList,
  listLXCSnapshots, createLXCSnapshot, restoreLXCSnapshot, deleteLXCSnapshot
} from '@/api/lxc'

const router = useRouter()
const userStore = useUserStore()
const tableData = ref([])
const loading = ref(false)
const selected = ref([])
const batchAction = ref('')
const autoRefresh = ref(false)
let timer = null

const fetchData = async () => {
  loading.value = true
  try {
    const res = await getLXCList()
    tableData.value = res.data || []
  } catch (e) {} finally { loading.value = false }
}

const operate = async (row, action) => {
  try { await operateLXC(row.name, action); ElMessage.success('操作已提交'); fetchData() } catch (e) {}
}
const remove = async (row) => {
  await ElMessageBox.confirm(`确认删除容器 ${row.name}？`, '删除', { type: 'warning' })
  try { await deleteLXC(row.name); ElMessage.success('删除任务已提交'); fetchData() } catch (e) {}
}
const handleBatch = async () => {
  await ElMessageBox.confirm(`对选中的 ${selected.value.length} 个容器执行「${batchAction.value}」？`, '批量操作', { type: 'warning' })
  try { await batchOperateLXC(selected.value.map(r => r.name), batchAction.value); ElMessage.success('已执行'); fetchData() } catch (e) {}
}
const openConsole = (row) => {
  // 终端窗口复用 token（路由守卫放行；WS 鉴权由后端 LXCAccessMiddleware + token query）
  router.push(`/lxc/console/${encodeURIComponent(row.name)}`)
}

// 创建
const createVisible = ref(false); const creating = ref(false)
const templates = ref([])
const createForm = ref({ name: '', template: '', cpu_shares: 256, memory_mb: 512, autostart: false, group_name: '', remark: '' })
const openCreate = async () => {
  createForm.value = { name: '', template: '', cpu_shares: 256, memory_mb: 512, autostart: false, group_name: '', remark: '' }
  try { const r = await getLXCTemplateList(); templates.value = r.data || [] } catch (e) {}
  createVisible.value = true
}
const handleCreate = async () => {
  if (!createForm.value.name || !createForm.value.template) { ElMessage.warning('请填写名称与模板'); return }
  creating.value = true
  try { await createLXC(createForm.value); ElMessage.success('创建任务已提交'); createVisible.value = false; fetchData() } catch (e) {} finally { creating.value = false }
}

// 配置
const configVisible = ref(false); const configSaving = ref(false); const configName = ref('')
const configForm = ref({ cpu_shares: 0, memory_mb: 0, autostart: false, group_name: '', remark: '' })
const openConfig = (row) => { configName.value = row.name; configForm.value = { cpu_shares: row.cpu_shares, memory_mb: row.memory_mb, autostart: row.autostart, group_name: row.group_name, remark: row.remark }; configVisible.value = true }
const handleConfigSave = async () => {
  configSaving.value = true
  try { await updateLXCConfig(configName.value, { ...configForm.value, autostart: configForm.value.autostart }); ElMessage.success('已保存'); configVisible.value = false; fetchData() } catch (e) {} finally { configSaving.value = false }
}

// 快照
const snapVisible = ref(false); const snapName = ref(''); const snapshots = ref([]); const snapCreating = ref(false)
const openSnapshots = async (row) => { snapName.value = row.name; snapVisible.value = true; await fetchSnapshots() }
const fetchSnapshots = async () => { try { const r = await listLXCSnapshots(snapName.value); snapshots.value = (r.data || []).map(n => ({ name: n })) } catch (e) {} }
const handleSnapCreate = async () => { snapCreating.value = true; try { await createLXCSnapshot(snapName.value); ElMessage.success('快照任务已提交'); fetchSnapshots() } catch (e) {} finally { snapCreating.value = false } }
const handleSnapRestore = async (row) => { try { await restoreLXCSnapshot(snapName.value, row.name); ElMessage.success('已恢复') } catch (e) {} }
const handleSnapDelete = async (row) => { try { await deleteLXCSnapshot(snapName.value, row.name); ElMessage.success('已删除'); fetchSnapshots() } catch (e) {} }

onMounted(() => { fetchData(); timer = setInterval(() => { if (autoRefresh.value) fetchData() }, 5000) })
onBeforeUnmount(() => { if (timer) clearInterval(timer) })
</script>

<style scoped>
.lxc-list-page { padding: 16px; }
.card-header { display: flex; align-items: center; justify-content: space-between; }
.card-header .actions { display: flex; gap: 8px; align-items: center; }
</style>
