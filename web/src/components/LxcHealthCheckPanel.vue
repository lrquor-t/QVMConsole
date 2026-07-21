<template>
  <div class="lxc-health-panel">
    <div class="panel-toolbar">
      <el-alert
        type="info"
        :closable="false"
        show-icon
        class="health-alert"
        title="健康检查会按规则周期性探测容器（HTTP/TCP/命令），结果用于列表页健康灯。script 类型会以 root 在容器内执行命令，仅管理员可配置。"
      />
      <div class="toolbar-actions">
        <el-tag v-if="aggregateStatus" :type="aggregateTagType" effect="dark">
          聚合：{{ aggregateText }}
        </el-tag>
        <el-button type="primary" :loading="probing" @click="handleProbe">立即探测</el-button>
        <el-button type="success" @click="openCreateDialog">新增检查</el-button>
      </div>
    </div>

    <el-table :data="checksList" border v-loading="loading" empty-text="暂无健康检查规则">
      <el-table-column label="名称" prop="check_name" min-width="140" />
      <el-table-column label="类型" width="100" align="center">
        <template #default="{ row }">
          <el-tag :type="typeTagType(row.type)" effect="plain">{{ typeText(row.type) }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="目标" prop="target" min-width="200" show-overflow-tooltip />
      <el-table-column label="核心" width="80" align="center">
        <template #default="{ row }">
          <el-tag v-if="row.critical" type="danger" size="small">核心</el-tag>
          <span v-else style="color: var(--el-text-color-placeholder);">-</span>
        </template>
      </el-table-column>
      <el-table-column label="启用" width="80" align="center">
        <template #default="{ row }">
          <el-switch
            :model-value="row.enabled"
            :loading="Boolean(switchLoadingMap[row.id])"
            @change="value => handleToggle(row, value)"
          />
        </template>
      </el-table-column>
      <el-table-column label="最近状态" width="110" align="center">
        <template #default="{ row }">
          <el-tag :type="lastStatusTagType(row.last_status)" size="small">
            {{ lastStatusText(row.last_status) }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="耗时" width="100" align="center">
        <template #default="{ row }">
          <span v-if="row.last_latency_ms">{{ row.last_latency_ms }} ms</span>
          <span v-else style="color: var(--el-text-color-placeholder);">-</span>
        </template>
      </el-table-column>
      <el-table-column label="错误" min-width="180" show-overflow-tooltip>
        <template #default="{ row }">
          <span v-if="row.last_error" class="health-error">{{ row.last_error }}</span>
          <span v-else style="color: var(--el-text-color-placeholder);">-</span>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="140" align="center" fixed="right">
        <template #default="{ row }">
          <el-button link type="primary" @click="openEditDialog(row)">编辑</el-button>
          <el-button link type="danger" @click="handleDelete(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog
      v-model="dialogVisible"
      :title="editingId ? '编辑健康检查' : '新增健康检查'"
      append-to-body
      width="560px"
      :close-on-click-modal="false"
      @closed="resetForm"
    >
      <el-form :model="form" label-width="110px">
        <el-form-item label="检查名称">
          <el-input v-model="form.check_name" placeholder="如 Nginx 网关" />
        </el-form-item>
        <el-form-item label="类型">
          <el-select v-model="form.type" style="width: 100%;">
            <el-option label="HTTP" value="http" />
            <el-option label="TCP" value="tcp" />
            <el-option v-if="isAdmin" label="Script（命令）" value="script" />
          </el-select>
        </el-form-item>
        <el-form-item label="目标">
          <el-input v-model="form.target" :placeholder="targetPlaceholder" />
        </el-form-item>
        <el-form-item v-if="form.type === 'http'" label="期望状态码">
          <el-input-number v-model="form.expected_code" :min="100" :max="599" controls-position="right" />
        </el-form-item>
        <el-form-item label="核心项">
          <el-switch v-model="form.critical" active-text="核心" inactive-text="非核心" />
        </el-form-item>
        <el-form-item label="启用">
          <el-switch v-model="form.enabled" active-text="启用" inactive-text="停用" />
        </el-form-item>
        <el-alert
          v-if="form.type === 'script'"
          type="warning"
          :closable="false"
          show-icon
          title="script 类型会以 root 在容器内执行任意命令（lxc-attach），请确保命令来源可信。"
        />
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="submitting" @click="handleSubmit">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  addLXCHealthCheck,
  deleteLXCHealthCheck,
  getLXCHealth,
  probeLXCHealth,
  updateLXCHealthCheck
} from '@/api/lxc'
import { useUserStore } from '@/store/user'

const props = defineProps({
  containerName: {
    type: String,
    required: true
  }
})

const userStore = useUserStore()
const isAdmin = computed(() => userStore.role === 'admin')

const loading = ref(false)
const submitting = ref(false)
const probing = ref(false)
const dialogVisible = ref(false)
const editingId = ref(0)
const checksList = ref([])
const aggregateStatus = ref('')
const switchLoadingMap = reactive({})

const form = reactive({
  check_name: '',
  type: 'http',
  target: '',
  expected_code: 200,
  critical: true,
  enabled: true
})

const resetForm = () => {
  editingId.value = 0
  form.check_name = ''
  form.type = 'http'
  form.target = ''
  form.expected_code = 200
  form.critical = true
  form.enabled = true
}

const typeText = (value) => {
  const map = { http: 'HTTP', tcp: 'TCP', script: 'Script' }
  return map[value] || (value || '-').toUpperCase()
}

const typeTagType = (value) => {
  if (value === 'tcp') return 'warning'
  if (value === 'script') return 'danger'
  return 'primary'
}

const aggregateText = computed(() => {
  const map = { healthy: '健康', degraded: '亚健康', unhealthy: '异常', unknown: '未知' }
  return map[aggregateStatus.value] || aggregateStatus.value || '未知'
})

const aggregateTagType = computed(() => {
  const map = { healthy: 'success', degraded: 'warning', unhealthy: 'danger', unknown: 'info' }
  return map[aggregateStatus.value] || 'info'
})

const lastStatusText = (value) => {
  const map = { healthy: '健康', unhealthy: '异常', unknown: '未知' }
  return map[value] || '未探测'
}

const lastStatusTagType = (value) => {
  const map = { healthy: 'success', unhealthy: 'danger', unknown: 'info' }
  return map[value] || 'info'
}

const targetPlaceholder = computed(() => {
  if (form.type === 'http') return 'http://10.10.0.5:80/health'
  if (form.type === 'tcp') return '10.10.0.5:22'
  if (form.type === 'script') return '命令，如 nginx -t'
  return ''
})

// 一次调用拿到聚合状态 + 各规则最新结果（后端 /health 内部就是 ListLXCHealthChecks + 聚合）
const fetchHealth = async () => {
  if (!props.containerName) return
  loading.value = true
  try {
    const res = await getLXCHealth(props.containerName)
    const data = res.data || {}
    aggregateStatus.value = data.status || ''
    checksList.value = Array.isArray(data.checks) ? data.checks : []
  } finally {
    loading.value = false
  }
}

const handleProbe = async () => {
  probing.value = true
  try {
    await probeLXCHealth(props.containerName)
    ElMessage.success('已触发探测')
    await fetchHealth()
  } finally {
    probing.value = false
  }
}

const openCreateDialog = () => {
  resetForm()
  dialogVisible.value = true
}

const openEditDialog = (row) => {
  editingId.value = row.id
  form.check_name = row.check_name || ''
  form.type = row.type || 'http'
  form.target = row.target || ''
  form.expected_code = row.expected_code || 200
  form.critical = row.critical !== false
  form.enabled = row.enabled !== false
  // 非 admin 不应触达 script 规则的编辑（后端 403），兜底把类型拉回 http 防提交被拒
  if (!isAdmin.value && form.type === 'script') {
    form.type = 'http'
  }
  dialogVisible.value = true
}

const validateForm = () => {
  if (!form.check_name.trim()) {
    ElMessage.warning('请填写检查名称')
    return false
  }
  if (!['http', 'tcp', 'script'].includes(form.type)) {
    ElMessage.warning('类型取值非法')
    return false
  }
  if (form.type === 'script' && !isAdmin.value) {
    ElMessage.warning('命令式检查仅管理员可配置')
    return false
  }
  if (!form.target.trim()) {
    ElMessage.warning('请填写目标')
    return false
  }
  return true
}

const buildPayload = () => ({
  check_name: form.check_name.trim(),
  type: form.type,
  target: form.target.trim(),
  expected_code: form.type === 'http' ? form.expected_code : 0,
  critical: form.critical,
  enabled: form.enabled
})

const handleSubmit = async () => {
  if (!validateForm()) return
  submitting.value = true
  try {
    if (editingId.value) {
      await updateLXCHealthCheck(props.containerName, editingId.value, buildPayload())
      ElMessage.success('健康检查已更新')
    } else {
      await addLXCHealthCheck(props.containerName, buildPayload())
      ElMessage.success('健康检查已创建')
    }
    dialogVisible.value = false
    await fetchHealth()
  } finally {
    submitting.value = false
  }
}

const buildPayloadFromRow = (row, enabled) => ({
  check_name: row.check_name,
  type: row.type,
  target: row.target,
  expected_code: row.expected_code || 0,
  critical: row.critical,
  enabled
})

const handleToggle = async (row, enabled) => {
  switchLoadingMap[row.id] = true
  try {
    await updateLXCHealthCheck(props.containerName, row.id, buildPayloadFromRow(row, enabled))
    row.enabled = enabled
    ElMessage.success(enabled ? '已启用' : '已停用')
    await fetchHealth()
  } finally {
    switchLoadingMap[row.id] = false
  }
}

const handleDelete = async (row) => {
  await ElMessageBox.confirm(
    `确定删除健康检查「${row.check_name}」吗？`,
    '删除健康检查',
    { type: 'warning' }
  )
  await deleteLXCHealthCheck(props.containerName, row.id)
  ElMessage.success('健康检查已删除')
  await fetchHealth()
}

watch(() => props.containerName, () => {
  fetchHealth()
})

onMounted(() => {
  fetchHealth()
})
</script>

<style scoped>
.lxc-health-panel {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.panel-toolbar {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
}

.health-alert {
  flex: 1;
}

.toolbar-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-shrink: 0;
}

.health-error {
  color: var(--el-color-danger);
  font-size: 12px;
}

@media (max-width: 768px) {
  .panel-toolbar {
    flex-direction: column;
  }

  .toolbar-actions {
    width: 100%;
  }
}
</style>
