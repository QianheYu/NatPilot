<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import api from '../api'
import Badge from '../components/Badge.vue'
import EmptyState from '../components/EmptyState.vue'
import Modal from '../components/Modal.vue'
import { toastOk, toastErr, canWrite } from '../store'
import { fmtRelative } from '../utils/format'

const loading = ref(true)
const hosts = ref([])
const interfaces = ref([])
const gateway = ref('')
const busy = ref(false)

// Policies bound to a host by MAC. Loaded once and grouped here instead of
// asking the server per host.
const policies = ref([])

const monitorHost = ref(null)
const saving = ref(false)
const form = reactive({
  name: '',
  port_ranges: '',
  protocol: 'any',
  auto_action: 'watch',
  priority: 0,
  note: '',
})

// normMAC mirrors the server-side normalisation: the MAC is the identity, so
// "AA-BB-.." and "aa:bb:.." must group together.
function normMAC(mac) {
  return String(mac || '').toLowerCase().replace(/[^0-9a-f]/g, '')
}

const policyCounts = computed(() => {
  const out = {}
  for (const p of policies.value) {
    if (!p.mac) continue
    const k = normMAC(p.mac)
    out[k] = (out[k] || 0) + 1
  }
  return out
})

// Policies of the host whose monitoring dialog is open. A host can have several:
// e.g. one that adds 22, another that only watches 80/443.
const openPolicies = computed(() => {
  if (!monitorHost.value || !monitorHost.value.mac) return []
  const key = normMAC(monitorHost.value.mac)
  return policies.value.filter((p) => p.mac && normMAC(p.mac) === key)
})

function policyCount(h) {
  if (!h.mac) return 0
  return policyCounts.value[normMAC(h.mac)] || 0
}

async function load() {
  loading.value = true
  try {
    const [h, n, p] = await Promise.all([
      api.hosts({ limit: 800 }),
      api.interfaces(),
      api.policies(),
    ])
    hosts.value = h.hosts || []
    interfaces.value = n.interfaces || []
    gateway.value = n.gateway || ''
    policies.value = p.policies || []
  } catch (err) {
    toastErr(err.message, '加载主机失败')
  } finally {
    loading.value = false
  }
}

async function discoverHosts() {
  busy.value = true
  try {
    const res = await api.hostScan()
    toastOk(`发现 ${res.discovered} 台主机`)
    load()
  } catch (err) {
    toastErr(err.message, '主机发现失败')
  } finally {
    busy.value = false
  }
}

async function removeHost(h) {
  try {
    await api.deleteHost(h.id)
    load()
  } catch (err) {
    toastErr(err.message, '删除失败')
  }
}

function openMonitor(h) {
  monitorHost.value = h
  Object.assign(form, { name: '', port_ranges: '', protocol: 'any', auto_action: 'watch', priority: 0, note: '' })
}

function closeMonitor() {
  monitorHost.value = null
}

// addPolicy() adds one more policy to the same host and keeps the dialog open,
// so a host can be given several rules in one go.
async function addPolicy() {
  if (!monitorHost.value) return
  saving.value = true
  try {
    await api.createHostPolicy(monitorHost.value.id, {
      name: form.name,
      port_ranges: form.port_ranges,
      protocol: form.protocol,
      auto_action: form.auto_action,
      auto_add: form.auto_action === 'add',
      priority: Number(form.priority) || 0,
      note: form.note,
    })
    toastOk('已添加监控策略')
    Object.assign(form, { name: '', port_ranges: '', protocol: 'any', auto_action: 'watch', priority: 0, note: '' })
    const p = await api.policies()
    policies.value = p.policies || []
  } catch (err) {
    toastErr(err.message, '添加失败')
  } finally {
    saving.value = false
  }
}

async function removePolicy(p) {
  try {
    await api.deletePolicy(p.id)
    toastOk('已删除策略')
    const res = await api.policies()
    policies.value = res.policies || []
  } catch (err) {
    toastErr(err.message, '删除失败')
  }
}

const actionLabel = (a) => ({ add: '自动添加映射', watch: '仅监控', ignore: '忽略', delete: '删除' })[a] || a

onMounted(load)
</script>

<template>
  <div>
    <div class="toolbar">
      <span class="dim small">
        共 {{ hosts.length }} 台主机 · 默认网关 {{ gateway || '未知' }} ·
        主机的 IP 可能随 DHCP 变动，监控按 MAC 绑定并自动跟随 IP
      </span>
      <div class="spacer" style="flex: 1" />
      <button class="btn" @click="load"><span v-if="loading" class="spin">◌</span>刷新</button>
      <button class="btn primary" :disabled="busy || !canWrite()" @click="discoverHosts">发现局域网主机</button>
    </div>

    <div class="grid cols-4" style="margin-bottom: 16px">
      <div v-for="i in interfaces" :key="i.name + i.ip" class="stat">
        <div class="label">{{ i.name }}</div>
        <div class="value" style="font-size: 18px">{{ i.ip }}</div>
        <div class="sub mono small">{{ i.network }}</div>
        <div class="sub">
          <span v-if="i.gateway">网关 {{ i.gateway }}</span>
          <span v-else-if="i.loopback" class="mute">回环</span>
        </div>
      </div>
    </div>

    <div class="card">
      <div class="card-body flush">
        <EmptyState v-if="!hosts.length && !loading" icon="▤" title="暂无主机记录" hint="点击“发现局域网主机”读取 ARP 邻居表" />
        <div v-else class="table-wrap">
          <table class="data">
            <thead>
              <tr>
                <th>IP</th>
                <th>MAC</th>
                <th>厂商</th>
                <th>主机名</th>
                <th>类型</th>
                <th>接口</th>
                <th>次数</th>
                <th>最近</th>
                <th>监控</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="h in hosts" :key="h.id">
                <td class="mono small">{{ h.ip }}</td>
                <td class="mono small mute">{{ h.mac || '—' }}</td>
                <td class="small">{{ h.vendor || '—' }}</td>
                <td class="small">{{ h.hostname || '—' }}</td>
                <td>
                  <Badge :kind="h.is_local ? 'accent' : h.kind === 'gateway' ? 'purple' : 'default'">
                    {{ h.is_local ? '本机' : h.kind || '对端' }}
                  </Badge>
                </td>
                <td class="small mute">{{ h.interface || '—' }}</td>
                <td class="small mute">{{ h.seen_count }}</td>
                <td class="small mute nowrap">{{ fmtRelative(h.last_seen) }}</td>
                <td>
                  <button
                    class="btn sm ghost"
                    :disabled="!canWrite() || !h.mac"
                    :title="h.mac ? '为此主机添加监控策略' : '该主机没有 MAC 地址，无法按 MAC 绑定监控'"
                    @click="openMonitor(h)"
                  >
                    监控<span v-if="policyCount(h)" class="badge accent" style="margin-left: 6px">{{ policyCount(h) }}</span>
                  </button>
                </td>
                <td class="right">
                  <button class="btn sm ghost" :disabled="!canWrite()" @click="removeHost(h)">移除</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <Modal v-if="monitorHost" title="添加监控" @close="closeMonitor">
      <div class="small dim" style="margin-bottom: 12px">
        按 MAC <span class="mono">{{ monitorHost.mac }}</span> 绑定，当前 IP
        <span class="mono">{{ monitorHost.ip }}</span>。IP 变化后策略会自动跟随，无需重设。
      </div>

      <div v-if="openPolicies.length" class="card" style="margin-bottom: 14px">
        <div class="card-head">
          <h3>已有策略（{{ openPolicies.length }}）</h3>
        </div>
        <div class="card-body flush">
          <div class="table-wrap">
            <table class="data">
              <thead>
                <tr>
                  <th>名称</th>
                  <th>端口</th>
                  <th>协议</th>
                  <th>动作</th>
                  <th>当前 IP</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="p in openPolicies" :key="p.id">
                  <td class="small">{{ p.name }}</td>
                  <td class="mono small">{{ p.port_ranges || '全部端口' }}</td>
                  <td class="small">{{ p.protocol || 'any' }}</td>
                  <td class="small">{{ actionLabel(p.auto_action) }}</td>
                  <td class="mono small">{{ p.host_ip || '—' }}</td>
                  <td class="right">
                    <button class="btn sm ghost" :disabled="!canWrite()" @click="removePolicy(p)">删除</button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </div>

      <div class="grid cols-2">
        <label class="field">
          <span>策略名称</span>
          <input v-model="form.name" class="input" :placeholder="`监控 ${monitorHost.hostname || monitorHost.ip}`" />
        </label>
        <label class="field">
          <span>端口范围</span>
          <input v-model="form.port_ranges" class="input" placeholder="如 22,80,443,8000-8100；留空表示全部端口" />
        </label>
        <label class="field">
          <span>协议</span>
          <select v-model="form.protocol" class="select">
            <option value="any">any</option>
            <option value="tcp">tcp</option>
            <option value="udp">udp</option>
          </select>
        </label>
        <label class="field">
          <span>动作</span>
          <select v-model="form.auto_action" class="select">
            <option value="watch">仅监控（不自动建映射）</option>
            <option value="add">发现端口即自动添加映射</option>
            <option value="ignore">忽略这些端口</option>
          </select>
        </label>
        <label class="field">
          <span>优先级</span>
          <input v-model="form.priority" class="input" type="number" min="0" />
        </label>
        <label class="field">
          <span>备注</span>
          <input v-model="form.note" class="input" placeholder="可选" />
        </label>
      </div>

      <template #footer>
        <button class="btn ghost" @click="closeMonitor">关闭</button>
        <button class="btn primary" :disabled="saving || !canWrite()" @click="addPolicy">
          <span v-if="saving" class="spin">◌</span>添加策略
        </button>
      </template>
    </Modal>
  </div>
</template>
