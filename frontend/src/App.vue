<script setup>
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import api, { setUnauthorizedHandler } from './api'
import { store, useStore, applyTheme, toggleTheme } from './store'
import { useLive } from './composables/useLive'
import { fmtUptime } from './utils/format'

const route = useRoute()
const router = useRouter()
const { dismissToast, toastWarn } = useStore()

const booting = ref(true)
const navOpen = ref(false)
const monitorActive = ref(false)
const unreadCount = ref(0)
const live = useLive((ev) => {
  if (ev.type === 'changed') {
    refreshBadges()
  } else if (ev.type === 'log') {
    if (ev.data && (ev.data.level === 'warn' || ev.data.level === 'error')) {
      toastWarn(ev.data.message, ev.data.category || '系统')
    }
    refreshBadges()
  }
})

const groups = computed(() => {
  const map = new Map()
  for (const r of router.getRoutes()) {
    if (!r.meta || !r.meta.group || !r.name) continue
    if (r.meta.admin && store.identity.authenticated && store.identity.role !== 'admin') continue
    if (!map.has(r.meta.group)) map.set(r.meta.group, [])
    map.get(r.meta.group).push(r)
  }
  return [...map.entries()].map(([name, items]) => ({ name, items }))
})

const pageTitle = computed(() => route.meta.title || 'NatPilot')
const pageGroup = computed(() => route.meta.group || '')
const isLogin = computed(() => route.name === 'login')

async function boot() {
  applyTheme(store.theme)
  setUnauthorizedHandler(() => {
    store.identity = { authenticated: false, username: '', name: '', role: 'viewer' }
    if (route.name !== 'login') router.replace({ name: 'login' })
  })
  try {
    const meta = await api.meta()
    store.meta = { ...store.meta, ...meta }
    const me = await api.me()
    if (me.authenticated) {
      store.identity = me.identity
    } else if (me.auth_enabled) {
      store.identity = { authenticated: false, username: '', name: '', role: 'viewer' }
      if (route.name !== 'login') router.replace({ name: 'login' })
    } else {
      store.identity = me.identity || { authenticated: false, username: 'local', name: '本地操作员', role: 'admin' }
    }
    store.ready = true
  } catch (err) {
    store.meta.error = err.message
  } finally {
    booting.value = false
  }
}

async function refreshBadges() {
  if (!store.ready || isLogin.value) return
  try {
    const [ports, events, monitor] = await Promise.all([
      api.ports({ status: 'not_added,open', limit: 1 }),
      api.events({ limit: 1 }),
      api.monitor(),
    ])
    unreadCount.value = (ports.total || 0) + (events.unread || 0)
    monitorActive.value = !!(monitor.monitor && monitor.monitor.active)
  } catch {
    /* badges are best-effort */
  }
}

watch(
  () => route.fullPath,
  () => {
    navOpen.value = false
    refreshBadges()
  },
)

onMounted(async () => {
  await boot()
  refreshBadges()
  if (!isLogin.value) {
    try {
      const monitor = await api.monitor()
      monitorActive.value = !!(monitor.monitor && monitor.monitor.active)
    } catch {
      /* ignore */
    }
  }
})

async function doLogout() {
  try {
    await api.logout()
  } catch {
    /* ignore */
  }
  store.identity = { authenticated: false, username: '', name: '', role: 'viewer' }
  router.replace({ name: 'login' })
}

defineExpose({ refreshBadges })
void fmtUptime
</script>

<template>
  <div v-if="booting" class="login-wrap">
    <div style="text-align: center">
      <div class="spin" style="font-size: 26px">◌</div>
      <div class="dim" style="margin-top: 10px">正在加载 NatPilot…</div>
    </div>
  </div>

  <router-view v-else-if="isLogin" />

  <div v-else class="shell">
    <aside class="sidebar">
      <div class="brand">
        <div class="logo">NP</div>
        <div>
          <div class="title">NatPilot</div>
          <div class="sub">NAT Console</div>
        </div>
      </div>
      <nav class="nav">
        <template v-for="group in groups" :key="group.name">
          <div class="nav-group">
            <div class="nav-group-label">{{ group.name }}</div>
            <router-link
              v-for="item in group.items"
              :key="item.name"
              :to="{ name: item.name }"
              class="nav-item"
              :class="{ active: route.name === item.name }"
            >
              <span class="ico">{{ item.meta.icon }}</span>
              <span>{{ item.meta.title }}</span>
              <span
                v-if="item.name === 'ports' && unreadCount > 0"
                class="badge accent"
                >{{ unreadCount }}</span
              >
            </router-link>
          </div>
        </template>
      </nav>
      <div class="sidebar-foot">
        <div class="row" style="justify-content: space-between">
          <span>{{ store.meta.mode || 'auto' }} 模式</span>
          <a href="#" @click.prevent="toggleTheme()">{{ store.theme === 'dark' ? '☾' : '☀' }}</a>
        </div>
        <div class="small" style="margin-top: 6px">
          v{{ store.meta.version || '0.1.0' }} ·
          <span :style="{ color: live.connected.value ? 'var(--ok)' : 'var(--danger)' }">
            {{ live.connected.value ? '实时已连接' : '实时断开' }}
          </span>
        </div>
      </div>
    </aside>

    <div class="main">
      <header class="topbar">
        <div>
          <div class="crumb">{{ pageGroup }}</div>
          <h1>{{ pageTitle }}</h1>
        </div>
        <div class="spacer" />
        <span
          v-if="monitorActive"
          class="badge ok dot"
          title="自动监控正在运行"
          >自动监控中</span
        >
        <span v-if="store.meta.auth_enabled" class="badge" :class="store.identity.authenticated ? 'accent' : 'warn'">
          {{ store.identity.authenticated ? store.identity.username : '未登录' }}
        </span>
        <span v-else class="badge">免登录</span>
        <button v-if="store.meta.auth_enabled && store.identity.authenticated" class="btn ghost sm" @click="doLogout">
          退出
        </button>
      </header>
      <main class="content">
        <router-view v-slot="{ Component }">
          <component :is="Component" :key="route.fullPath" />
        </router-view>
      </main>
    </div>
  </div>

  <div class="toasts">
    <div v-for="t in store.toasts" :key="t.id" class="toast" :class="t.kind" @click="dismissToast(t.id)">
      <div v-if="t.title" class="t-title">{{ t.title }}</div>
      <div>{{ t.message }}</div>
    </div>
  </div>
</template>
