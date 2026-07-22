<template>
  <div class="lxc-portforward-panel">
    <div class="panel-toolbar">
      <el-alert
        type="info"
        :closable="false"
        show-icon
        class="portforward-alert"
        title="端口映射将容器内端口通过宿主机对外暴露（iptables DNAT）。宿主端口留空自动分配；删除属于高风险动作，会触发二次验证。"
      />
      <el-button type="primary" @click="openCreateDialog">新增端口映射</el-button>
    </div>

    <el-table :data="portForwardList" border v-loading="loading" empty-text="暂无端口映射">
      <el-table-column label="协议" width="100" align="center">
        <template #default="{ row }">
          <el-tag :type="protocolTagType(row.protocol)" effect="plain">
            {{ protocolText(row.protocol) }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="宿主端口" prop="host_port" width="120" align="center" />
      <el-table-column label="访问地址" min-width="220">
        <template #default="{ row }">
          <code v-if="row.access_address">{{ row.access_address }}</code>
          <span v-else style="color: var(--el-text-color-placeholder);">-</span>
        </template>
      </el-table-column>
      <el-table-column label="容器端口" width="120" align="center">
        <template #default="{ row }">
          {{ row.dest_port || '-' }}
        </template>
      </el-table-column>
      <el-table-column label="操作" width="100" align="center" fixed="right">
        <template #default="{ row }">
          <el-button link type="danger" @click="handleDelete(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog
      v-model="dialogVisible"
      title="新增端口映射"
      append-to-body
      width="480px"
      :close-on-click-modal="false"
      @closed="resetForm"
    >
      <el-form :model="form" label-width="100px">
        <el-form-item label="宿主端口">
          <el-input v-model="form.host_port" placeholder="留空自动分配" />
        </el-form-item>
        <el-form-item label="容器端口">
          <el-input v-model="form.vm_port" placeholder="如 22" />
        </el-form-item>
        <el-form-item label="协议">
          <el-select v-model="form.protocol" style="width: 100%;">
            <el-option label="TCP" value="tcp" />
            <el-option label="UDP" value="udp" />
            <el-option label="TCP+UDP" value="both" />
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dialogVisible = false">取消</el-button>
        <el-button type="primary" :loading="submitting" @click="handleSubmit">确定</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { getLXCPortForwards, addLXCPortForward, deleteLXCPortForward } from '@/api/lxc'

const props = defineProps({
  containerName: {
    type: String,
    required: true
  }
})

const loading = ref(false)
const submitting = ref(false)
const dialogVisible = ref(false)
const portForwardList = ref([])

const form = reactive({
  host_port: '',
  vm_port: '',
  protocol: 'tcp'
})

const resetForm = () => {
  form.host_port = ''
  form.vm_port = ''
  form.protocol = 'tcp'
}

const protocolText = (value) => {
  const v = String(value || '').toLowerCase()
  if (v === 'udp') return 'UDP'
  if (v === 'tcp') return 'TCP'
  if (!v) return '-'
  return v.toUpperCase()
}

const protocolTagType = (value) => {
  const v = String(value || '').toLowerCase()
  if (v === 'udp') return 'warning'
  return 'primary'
}

const fetchList = async () => {
  if (!props.containerName) return
  loading.value = true
  try {
    const res = await getLXCPortForwards(props.containerName)
    portForwardList.value = Array.isArray(res.data) ? res.data : []
  } finally {
    loading.value = false
  }
}

const openCreateDialog = () => {
  resetForm()
  dialogVisible.value = true
}

const validateForm = () => {
  if (!form.vm_port) {
    ElMessage.warning('请填写容器端口')
    return false
  }
  if (!['tcp', 'udp', 'both'].includes(form.protocol)) {
    ElMessage.warning('协议取值非法')
    return false
  }
  return true
}

const handleSubmit = async () => {
  if (!validateForm()) return
  submitting.value = true
  try {
    await addLXCPortForward(props.containerName, {
      host_port: form.host_port,
      vm_port: form.vm_port,
      protocol: form.protocol
    })
    ElMessage.success('端口映射已创建')
    dialogVisible.value = false
    await fetchList()
  } finally {
    submitting.value = false
  }
}

// 删除：后端 requireHighRiskVerification，需要二次验证时由全局 axios 拦截器接管
const handleDelete = async (row) => {
  await ElMessageBox.confirm(
    `确定删除「${protocolText(row.protocol)} ${row.host_port} → ${row.dest_ip}:${row.dest_port}」这条端口映射吗？`,
    '删除端口映射',
    { type: 'warning' }
  )
  await deleteLXCPortForward(props.containerName, row.id)
  ElMessage.success('端口映射已删除')
  await fetchList()
}

watch(() => props.containerName, () => {
  fetchList()
})

onMounted(() => {
  fetchList()
})
</script>

<style scoped>
.lxc-portforward-panel {
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

.portforward-alert {
  flex: 1;
}

@media (max-width: 768px) {
  .panel-toolbar {
    flex-direction: column;
  }
}
</style>
