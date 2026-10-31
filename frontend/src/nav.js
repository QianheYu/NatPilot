// Single source of truth for the console navigation.
//
// The router, the sidebar, the breadcrumb and the command palette all read from
// this list, so a page can never show up in one place but be missing from
// another. Add a page here and it appears everywhere.

const page = (path, name, title, icon, loader, extra = {}) => ({
  path,
  name,
  title,
  icon,
  component: loader,
  ...extra,
})

export const modules = [
  {
    key: 'overview',
    title: '概览',
    items: [
      page('/dashboard', 'dashboard', '运行总览', '◈', () => import('./views/Dashboard.vue'), {
        desc: '映射状态、网关健康与实时指标',
      }),
    ],
  },
  {
    key: 'mapping',
    title: '端口映射',
    items: [
      page('/rules', 'rules', '映射规则', '⇄', () => import('./views/Rules.vue'), {
        desc: '需要映射的端口与目标地址',
      }),
      page('/ports', 'ports', '端口发现', '⌗', () => import('./views/Ports.vue'), {
        desc: '本机监听端口与候选映射',
      }),
    ],
  },
  {
    key: 'network',
    title: '网络资源',
    items: [
      page('/gateways', 'gateways', '网关与协议', '⬡', () => import('./views/Gateways.vue'), {
        desc: 'UPnP / PCP / NAT-PMP 网关',
      }),
      page('/hosts', 'hosts', '主机清单', '▤', () => import('./views/Hosts.vue'), {
        desc: '局域网内发现的主机',
      }),
    ],
  },
  {
    key: 'discovery',
    title: '自动发现',
    items: [
      page('/scans', 'scans', '扫描任务', '◎', () => import('./views/Scans.vue'), {
        desc: '端口扫描任务与结果',
      }),
      page('/policies', 'policies', '策略与监控', '⚑', () => import('./views/Policies.vue'), {
        desc: '自动映射策略与后台监控',
      }),
      page('/schedules', 'schedules', '计划任务', '◷', () => import('./views/Schedules.vue'), {
        desc: 'cron 定时扫描计划',
      }),
    ],
  },
  {
    key: 'security',
    title: '安全与审计',
    items: [
      page('/users', 'users', '用户与权限', '☺', () => import('./views/Users.vue'), {
        desc: '账号、角色与访问令牌',
        admin: true,
        // 免登录(桌面)模式只有内置管理员 -> 该页整体不可用
        userManagement: true,
      }),
      page('/events', 'events', '日志与审计', '≡', () => import('./views/Events.vue'), {
        desc: '事件流与操作审计记录',
      }),
    ],
  },
  {
    key: 'system',
    title: '系统',
    items: [
      page('/settings', 'settings', '系统设置', '⚙', () => import('./views/Settings.vue'), {
        desc: '配置项与运行时参数',
        admin: true,
      }),
      page('/swagger', 'swagger', 'API 浏览器', '⟐', () => import('./views/SwaggerView.vue'), {
        desc: 'REST 接口文档与调试',
        admin: true,
      }),
    ],
  },
]

// visibleModules drops the admin-only pages for non-administrators. It also
// drops a whole module once nothing inside it is visible.
//
// userManagement=false means the process runs as a single built-in administrator
// (desktop / 免登录), where user & permission management has no meaning.
export function visibleModules(isAdmin, userManagement = true) {
  return modules
    .map((m) => ({
      ...m,
      items: m.items.filter((i) => (isAdmin || !i.admin) && (userManagement || !i.userManagement)),
    }))
    .filter((m) => m.items.length > 0)
}

// flatPages is the searchable index used by the command palette.
export function flatPages(isAdmin, userManagement = true) {
  return visibleModules(isAdmin, userManagement).flatMap((m) =>
    m.items.map((i) => ({ ...i, module: m.title })),
  )
}
