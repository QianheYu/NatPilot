// Formatting and status helpers shared by every view.

export function fmtTime(value, withSeconds = false) {
  if (!value) return '—'
  const d = typeof value === 'string' || typeof value === 'number' ? new Date(value) : value
  if (!(d instanceof Date) || Number.isNaN(d.getTime()) || d.getFullYear() < 1971) return '—'
  const p = (n) => String(n).padStart(2, '0')
  const base = `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
  return withSeconds ? `${base}:${p(d.getSeconds())}` : base
}

export function fmtRelative(value) {
  if (!value) return '—'
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return '—'
  const diff = Date.now() - d.getTime()
  const abs = Math.abs(diff)
  const future = diff < 0
  const units = [
    [1000, '秒'],
    [60 * 1000, '分钟'],
    [3600 * 1000, '小时'],
    [86400 * 1000, '天'],
  ]
  let out = '刚刚'
  if (abs < 1000) return '刚刚'
  if (abs < 60 * 1000) out = `${Math.round(abs / 1000)} 秒`
  else if (abs < 3600 * 1000) out = `${Math.round(abs / 60000)} 分钟`
  else if (abs < 86400 * 1000) out = `${Math.round(abs / 3600000)} 小时`
  else out = `${Math.round(abs / 86400000)} 天`
  void units
  return future ? `${out}后` : `${out}前`
}

export function fmtUptime(seconds) {
  if (!seconds && seconds !== 0) return '—'
  const s = Math.max(0, Math.floor(seconds))
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = s % 60
  if (d > 0) return `${d}天 ${h}小时`
  if (h > 0) return `${h}小时 ${m}分`
  if (m > 0) return `${m}分 ${sec}秒`
  return `${sec}秒`
}

export function fmtLease(ms) {
  if (!ms || ms <= 0) return '—'
  const seconds = Math.floor(ms / 1000)
  return fmtUptime(seconds)
}

export function fmtPorts(count) {
  if (!count && count !== 0) return '—'
  return count.toLocaleString()
}

export function truncate(s, n = 40) {
  if (!s) return ''
  return s.length <= n ? s : `${s.slice(0, n - 1)}…`
}

export function hostPort(host, port) {
  if (!host) return port ? `:${port}` : '—'
  return `${host}:${port}`
}

// Mapping status -> badge kind + label.
const STATUS_MAP = {
  active: { kind: 'ok', label: '已开启', dot: true },
  mapped: { kind: 'ok', label: '已开启' },
  pending: { kind: 'info', label: '等待中' },
  failed: { kind: 'danger', label: '失败' },
  conflict: { kind: 'warn', label: '端口冲突' },
  disabled: { kind: 'default', label: '已停用' },
  removed: { kind: 'default', label: '已移除' },
  unmapped: { kind: 'default', label: '未映射' },
  source_gone: { kind: 'warn', label: '源已删除' },
}

export function statusInfo(status) {
  return STATUS_MAP[status] || { kind: 'default', label: status || '未知' }
}

// Discovered-port lifecycle status.
const PORT_STATUS_MAP = {
  added: { kind: 'ok', label: '已添加' },
  open: { kind: 'info', label: '已开启' },
  not_added: { kind: 'accent', label: '未添加' },
  closed: { kind: 'default', label: '已关闭' },
  source_gone: { kind: 'warn', label: '源已删除' },
  ignored: { kind: 'default', label: '已忽略' },
}

export function portStatusInfo(status) {
  return PORT_STATUS_MAP[status] || { kind: 'default', label: status || '未知' }
}

export function scanStatusInfo(status) {
  switch (status) {
    case 'done':
      return { kind: 'ok', label: '已完成' }
    case 'running':
      return { kind: 'info', label: '进行中' }
    case 'failed':
      return { kind: 'danger', label: '失败' }
    case 'canceled':
      return { kind: 'warn', label: '已取消' }
    default:
      return { kind: 'default', label: status || '未知' }
  }
}

export function gatewayKindInfo(kind) {
  switch (kind) {
    case 'igd2':
      return { label: 'UPnP IGD v2', kind: 'accent', rank: 0 }
    case 'igd1':
      return { label: 'UPnP IGD v1', kind: 'info', rank: 1 }
    case 'pcp':
      return { label: 'PCP', kind: 'purple', rank: 2 }
    case 'natpmp':
      return { label: 'NAT-PMP', kind: 'warn', rank: 3 }
    default:
      return { label: kind || '未知', kind: 'default', rank: 9 }
  }
}

export function protocolTag(protocol) {
  const p = (protocol || '').toLowerCase()
  if (p === 'udp') return { label: 'UDP', kind: 'purple' }
  if (p === 'any') return { label: 'ANY', kind: 'warn' }
  return { label: 'TCP', kind: 'info' }
}

export function ruleState(rule) {
  if (!rule) return { kind: 'default', label: '未知' }
  if (!rule.enabled) return { kind: 'default', label: '已停用' }
  const st = rule.mapping_status || rule.status || 'unmapped'
  return statusInfo(st)
}

export function shortProvider(key) {
  if (!key) return '—'
  const i = key.indexOf('|')
  const kind = i > 0 ? key.slice(0, i) : key
  return gatewayKindInfo(kind).label
}

export function parsePortSpec(spec) {
  if (!spec) return []
  const out = []
  for (const part of String(spec).split(',')) {
    const p = part.trim()
    if (!p) continue
    if (p.includes('-')) {
      const [a, b] = p.split('-').map((x) => parseInt(x, 10))
      if (!Number.isNaN(a) && !Number.isNaN(b)) out.push([Math.min(a, b), Math.max(a, b)])
    } else {
      const n = parseInt(p, 10)
      if (!Number.isNaN(n)) out.push([n, n])
    }
  }
  return out
}

export function specPortCount(spec) {
  return parsePortSpec(spec).reduce((acc, [a, b]) => acc + (b - a + 1), 0)
}

export function classNames(...args) {
  return args.filter(Boolean).join(' ')
}

export function downloadJSON(obj, filename) {
  const blob = new Blob([JSON.stringify(obj, null, 2)], { type: 'application/json' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  URL.revokeObjectURL(url)
}

export function copyText(text) {
  if (navigator.clipboard && window.isSecureContext) {
    return navigator.clipboard.writeText(text)
  }
  const ta = document.createElement('textarea')
  ta.value = text
  ta.style.position = 'fixed'
  ta.style.opacity = '0'
  document.body.appendChild(ta)
  ta.select()
  let ok = false
  try {
    ok = document.execCommand('copy')
  } catch {
    ok = false
  }
  document.body.removeChild(ta)
  return ok ? Promise.resolve() : Promise.reject(new Error('copy failed'))
}
