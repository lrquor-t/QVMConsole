<template>
  <div class="lxc-configfile-panel">
    <el-alert :type="editable ? 'warning' : 'info'" :closable="false" show-icon class="cfgfile-alert">
      <template #title>
        直接改写容器 config 文件，<b>重启后生效</b>。误改可能导致容器无法启动，保存前已自动备份。
      </template>
      <div v-if="!editable" class="cfgfile-alert-sub">
        容器当前{{ statusText }}，<b>修改/恢复已禁用</b>（查看不受影响），请先停止容器。
      </div>
    </el-alert>

    <!-- 编辑器卡片：header 工具条 + meta 副行 + 文本编辑区 -->
    <el-card shadow="hover" class="cfgfile-card">
      <template #header>
        <div class="cfgfile-header">
          <div class="cfgfile-fileinfo">
            <el-icon class="cfgfile-fileicon"><Document /></el-icon>
            <el-tooltip :content="meta.path" placement="top" :disabled="!meta.path">
              <span class="cfgfile-path">{{ meta.path || '—' }}</span>
            </el-tooltip>
            <el-tag :type="editable ? 'success' : 'info'" size="small" effect="light" class="cfgfile-status">
              {{ editable ? '可编辑' : '只读' }}
            </el-tag>
          </div>
          <div class="cfgfile-header-actions">
            <span v-if="dirty" class="cfgfile-dirty"><i class="cfgfile-dirty-dot" />有未保存改动</span>
            <el-button :icon="Refresh" :disabled="loading" @click="loadContent">重新加载</el-button>
            <el-button
              type="primary"
              :icon="Check"
              :disabled="!editable || !dirty || saving"
              :loading="saving"
              @click="onSave"
            >保存</el-button>
          </div>
        </div>
      </template>

      <div class="cfgfile-meta">
        <span>{{ formatBytes(meta.size) }}</span>
        <i class="cfgfile-dot" />
        <span>修改于 {{ formatTime(meta.mtime) }}</span>
        <i class="cfgfile-dot" />
        <span>{{ lineCount }} 行</span>
      </div>

      <div v-loading="loading" class="cfgfile-editor-wrap">
        <el-input
          v-model="content"
          type="textarea"
          :autosize="{ minRows: 18, maxRows: 30 }"
          :disabled="!editable"
          class="cfgfile-editor"
          placeholder="正在加载配置文件…"
        />
      </div>
    </el-card>

    <!-- 历史备份卡片 -->
    <el-card shadow="hover" class="cfgfile-card">
      <div class="section-title">
        历史备份
        <span v-if="backups.length" class="cfgfile-count">{{ backups.length }}</span>
      </div>
      <el-table v-loading="backupsLoading" :data="backups" size="small" empty-text="暂无备份">
        <el-table-column prop="name" label="文件名" min-width="220" />
        <el-table-column label="大小" width="100">
          <template #default="{ row }">{{ formatBytes(row.size) }}</template>
        </el-table-column>
        <el-table-column label="时间" width="180">
          <template #default="{ row }">{{ formatTime(row.mtime) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="210" align="right">
          <template #default="{ row }">
            <el-button size="small" link :icon="View" @click="onView(row)">查看</el-button>
            <el-button size="small" link :disabled="!editable" @click="onRestore(row.name)">恢复</el-button>
            <el-button size="small" link type="danger" @click="onDelete(row.name)">删除</el-button>
          </template>
        </el-table-column>
        <template #empty>
          <el-empty description="暂无备份" :image-size="60" />
        </template>
      </el-table>
    </el-card>

    <!-- 查看历史备份内容（只读） -->
    <el-dialog
      v-model="viewVisible"
      :title="`备份内容 · ${viewMeta.name}`"
      width="720px"
      append-to-body
      destroy-on-close
    >
      <div class="cfgfile-view-meta">
        <span>{{ formatBytes(viewMeta.size) }}</span>
        <i class="cfgfile-dot" />
        <span>备份于 {{ formatTime(viewMeta.mtime) }}</span>
      </div>
      <div v-loading="viewLoading" class="cfgfile-view-body">
        <el-input
          v-model="viewContent"
          type="textarea"
          :autosize="{ minRows: 22, maxRows: 30 }"
          readonly
          class="cfgfile-editor"
          placeholder="正在加载备份内容…"
        />
      </div>
      <template #footer>
        <el-button @click="viewVisible = false">关闭</el-button>
        <el-button :icon="CopyDocument" @click="onCopy">复制</el-button>
        <el-button type="warning" :disabled="!editable" @click="onRestoreFromView">恢复此备份</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { ref, computed, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Document, Refresh, Check, View, CopyDocument } from '@element-plus/icons-vue'
import {
  getLXCConfigFile, setLXCConfigFile,
  getLXCConfigFileBackups, getLXCConfigFileBackup, restoreLXCConfigFileBackup, deleteLXCConfigFileBackup
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

// 查看历史备份内容
const viewVisible = ref(false)
const viewLoading = ref(false)
const viewContent = ref('')
const viewMeta = ref({ name: '', size: 0, mtime: 0 })

const editable = computed(() => props.status === 'STOPPED')
const statusText = computed(() => {
  const m = { RUNNING: '运行中', FROZEN: '已冻结', STARTING: '启动中', ABORTING: '异常' }
  return m[props.status] || '非停止'
})
const dirty = computed(() => content.value !== serverContent.value)
const lineCount = computed(() => (content.value ? content.value.split('\n').length : 0))

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

const onView = async (row) => {
  viewMeta.value = { name: row.name, size: row.size, mtime: row.mtime }
  viewContent.value = ''
  viewVisible.value = true
  viewLoading.value = true
  try {
    const r = await getLXCConfigFileBackup(props.name, row.name)
    viewContent.value = (r.data && r.data.content) || ''
  } catch (e) {
    viewContent.value = ''
  } finally {
    viewLoading.value = false
  }
}

const onCopy = async () => {
  try {
    await navigator.clipboard.writeText(viewContent.value)
    ElMessage.success('已复制到剪贴板')
  } catch (e) {
    ElMessage.error('复制失败，请手动选择文本复制')
  }
}

// 对话框内「恢复此备份」：先关对话框，再走主恢复流程（含二次确认 + 自动备份当前版本）
const onRestoreFromView = () => {
  const bak = viewMeta.value.name
  viewVisible.value = false
  onRestore(bak)
}

// name 变化（切容器）重载；immediate 触发首次加载
watch(() => props.name, reloadAll, { immediate: true })
</script>

<style scoped>
.lxc-configfile-panel {
  padding: 4px 2px;
  display: flex;
  flex-direction: column;
  gap: 16px;
}

/* 告警条：去掉默认外边距，让其融入面板节奏 */
.cfgfile-alert {
  margin: 0;
}
.cfgfile-alert-sub {
  margin-top: 4px;
  font-size: 12px;
}

/* 卡片：与快照/配置面板同节奏 */
.cfgfile-card {
  border-radius: 12px;
  border: none;
}
.cfgfile-card :deep(.el-card__header) {
  padding: 0 18px;
}
.cfgfile-card :deep(.el-card__body) {
  padding: 14px 18px 18px;
}

/* 编辑器 header 工具条 */
.cfgfile-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  height: 52px;
}
.cfgfile-fileinfo {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.cfgfile-fileicon {
  color: var(--el-color-primary);
  font-size: 18px;
  flex-shrink: 0;
}
.cfgfile-path {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 13px;
  color: var(--el-text-color-primary);
  max-width: 300px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.cfgfile-status {
  flex-shrink: 0;
}
.cfgfile-header-actions {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-shrink: 0;
}
.cfgfile-dirty {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 12px;
  color: var(--el-color-warning);
}
.cfgfile-dirty-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--el-color-warning);
}

/* meta 副行 */
.cfgfile-meta {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  margin-bottom: 10px;
}
.cfgfile-dot {
  display: inline-block;
  width: 3px;
  height: 3px;
  border-radius: 50%;
  background: var(--el-text-color-placeholder);
}

/* 文本编辑区 */
.cfgfile-editor :deep(textarea) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  border-radius: 8px;
}

/* 分区标题（沿用全局约定） */
.section-title {
  font-size: 16px;
  font-weight: 700;
  padding-left: 10px;
  border-left: 4px solid var(--el-color-primary);
  margin-bottom: 14px;
  color: var(--el-text-color-primary);
  display: flex;
  align-items: center;
  gap: 8px;
}
.cfgfile-count {
  font-size: 12px;
  font-weight: 500;
  color: var(--el-text-color-secondary);
  background: var(--el-fill-color-light, #f5f7fa);
  padding: 1px 8px;
  border-radius: 10px;
}

/* 查看备份对话框 */
.cfgfile-view-meta {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  margin-bottom: 10px;
}
.cfgfile-view-body {
  min-height: 120px;
}
</style>
