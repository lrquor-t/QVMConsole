<template>
  <el-dialog title="扩容 Btrfs 存储池" v-model="visible" width="560px" :close-on-click-modal="false" append-to-body>
    <el-alert type="info" :closable="false" show-icon style="margin-bottom: 16px;">
      <template #title>向存储池「{{ label }}」加入新磁盘以增加容量（btrfs device add）。</template>
    </el-alert>
    <el-form label-position="top">
      <el-form-item label="选择新成员盘">
        <el-alert v-if="targets.length === 0" type="info" :closable="false">未找到可用磁盘。</el-alert>
        <el-checkbox-group v-model="selected" v-else>
          <el-card v-for="disk in targets" :key="disk.id" shadow="never" class="pv-disk-item" style="margin-bottom: 8px;">
            <el-checkbox :value="disk.id" style="width: 100%;">
              <div style="display: flex; justify-content: space-between; align-items: center; width: 100%;">
                <span style="font-weight: 500;">{{ disk.display_name }}</span>
                <span style="color: #999; font-size: 12px;">{{ disk.device_path }} · {{ formatBytes(disk.size) }}</span>
              </div>
            </el-checkbox>
          </el-card>
        </el-checkbox-group>
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="visible = false">取消</el-button>
      <el-button type="primary" :loading="loading" :disabled="selected.length === 0" @click="submit">扩容</el-button>
    </template>
  </el-dialog>
</template>

<script setup>
import { ref } from 'vue'
import { ElMessage } from 'element-plus'
import { getAvailablePVTargets, expandBtrfsPool } from '@/api/infra'

const emit = defineEmits(['success'])
const visible = ref(false)
const loading = ref(false)
const label = ref('')
const targets = ref([])
const selected = ref([])

const formatBytes = (b) => {
  if (!b) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0, n = b
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++ }
  return n.toFixed(1) + ' ' + u[i]
}

const open = async (disk) => {
  label.value = disk.name || disk.btrfs_label || ''
  selected.value = []
  visible.value = true
  try {
    const res = await getAvailablePVTargets()
    targets.value = res.data || []
  } catch (e) {
    targets.value = []
  }
}

const submit = async () => {
  loading.value = true
  try {
    await expandBtrfsPool({ label: label.value, device_ids: selected.value })
    ElMessage.success('存储池 ' + label.value + ' 已扩容')
    visible.value = false
    emit('success')
  } catch (e) {
    // request 拦截器已提示错误
  } finally {
    loading.value = false
  }
}

defineExpose({ open })
</script>
