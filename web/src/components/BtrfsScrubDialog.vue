<template>
  <el-dialog v-model="visible" :title="`Scrub / 健康 - ${label}`" width="600px" append-to-body :close-on-click-modal="false" @closed="handleClosed">
    <div v-loading="loading" class="btrfs-scrub-body">
      <el-descriptions :column="2" border size="small">
        <el-descriptions-item label="Scrub 状态">{{ stateText }}</el-descriptions-item>
        <el-descriptions-item label="耗时">{{ status.duration || '-' }}</el-descriptions-item>
        <el-descriptions-item label="错误计数" :span="2">
          READ {{ status.read_err || 0 }} / WRITE {{ status.write_err || 0 }} / CSUM {{ status.csum_err || 0 }}
        </el-descriptions-item>
      </el-descriptions>

      <div v-if="status.state === 'running'" class="scrub-progress">
        <el-progress :percentage="status.pct || 0" status="warning" />
        <div class="scrub-meta">
          <span>已扫描 {{ formatBytes(status.scanned) }} / {{ formatBytes(status.total) }}</span>
        </div>
      </div>

      <el-alert v-if="status.state === 'finished'" :title="`上次 scrub 完成，用时 ${status.duration || '-'}，发现 ${totalErr} 个错误`" :type="totalErr > 0 ? 'error' : 'success'" :closable="false" show-icon />
      <el-alert v-if="status.state === 'canceled'" title="上次 scrub 已取消" type="warning" :closable="false" show-icon />
      <el-alert v-if="status.state === 'none'" title="该池从未执行过 scrub" type="info" :closable="false" show-icon />
    </div>

    <template #footer>
      <el-button v-if="status.state !== 'running'" type="primary" :loading="acting" @click="handleStart">启动 Scrub</el-button>
      <el-button v-if="status.state === 'running'" type="warning" :loading="acting" @click="handleCancel">取消 Scrub</el-button>
      <el-button @click="visible = false">关闭</el-button>
    </template>
  </el-dialog>
</template>

<script setup>
import { ref, computed } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { getBtrfsScrubStatus, startBtrfsScrub, cancelBtrfsScrub } from '@/api/infra'
import { useBtrfsBackgroundOp } from '@/composables/useBtrfsBackgroundOp'

const visible = ref(false)
const label = ref('')
const acting = ref(false)

const { status, loading, refresh, stopPoll } = useBtrfsBackgroundOp({
  fetchStatus: () => getBtrfsScrubStatus(label.value),
  isRunning: (s) => s && s.state === 'running',
})

const stateText = computed(() => ({ running: '运行中', finished: '已完成', canceled: '已取消', none: '从未执行' }[status.value.state] || '-'))
const totalErr = computed(() => (status.value.read_err || 0) + (status.value.write_err || 0) + (status.value.csum_err || 0))

const formatBytes = (n) => {
  if (!n) return '0B'
  const u = ['B', 'K', 'M', 'G', 'T', 'P']
  let i = 0, v = n
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(v >= 100 ? 0 : 1)}${u[i]}`
}

async function handleStart() {
  try { await ElMessageBox.confirm(`确认对存储池 ${label.value} 启动 scrub 数据校验？`, '启动 Scrub', { type: 'warning' }) } catch { return }
  acting.value = true
  try {
    await startBtrfsScrub(label.value)
    ElMessage.success('scrub 已启动')
    await refresh()
  } catch (e) { ElMessage.error(e?.message || '启动失败') }
  finally { acting.value = false }
}
async function handleCancel() {
  acting.value = true
  try {
    await cancelBtrfsScrub(label.value)
    ElMessage.success('scrub 已取消')
    await refresh()
  } catch (e) { ElMessage.error(e?.message || '取消失败') }
  finally { acting.value = false }
}

function open(name) {
  label.value = name || ''
  status.value = {}
  visible.value = true
  refresh().catch(() => {})
}
function handleClosed() { stopPoll(); label.value = ''; status.value = {} }

defineExpose({ open })
</script>

<style scoped>
.btrfs-scrub-body { display: flex; flex-direction: column; gap: 14px; }
.scrub-progress { display: flex; flex-direction: column; gap: 6px; }
.scrub-meta { font-size: 12px; color: #666; }
</style>
