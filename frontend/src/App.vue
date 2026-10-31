<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import api, { setUnauthorizedHandler } from './api'
import { store, applyTheme, toggleTheme, dismissToast, toastWarn, bumpEventsRevision } from './store'
import { useLive } from './composables/useLive'
import { useResponsiveTables } from './composables/useResponsiveTables'
import { visibleModules, flatPages } from './nav'
import AppSidebar from './layout/AppSidebar.vue'
import AppTopbar from './layout/AppTopbar.vue'
import CommandPalette from './layout/CommandPalette.vue'
import Modal from './components/Modal.vue'
import ChangePassword from './components/ChangePassword.vue'
import RowDetailModal from './components/RowDetailModal.vue'
import { installRowDetail } from './composables/useRowDetail'

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

// Holds <router-view>; tables inside it are re-labelled for the phone card layout.
const contentEl = ref(null)
useResponsiveTables(contentEl)

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
// 免登录(桌面)模式没有独立的用户体系，只有内置管理员。
const userManagement = computed(() => store.meta.user_management !== false)
// 桌面版才有 Electron 启动器（preload 注入 window.launcher）
const isDesktop = typeof window !== 'undefined' && !!window.launcher
const navModules = computed(() => visibleModules(admin.value, userManagement.value, isDesktop))
const pages = computed(() => flatPages(admin.value, userManagement.value, isDesktop))
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
    // Bumping the shared revision refreshes the bell here and the 日志与审计
    // list at the same time (see the watcher below), so the two stay in sync.
    bumpEventsRevision()
  } catch {
    /* ignore */
  }
  refreshBadges()
}

// Any page that marks events read (all or a single one) bumps the revision;
// the bell and its popover follow immediately.
watch(() => store.eventsRevision, () => refreshBadges())

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

// Opening the drawer locks the page behind it: on a phone the sidebar would
// otherwise scroll the content underneath while the finger drags.
watch(mobileOpen, (open) => {
  if (typeof document === 'undefined') return
  document.body.classList.toggle('drawer-open', open)
})

// The drawer only makes sense below the 860px breakpoint; growing the window
// back past it must close it or the page stays locked.
function onResize() {
  if (window.innerWidth > 860 && mobileOpen.value) mobileOpen.value = false
}

watch(
  () => route.meta.title,
  (t) => {
    document.title = t ? `NatPilot · ${t}` : 'NatPilot'
  },
  { immediate: true },
)

let disposeRowDetail = null

onMounted(async () => {
  disposeRowDetail = installRowDetail(document.body)

  window.addEventListener('keydown', onKey)
  window.addEventListener('resize', onResize)
  await boot()
  await nextTick()
  refreshBadges()
})

onBeforeUnmount(() => {
  if (disposeRowDetail) disposeRowDetail()

  window.removeEventListener('keydown', onKey)
  window.removeEventListener('resize', onResize)
  document.body.classList.remove('drawer-open')
})

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

  <div
    v-else
    class="app-shell"
    :class="{ 'is-collapsed': collapsed, 'mobile-open': mobileOpen }"
  >
    <AppSidebar
      :class="{ 'is-open': mobileOpen }"
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

    <main ref="contentEl" class="app-content">
      <router-view v-slot="{ Component }">
        <component :is="Component" :key="route.fullPath" />
      </router-view>
    </main>
  </div>

  <!-- Tapping outside the drawer closes it. Rendered outside .app-shell so the
       fixed overlay never takes part in the shell grid. -->
  <div v-if="mobileOpen" class="drawer-scrim" @click="mobileOpen = false" />

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
  <RowDetailModal />
</template>
