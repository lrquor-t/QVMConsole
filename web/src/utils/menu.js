// 菜单目录：每个菜单项元信息的唯一事实来源（代码）。仅当真实功能页增删时改动。
// 数据库只存结构(menu_layout)，标题/图标/路由不进数据库，避免与真实路由漂移。
export const menuCatalog = [
  { key: 'home',         title: '首页',       icon: 'home',        route: '/dashboard',       adminOnly: false, lightweightHidden: true,  protected: false, defaultGroup: null },
  { key: 'vm-list',      title: '虚拟机列表', icon: 'vm',          route: '/vm/list',         adminOnly: false, lightweightHidden: false, protected: true,  defaultGroup: 'host' },
  { key: 'nodes',        title: '节点管理',   icon: 'node',        route: '/nodes',           adminOnly: true,  lightweightHidden: true,  protected: false, defaultGroup: 'host' },
  { key: 'template',     title: 'KVM模板',    icon: 'template',    route: '/template/list',   adminOnly: true,  lightweightHidden: true,  protected: false, defaultGroup: 'template' },
  { key: 'network',      title: '网络',       icon: 'network',     route: '/network',         adminOnly: false, lightweightHidden: true,  protected: false, defaultGroup: 'network', alt: { title: 'VPC网络', icon: 'vpc' } },
  { key: 'public-ip',    title: '公网 IP',    icon: 'globe',       route: '/public-ip',       adminOnly: true,  lightweightHidden: true,  protected: false, defaultGroup: 'network' },
  { key: 'firewall',     title: '防火墙',    icon: 'firewall',    route: '/firewall',        adminOnly: true,  lightweightHidden: true,  protected: false, defaultGroup: 'network' },
  { key: 'storage-pool', title: '存储池',    icon: 'storage-pool',route: '/storage-pool/list',adminOnly: true,  lightweightHidden: true,  protected: false, defaultGroup: 'storage' },
  { key: 'my-storage',   title: '我的存储',   icon: 'folder',      route: '/my-storage',      adminOnly: false, lightweightHidden: true,  protected: false, defaultGroup: 'storage' },
  { key: 'user-list',    title: '用户管理',   icon: 'user',        route: '/user/list',       adminOnly: true,  lightweightHidden: true,  protected: false, defaultGroup: 'system' },
  { key: 'scheduler',    title: '调度事件',   icon: 'scheduler',   route: '/scheduler/events',adminOnly: true,  lightweightHidden: true,  protected: false, defaultGroup: 'system' },
  { key: 'settings',     title: '系统设置',   icon: 'setting',     route: '/settings',        adminOnly: true,  lightweightHidden: true,  protected: true,  defaultGroup: 'system' },
  { key: 'about',        title: '关于项目',   icon: 'about',       route: '/about',           adminOnly: false, lightweightHidden: false, protected: true,  defaultGroup: null }
]

// 回退默认树（镜像当前硬编码菜单）。配置缺失/非法时使用。
export const defaultMenuLayout = {
  version: 1,
  nodes: [
    { kind: 'item', key: 'home', enabled: true },
    { kind: 'group', id: 'host', title: '主机管理', icon: 'host', enabled: true, children: [
      { kind: 'item', key: 'vm-list', enabled: true },
      { kind: 'item', key: 'nodes', enabled: true }
    ]},
    { kind: 'group', id: 'template', title: '模板管理', icon: 'template', enabled: true, children: [
      { kind: 'item', key: 'template', enabled: true }
    ]},
    { kind: 'group', id: 'network', title: '网络管理', icon: 'network', enabled: true, children: [
      { kind: 'item', key: 'network', enabled: true },
      { kind: 'item', key: 'public-ip', enabled: true },
      { kind: 'item', key: 'firewall', enabled: true }
    ]},
    { kind: 'group', id: 'storage', title: '存储管理', icon: 'storage', enabled: true, children: [
      { kind: 'item', key: 'storage-pool', enabled: true },
      { kind: 'item', key: 'my-storage', enabled: true }
    ]},
    { kind: 'group', id: 'system', title: '系统管理', icon: 'system', enabled: true, children: [
      { kind: 'item', key: 'user-list', enabled: true },
      { kind: 'item', key: 'scheduler', enabled: true },
      { kind: 'item', key: 'settings', enabled: true }
    ]},
    { kind: 'item', key: 'about', enabled: true }
  ]
}

function clone(value) {
  return JSON.parse(JSON.stringify(value))
}

// 解析存储的 JSON；空/非法/缺 nodes → 回退默认树
function parseLayout(raw) {
  if (typeof raw !== 'string' || raw.trim() === '') return clone(defaultMenuLayout)
  try {
    const obj = JSON.parse(raw)
    if (!obj || !Array.isArray(obj.nodes)) return clone(defaultMenuLayout)
    return { nodes: obj.nodes }
  } catch {
    return clone(defaultMenuLayout)
  }
}

function collectItemKeys(nodes, set) {
  for (const n of nodes) {
    if (n.kind === 'item' && n.key) set.add(n.key)
    else if (n.kind === 'group' && Array.isArray(n.children)) collectItemKeys(n.children, set)
  }
  return set
}

function findGroupById(nodes, id) {
  for (const n of nodes) {
    if (n.kind === 'group' && n.id === id) return n
  }
  return null
}

// 合并 catalog × 存储结构 → 渲染树（纯函数）。
// 规则：解析→裁剪陈旧项→注入缺失项(含受保护项恢复)→受保护强制 enabled→
//       enabled 过滤→角色过滤→整组关闭/空分组隐藏。
export function composeMenu(layoutRaw, ctx = {}) {
  const isAdmin = !!ctx.isAdmin
  const isLightweight = !isAdmin && !!ctx.isLightweight

  const byKey = Object.create(null)
  for (const item of menuCatalog) byKey[item.key] = item

  const parsed = parseLayout(layoutRaw)
  const working = clone(parsed.nodes)

  // 注入 catalog 中存在但树里缺失的项（含受保护项缺失时的恢复）
  const referenced = collectItemKeys(working, new Set())
  for (const item of menuCatalog) {
    if (!referenced.has(item.key)) {
      const node = { kind: 'item', key: item.key, enabled: true }
      const group = item.defaultGroup ? findGroupById(working, item.defaultGroup) : null
      if (group && Array.isArray(group.children)) group.children.push(node)
      else working.push(node)
    }
  }

  const roleVisible = (item) => {
    if (isAdmin) return true
    if (isLightweight) return !item.lightweightHidden
    return !item.adminOnly // 弹性云非管理员
  }

  const resolveMeta = (item) => {
    const useAlt = !isAdmin && !isLightweight && item.alt
    return {
      key: item.key,
      title: useAlt ? item.alt.title : item.title,
      icon: useAlt ? item.alt.icon : item.icon,
      route: item.route
    }
  }

  const composeNodes = (nodes) => {
    const out = []
    for (const node of nodes) {
      if (node.kind === 'item') {
        const item = byKey[node.key]
        if (!item) continue // 裁剪陈旧项
        const enabled = item.protected ? true : node.enabled !== false // 受保护强制可见
        if (!enabled) continue
        if (!roleVisible(item)) continue
        out.push({ type: 'item', ...resolveMeta(item) })
      } else if (node.kind === 'group') {
        if (node.enabled === false) continue // 整组关闭则隐藏
        const children = composeNodes(node.children || [])
        if (children.length === 0) continue // 空分组隐藏
        out.push({
          type: 'group',
          id: node.id,
          title: (node.title && String(node.title).trim()) || '未命名分组',
          icon: node.icon || 'folder',
          children
        })
      }
    }
    return out
  }

  return composeNodes(working)
}
