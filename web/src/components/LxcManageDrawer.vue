<template>
  <el-drawer
    v-model="visible"
    :title="currentName ? `管理 · ${currentName}` : '管理'"
    size="720px"
    append-to-body
    @closed="onClosed"
  >
    <el-tabs v-model="activeTab">
      <el-tab-pane name="snapshot">
        <template #label>
          <span class="lxc-tab-label"><el-icon><Camera /></el-icon> 快照</span>
        </template>
        <LxcSnapshotPanel
          v-if="visible && currentName"
          :name="currentName"
          :status="currentStatus"
          :backing="currentBacking"
        />
      </el-tab-pane>
      <el-tab-pane name="config" lazy>
        <template #label>
          <span class="lxc-tab-label"><el-icon><Setting /></el-icon> 配置</span>
        </template>
        <LxcConfigPanel
          v-if="visible && currentName"
          :name="currentName"
          :initial-config="currentConfig"
          @saved="onConfigSaved"
        />
      </el-tab-pane>
    </el-tabs>
  </el-drawer>
</template>

<script setup>
import { ref } from 'vue'
import { Camera, Setting } from '@element-plus/icons-vue'
import LxcSnapshotPanel from './LxcSnapshotPanel.vue'
import LxcConfigPanel from './LxcConfigPanel.vue'

const emit = defineEmits(['refresh'])

const visible = ref(false)
const activeTab = ref('snapshot')
const currentName = ref('')
const currentStatus = ref('')
const currentBacking = ref('')
const currentConfig = ref({})

const open = (row) => {
  currentName.value = row.name
  currentStatus.value = row.status
  currentBacking.value = row.backing || ''
  currentConfig.value = {
    cpu_shares: row.cpu_shares,
    memory_mb: row.memory_mb,
    autostart: row.autostart,
    group_name: row.group_name,
    remark: row.remark
  }
  activeTab.value = 'snapshot'
  visible.value = true
}

const onClosed = () => {
  currentName.value = ''
}

// 配置保存后通知父组件刷新容器列表
const onConfigSaved = () => {
  emit('refresh')
}

defineExpose({ open })
</script>

<style scoped>
.lxc-tab-label {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
</style>
