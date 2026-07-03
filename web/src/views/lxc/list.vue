<template>
  <div class="lxc-list-container">
    <!-- 页面头 -->
    <div class="lxc-header">
      <div class="lxc-header-left">
        <h2 class="lxc-title">LXC 容器</h2>
      </div>
      <div class="lxc-header-center">
        <el-input
          v-model="lxcSearchText"
          placeholder="搜索名称、模板、备注、分组..."
          clearable
          :prefix-icon="Search"
          class="lxc-search-input"
        />
      </div>
      <div class="lxc-header-right">
        <el-switch
          v-model="autoRefresh"
          active-text="自动刷新"
          class="auto-refresh-switch"
        />
        <el-button type="success" :icon="Refresh" :loading="loading" @click="fetchData">刷新</el-button>
        <el-button type="primary" :icon="Plus" @click="openCreate">创建容器</el-button>
      </div>
    </div>

    <!-- 批量操作条（仅选中时出现） -->
    <div v-show="hasSelection" class="lxc-batch-bar">
      <span class="lxc-batch-info">已选 {{ selected.length }} 个容器</span>
      <el-button size="small" type="success" :loading="batchOperating" @click="handleBatchOperate('start')">开机</el-button>
      <el-button size="small" type="warning" :loading="batchOperating" @click="handleBatchOperate('stop')">关机</el-button>
      <el-button size="small" type="info" :loading="batchOperating" @click="handleBatchOperate('restart')">重启</el-button>
      <el-button size="small" type="danger" :loading="batchOperating" @click="handleBatchOperate('delete')">删除</el-button>
    </div>

    <!-- 表格 -->
    <div class="lxc-table-wrap">
      <el-table
        :data="filteredData"
        v-loading="loading"
        style="width: 100%"
        @selection-change="selected = $event"
      >
        <el-table-column type="selection" width="45" align="center" />
        <el-table-column label="名称" min-width="150" show-overflow-tooltip>
          <template #default="{ row }">
            <span class="lxc-name">{{ row.name }}</span>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="110" align="center">
          <template #default="{ row }">
            <span class="lxc-status" :class="'is-' + statusType(row.status)">
              <span class="lxc-status-dot"></span>
              <el-tag :type="statusType(row.status)" size="small" effect="light">{{ statusText(row.status) }}</el-tag>
            </span>
          </template>
        </el-table-column>
        <el-table-column prop="template" label="模板" width="130" show-overflow-tooltip>
          <template #default="{ row }">{{ row.template || '-' }}</template>
        </el-table-column>
        <el-table-column label="规格" width="130" align="center">
          <template #default="{ row }">
            <span class="lxc-spec">{{ row.cpu_shares || '-' }} / {{ row.memory_mb ? row.memory_mb + 'MB' : '-' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="IP 地址" width="140">
          <template #default="{ row }">
            <span class="lxc-ip">{{ row.cached_ip || '-' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="分组 / 备注" min-width="190">
          <template #default="{ row }">
            <div class="lxc-remark-cell">
              <div class="lxc-remark-left">
                <el-tag v-if="row.group_name" size="small" type="warning" effect="plain">{{ row.group_name }}</el-tag>
                <span class="lxc-remark-text" :title="row.remark || ''">{{ row.remark || '-' }}</span>
              </div>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="150" fixed="right" align="center">
          <template #default="{ row }">
            <el-tooltip :content="row.status === 'RUNNING' ? '关机' : '开机'" placement="top">
              <el-button
                size="small"
                circle
                class="lxc-op-btn"
                :type="row.status === 'RUNNING' ? 'warning' : 'success'"
                :icon="row.status === 'RUNNING' ? SwitchButton : VideoPlay"
                :loading="!!operatingMap[row.name]"
                @click="operate(row, row.status === 'RUNNING' ? 'stop' : 'start')"
              />
            </el-tooltip>
            <el-tooltip content="终端" placement="top">
              <el-button size="small" circle type="primary" plain :icon="Monitor" @click="openConsole(row)" />
            </el-tooltip>
            <el-dropdown trigger="click" @command="cmd => handleMore(cmd, row)">
              <el-tooltip content="更多" placement="top">
                <el-button size="small" circle :icon="MoreFilled" />
              </el-tooltip>
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item command="restart">重启</el-dropdown-item>
                  <el-dropdown-item command="config">配置</el-dropdown-item>
                  <el-dropdown-item command="snapshot">快照</el-dropdown-item>
                  <el-dropdown-item command="delete" divided class="lxc-dropdown-danger">删除</el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- 创建对话框 -->
    <el-dialog v-model="createVisible" title="创建 LXC 容器" width="520px">
      <el-form :model="createForm" label-width="100px">
        <el-form-item label="名称" required><el-input v-model="createForm.name" /></el-form-item>
        <el-form-item label="容器目录">
          <el-input :model-value="containerPathPreview" disabled />
          <div class="form-tip" style="font-size:12px;color:var(--el-text-color-secondary);margin-top:2px">容器将创建于此（rootfs 在其下）；目录由系统设置「LXC 容器目录」决定。</div>
        </el-form-item>
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
import { ref, computed, onMounted, onBeforeUnmount } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Search, Plus, Refresh, VideoPlay, SwitchButton, Monitor, MoreFilled } from '@element-plus/icons-vue'
import { useUserStore } from '@/store/user'
import {
  getLXCList, createLXC, operateLXC, deleteLXC, batchOperateLXC,
  updateLXCConfig, getLXCTemplateList,
  listLXCSnapshots, createLXCSnapshot, restoreLXCSnapshot, deleteLXCSnapshot
} from '@/api/lxc'
import { getSettings } from '@/api/settings'

const router = useRouter()
const userStore = useUserStore()
const tableData = ref([])
const loading = ref(false)
const selected = ref([])
const autoRefresh = ref(false)
const lxcSearchText = ref('')
const operatingMap = ref({})
const batchOperating = ref(false)
let timer = null

const hasSelection = computed(() => selected.value.length > 0)

const filteredData = computed(() => {
  if (!lxcSearchText.value) return tableData.value
  const q = lxcSearchText.value.toLowerCase()
  return tableData.value.filter(r =>
    (r.name || '').toLowerCase().includes(q) ||
    (r.template || '').toLowerCase().includes(q) ||
    (r.remark || '').toLowerCase().includes(q) ||
    (r.group_name || '').toLowerCase().includes(q)
  )
})

const statusType = (status) => {
  if (status === 'RUNNING') return 'success'
  if (status === 'FROZEN') return 'warning'
  return 'info'
}
const statusText = (status) => {
  const map = { RUNNING: '运行中', STOPPED: '已停止', FROZEN: '已冻结', STARTING: '启动中', ABORTING: '异常' }
  return map[status] || (status ? status : '已停止')
}

const fetchData = async () => {
  loading.value = true
  try {
    const res = await getLXCList()
    tableData.value = res.data || []
  } catch (e) {} finally { loading.value = false }
}

const operate = async (row, action) => {
  operatingMap.value = { ...operatingMap.value, [row.name]: true }
  try { await operateLXC(row.name, action); ElMessage.success('操作已提交'); fetchData() } catch (e) {} finally {
    operatingMap.value = { ...operatingMap.value, [row.name]: false }
  }
}
const remove = async (row) => {
  await ElMessageBox.confirm(`确认删除容器 ${row.name}？`, '删除', { type: 'warning' })
  try { await deleteLXC(row.name); ElMessage.success('删除任务已提交'); fetchData() } catch (e) {}
}
const handleBatchOperate = async (action) => {
  const names = selected.value.map(r => r.name)
  const label = { start: '开机', stop: '关机', restart: '重启', delete: '删除' }[action]
  await ElMessageBox.confirm(`对选中的 ${names.length} 个容器执行「${label}」？`, '批量操作', { type: 'warning' })
  batchOperating.value = true
  try {
    await batchOperateLXC(names, action)
    ElMessage.success('已执行')
    fetchData()
  } catch (e) {} finally { batchOperating.value = false }
}
const handleMore = async (cmd, row) => {
  if (cmd === 'restart') operate(row, 'restart')
  else if (cmd === 'config') openConfig(row)
  else if (cmd === 'snapshot') openSnapshots(row)
  else if (cmd === 'delete') remove(row)
}
const openConsole = (row) => {
  // 终端窗口复用 token（路由守卫放行；WS 鉴权由后端 LXCAccessMiddleware + token query）
  router.push(`/lxc/console/${encodeURIComponent(row.name)}`)
}

// 创建
const createVisible = ref(false); const creating = ref(false)
const templates = ref([])
const lxcLxcPath = ref('') // LXC 容器根目录，用于在创建弹窗展示容器落盘位置
const createForm = ref({ name: '', template: '', cpu_shares: 256, memory_mb: 512, autostart: false, group_name: '', remark: '' })
const openCreate = async () => {
  createForm.value = { name: '', template: '', cpu_shares: 256, memory_mb: 512, autostart: false, group_name: '', remark: '' }
  try { const r = await getLXCTemplateList(); templates.value = r.data || [] } catch (e) {}
  if (!lxcLxcPath.value) { try { const s = await getSettings(); lxcLxcPath.value = s.data?.lxc_lxc_path || '' } catch (e) {} }
  createVisible.value = true
}
const containerPathPreview = computed(() => {
  const base = (lxcLxcPath.value || '/var/lib/lxc').replace(/\/+$/, '')
  const name = createForm.value.name ? createForm.value.name : '<名称>'
  return `${base}/${name}/`
})
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
.lxc-list-container {
  padding: 10px;
}

/* 页面头 */
.lxc-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 16px;
}
.lxc-header-left {
  flex-shrink: 0;
}
.lxc-title {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
  color: var(--el-text-color-primary);
  white-space: nowrap;
}
.lxc-header-center {
  flex: 1;
  display: flex;
  justify-content: center;
  max-width: 460px;
  margin: 0 auto;
}
.lxc-search-input {
  width: 100%;
}
.lxc-header-right {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-shrink: 0;
}
.auto-refresh-switch {
  margin-right: 4px;
}

/* 批量操作条 */
.lxc-batch-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 14px;
  margin-bottom: 12px;
  border-radius: var(--app-radius-md, 10px);
  background: var(--el-color-warning-light-9);
  border: 1px solid var(--el-color-warning-light-7);
}
.lxc-batch-info {
  margin-right: auto;
  font-size: 13px;
  color: var(--el-text-color-regular);
}

/* 表格容器 */
.lxc-table-wrap {
  overflow-x: auto;
  -webkit-overflow-scrolling: touch;
  background: var(--app-bg-card);
  border-radius: var(--app-radius-md, 10px);
  box-shadow: var(--app-shadow-sm);
  border: 1px solid var(--app-border-light);
  padding: 2px;
}

/* 单元格样式 */
.lxc-name {
  font-weight: 600;
  color: var(--el-text-color-primary);
}
.lxc-spec {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.lxc-ip {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
  color: var(--el-text-color-regular);
}
.lxc-status {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
.lxc-status-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--el-color-info);
  flex-shrink: 0;
}
.lxc-status.is-success .lxc-status-dot {
  background: var(--el-color-success);
  box-shadow: 0 0 0 3px rgba(103, 194, 58, 0.18);
}
.lxc-status.is-warning .lxc-status-dot {
  background: var(--el-color-warning);
  box-shadow: 0 0 0 3px rgba(230, 162, 60, 0.18);
}
.lxc-remark-cell {
  display: flex;
  align-items: center;
  min-width: 0;
}
.lxc-remark-left {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
  min-width: 0;
}
.lxc-remark-text {
  font-size: 13px;
  color: var(--el-text-color-regular);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.lxc-op-btn {
  transition: all var(--app-transition-fast, 0.15s);
}

/* 下拉“删除”红色 */
:deep(.lxc-dropdown-danger) {
  color: var(--el-color-danger);
}

/* ===== 深色模式 ===== */
html.dark .lxc-batch-bar {
  background: rgba(230, 162, 60, 0.12);
  border-color: rgba(230, 162, 60, 0.3);
}
html.dark .lxc-status.is-success .lxc-status-dot {
  box-shadow: 0 0 0 3px rgba(103, 194, 58, 0.22);
}

/* ===== 移动端 ===== */
@media (max-width: 768px) {
  .lxc-header {
    flex-wrap: wrap;
    gap: 10px;
  }
  .lxc-header-center {
    order: 3;
    max-width: none;
    width: 100%;
  }
  .lxc-title {
    font-size: 18px;
  }
}
</style>
