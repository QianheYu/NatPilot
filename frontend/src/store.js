import { reactive, readonly } from 'vue'

// A tiny reactive store (no Pinia dependency) that holds session identity,
// UI preferences and shared transient state such as the toast queue.

const state = reactive({
  identity: { authenticated: false, username: '', name: '', role: 'admin' },
  meta: { version: '', mode: 'auto', auth_enabled: false, auth_required: false, protocols: [] },
  ready: false,
  theme: localStorage.getItem('natpilot-theme') || 'dark',
  toasts: [],
  sidebarCollapsed: localStorage.getItem('natpilot-sidebar') === '1',
})

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
  localStorage.setItem('natpilot-theme', theme)
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
    setIdentity,
    clearIdentity,
    isAdmin,
    canWrite,
  }
}
