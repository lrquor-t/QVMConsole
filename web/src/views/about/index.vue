<template>
  <div class="about-container">
    <el-tabs v-model="activeTab" type="border-card" class="about-tabs">
      <el-tab-pane label="版本信息" name="version">
        <div class="tab-content">
          <el-descriptions :column="1" border direction="vertical" class="version-info">
            <el-descriptions-item label="当前版本" label-align="right">
              <el-tag v-if="versionInfo.version" type="primary" size="large">{{ versionInfo.version }}</el-tag>
              <el-tag v-else type="info" size="large">加载中...</el-tag>
            </el-descriptions-item>
            <el-descriptions-item label="构建时间" label-align="right">
              <span v-if="versionInfo.build_time">{{ versionInfo.build_time }}</span>
              <span v-else class="text-muted">开发环境</span>
            </el-descriptions-item>
            <el-descriptions-item label="站点名称" label-align="right">
              {{ versionInfo.site_title || '-' }}
            </el-descriptions-item>
          </el-descriptions>
        </div>
      </el-tab-pane>
    </el-tabs>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { getPublicVersion } from '@/api/settings'
import { applyDocumentTitle } from '@/utils/site'

const activeTab = ref('version')
const versionInfo = ref({
  version: '',
  build_time: '',
  site_title: ''
})

const fetchVersion = async () => {
  try {
    const res = await getPublicVersion()
    versionInfo.value = {
      version: res.data?.version || '',
      build_time: res.data?.build_time || '',
      site_title: res.data?.site_title || ''
    }
  } catch {
    versionInfo.value = { version: 'dev', build_time: '', site_title: '' }
  }
}

onMounted(() => {
  applyDocumentTitle('关于项目')
  fetchVersion()
})
</script>

<style scoped>
.about-container {
  max-width: 800px;
  margin: 0 auto;
}

.about-tabs {
  box-shadow: var(--el-box-shadow-light);
}

.tab-content {
  padding: 24px 20px;
}

.version-info {
  max-width: 600px;
}

.text-muted {
  color: var(--el-text-color-secondary);
}
</style>
