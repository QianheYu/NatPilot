<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import api from '../api'
import Badge from '../components/Badge.vue'
import { toastErr } from '../store'

const filter = ref('')
const open = reactive({})
const results = reactive({})
const busy = reactive({})
const bodies = reactive({})
const query = reactive({})

const groups = ref([])

function groupOf(path) {
  const seg = path.replace('/api/', '').split('/')[0] || 'root'
  return seg
}

function buildGroups() {
  // The endpoint catalogue mirrors the router; it is descriptive metadata used
  // for the built-in API explorer. Every entry can be executed in place.
  const endpoints = [
    { method: 'GET', path: '/api/health' },
    { method: 'GET', path: '/api/meta' },
    { method: 'GET', path: '/api/auth/me' },
    { method: 'POST', path: '/api/auth/login' },
    { method: 'GET', path: '/api/stats' },
    { method: 'GET', path: '/api/dashboard' },
    { method: 'GET', path: '/api/rules' },
    { method: 'POST', path: '/api/rules', body: { host: '127.0.0.1', port: 8080, protocol: 'tcp', enabled: true } },
    { method: 'GET', path: '/api/rules/{id}' },
    { method: 'PUT', path: '/api/rules/{id}', body: { port: 8080 } },
    { method: 'PATCH', path: '/api/rules/{id}', body: { enabled: true } },
    { method: 'DELETE', path: '/api/rules/{id}' },
    { method: 'POST', path: '/api/rules/bulk', body: { ids: [], action: 'enable' } },
    { method: 'POST', path: '/api/rules/{id}/toggle', body: { enabled: true } },
    { method: 'POST', path: '/api/rules/{id}/apply' },
    { method: 'GET', path: '/api/rules/{id}/mapping' },
    { method: 'GET', path: '/api/ports' },
    { method: 'POST', path: '/api/ports/add', body: { ids: [] } },
    { method: 'POST', path: '/api/ports/{id}/action', body: { decision: 'add' } },
    { method: 'PATCH', path: '/api/ports/{id}', body: { tags: [], note: '' } },
    { method: 'DELETE', path: '/api/ports/{id}' },
    { method: 'POST', path: '/api/ports/bulk', body: { ids: [], action: 'delete' } },
    { method: 'POST', path: '/api/ports/promote', body: { statuses: ['open'] } },
    { method: 'GET', path: '/api/scans' },
    { method: 'POST', path: '/api/scans', body: { target_kind: 'local', wait: true } },
    { method: 'GET', path: '/api/scans/{id}' },
    { method: 'DELETE', path: '/api/scans/{id}' },
    { method: 'POST', path: '/api/scans/{id}/cancel' },
    { method: 'GET', path: '/api/scans/{id}/results' },
    { method: 'POST', path: '/api/scans/preview', body: { target_kind: 'local' } },
    { method: 'GET', path: '/api/policies' },
    { method: 'POST', path: '/api/policies', body: { name: '', port_ranges: '', auto_action: 'watch' } },
    { method: 'PUT', path: '/api/policies/{id}' },
    { method: 'DELETE', path: '/api/policies/{id}' },
    { method: 'GET', path: '/api/monitor' },
    { method: 'POST', path: '/api/monitor/start' },
    { method: 'POST', path: '/api/monitor/stop' },
    { method: 'GET', path: '/api/gateways' },
    { method: 'POST', path: '/api/gateways/discover' },
    { method: 'POST', path: '/api/gateways/{id}/preferred' },
    { method: 'POST', path: '/api/gateways/{id}/enable', body: { enabled: true } },
    { method: 'DELETE', path: '/api/gateways/{id}' },
    { method: 'GET', path: '/api/gateways/mappings' },
    { method: 'GET', path: '/api/external-ip' },
    { method: 'POST', path: '/api/mapper/reconcile' },
    { method: 'POST', path: '/api/mapper/cleanup' },
    { method: 'GET', path: '/api/hosts' },
    { method: 'POST', path: '/api/hosts/scan' },
    { method: 'DELETE', path: '/api/hosts/{id}' },
    { method: 'GET', path: '/api/schedules' },
    { method: 'POST', path: '/api/schedules', body: { name: '', kind: 'scan', cron: '*/30 * * * *' } },
    { method: 'PUT', path: '/api/schedules/{id}' },
    { method: 'DELETE', path: '/api/schedules/{id}' },
    { method: 'GET', path: '/api/network/interfaces' },
    { method: 'GET', path: '/api/system/info' },
    { method: 'GET', path: '/api/settings' },
    { method: 'PATCH', path: '/api/settings' },
    { method: 'GET', path: '/api/events' },
    { method: 'POST', path: '/api/events/read', body: {} },
    { method: 'DELETE', path: '/api/events' },
    { method: 'GET', path: '/api/audit' },
    { method: 'GET', path: '/api/users' },
    { method: 'POST', path: '/api/users', body: { username: '', password: '', role: 'operator' } },
    { method: 'PUT', path: '/api/users/{id}' },
    { method: 'DELETE', path: '/api/users/{id}' },
    { method: 'POST', path: '/api/users/{id}/token' },
    { method: 'POST', path: '/api/account/password' },
    { method: 'GET', path: '/api/export' },
  ]
  const map = new Map()
  for (const e of endpoints) {
    const g = groupOf(e.path)
    if (!map.has(g)) map.set(g, [])
    map.get(g).push(e)
  }
  groups.value = [...map.entries()].map(([name, items]) => ({ name, items }))
}

const filtered = computed(() => {
  const q = filter.value.toLowerCase().trim()
  if (!q) return groups.value
  return groups.value
    .map((g) => ({ ...g, items: g.items.filter((e) => (e.path + ' ' + e.method).toLowerCase().includes(q)) }))
    .filter((g) => g.items.length)
})

function key(e) {
  return `${e.method} ${e.path}`
}

async function call(e) {
  const k = key(e)
  busy[k] = true
  let path = e.path
  // Fill {id} placeholders from the query map.
  path = path.replace(/\{id\}/g, query['{id}'] || query.id || '1')
  try {
    const method = e.method.toLowerCase()
    const opts = { method, headers: {} }
    const rawBody = bodies[k] !== undefined ? bodies[k] : e.body ? JSON.stringify(e.body, null, 2) : ''
    if (rawBody && (method === 'post' || method === 'put' || method === 'patch')) {
      opts.headers['Content-Type'] = 'application/json'
      opts.body = rawBody
    }
    const res = await fetch(path, { ...opts, credentials: 'include' })
    const text = await res.text()
    let parsed = text
    try {
      parsed = JSON.parse(text)
    } catch {
      /* keep raw */
    }
    results[k] = { status: res.status, ok: res.ok, body: parsed }
  } catch (err) {
    toastErr(err.message, '请求失败')
    results[k] = { status: 0, ok: false, body: String(err) }
  } finally {
    busy[k] = false
  }
}

onMounted(buildGroups)
void api
</script>

<template>
  <div>
    <div class="toolbar">
      <span class="dim small">内置 REST API 浏览器 · 需要管理员登录（或桌面免登录模式）· 请求会真实执行</span>
      <div class="spacer" style="flex: 1" />
      <input v-model="filter" class="input" style="max-width: 240px" placeholder="筛选接口，如 rules / scan" />
    </div>

    <div class="card" style="margin-bottom: 14px">
      <div class="card-body">
        <div class="row wrap" style="gap: 14px">
          <label class="field" style="margin: 0; min-width: 160px">
            <span>路径参数 {id}</span>
            <input v-model="query['{id}']" class="input" placeholder="例如 1" />
          </label>
          <div class="dim small" style="align-self: end; padding-bottom: 6px">
            认证：会话 Cookie（浏览器已登录时自动携带）。外部脚本可改用
            <code>Authorization: Bearer &lt;API 令牌&gt;</code>。
          </div>
        </div>
      </div>
    </div>

    <div v-for="g in filtered" :key="g.name" class="card" style="margin-bottom: 14px">
      <div class="card-head">
        <h3 class="mono">/api/{{ g.name }}</h3>
        <div class="spacer" style="flex: 1" />
        <Badge>{{ g.items.length }} 个接口</Badge>
      </div>
      <div class="card-body flush">
        <div v-for="e in g.items" :key="key(e)" style="border-bottom: 1px solid var(--border-soft); padding: 12px 16px">
          <div class="row" style="gap: 10px">
            <Badge
              :kind="e.method === 'GET' ? 'info' : e.method === 'DELETE' ? 'danger' : e.method === 'POST' ? 'ok' : 'warn'"
            >
              {{ e.method }}
            </Badge>
            <span class="mono">{{ e.path }}</span>
            <div class="spacer" style="flex: 1" />
            <button class="btn sm" @click="open[key(e)] = !open[key(e)]">
              {{ open[key(e)] ? '收起' : '试运行' }}
            </button>
            <button class="btn sm primary" :disabled="busy[key(e)]" @click="call(e)">
              <span v-if="busy[key(e)]" class="spin">◌</span>发送
            </button>
          </div>
          <div v-if="open[key(e)]" style="margin-top: 10px">
            <label v-if="e.body" class="field">
              <span>请求体 (JSON)</span>
              <textarea
                class="input mono"
                rows="5"
                :value="bodies[key(e)] !== undefined ? bodies[key(e)] : JSON.stringify(e.body, null, 2)"
                @input="bodies[key(e)] = $event.target.value"
              ></textarea>
            </label>
            <div v-else class="mute small">该接口无请求体。</div>
          </div>
          <div v-if="results[key(e)]" style="margin-top: 10px">
            <div class="row" style="gap: 8px; margin-bottom: 6px">
              <Badge :kind="results[key(e)].ok ? 'ok' : 'danger'">HTTP {{ results[key(e)].status }}</Badge>
            </div>
            <pre class="mono small" style="max-height: 260px; overflow: auto; background: var(--bg); border: 1px solid var(--border-soft); border-radius: 7px; padding: 10px; white-space: pre-wrap">{{
              typeof results[key(e)].body === 'string'
                ? results[key(e)].body
                : JSON.stringify(results[key(e)].body, null, 2)
            }}</pre>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
