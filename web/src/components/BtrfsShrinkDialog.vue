<template>
  <el-dialog title="缩容 Btrfs 存储池" v-model="visible" width="560px" :close-on-click-modal="false" append-to-body>
    <el-alert type="warning" :closable="false" show-icon style="margin-bottom: 12px;">
      <template #title>从存储池「{{ label }}」移除成员盘（btrfs device delete）。被移盘上的数据会迁移到剩余盘，期间请勿关机。</template>
    </el-alert>
    <div v-if="!members.length" class="empty">正在读取成员盘…</div>
    <el-checkbox-group v-else v-model="selected">
      <el-card v-for="m in members" :key="m.path" shadow="never" class="member-item" style="margin-bottom: 8px;">
        <el-checkbox :value="m.path" style="width:100%;">
          <span style="font-weight:500;">{{ m.path }}</span>
          <span style="color:#999;font-size:12px;margin-left:8px;">已用 {{ formatBytes(m.used) }}</span>
        </el-checkbox>
      </el-card>
    </el-checkbox-group>
    <el-alert v-if="members.length" type="info" :closable="false" show-icon title="严格护栏：移除后剩余盘数/空间不足会前置拦截（任务不会被提交）。" style="margin-top: 8px;" />
    <template #footer>
      <el-button @click="visible = false">取消</el-button>
      <el-button type="primary" :loading="loading" :disabled="!selected.length || selected.length >= members.length" @click="submit">移除选中</el-button>
    </template>
  </el-dialog>
</template>

<script setup>
import { ref } from 'vue'
import { ElMessage } from 'element-plus'
import { shrinkBtrfsPool } from '@/api/infra'

const emit = defineEmits(['success'])
const visible = ref(false)
const loading = ref(false)
const label = ref('')
const members = ref([])
const selected = ref([])

const formatBytes = (b) => {
  if (!b) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0, n = b
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++ }
  return n.toFixed(1) + ' ' + u[i]
}

// 成员盘 + per-device used 由后端在列表树注入；此处用 disk.children 里的成员盘引用节点
const open = (disk) => {
  label.value = disk.name || disk.btrfs_label || ''
  selected.value = []
  members.value = []
  visible.value = true
  // 成员盘引用节点：device_path 字段（buildBtrfsMemberRefNode 注入）
  const kids = (disk.children || []).filter(c => c.type === 'pv' && c.is_btrfs_pool && c.device_path)
  members.value = kids.map(c => ({ path: c.device_path, used: 0 }))
}
const submit = async () => {
  loading.value = true
  try {
    const res = await shrinkBtrfsPool({ label: label.value, device_ids: selected.value })
    ElMessage.success(res.message || '缩容任务已提交，请在任务中心查看进度')
    visible.value = false
    emit('success')
  } catch (e) {}
  finally { loading.value = false }
}
defineExpose({ open })
</script>

<style scoped>
.empty { color: #999; font-size: 13px; }
</style>
