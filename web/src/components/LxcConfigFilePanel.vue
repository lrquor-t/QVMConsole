<template>
  <div class="lxc-configfile-panel">
    <el-alert :type="editable ? 'warning' : 'info'" :closable="false" show-icon>
      <template #title>
        直接改写容器 config 文件，<b>重启后生效</b>。误改可能导致容器无法启动，保存前已自动备份。
      </template>
      <div v-if="!editable" style="margin-top: 4px">
        容器当前{{ statusText }}，<b>修改/恢复已禁用</b>，请先停止容器（查看不受影响）。
      </div>
    </el-alert>

    <div class="cfgfile-meta">
      <span>路径：<code>{{ meta.path || '—' }}</code></span>
      <span>大小：{{ formatBytes(meta.size) }}</span>
      <span>修改：{{ formatTime(meta.mtime) }}</span>
    </div>

    <el-input
      v-model="content"
      type="textarea"
      :autosize="{ minRows: 18, maxRows: 30 }"
      :disabled="!editable"
      class="cfgfile-editor"
      placeholder="正在加载配置文件…"
    />

    <div class="cfgfile-actions">
      <el-button type="primary" :disabled="!editable || !dirty || saving" :loading="saving" @click="onSave">保存</el-button>
      <el-button :disabled="loading" @click="loadContent">重新加载</el-button>
      <span v-if="dirty" class="cfgfile-dirty">有未保存改动</span>
    </div>

    <div class="cfgfile-backups">
      <div class="section-title">历史备份</div>
      <el-table v-loading="backupsLoading" :data="backups" size="small" empty-text="暂无备份">
        <el-table-column prop="name" label="文件名" min-width="220" />
        <el-table-column label="大小" width="100">
          <template #default="{ row }">{{ formatBytes(row.size) }}</template>
        </el-table-column>
        <el-table-column label="时间" width="180">
          <template #default="{ row }">{{ formatTime(row.mtime) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="140" align="right">
          <template #default="{ row }">
            <el-button size="small" link :disabled="!editable" @click="onRestore(row.name)">恢复</el-button>
            <el-button size="small" link type="danger" @click="onDelete(row.name)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  getLXCConfigFile, setLXCConfigFile,
  getLXCConfigFileBackups, restoreLXCConfigFileBackup, deleteLXCConfigFileBackup
} from '@/api/lxc'

const props = defineProps({
  name: { type: String, required: true },
  status: { type: String, default: '' }
})

const content = ref('')
const serverContent = ref('') // 最后一次服务端内容，用于判脏
const meta = ref({ path: '', size: 0, mtime: 0 })
const loading = ref(false)
const saving = ref(false)

const backups = ref([])
const backupsLoading = ref(false)

const editable = computed(() => props.status === 'STOPPED')
const statusText = computed(() => {
  const m = { RUNNING: '运行中', FROZEN: '已冻结', STARTING: '启动中', ABORTING: '异常' }
  return m[props.status] || '非停止'
})
const dirty = computed(() => content.value !== serverContent.value)

const formatBytes = (b) => {
  if (!b) return '0 B'
  const u = ['B', 'KB', 'MB']
  let i = 0, n = b
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++ }
  return `${n.toFixed(i ? 1 : 0)} ${u[i]}`
}
const formatTime = (ts) => {
  if (!ts) return '—'
  const d = new Date(ts * 1000)
  const p = (x) => String(x).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

const loadContent = async () => {
  if (!props.name) return
  loading.value = true
  try {
    const r = await getLXCConfigFile(props.name)
    const d = r.data || {}
    content.value = d.content || ''
    serverContent.value = d.content || ''
    meta.value = { path: d.path || '', size: d.size || 0, mtime: d.mtime || 0 }
  } catch (e) {
    content.value = ''
    serverContent.value = ''
  } finally {
    loading.value = false
  }
}

const loadBackups = async () => {
  if (!props.name) return
  backupsLoading.value = true
  try {
    const r = await getLXCConfigFileBackups(props.name)
    backups.value = r.data || []
  } catch (e) {
    backups.value = []
  } finally {
    backupsLoading.value = false
  }
}

const reloadAll = () => { loadContent(); loadBackups() }

const onSave = async () => {
  try {
    await ElMessageBox.confirm(
      '确认覆盖容器 config 文件？保存前会自动备份当前版本；改动需重启容器后生效。',
      '保存配置文件',
      { type: 'warning', confirmButtonText: '保存', cancelButtonText: '取消' }
    )
  } catch { return }
  saving.value = true
  try {
    await setLXCConfigFile(props.name, content.value)
    serverContent.value = content.value
    ElMessage.success('配置文件已保存')
    await loadContent() // 刷新 meta（mtime/size）
    await loadBackups() // 保存会生成新备份
  } catch (e) {
    // request 拦截器已弹错误消息（含 stale 状态被后端拒的「请先停止」）
  } finally {
    saving.value = false
  }
}

const onRestore = async (bak) => {
  try {
    await ElMessageBox.confirm(
      `确认用备份 ${bak} 覆盖当前 config？当前版本会先自动备份（可再次恢复）。`,
      '恢复配置文件',
      { type: 'warning', confirmButtonText: '恢复', cancelButtonText: '取消' }
    )
  } catch { return }
  try {
    await restoreLXCConfigFileBackup(props.name, bak)
    ElMessage.success('已恢复')
    reloadAll()
  } catch (e) {}
}

const onDelete = async (bak) => {
  try {
    await ElMessageBox.confirm(`确认删除备份 ${bak}？此操作不可撤销。`, '删除备份', {
      type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消'
    })
  } catch { return }
  try {
    await deleteLXCConfigFileBackup(props.name, bak)
    ElMessage.success('已删除')
    await loadBackups()
  } catch (e) {}
}

// name 变化（切容器）重载；immediate 触发首次加载
watch(() => props.name, reloadAll, { immediate: true })
</script>

<style scoped>
.lxc-configfile-panel {
  padding: 4px 2px;
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.cfgfile-meta {
  display: flex;
  gap: 18px;
  flex-wrap: wrap;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.cfgfile-meta code {
  font-family: ui-monospace, monospace;
  background: var(--app-bg-card);
  padding: 1px 5px;
  border-radius: 4px;
}
.cfgfile-editor :deep(textarea) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}
.cfgfile-actions {
  display: flex;
  align-items: center;
  gap: 12px;
}
.cfgfile-dirty {
  font-size: 12px;
  color: var(--el-color-warning);
}
.section-title {
  font-size: 16px;
  font-weight: 700;
  padding-left: 10px;
  border-left: 4px solid var(--el-color-primary);
  color: var(--el-text-color-primary);
}
</style>
