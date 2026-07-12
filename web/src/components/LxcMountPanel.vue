<template>
  <div class="lxc-mount-panel">
    <el-alert
      v-if="restartRequired"
      type="warning"
      :closable="false"
      show-icon
      title="容器运行中，目录挂载的修改需重启容器后生效"
      style="margin-bottom: 12px"
    />
    <div class="panel-toolbar">
      <span class="panel-title">目录挂载</span>
      <el-button type="primary" size="small" :icon="Plus" @click="openAdd">添加挂载</el-button>
    </div>
    <el-table :data="mounts" v-loading="loading" empty-text="暂无挂载" size="small">
      <el-table-column prop="host_path" label="宿主机路径" min-width="220" show-overflow-tooltip />
      <el-table-column prop="target" label="容器挂载点" min-width="160" show-overflow-tooltip />
      <el-table-column label="模式" width="90">
        <template #default="{ row }">
          <el-tag size="small" :type="row.read_only ? 'info' : 'success'" effect="light">
            {{ row.read_only ? '只读' : '读写' }}
          </el-tag>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="90" fixed="right">
        <template #default="{ row }">
          <el-button type="danger" link size="small" @click="remove(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="addVisible" title="添加目录挂载" width="520px" append-to-body>
      <el-form :model="form" label-width="120px" :rules="rules" ref="formRef">
        <el-form-item label="宿主机路径" prop="host_path">
          <el-input v-model="form.host_path" placeholder="如 /data/share" />
        </el-form-item>
        <el-form-item label="容器挂载点" prop="target">
          <el-input v-model="form.target" placeholder="如 /mnt/data" />
        </el-form-item>
        <el-form-item label="只读">
          <el-switch v-model="form.read_only" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="addVisible = false">取消</el-button>
        <el-button type="primary" :loading="submitting" @click="submit">确定</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { ref, reactive } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Plus } from '@element-plus/icons-vue'
import { getLXCMounts, addLXCMount, deleteLXCMount } from '@/api/lxc'

const props = defineProps({ name: { type: String, required: true } })

const loading = ref(false)
const submitting = ref(false)
const mounts = ref([])
const restartRequired = ref(false)
const addVisible = ref(false)
const formRef = ref(null)
const form = reactive({ host_path: '', target: '', read_only: false })
const rules = {
  host_path: [{ required: true, message: '请输入宿主机路径', trigger: 'blur' }],
  target: [{ required: true, message: '请输入容器挂载点', trigger: 'blur' }]
}

const fetch = async () => {
  loading.value = true
  try {
    const res = await getLXCMounts(props.name)
    const d = res.data || {}
    mounts.value = d.mounts || []
    restartRequired.value = !!d.restart_required
  } finally {
    loading.value = false
  }
}

const openAdd = () => {
  form.host_path = ''
  form.target = ''
  form.read_only = false
  addVisible.value = true
}

const submit = async () => {
  if (!formRef.value) return
  await formRef.value.validate(async (valid) => {
    if (!valid) return
    submitting.value = true
    try {
      await addLXCMount(props.name, {
        host_path: form.host_path.trim(),
        target: form.target.trim(),
        read_only: form.read_only
      })
      ElMessage.success(restartRequired.value ? '已添加，需重启容器后生效' : '添加成功')
      addVisible.value = false
      await fetch()
    } finally {
      submitting.value = false
    }
  })
}

const remove = (row) => {
  ElMessageBox.confirm(
    `确认删除挂载？宿主机 ${row.host_path} → 容器 ${row.target}`,
    '删除挂载',
    { type: 'warning' }
  ).then(async () => {
    await deleteLXCMount(props.name, row.target)
    ElMessage.success(restartRequired.value ? '已删除，需重启容器后生效' : '已删除')
    await fetch()
  }).catch(() => {})
}

defineExpose({ fetch })
fetch()
</script>

<style scoped>
.lxc-mount-panel {
  padding: 4px 0;
}
.panel-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}
.panel-title {
  font-size: 14px;
  font-weight: 600;
  color: var(--el-text-color-primary);
}
</style>
