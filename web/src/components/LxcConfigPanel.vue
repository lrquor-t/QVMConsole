<template>
  <div class="lxc-config-panel">
    <el-card shadow="hover" class="cfg-card">
      <div class="section-title">基本配置</div>
      <el-form :model="form" label-width="110px" :disabled="saving">
        <el-form-item label="CPU 权重">
          <el-input-number v-model="form.cpu_shares" :min="0" />
          <div class="cfg-hint"><el-icon><InfoFilled /></el-icon> cgroup cpu.shares，越大优先级越高（默认 256）</div>
        </el-form-item>
        <el-form-item label="内存(MB)">
          <el-input-number v-model="form.memory_mb" :min="0" />
          <div class="cfg-hint"><el-icon><InfoFilled /></el-icon> cgroup memory.limit_in_bytes</div>
        </el-form-item>
        <el-divider style="margin: 6px 0 14px" />
        <el-form-item label="自动启动"><el-switch v-model="form.autostart" /></el-form-item>
        <el-form-item label="分组"><el-input v-model="form.group_name" /></el-form-item>
        <el-form-item label="备注"><el-input v-model="form.remark" /></el-form-item>
        <el-form-item>
          <el-button type="primary" :loading="saving" @click="save">保存</el-button>
        </el-form-item>
      </el-form>
    </el-card>

    <el-card v-if="backing === 'zfs'" shadow="hover" class="cfg-card">
      <div class="section-title">存储</div>
      <el-form label-width="110px" :disabled="diskSaving">
        <el-form-item label="磁盘配额(GB)">
          <el-input-number v-model="diskLimitGB" :min="0" :step="10" />
          <div class="cfg-hint"><el-icon><InfoFilled /></el-icon> refquota，限制容器 rootfs 数据量；0 = 不限。缩小到低于已用空间会被 ZFS 拒绝。</div>
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :loading="diskSaving" @click="saveDiskLimit">保存配额</el-button>
        </el-form-item>
      </el-form>
    </el-card>

    <el-card v-if="isAdmin" shadow="hover" class="cfg-card">
      <div class="section-title">CPU 限制（管理员）</div>
      <el-form label-width="110px" :disabled="cpuSaving">
        <el-form-item label="核数上限">
          <el-input-number v-model="cpuLimit.cores" :min="0" :step="0.5" :precision="3" />
          <div class="cfg-hint"><el-icon><InfoFilled /></el-icon> cgroup cpu.max，限制容器最大可用核数（支持小数）；0 = 不限。与上面「CPU 权重」正交（权重=相对争用，核数=绝对上限）。</div>
        </el-form-item>
        <el-form-item label="CPU 绑核">
          <el-input v-model="cpuLimit.cpuset" placeholder="如 0-3,^2（留空=不绑）" />
          <div class="cfg-hint"><el-icon><InfoFilled /></el-icon> cgroup cpuset.cpus，限定容器只能用指定物理核。运行中清除绑核需重启生效（核数上限可热生效）。</div>
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :loading="cpuSaving" @click="saveCPULimit">保存</el-button>
        </el-form-item>
      </el-form>
    </el-card>
  </div>
</template>

<script setup>
import { ref, watch, computed } from 'vue'
import { ElMessage } from 'element-plus'
import { InfoFilled } from '@element-plus/icons-vue'
import { updateLXCConfig, getLXCDiskLimit, setLXCDiskLimit, getLXCCPULimit, setLXCCPULimit } from '@/api/lxc'
import { useUserStore } from '@/store/user'

const props = defineProps({
  name: { type: String, required: true },
  backing: { type: String, default: '' },
  initialConfig: { type: Object, default: () => ({}) }
})
const emit = defineEmits(['saved'])

const userStore = useUserStore()
const isAdmin = computed(() => userStore.role === 'admin')

const saving = ref(false)
const form = ref({ cpu_shares: 0, memory_mb: 0, autostart: false, group_name: '', remark: '' })

const loadFromProps = () => {
  const c = props.initialConfig || {}
  form.value = {
    cpu_shares: c.cpu_shares ?? 0,
    memory_mb: c.memory_mb ?? 0,
    autostart: !!c.autostart,
    group_name: c.group_name || '',
    remark: c.remark || ''
  }
}
// initialConfig 随抽屉切换容器变化 → 重新填充表单
watch(() => props.initialConfig, loadFromProps, { immediate: true, deep: true })

const save = async () => {
  saving.value = true
  try {
    await updateLXCConfig(props.name, { ...form.value })
    ElMessage.success('已保存')
    emit('saved')
  } catch (e) {} finally { saving.value = false }
}

// 磁盘配额（refquota，仅 zfs backing；独立端点，单独保存）
const diskLimitGB = ref(0)
const diskSaving = ref(false)
const loadDiskLimit = async () => {
  if (props.backing !== 'zfs' || !props.name) { diskLimitGB.value = 0; return }
  diskLimitGB.value = 0
  try { const r = await getLXCDiskLimit(props.name); diskLimitGB.value = r.data?.gb || 0 }
  catch {} // 非 zfs / 读取失败静默
}
const saveDiskLimit = async () => {
  diskSaving.value = true
  try {
    await setLXCDiskLimit(props.name, diskLimitGB.value)
    ElMessage.success('磁盘配额已更新')
  } catch (e) { ElMessage.error(e?.message || '更新失败') }
  finally { diskSaving.value = false }
}
// CPU 硬限制 + 绑核（管理员；不限 backing，cpu.max 与存储后端无关）
const cpuLimit = ref({ cores: 0, cpuset: '' })
const cpuSaving = ref(false)
const loadCPULimit = async () => {
  if (!isAdmin.value || !props.name) { cpuLimit.value = { cores: 0, cpuset: '' }; return }
  try {
    const r = await getLXCCPULimit(props.name)
    cpuLimit.value = { cores: r.data?.cores ?? 0, cpuset: r.data?.cpuset ?? '' }
  } catch {
    cpuLimit.value = { cores: 0, cpuset: '' }
  }
}
const saveCPULimit = async () => {
  cpuSaving.value = true
  try {
    await setLXCCPULimit(props.name, { cores: cpuLimit.value.cores, cpuset: cpuLimit.value.cpuset })
    ElMessage.success('CPU 限制已更新')
  } catch (e) {
    ElMessage.error(e?.message || '更新失败')
  } finally {
    cpuSaving.value = false
  }
}
watch(() => [props.name, isAdmin.value], loadCPULimit, { immediate: true })

// 切容器或 backing 变化时重载配额（config tab 为 lazy，挂载时 immediate 触发首次加载）
watch(() => [props.name, props.backing], loadDiskLimit, { immediate: true })
</script>

<style scoped>
.lxc-config-panel {
  padding: 4px 2px;
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.cfg-card {
  border-radius: 12px;
  border: none;
}
.cfg-card :deep(.el-card__body) {
  padding: 16px 18px;
}
.cfg-hint {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  margin-top: 6px;
  line-height: 1.4;
  display: flex;
  align-items: center;
  gap: 4px;
}
.section-title {
  font-size: 16px;
  font-weight: 700;
  padding-left: 10px;
  border-left: 4px solid var(--el-color-primary);
  margin-bottom: 14px;
  color: var(--el-text-color-primary);
}
</style>
