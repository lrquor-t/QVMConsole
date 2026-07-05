<template>
  <el-drawer
    v-model="visible"
    :title="currentName ? `管理 · ${currentName}` : '管理'"
    size="900px"
    append-to-body
    @closed="onClosed"
  >
    <div class="drawer-body">
      <!-- 容器概览 hero -->
      <div v-if="currentName" class="container-hero">
        <el-icon class="hero-icon"><Monitor /></el-icon>
        <div class="hero-info">
          <div class="hero-name">{{ currentName }}</div>
          <div class="hero-meta">
            <el-tag :type="heroStatusType" size="small" effect="light">{{ heroStatusText }}</el-tag>
            <span class="hero-meta-item">{{ currentBacking || 'dir' }} backing</span>
            <span v-if="currentConfig.cpu_shares" class="hero-meta-item">{{ currentConfig.cpu_shares }} cpu</span>
            <span v-if="currentConfig.memory_mb" class="hero-meta-item">{{ currentConfig.memory_mb }}MB</span>
          </div>
        </div>
      </div>

      <el-tabs v-model="activeTab" class="lxc-tabs">
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
    </div>
  </el-drawer>
</template>

<script setup>
import { ref, computed } from 'vue'
import { Camera, Setting, Monitor } from '@element-plus/icons-vue'
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

const heroStatusType = computed(() => {
  if (currentStatus.value === 'RUNNING') return 'success'
  if (currentStatus.value === 'FROZEN') return 'warning'
  return 'info'
})
const heroStatusText = computed(() => {
  const map = { RUNNING: '运行中', STOPPED: '已停止', FROZEN: '已冻结', STARTING: '启动中', ABORTING: '异常' }
  return map[currentStatus.value] || (currentStatus.value || '未知')
})

defineExpose({ open })
</script>

<style scoped>
/* 抽屉主体：铺页面底色，让 hero/卡片浮起来（负 margin 抵消 el-drawer__body 默认 20px padding） */
.drawer-body {
  background: var(--app-bg-page);
  margin: -20px;
  padding: 18px 24px 24px;
  min-height: 100%;
  box-sizing: border-box;
}

/* 容器概览 hero */
.container-hero {
  display: flex;
  align-items: center;
  gap: 14px;
  background: var(--app-bg-card);
  border: 1px solid var(--app-border-light);
  border-radius: 12px;
  padding: 14px 18px;
  margin-bottom: 18px;
  box-shadow: var(--app-shadow-sm);
}
.hero-icon {
  font-size: 28px;
  color: var(--el-color-primary);
  flex-shrink: 0;
}
.hero-name {
  font-size: 17px;
  font-weight: 700;
  color: var(--el-text-color-primary);
  line-height: 1.3;
}
.hero-meta {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 6px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  flex-wrap: wrap;
}

.lxc-tab-label {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
/* tabs 设计感：更大标签 + 加粗激活态 + 加粗活动条 */
.lxc-tabs :deep(.el-tabs__header) {
  margin: 0 0 20px;
}
.lxc-tabs :deep(.el-tabs__nav-wrap::after) {
  background: var(--app-border-light);
}
.lxc-tabs :deep(.el-tabs__item) {
  font-size: 15px;
  height: 44px;
  padding: 0 20px;
}
.lxc-tabs :deep(.el-tabs__item.is-active) {
  font-weight: 600;
  color: var(--el-color-primary);
}
.lxc-tabs :deep(.el-tabs__active-bar) {
  height: 3px;
  border-radius: 2px;
}
</style>
