<template>
  <div class="lxc-config-panel">
    <el-form :model="form" label-width="90px">
      <el-form-item label="CPU 权重"><el-input-number v-model="form.cpu_shares" :min="0" /></el-form-item>
      <el-form-item label="内存(MB)"><el-input-number v-model="form.memory_mb" :min="0" /></el-form-item>
      <el-form-item label="自动启动"><el-switch v-model="form.autostart" /></el-form-item>
      <el-form-item label="分组"><el-input v-model="form.group_name" /></el-form-item>
      <el-form-item label="备注"><el-input v-model="form.remark" /></el-form-item>
      <el-form-item>
        <el-button type="primary" :loading="saving" @click="save">保存</el-button>
      </el-form-item>
    </el-form>
  </div>
</template>

<script setup>
import { ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { updateLXCConfig } from '@/api/lxc'

const props = defineProps({
  name: { type: String, required: true },
  initialConfig: { type: Object, default: () => ({}) }
})
const emit = defineEmits(['saved'])

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
</script>

<style scoped>
.lxc-config-panel {
  padding: 4px 2px;
}
</style>
