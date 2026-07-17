<template>
  <el-dialog v-model="visible" :title="`属性 - ${label}`" width="560px" append-to-body :close-on-click-modal="false">
    <div v-loading="loading" class="prop-body">
      <el-form label-width="110px" label-position="left">
        <el-form-item label="压缩">
          <el-select v-model="form.compression" style="width:100%">
            <el-option v-for="a in info.algos || ['off','zstd','lzo','zlib']" :key="a" :label="a" :value="a" />
          </el-select>
          <div class="prop-hint">当前: {{ info.compression || '-' }}。仅对<b>新写入</b>数据生效，已有数据不变。</div>
        </el-form-item>
        <el-form-item label="vm-disks CoW">
          <el-switch v-model="form.nocow" active-text="关闭 CoW（nodatacow）" inactive-text="保留 CoW" />
          <div class="prop-hint">当前: {{ info.nocow ? '已关闭 CoW' : '保留 CoW' }}。只影响 vm-disks 内<b>新创建</b>文件；现有 VM 磁盘不变。</div>
        </el-form-item>
      </el-form>
      <el-alert type="warning" :closable="false" show-icon title="btrfs 改属性仅对新数据生效，不重写已有数据（不含 defrag）。改压缩需 remount。" />
    </div>
    <template #footer>
      <el-button @click="visible = false">取消</el-button>
      <el-button type="primary" :loading="saving" :disabled="!dirty" @click="save">保存</el-button>
    </template>
  </el-dialog>
</template>

<script setup>
import { ref, reactive, computed } from 'vue'
import { ElMessage } from 'element-plus'
import { getBtrfsProperty, setBtrfsProperty } from '@/api/infra'

const visible = ref(false)
const loading = ref(false)
const saving = ref(false)
const label = ref('')
const info = ref({ compression: 'off', nocow: false, algos: ['off', 'zstd', 'lzo', 'zlib'] })
const form = reactive({ compression: 'off', nocow: false })

const dirty = computed(() => form.compression !== info.value.compression || form.nocow !== info.value.nocow)

async function fetchInfo() {
  loading.value = true
  try {
    const res = await getBtrfsProperty(label.value)
    info.value = res.data || info.value
    form.compression = info.value.compression || 'off'
    form.nocow = !!info.value.nocow
  } finally { loading.value = false }
}
async function save() {
  saving.value = true
  const data = { label: label.value }
  if (form.compression !== info.value.compression) data.compression = form.compression
  if (form.nocow !== info.value.nocow) data.nocow = form.nocow
  try {
    await setBtrfsProperty(data)
    ElMessage.success('属性已更新')
    await fetchInfo()
  } catch (e) {}
  finally { saving.value = false }
}
function open(name) { label.value = name || ''; visible.value = true; fetchInfo() }
defineExpose({ open })
</script>

<style scoped>
.prop-body { display: flex; flex-direction: column; gap: 12px; }
.prop-hint { font-size: 12px; color: #999; margin-top: 2px; }
</style>
