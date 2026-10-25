<script setup>
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import api, { setUnauthorizedHandler } from './api'
import { store, applyTheme, toggleTheme, dismissToast, toastWarn } from './store'
import { useLive } from './composables/useLive'
import { visibleModules, flatPages } from './nav'
import AppSidebar from './layout/AppSidebar.vue'
import AppTopbar from './layout/AppTopbar.vue'
import CommandPalette from './layout/CommandPalette.vue'
import Modal from './components/Modal.vue'
import ChangePassword from './components/ChangePassword.vue'

const route = useRoute()
const router = useRouter()

const booting = ref(true)
const collapsed = ref(store.sidebarCollapsed)
const mobileOpen = ref(false)
const paletteOpen = ref(false)
const pwOpen = ref(false)
const unreadPorts = ref(0)
const unreadEvents = ref(0)
const notifEvents = ref([])
const monitorActive = ref(false)

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

const admin = computed(() => store.identity.role === 'admin' || !store.identity.authenticated)
const navModules = computed(() => visibleModules(admin.value))
const pages = computed(() => flatPages(admin.value))
const isLogin = computed(() => route.name === 'login')
const pageTitle = computed(() => route.meta.title || 'NatPilot')
const pageModule = computed(() => route.meta.group || '')

const badges = computed(() => {
  const out = {}
  if (unreadPorts.value > 0) out.ports = unreadPorts.value
  if (unreadEvents.value > 0) out.events = unreadEvents.value
  return out
})

function toggleSidebar() {
  if (window.innerWidth <= 860) {
    mobileOpen.value = !mobileOpen.value
    return
  }
  collapsed.value = !collapsed.value
  store.sidebarCollapsed = collapsed.value
  localStorage.setItem('natpilot-sidebar', collapsed.value ? '1' : '0')
}

function onPick(page) {
  router.push({ name: page.name })
}

function onKey(e) {
  const key = (e.key || '').toLowerCase()
  if ((e.ctrlKey || e.metaKey) && key === 'k') {
    e.preventDefault()
    paletteOpen.value = !paletteOpen.value
    return
  }
  if (key === 'escape') {
    paletteOpen.value = false
    mobileOpen.value = false
  }
}

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
      store.identity =
        me.identity || { authenticated: false, username: 'local', name: '本地操作员', role: 'admin' }
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
      api.events({ limit: 8 }),
      api.monitor(),
    ])
    unreadPorts.value = ports.total || 0
    notifEvents.value = events.events || []
    unreadEvents.value = events.unread || 0
    monitorActive.value = !!(monitor.monitor && monitor.monitor.active)
  } catch {
    /* badges and notifications are best-effort */
  }
}

async function markAllRead() {
  try {
    await api.markEventsRead([])
  } catch {
    /* ignore */
  }
  refreshBadges()
}

async function doLogout() {
  try {
    await api.logout()
  } catch {
    /* ignore */
  }
  store.identity = { authenticated: false, username: '', name: '', role: 'viewer' }
  router.replace({ name: 'login' })
}

watch(
  () => route.fullPath,
  () => {
    mobileOpen.value = false
    paletteOpen.value = false
    refreshBadges()
  },
)

watch(
  () => route.meta.title,
  (t) => {
    document.title = t ? `NatPilot · ${t}` : 'NatPilot'
  },
  { immediate: true },
)

onMounted(async () => {
  window.addEventListener('keydown', onKey)
  await boot()
  refreshBadges()
})

onBeforeUnmount(() => window.removeEventListener('keydown', onKey))

defineExpose({ refreshBadges })
</script>

<template>
  <div v-if="booting" class="login-wrap">
    <div style="text-align: center">
      <div class="spin" style="font-size: 26px">◌</div>
      <div class="dim" style="margin-top: 10px">正在加载 NatPilot…</div>
    </div>
  </div>

  <router-view v-else-if="isLogin" />

  <div v-else class="app-shell" :class="{ 'is-collapsed': collapsed, 'mobile-open': mobileOpen }">
    <AppSidebar
      :modules="navModules"
      :collapsed="collapsed"
      :version="store.meta.version"
      :mode="store.meta.mode"
      :live="live.connected.value"
      :badges="badges"
    />

    <AppTopbar
      :title="pageTitle"
      :module="pageModule"
      :collapsed="collapsed"
      :unread="unreadEvents"
      :events="notifEvents"
      :monitor-active="monitorActive"
      :live="live.connected.value"
      :theme="store.theme"
      :auth-enabled="!!store.meta.auth_enabled"
      :identity="store.identity"
      @toggle-sidebar="toggleSidebar"
      @open-search="paletteOpen = true"
      @toggle-theme="toggleTheme()"
      @mark-read="markAllRead"
      @goto-events="router.push({ name: 'events' })"
      @change-password="pwOpen = true"
      @logout="doLogout"
    />

    <main class="app-content">
      <router-view v-slot="{ Component }">
        <component :is="Component" :key="route.fullPath" />
      </router-view>
    </main>
  </div>

  <CommandPalette :open="paletteOpen" :pages="pages" @close="paletteOpen = false" @pick="onPick" />

  <Modal v-if="pwOpen" title="修改密码" @close="pwOpen = false">
    <ChangePassword />
  </Modal>

  <div class="toasts">
    <div v-for="t in store.toasts" :key="t.id" class="toast" :class="t.kind" @click="dismissToast(t.id)">
      <div v-if="t.title" class="t-title">{{ t.title }}</div>
      <div>{{ t.message }}</div>
    </div>
  </div>
</template>
