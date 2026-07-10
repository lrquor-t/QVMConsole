<template>
  <el-dialog :model-value="modelValue" @update:model-value="$emit('update:modelValue', $event)"
    title="选择可分配 IP" width="420px" @open="load">
    <div v-loading="loading">
      <el-alert v-if="!loading && !ips.length" type="info" :closable="false" title="该子网暂无可分配 IP" />
      <div v-else class="ip-list">
        <div v-for="ip in ips" :key="ip" class="ip-item" @click="pick(ip)">{{ ip }}</div>
      </div>
    </div>
  </el-dialog>
</template>

<script setup>
import { ref, watch } from 'vue'
import { getAvailableIPs } from '@/api/network'

const props = defineProps({ modelValue: Boolean, switchId: { type: [Number, String], default: 0 } })
const emit = defineEmits(['update:modelValue', 'select'])
const ips = ref([])
const loading = ref(false)

const load = async () => {
  if (!props.switchId) { ips.value = []; return }
  loading.value = true
  try { const r = await getAvailableIPs(props.switchId); ips.value = r.data || [] }
  catch { ips.value = [] }
  finally { loading.value = false }
}

const pick = (ip) => { emit('select', ip); emit('update:modelValue', false) }

watch(() => props.switchId, () => { if (props.modelValue) load() })
</script>

<style scoped>
.ip-list { max-height: 320px; overflow-y: auto; display: grid; grid-template-columns: repeat(3, 1fr); gap: 8px; }
.ip-item { padding: 8px; text-align: center; border: 1px solid var(--el-border-color); border-radius: 4px; cursor: pointer; font-family: monospace; }
.ip-item:hover { background: var(--el-color-primary-light-9); border-color: var(--el-color-primary); }
</style>
