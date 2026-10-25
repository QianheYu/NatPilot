import { createRouter, createWebHashHistory } from 'vue-router'
import { modules } from './nav'

// Pages are declared once in nav.js; the route table is derived from it.
const pageRoutes = modules.flatMap((m) =>
  m.items.map((item) => ({
    path: item.path,
    name: item.name,
    component: item.component,
    meta: {
      title: item.title,
      desc: item.desc || '',
      icon: item.icon,
      group: m.title,
      moduleKey: m.key,
      admin: !!item.admin,
    },
  })),
)

// Hash history keeps deep links working when the SPA is served from the Go
// binary at any base path and when Electron loads it from file://.
const routes = [
  { path: '/', redirect: '/dashboard' },
  {
    path: '/login',
    name: 'login',
    component: () => import('./views/Login.vue'),
    meta: { public: true, title: '登录' },
  },
  ...pageRoutes,
  { path: '/:pathMatch(.*)*', redirect: '/dashboard' },
]

const router = createRouter({
  history: createWebHashHistory(),
  routes,
})

export default router
