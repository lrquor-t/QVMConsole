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
    </el-tabs>
  </el-drawer>
</template>

<script setup>
import { ref } from 'vue'
import { Camera } from '@element-plus/icons-vue'
import LxcSnapshotPanel from './LxcSnapshotPanel.vue'

const visible = ref(false)
const activeTab = ref('snapshot')
const currentName = ref('')
const currentStatus = ref('')
const currentBacking = ref('')

const open = (row) => {
  currentName.value = row.name
  currentStatus.value = row.status
  currentBacking.value = row.backing || ''
  activeTab.value = 'snapshot'
  visible.value = true
}

const onClosed = () => {
  currentName.value = ''
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
