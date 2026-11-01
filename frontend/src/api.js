// Centralised API client. Every call goes through request() so auth failures,
// error toasts and the profile header are handled in one place.

const PROFILE_HEADER = 'X-Hermes-Profile'

let onUnauthorized = null
export function setUnauthorizedHandler(fn) {
  onUnauthorized = fn
}

export class ApiError extends Error {
  constructor(message, status, payload) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.payload = payload
  }
}

async function request(method, path, { body, query, headers, raw } = {}) {
  const url = new URL(path, window.location.origin)
  if (query) {
    for (const [k, v] of Object.entries(query)) {
      if (v === undefined || v === null || v === '') continue
      if (Array.isArray(v)) {
        v.forEach((item) => url.searchParams.append(k, item))
      } else {
        url.searchParams.set(k, String(v))
      }
    }
  }
  const init = {
    method,
    credentials: 'include',
    headers: {
      Accept: raw ? '*/*' : 'application/json',
      [PROFILE_HEADER]: 'dev',
      ...(headers || {}),
    },
  }
  if (body !== undefined) {
    init.headers['Content-Type'] = 'application/json'
    init.body = JSON.stringify(body)
  }
  let resp
  try {
    resp = await fetch(url.toString(), init)
  } catch (err) {
    throw new ApiError('网络请求失败：无法连接后端服务', 0, null)
  }
  if (resp.status === 401) {
    if (onUnauthorized) onUnauthorized()
    throw new ApiError('需要登录', 401, null)
  }
  if (raw) {
    if (!resp.ok) {
      throw new ApiError(`请求失败 (${resp.status})`, resp.status, null)
    }
    return resp
  }
  const text = await resp.text()
  let payload = null
  if (text) {
    try {
      payload = JSON.parse(text)
    } catch {
      payload = { message: text }
    }
  }
  if (!resp.ok) {
    const msg =
      (payload && (payload.error || payload.message)) || `请求失败 (${resp.status})`
    throw new ApiError(msg, resp.status, payload)
  }
  return payload
}

const get = (p, query) => request('GET', p, { query })
const post = (p, body, query) => request('POST', p, { body, query })
const put = (p, body) => request('PUT', p, { body })
const patch = (p, body) => request('PATCH', p, { body })
const del = (p, query) => request('DELETE', p, { query })

export const api = {
  // meta
  meta: () => get('/api/meta'),
  health: () => get('/api/health'),
  me: () => get('/api/auth/me'),
  login: (username, password) => post('/api/auth/login', { username, password }),
  logout: () => post('/api/auth/logout', {}),

  // dashboard
  stats: () => get('/api/stats'),
  dashboard: () => get('/api/dashboard'),

  // rules
  rules: (query) => get('/api/rules', query),
  rule: (id) => get(`/api/rules/${id}`),
  createRule: (body) => post('/api/rules', body),
  updateRule: (id, body) => put(`/api/rules/${id}`, body),
  patchRule: (id, body) => patch(`/api/rules/${id}`, body),
  deleteRule: (id) => del(`/api/rules/${id}`),
  toggleRule: (id, enabled) => post(`/api/rules/${id}/toggle`, { enabled }),
  applyRule: (id) => post(`/api/rules/${id}/apply`, {}),
  ruleMapping: (id) => get(`/api/rules/${id}/mapping`),
  bulkRules: (body) => post('/api/rules/bulk', body),

  // ports
  ports: (query) => get('/api/ports', query),
  portAction: (id, body) => post(`/api/ports/${id}/action`, body),
  addPorts: (body) => post('/api/ports/add', body),
  deletePort: (id) => del(`/api/ports/${id}`),
  patchPort: (id, body) => patch(`/api/ports/${id}`, body),
  bulkPorts: (body) => post('/api/ports/bulk', body),
  promotePorts: (body) => post('/api/ports/promote', body),

  // scans
  scans: (query) => get('/api/scans', query),
  scan: (id) => get(`/api/scans/${id}`),
  startScan: (body) => post('/api/scans', body),
  cancelScan: (id) => post(`/api/scans/${id}/cancel`, {}),
  deleteScan: (id) => del(`/api/scans/${id}`),
  scanResults: (id, query) => get(`/api/scans/${id}/results`, query),
  previewScan: (body) => post('/api/scans/preview', body),

  // policies
  policies: (query) => get('/api/policies', query),
  createPolicy: (body) => post('/api/policies', body),
  updatePolicy: (id, body) => put(`/api/policies/${id}`, body),
  deletePolicy: (id) => del(`/api/policies/${id}`),

  // monitor
  monitor: () => get('/api/monitor'),
  startMonitor: () => post('/api/monitor/start', {}),
  stopMonitor: () => post('/api/monitor/stop', {}),

  // gateways
  gateways: () => get('/api/gateways'),
  discover: () => post('/api/gateways/discover', {}),
  preferGateway: (id) => post(`/api/gateways/${id}/preferred`, {}),
  enableGateway: (id, enabled) => post(`/api/gateways/${id}/enable`, { enabled }),
  deleteGateway: (id) => del(`/api/gateways/${id}`),
  gatewayMappings: () => get('/api/gateways/mappings'),
  externalIP: () => get('/api/external-ip'),
  reconcile: () => post('/api/mapper/reconcile', {}),
  cleanup: () => post('/api/mapper/cleanup', {}),

  // hosts & network
  hosts: (query) => get('/api/hosts', query),
  hostScan: (body) => post('/api/hosts/scan', body || {}),
  deleteHost: (id) => del(`/api/hosts/${id}`),
  // 主机名/备注/标签都挂在主机记录（即 MAC）上，换 IP 不会丢。
  patchHost: (id, body) => patch(`/api/hosts/${id}`, body),
  // Per-host monitoring: policies bound to the host's MAC (DHCP-safe).
  hostPolicies: (id) => get(`/api/hosts/${id}/policies`),
  createHostPolicy: (id, body) => post(`/api/hosts/${id}/policies`, body),
  interfaces: () => get('/api/network/interfaces'),

  // schedules
  schedules: (query) => get('/api/schedules', query),
  createSchedule: (body) => post('/api/schedules', body),
  updateSchedule: (id, body) => put(`/api/schedules/${id}`, body),
  deleteSchedule: (id) => del(`/api/schedules/${id}`),

  // system
  systemInfo: () => get('/api/system/info'),
  settings: () => get('/api/settings'),
  putSettings: (body) => put('/api/settings', body),
  patchSettings: (body) => patch('/api/settings', body),
  events: (query) => get('/api/events', query),
  markEventsRead: (ids) => post('/api/events/read', { ids }),
  clearEvents: (keep) => del('/api/events', { keep }),
  audit: (query) => get('/api/audit', query),
  exportURL: (format) => `/api/export?format=${format || 'json'}`,

  // users
  users: (query) => get('/api/users', query),
  createUser: (body) => post('/api/users', body),
  updateUser: (id, body) => put(`/api/users/${id}`, body),
  deleteUser: (id) => del(`/api/users/${id}`),
  userToken: (id) => post(`/api/users/${id}/token`, {}),
  changePassword: (body) => post('/api/account/password', body),
}

export default api
