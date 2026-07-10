<template>
  <el-dialog :model-value="modelValue" @update:model-value="$emit('update:modelValue', $event)"
    title="选择可分配 IP" width="460px" append-to-body @open="load" class="ip-picker">
    <div class="ip-body" v-loading="loading">
      <div v-if="!loading && ips.length" class="ip-meta">可分配 IP · 共 {{ ips.length }} 个，点击选择</div>
      <el-empty v-if="!loading && !ips.length" description="该子网暂无可分配 IP" :image-size="80" />
      <div v-else-if="!loading" class="ip-grid">
        <div v-for="ip in ips" :key="ip" class="ip-card" @click="pick(ip)">
          <span class="dot" />
          <span class="addr">{{ ip }}</span>
        </div>
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
.ip-body { min-height: 160px; }
.ip-meta {
  margin-bottom: 12px;
  font-size: 13px;
  color: var(--el-text-color-secondary);
}
.ip-grid {
  max-height: 360px;
  overflow-y: auto;
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 10px;
  padding: 2px;
}
.ip-card {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 11px 14px;
  background: var(--el-fill-color-light);
  border: 1px solid var(--el-border-color-light);
  border-radius: 10px;
  cursor: pointer;
  user-select: none;
  transition: all 0.2s ease;
}
.ip-card .dot {
  flex: 0 0 auto;
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--el-color-success);
  box-shadow: 0 0 0 3px var(--el-color-success-light-9);
}
.ip-card .addr {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 14px;
  color: var(--el-text-color-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.ip-card:hover {
  border-color: var(--el-color-primary);
  background: var(--el-color-primary-light-9);
  box-shadow: 0 2px 10px rgba(64, 158, 255, 0.14);
  transform: translateY(-1px);
}
.ip-card:hover .addr { color: var(--el-color-primary); }
.ip-card:active { transform: translateY(0); }
.ip-grid::-webkit-scrollbar { width: 6px; }
.ip-grid::-webkit-scrollbar-thumb { background: var(--el-border-color); border-radius: 3px; }
.ip-grid::-webkit-scrollbar-thumb:hover { background: var(--el-border-color-darker); }
</style>
