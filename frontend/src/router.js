import { createRouter, createWebHashHistory } from 'vue-router'

// Hash history keeps deep links working when the SPA is served from the Go
// binary at any base path and when Electron loads it from file://.
const routes = [
  { path: '/', redirect: '/dashboard' },
  { path: '/login', name: 'login', component: () => import('./views/Login.vue'), meta: { public: true } },
  { path: '/dashboard', name: 'dashboard', component: () => import('./views/Dashboard.vue'), meta: { title: '总览', group: '概览', icon: '◈' } },
  { path: '/rules', name: 'rules', component: () => import('./views/Rules.vue'), meta: { title: '端口映射管理', group: '映射', icon: '⇄' } },
  { path: '/ports', name: 'ports', component: () => import('./views/Ports.vue'), meta: { title: '端口发现', group: '映射', icon: '⌗' } },
  { path: '/scans', name: 'scans', component: () => import('./views/Scans.vue'), meta: { title: '扫描任务', group: '映射', icon: '◎' } },
  { path: '/policies', name: 'policies', component: () => import('./views/Policies.vue'), meta: { title: '策略与监控', group: '映射', icon: '⚑' } },
  { path: '/gateways', name: 'gateways', component: () => import('./views/Gateways.vue'), meta: { title: '网关与协议', group: '网络', icon: '⬡' } },
  { path: '/hosts', name: 'hosts', component: () => import('./views/Hosts.vue'), meta: { title: '主机清单', group: '网络', icon: '▤' } },
  { path: '/schedules', name: 'schedules', component: () => import('./views/Schedules.vue'), meta: { title: '计划任务', group: '网络', icon: '◷' } },
  { path: '/events', name: 'events', component: () => import('./views/Events.vue'), meta: { title: '日志与审计', group: '系统', icon: '≡' } },
  { path: '/swagger', name: 'swagger', component: () => import('./views/SwaggerView.vue'), meta: { title: 'API 浏览器', group: '系统', icon: '⟐', admin: true } },
  { path: '/settings', name: 'settings', component: () => import('./views/Settings.vue'), meta: { title: '系统设置', group: '系统', icon: '⚙', admin: true } },
  { path: '/users', name: 'users', component: () => import('./views/Users.vue'), meta: { title: '用户与权限', group: '系统', icon: '☺', admin: true } },
  { path: '/:pathMatch(.*)*', redirect: '/dashboard' },
]

const router = createRouter({
  history: createWebHashHistory(),
  routes,
})

export default router
