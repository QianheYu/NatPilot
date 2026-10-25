import { reactive, readonly } from 'vue'

// A tiny reactive store (no Pinia dependency) that holds session identity,
// UI preferences and shared transient state such as the toast queue.

// THEME_KEY is versioned: the light default replaced the old dark one, and every
// earlier run had already written "dark" into the unversioned key, so reading
// that key back would keep everybody on the old default forever. A new key makes
// the new default take effect exactly once while still remembering an explicit
// choice made afterwards.
const THEME_KEY = 'natpilot-theme-v2'
export const THEME_STORAGE_KEY = THEME_KEY

const state = reactive({
  identity: { authenticated: false, username: '', name: '', role: 'admin' },
  meta: { version: '', mode: 'auto', auth_enabled: false, auth_required: false, protocols: [] },
  ready: false,
  // Light is the default look; a stored preference still wins, so anyone who
  // already switched to dark keeps it.
  theme: localStorage.getItem(THEME_KEY) || 'light',
  toasts: [],
  sidebarCollapsed: localStorage.getItem('natpilot-sidebar') === '1',
  // eventsRevision is bumped whenever events are marked read/cleared. Both the
  // topbar notification bell and the 日志与审计 page watch it, so marking read in
  // one place is reflected in the other instead of them drifting apart.
  eventsRevision: 0,
})

// bumpEventsRevision tells every viewer that the read state changed.
export function bumpEventsRevision() {
  state.eventsRevision += 1
}

let toastSeq = 0

export function pushToast(message, kind = 'info', title = '', timeout = 4200) {
  const id = ++toastSeq
  state.toasts.push({ id, message, kind, title })
  if (timeout > 0) {
    setTimeout(() => dismissToast(id), timeout)
  }
  return id
}

export function dismissToast(id) {
  const i = state.toasts.findIndex((t) => t.id === id)
  if (i >= 0) state.toasts.splice(i, 1)
}

export function toastOk(message, title = '成功') {
  return pushToast(message, 'ok', title)
}
export function toastErr(message, title = '出错了') {
  return pushToast(message, 'error', title, 7000)
}
export function toastWarn(message, title = '注意') {
  return pushToast(message, 'warn', title)
}

export function applyTheme(theme) {
  state.theme = theme
  localStorage.setItem(THEME_KEY, theme)
  document.documentElement.setAttribute('data-theme', theme)
}

export function toggleTheme() {
  applyTheme(state.theme === 'dark' ? 'light' : 'dark')
}

export function setIdentity(identity, meta) {
  if (identity) state.identity = { ...state.identity, ...identity }
  if (meta) state.meta = { ...state.meta, ...meta }
}

export function clearIdentity() {
  state.identity = { authenticated: false, username: '', name: '', role: 'viewer' }
}

export function isAdmin() {
  return state.identity.role === 'admin' || !state.identity.authenticated
}

export function canWrite() {
  return state.identity.role !== 'viewer'
}

export const store = state

export function useStore() {
  return {
    state: readonly(state),
    raw: state,
    pushToast,
    dismissToast,
    toastOk,
    toastErr,
    toastWarn,
    applyTheme,
    toggleTheme,
    bumpEventsRevision,
    setIdentity,
    clearIdentity,
    isAdmin,
    canWrite,
  }
}
