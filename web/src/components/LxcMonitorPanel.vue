<template>
  <div class="lxc-monitor-panel">
    <div v-if="status && !isRunning" class="lxc-monitor-muted">
      容器未运行，暂无监控数据
    </div>
    <ResourceCharts
      v-else
      type="lxc"
      :name="name"
      :status="status"
      default-mode="history"
    />
  </div>
</template>

<script setup>
import { computed } from 'vue'
import ResourceCharts from './ResourceCharts.vue'

const props = defineProps({
  name: { type: String, required: true },
  status: { type: String, default: '' }
})

// 停机遮罩：LXCCache.Status 为大写（RUNNING/STOPPED/...）
const isRunning = computed(() => (props.status || '').toLowerCase() === 'running')
</script>

<style scoped>
.lxc-monitor-panel {
  padding: 4px 2px;
}
.lxc-monitor-muted {
  padding: 40px 0;
  text-align: center;
  color: var(--el-text-color-secondary);
  font-size: 14px;
}
</style>
