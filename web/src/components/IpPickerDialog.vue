<template>
  <el-dialog v-model="visible" title="选择可分配 IP" width="520px" append-to-body :close-on-click-modal="false" @open="load">
    <div v-loading="loading">
      <div v-if="ips.length" class="ip-meta">可分配 IP · 共 {{ ips.length }} 个，点击选择</div>
      <div v-if="ips.length" class="ip-grid">
        <div v-for="ip in ips" :key="ip" class="ip-card" @click="pick(ip)">
          <span class="ip-dot" />
          <span class="ip-addr">{{ ip }}</span>
        </div>
      </div>
      <el-empty v-if="!loading && !ips.length" description="该子网暂无可分配 IP" :image-size="60" />
    </div>
    <template #footer>
      <el-button @click="visible = false">关闭</el-button>
    </template>
  </el-dialog>
</template>

<script setup>
import { ref, watch } from 'vue'
import { getAvailableIPs } from '@/api/vpc'

const props = defineProps({
  modelValue: { type: Boolean, default: false },
  switchId: { type: [Number, String], default: 0 },
})
const emit = defineEmits(['update:modelValue', 'select'])

const visible = ref(props.modelValue)
const loading = ref(false)
const ips = ref([])

watch(() => props.modelValue, (v) => { visible.value = v })
watch(visible, (v) => { emit('update:modelValue', v) })

const load = async () => {
  ips.value = []
  if (!props.switchId) return
  loading.value = true
  try {
    const res = await getAvailableIPs(props.switchId)
    ips.value = res.data || []
  } catch (e) { /* request 拦截器已弹 toast */ } finally { loading.value = false }
}

watch(() => props.switchId, () => { if (visible.value) load() })

const pick = (ip) => {
  emit('select', ip)
  visible.value = false
}
</script>

<style scoped>
.ip-meta { color: var(--el-text-color-secondary); font-size: 13px; margin-bottom: 12px; }
.ip-grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 10px; max-height: 320px; overflow: auto; }
.ip-card { display: flex; align-items: center; gap: 8px; padding: 10px 12px; border: 1px solid var(--el-border-color); border-radius: 8px; cursor: pointer; transition: all .15s; }
.ip-card:hover { border-color: var(--el-color-primary); box-shadow: 0 2px 8px rgba(0,0,0,.08); transform: translateY(-1px); }
.ip-dot { width: 8px; height: 8px; border-radius: 50%; background: var(--el-color-success); flex-shrink: 0; }
.ip-addr { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 14px; }
</style>
