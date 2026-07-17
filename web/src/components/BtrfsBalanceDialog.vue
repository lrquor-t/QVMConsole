<template>
  <el-dialog v-model="visible" :title="`Balance / 重组 - ${label}`" width="600px" append-to-body :close-on-click-modal="false" @closed="handleClosed">
    <div v-loading="loading" class="bal-body">
      <!-- 运行中 / 暂停 -->
      <template v-if="status.state === 'running' || status.state === 'paused'">
        <el-alert :title="status.state === 'running' ? '正在重组数据…' : '已暂停'" :type="status.state === 'running' ? 'warning' : 'info'" :closable="false" show-icon />
        <el-progress :percentage="status.pct || 0" :status="status.state === 'paused' ? 'success' : 'warning'" />
        <div class="bal-meta">已重组 {{ status.chunks_done }} / {{ status.chunks_total }} 个 chunk<span v-if="status.left">，剩余 {{ formatBytes(status.left) }}</span></div>
        <div class="bal-warn">重组期间磁盘 IO 较高；可暂停让出 IO，请勿关机。</div>
      </template>
      <template v-else>
        <!-- 模式选择 -->
        <el-radio-group v-model="form.mode" class="bal-mode">
          <el-radio-button label="reclaim">空间回收</el-radio-button>
          <el-radio-button label="convert">在线转换 RAID</el-radio-button>
        </el-radio-group>
        <el-form label-width="100px" label-position="left" style="margin-top: 12px;">
          <el-form-item v-if="form.mode === 'reclaim'" label="利用率阈值">
            <el-slider v-model="form.usage" :min="0" :max="50" show-input style="padding-right: 8px;" />
            <div class="bal-hint">只重组利用率 ≤ 该值的 chunk（0 = 仅回收全空 chunk，最安全最快）</div>
          </el-form-item>
          <el-form-item v-else label="目标 profile">
            <el-select v-model="form.target" style="width: 100%;">
              <el-option label="single（无冗余）" value="single" />
              <el-option label="raid1（镜像，≥2 盘）" value="raid1" />
              <el-option label="raid10（≥4 盘）" value="raid10" />
            </el-select>
          </el-form-item>
        </el-form>
        <el-alert type="warning" :closable="false" show-icon title="Balance 会全盘读写重组数据，可能耗时较长。已设严格护栏（盘数/空间/并发），不满足会前置拦截。" />
      </template>
    </div>

    <template #footer>
      <template v-if="status.state === 'running'">
        <el-button type="info" :loading="acting" @click="act(pauseBtrfsBalance, 'pause')">暂停</el-button>
        <el-button type="warning" :loading="acting" @click="act(cancelBtrfsBalance, 'cancel')">取消</el-button>
      </template>
      <template v-else-if="status.state === 'paused'">
        <el-button type="primary" :loading="acting" @click="act(resumeBtrfsBalance, 'resume')">恢复</el-button>
        <el-button type="warning" :loading="acting" @click="act(cancelBtrfsBalance, 'cancel')">取消</el-button>
      </template>
      <template v-else>
        <el-button type="primary" :loading="acting" :disabled="!canStart" @click="handleStart">启动 Balance</el-button>
      </template>
      <el-button @click="visible = false">关闭</el-button>
    </template>
  </el-dialog>
</template>

<script setup>
import { ref, reactive, computed } from 'vue'
import { ElMessage } from 'element-plus'
import { getBtrfsBalanceStatus, startBtrfsBalance, cancelBtrfsBalance, pauseBtrfsBalance, resumeBtrfsBalance } from '@/api/infra'
import { useBtrfsBackgroundOp } from '@/composables/useBtrfsBackgroundOp'

const visible = ref(false)
const label = ref('')
const acting = ref(false)
const form = reactive({ mode: 'reclaim', usage: 0, target: 'raid1' })

const { status, loading, refresh, stopPoll, schedulePoll } = useBtrfsBackgroundOp({
  fetchStatus: () => getBtrfsBalanceStatus(label.value),
  isRunning: (s) => s && (s.state === 'running' || s.state === 'paused'),
})

const canStart = computed(() => form.mode === 'reclaim' || !!form.target)
const formatBytes = (n) => {
  if (!n) return '0B'
  const u = ['B', 'K', 'M', 'G', 'T', 'P']
  let i = 0, v = n
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(v >= 100 ? 0 : 1)}${u[i]}`
}

async function handleStart() {
  acting.value = true
  try {
    await startBtrfsBalance({ label: label.value, mode: form.mode, usage: form.usage, target_profile: form.target })
    ElMessage.success('balance 已启动')
    await refresh()
  } catch (e) { /* request 拦截器已提示护栏错误 */ }
  finally { acting.value = false }
}
async function act(fn, _op) {
  acting.value = true
  try { await fn(label.value); await refresh() } catch (e) {}
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
.bal-body { display: flex; flex-direction: column; gap: 12px; }
.bal-meta { font-size: 12px; color: #666; }
.bal-hint { font-size: 12px; color: #999; }
.bal-warn { font-size: 12px; color: #e6a23c; }
</style>
