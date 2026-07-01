<template>
  <div class="menu-editor">
    <el-alert type="info" :closable="false" show-icon style="margin-bottom: 12px;">
      <template #title>菜单编辑（全局生效）</template>
      <div class="me-tip">
        开关控制显示；「所属」选「顶层」即<strong>主菜单</strong>，选某个分组即<strong>子菜单</strong>。
        受保护项（虚拟机列表/系统设置/关于）不可隐藏。删除分组会将其子项移到顶层。
      </div>
    </el-alert>

    <div class="me-toolbar">
      <el-button :icon="Plus" @click="addGroup">新建分组</el-button>
      <el-button @click="resetDefault">恢复默认</el-button>
      <el-button type="primary" :loading="saving" :icon="Check" @click="save">保存</el-button>
    </div>

    <el-row :gutter="16">
      <!-- 编辑区 -->
      <el-col :xs="24" :md="14">
        <div class="me-rows">
          <template v-for="(row, idx) in flatRows" :key="(row.rowKind === 'group' ? 'g:' : 'i:') + (row.rowKind === 'group' ? row.node.id : row.node.key) + ':' + idx">
            <!-- 分组行 -->
            <div v-if="row.rowKind === 'group'" class="me-row me-row-group">
              <div class="me-row-main">
                <el-input v-model="row.node.title" class="me-group-title" placeholder="分组名称" />
                <el-select v-model="row.node.icon" class="me-icon-sel" :teleported="false">
                  <el-option v-for="ic in ICON_OPTIONS" :key="ic" :label="ic" :value="ic">
                    <SidebarIcons :icon="ic" :size="16" />
                    <span style="margin-left:6px;">{{ ic }}</span>
                  </el-option>
                </el-select>
                <el-tooltip :content="groupContainsProtected(row.node) ? '该分组含受保护项，不可关闭' : '显示/隐藏整组'" placement="top">
                  <span>
                    <el-switch v-model="row.node.enabled" :disabled="groupContainsProtected(row.node)" />
                  </span>
                </el-tooltip>
              </div>
              <div class="me-row-ops">
                <el-button text :icon="ArrowUp" :disabled="idx === 0" @click="reorderNode(row, -1)" />
                <el-button text :icon="ArrowDown" :disabled="isLastRow(idx)" @click="reorderNode(row, 1)" />
                <el-button text type="danger" :icon="Delete" @click="removeGroup(row.node)" />
              </div>
            </div>

            <!-- 菜单项行 -->
            <div v-else class="me-row me-row-item" :class="{ 'is-child': !!row.parent }">
              <div class="me-row-main">
                <SidebarIcons :icon="itemMeta(row.node.key)?.icon || 'folder'" :size="16" />
                <span class="me-item-title">{{ itemMeta(row.node.key)?.title || row.node.key }}</span>
                <el-tag v-if="itemMeta(row.node.key)?.protected" size="small" type="warning" effect="plain">受保护</el-tag>
                <el-select
                  :model-value="row.parent ? row.parent.id : '__top__'"
                  class="me-parent-sel"
                  :teleported="false"
                  @change="(v) => moveItem(row.node.key, v)"
                >
                  <el-option label="顶层（主菜单）" value="__top__" />
                  <el-option v-for="g in topLevelGroups" :key="g.id" :label="g.title || g.id" :value="g.id" />
                </el-select>
                <el-tooltip :content="itemMeta(row.node.key)?.protected ? '受保护，不可隐藏' : '显示/隐藏'" placement="top">
                  <span>
                    <el-switch v-model="row.node.enabled" :disabled="!!itemMeta(row.node.key)?.protected" />
                  </span>
                </el-tooltip>
              </div>
              <div class="me-row-ops">
                <el-button text :icon="ArrowUp" :disabled="!canMoveItem(row, -1)" @click="moveItemUpDown(row, -1)" />
                <el-button text :icon="ArrowDown" :disabled="!canMoveItem(row, 1)" @click="moveItemUpDown(row, 1)" />
              </div>
            </div>
          </template>
        </div>

        <!-- 未放置的可添加项 -->
        <div v-if="addableItems.length" class="me-addable">
          <div class="me-addable-title">未放置的菜单项：</div>
          <el-button v-for="key in addableItems" :key="key" size="small" @click="addItem(key)">
            <SidebarIcons :icon="itemMeta(key)?.icon || 'folder'" :size="14" />
            <span style="margin-left:4px;">{{ itemMeta(key)?.title || key }}</span>
          </el-button>
        </div>
      </el-col>

      <!-- 实时预览（管理员视角） -->
      <el-col :xs="24" :md="10">
        <el-card class="me-preview" shadow="never">
          <template #header><span>实时预览（管理员视角）</span></template>
          <el-menu class="me-preview-menu">
            <template v-for="node in previewNodes" :key="node.type === 'group' ? ('g:' + node.id) : ('i:' + node.route)">
              <el-menu-item v-if="node.type === 'item'" :index="node.route" disabled>
                <SidebarIcons :icon="node.icon" />
                <span>{{ node.title }}</span>
              </el-menu-item>
              <el-sub-menu v-else :index="'g:' + node.id">
                <template #title>
                  <SidebarIcons :icon="node.icon" />
                  <span>{{ node.title }}</span>
                </template>
                <el-menu-item v-for="child in node.children" :key="child.route" :index="child.route" disabled>
                  <SidebarIcons :icon="child.icon" />
                  <span>{{ child.title }}</span>
                </el-menu-item>
              </el-sub-menu>
            </template>
          </el-menu>
        </el-card>
      </el-col>
    </el-row>
  </div>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { Plus, Check, ArrowUp, ArrowDown, Delete } from '@element-plus/icons-vue'
import { updateSettings } from '@/api/settings'
import SidebarIcons from '@/components/icons/SidebarIcons.vue'
import { menuCatalog, defaultMenuLayout, composeMenu } from '@/utils/menu'
import { menuLayoutRaw, setMenuLayoutRaw } from '@/utils/site'

const ICON_OPTIONS = ['home', 'vm', 'template', 'network', 'globe', 'vpc', 'firewall', 'storage-pool', 'node', 'folder', 'user', 'scheduler', 'setting', 'storage', 'system', 'host', 'about']

const byKey = Object.create(null)
menuCatalog.forEach((i) => { byKey[i.key] = i })
const itemMeta = (key) => byKey[key]

const clone = (x) => JSON.parse(JSON.stringify(x))

// 编辑态：嵌套 nodes 树（与存储格式一致）
const working = ref(clone(defaultMenuLayout.nodes))
const saving = ref(false)

function loadFromRaw(raw) {
  let nodes
  try {
    const obj = typeof raw === 'string' && raw.trim() ? JSON.parse(raw) : null
    nodes = obj && Array.isArray(obj.nodes) ? obj.nodes : null
  } catch { nodes = null }
  working.value = clone(nodes || defaultMenuLayout.nodes)
}
onMounted(() => loadFromRaw(menuLayoutRaw.value))

// ---- 只读派生 ----
const referencedKeys = computed(() => {
  const set = new Set()
  const walk = (arr) => arr.forEach((n) => {
    if (n.kind === 'item' && n.key) set.add(n.key)
    else if (n.kind === 'group') walk(n.children || [])
  })
  walk(working.value)
  return set
})
const addableItems = computed(() => menuCatalog.map((i) => i.key).filter((k) => !referencedKeys.value.has(k)))
const topLevelGroups = computed(() => working.value.filter((n) => n.kind === 'group'))

// 扁平化为行（组行 + 子项行 + 顶层项行），保留 parent 与索引以便操作
const flatRows = computed(() => {
  const rows = []
  working.value.forEach((node, topIndex) => {
    if (node.kind === 'group') {
      rows.push({ rowKind: 'group', node, topIndex })
      ;(node.children || []).forEach((c) => {
        rows.push({ rowKind: 'item', node: c, parent: node, topIndex })
      })
    } else {
      rows.push({ rowKind: 'item', node, parent: null, topIndex })
    }
  })
  return rows
})
const isLastRow = (idx) => idx >= flatRows.value.length - 1

const previewNodes = computed(() =>
  composeMenu(JSON.stringify({ version: 1, nodes: working.value }), { isAdmin: true, isLightweight: false })
)

// ---- 分组操作 ----
function genGroupId() {
  return 'g_' + Math.random().toString(36).slice(2, 8)
}
function addGroup() {
  working.value.push({ kind: 'group', id: genGroupId(), title: '新分组', icon: 'folder', enabled: true, children: [] })
}
function groupContainsProtected(group) {
  return (group.children || []).some((c) => c.kind === 'item' && itemMeta(c.key)?.protected)
}
function removeGroup(group) {
  // 子项移到顶层（保留其在原分组中的相对顺序，插到该分组位置）
  const idx = working.value.indexOf(group)
  if (idx < 0) return
  const replacement = []
  for (const c of group.children || []) {
    replacement.push(c.kind === 'item' ? { ...c } : c)
  }
  working.value.splice(idx, 1, ...replacement)
}

// ---- 通用树操作 ----
function removeItemByKey(nodes, key) {
  for (let i = 0; i < nodes.length; i++) {
    const n = nodes[i]
    if (n.kind === 'item' && n.key === key) return nodes.splice(i, 1)[0]
    if (n.kind === 'group') {
      const r = removeItemByKey(n.children || [], key)
      if (r) return r
    }
  }
  return null
}
function findGroup(id) {
  return topLevelGroups.value.find((g) => g.id === id) || null
}
function moveItem(key, target) {
  const node = removeItemByKey(working.value, key)
  if (!node) return
  if (target === '__top__' || target == null) {
    working.value.push(node)
  } else {
    const g = findGroup(target)
    if (g) g.children.push(node)
    else working.value.push(node)
  }
}
function addItem(key) {
  if (referencedKeys.value.has(key)) return
  working.value.push({ kind: 'item', key, enabled: true })
}

// ---- 排序 ----
function reorderNode(row, delta) {
  // 组与顶层项混排：直接在 working 顶层按 topIndex 换位
  const arr = working.value
  const i = row.topIndex
  const j = i + delta
  if (j < 0 || j >= arr.length) return
  const me = arr[i]
  // 只与相邻顶层节点整体交换（组被视为一个整体，不拆散其子项行）
  arr.splice(i, 1)
  arr.splice(j, 0, me)
}
function itemSiblingAndIndex(row) {
  if (row.parent) {
    const arr = row.parent.children
    return { arr, index: arr.indexOf(row.node) }
  }
  return { arr: working.value, index: working.value.indexOf(row.node) }
}
function canMoveItem(row, delta) {
  const { arr, index } = itemSiblingAndIndex(row)
  return index + delta >= 0 && index + delta < arr.length
}
function moveItemUpDown(row, delta) {
  const { arr, index } = itemSiblingAndIndex(row)
  const j = index + delta
  if (j < 0 || j >= arr.length) return
  const [me] = arr.splice(index, 1)
  arr.splice(j, 0, me)
}

// ---- 校验与保存 ----
function validate() {
  const ids = new Set()
  for (const g of topLevelGroups.value) {
    if (!(g.title && String(g.title).trim())) return '存在未命名的分组'
    if (ids.has(g.id)) return '存在重复的分组标识'
    ids.add(g.id)
    if (g.enabled === false && groupContainsProtected(g)) return '受保护项所在的分组不能被关闭'
  }
  // 受保护项必须存在且启用（UI 已保证，防御性再查）
  for (const key of ['vm-list', 'settings', 'about']) {
    if (!referencedKeys.value.has(key)) return `受保护项 ${key} 缺失`
  }
  return null
}
function resetDefault() {
  working.value = clone(defaultMenuLayout.nodes)
  ElMessage.info('已恢复为默认菜单（需点击保存生效）')
}
async function save() {
  const err = validate()
  if (err) { ElMessage.warning(err); return }
  const raw = JSON.stringify({ version: 1, nodes: working.value })
  saving.value = true
  try {
    await updateSettings({ menu_layout: raw })
    setMenuLayoutRaw(raw) // 即时刷新侧边栏
    ElMessage.success('菜单配置已保存')
  } finally {
    saving.value = false
  }
}
</script>

<style scoped>
.menu-editor { padding: 4px 2px; }
.me-tip { font-size: 12px; color: var(--el-text-color-secondary); line-height: 1.6; margin-top: 2px; }
.me-toolbar { margin-bottom: 12px; display: flex; gap: 8px; flex-wrap: wrap; }
.me-rows { display: flex; flex-direction: column; gap: 8px; }
.me-row {
  display: flex; align-items: center; justify-content: space-between;
  border: 1px solid var(--el-border-color-light); border-radius: 8px; padding: 8px 10px;
  background: var(--el-bg-color-overlay);
}
.me-row-group { background: var(--el-fill-color-light); }
.me-row-item.is-child { margin-left: 28px; }
.me-row-main { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.me-group-title { width: 160px; }
.me-icon-sel { width: 130px; }
.me-parent-sel { width: 180px; }
.me-item-title { font-weight: 500; }
.me-row-ops { display: flex; align-items: center; gap: 2px; }
.me-addable { margin-top: 14px; padding-top: 12px; border-top: 1px dashed var(--el-border-color); }
.me-addable-title { font-size: 12px; color: var(--el-text-color-secondary); margin-bottom: 8px; }
.me-preview { background: var(--el-bg-color-overlay); }
.me-preview-menu { border-right: none; }
.me-preview-menu :deep(.is-disabled) { opacity: 1 !important; cursor: default !important; color: var(--el-text-color-primary) !important; }
</style>
