<template>
  <el-dialog v-model="visible" :title="`执行命令 · ${name}`" width="640px" @close="result = null">
    <el-form @submit.prevent>
      <el-form-item label="命令">
        <el-input
          v-model="command"
          type="textarea"
          :rows="2"
          placeholder="如 uname -a  （Ctrl/Cmd + Enter 执行）"
          @keydown.enter.ctrl="run"
          @keydown.enter.meta="run"
        />
      </el-form-item>
      <el-form-item label="超时(秒)">
        <el-input-number v-model="timeoutSec" :min="0" :max="300" :step="5" />
        <span class="hint">0 = 默认 30 秒，上限 300</span>
      </el-form-item>
    </el-form>
    <div class="exec-actions">
      <el-button type="primary" :loading="running" :disabled="!command.trim()" @click="run">运行</el-button>
    </div>
    <div v-if="result" class="exec-output">
      <div class="exec-meta">
        <el-tag size="small" :type="result.exit_code === 0 ? 'success' : 'danger'">退出码 {{ result.exit_code }}</el-tag>
        <el-tag v-if="result.timed_out" size="small" type="warning">超时</el-tag>
        <el-tag v-if="result.truncated" size="small" type="info">输出已截断</el-tag>
      </div>
      <div v-if="result.stdout" class="exec-block">
        <div class="block-title">stdout</div>
        <pre>{{ result.stdout }}</pre>
      </div>
      <div v-if="result.stderr" class="exec-block">
        <div class="block-title">stderr</div>
        <pre class="err">{{ result.stderr }}</pre>
      </div>
    </div>
  </el-dialog>
</template>

<script setup>
import { ref } from 'vue'
import { ElMessage } from 'element-plus'
import { execLXC } from '@/api/lxc'

const visible = ref(false)
const name = ref('')
const command = ref('')
const timeoutSec = ref(0)
const running = ref(false)
const result = ref(null)

const open = (row) => {
  name.value = row.name
  command.value = ''
  result.value = null
  visible.value = true
}
defineExpose({ open })

const run = async () => {
  if (!command.value.trim()) return
  running.value = true
  result.value = null
  try {
    const res = await execLXC(name.value, { command: command.value, timeout_sec: timeoutSec.value })
    result.value = res.data
  } catch (e) {
    ElMessage.error(e?.message || '执行失败')
  } finally {
    running.value = false
  }
}
</script>

<style scoped>
.hint { margin-left: 8px; font-size: 12px; color: var(--el-text-color-secondary); }
.exec-actions { margin: 8px 0 12px; }
.exec-meta { display: flex; gap: 6px; margin-bottom: 8px; }
.exec-block { margin-bottom: 10px; }
.block-title { font-size: 12px; color: var(--el-text-color-secondary); margin-bottom: 4px; }
.exec-block pre {
  margin: 0; padding: 10px; max-height: 240px; overflow: auto;
  background: var(--el-fill-color-light); border-radius: 8px;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px;
  white-space: pre-wrap; word-break: break-all; color: var(--el-text-color-primary);
}
.exec-block pre.err { color: var(--el-color-danger); }
</style>
