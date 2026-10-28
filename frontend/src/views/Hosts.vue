<script setup>
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import api from '../api'
import Badge from '../components/Badge.vue'
import EmptyState from '../components/EmptyState.vue'
import Modal from '../components/Modal.vue'
import PolicyFields from '../components/PolicyFields.vue'
import { toastOk, toastErr, canWrite } from '../store'
import { fmtRelative, portStatusInfo, protocolTag, truncate } from '../utils/format'

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
// 与 PolicyFields 组件一一对应：主机入口和策略页共享同一份字段定义。
function emptyPolicyForm() {
  return {
    name: '',
    description: '',
    match_target_kind: 'mac',
    mac_patterns: '',
    port_ranges: '',
    exclude_ranges: '',
    protocol: 'any',
    auto_action: 'watch',
    auto_add: false,
    min_port: 0,
    max_port: 0,
    require_name: '',
    priority: 0,
    tags: [],
    note: '',
  }
}
const form = reactive(emptyPolicyForm())

// ---- 选择的「主机」与「扫描结果端口」----
const selected = ref(new Set())
const allSelected = computed(() => hosts.value.length > 0 && selected.value.size === hosts.value.length)

function toggleSelect(id) {
  const next = new Set(selected.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  selected.value = next
}

function toggleSelectAll() {
  selected.value = allSelected.value ? new Set() : new Set(hosts.value.map((h) => h.id))
}

const selectedHosts = computed(() => hosts.value.filter((h) => selected.value.has(h.id)))

// 针对所选主机的端口扫描
const scanOpen = ref(false)
// 扫描目标：来自勾选（工具栏）或单行（行内「扫描端口」），与列表勾选解耦。
const scanTargets = ref([])
const scanForm = reactive({ ports_spec: '' })
const scanning = ref(false)
const scanRun = ref(null)
const scanPorts = ref([])
const portSel = ref(new Set())
const actionBusy = reactive({ rules: false, policy: false })
const policyAction = ref('watch')
let pollTimer = null

const selectedPorts = computed(() => scanPorts.value.filter((p) => portSel.value.has(p.id)))
const allPortsSelected = computed(
  () => scanPorts.value.length > 0 && portSel.value.size === scanPorts.value.length,
)

function togglePort(id) {
  const next = new Set(portSel.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  portSel.value = next
}

function toggleAllPorts() {
  portSel.value = allPortsSelected.value ? new Set() : new Set(scanPorts.value.map((p) => p.id))
}

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
    const [h, n, p] = await Promise.all([api.hosts({ limit: 800 }), api.interfaces(), api.policies()])
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

// ---- 扫描所选主机 ----

function openScan() {
  if (!selectedHosts.value.length) {
    toastErr('请先在列表中选择要扫描的主机', '未选择主机')
    return
  }
  startScanFor(selectedHosts.value)
}

// 行内「扫描端口」：只扫描这一台主机
function openScanFor(host) {
  startScanFor([host])
}

function startScanFor(targets) {
  scanTargets.value = targets
  scanForm.ports_spec = ''
  scanRun.value = null
  scanPorts.value = []
  portSel.value = new Set()
  scanOpen.value = true
}

function clearPoll() {
  if (pollTimer) {
    clearTimeout(pollTimer)
    pollTimer = null
  }
}

async function startScan() {
  if (!scanTargets.value.length) return
  scanning.value = true
  clearPoll()
  try {
    const res = await api.startScan({
      name: `扫描所选主机（${scanTargets.value.length} 台）`,
      target_kind: 'hosts',
      target_ref: scanTargets.value.map((h) => h.ip).join(','),
      ports_spec: scanForm.ports_spec,
      apply_policy: false, // 结果交给用户挑选，不自动建规则
    })
    scanRun.value = res.scan || null
    pollScan(res.scan && res.scan.id)
  } catch (err) {
    scanning.value = false
    toastErr(err.message, '扫描启动失败')
  }
}

// pollScan waits for the scan to finish, then loads the discovered ports.
function pollScan(id) {
  if (!id) {
    scanning.value = false
    return
  }
  pollTimer = setTimeout(async () => {
    try {
      const res = await api.scan(id)
      scanRun.value = res.scan || scanRun.value
      const status = (res.scan || {}).status
      if (status === 'running') {
        pollScan(id)
        return
      }
      const ports = await api.scanResults(id, { limit: 500 })
      scanPorts.value = ports.ports || []
      portSel.value = new Set(scanPorts.value.map((p) => p.id))
      scanning.value = false
      if (status === 'failed') {
        toastErr((res.scan || {}).message || '扫描失败', '扫描失败')
      } else if (!scanPorts.value.length) {
        toastOk('扫描完成，没有发现开放端口')
      } else {
        toastOk(`扫描完成，发现 ${scanPorts.value.length} 个开放端口`)
      }
    } catch (err) {
      scanning.value = false
      toastErr(err.message, '获取扫描结果失败')
    }
  }, 1200)
}

function closeScan() {
  clearPoll()
  scanning.value = false
  scanOpen.value = false
}

// ---- 对选中的端口执行动作 ----

async function addToRules() {
  const ports = selectedPorts.value
  if (!ports.length) {
    toastErr('请先勾选端口', '未选择端口')
    return
  }
  actionBusy.rules = true
  try {
    const res = await api.addPorts({ ids: ports.map((p) => p.id) })
    toastOk(`已添加 ${res.added} 条映射规则${res.failed ? `，${res.failed} 条失败` : ''}`)
    await load()
    portSel.value = new Set()
  } catch (err) {
    toastErr(err.message, '添加映射规则失败')
  } finally {
    actionBusy.rules = false
  }
}

// createPolicies groups the selected ports per host and creates one host-bound
// policy (keyed by MAC) for each host, so DHCP address changes keep working.
async function createPolicies() {
  const ports = selectedPorts.value
  if (!ports.length) {
    toastErr('请先勾选端口', '未选择端口')
    return
  }
  const byHost = new Map()
  for (const p of ports) {
    if (!byHost.has(p.host)) byHost.set(p.host, [])
    byHost.get(p.host).push(p.port)
  }
  const hostByIP = new Map(hosts.value.map((h) => [h.ip, h]))
  let created = 0
  let skipped = 0
  actionBusy.policy = true
  try {
    for (const [ip, portList] of byHost) {
      const host = hostByIP.get(ip)
      if (!host || !host.mac) {
        skipped++
        continue
      }
      const ranges = [...new Set(portList)].sort((a, b) => a - b).join(',')
      // 与单机弹窗/策略页保持同一份字段语义：mac 匹配 + 自动填入主机 MAC。
      await api.createHostPolicy(host.id, {
        name: `监控 ${host.hostname || host.ip}`,
        match_target_kind: 'mac',
        mac_patterns: host.mac,
        port_ranges: ranges,
        protocol: 'tcp',
        auto_action: policyAction.value,
        auto_add: policyAction.value === 'add',
      })
      created++
    }
    if (created) toastOk(`已为 ${created} 台主机生成监控策略`)
    if (skipped) toastErr(`${skipped} 台主机缺少 MAC 地址，未能生成策略（请先执行主机发现）`, '部分未生成')
    await load()
    portSel.value = new Set()
  } catch (err) {
    toastErr(err.message, '生成监控策略失败')
  } finally {
    actionBusy.policy = false
  }
}

// ---- 单机监控策略弹窗 ----

function openMonitor(h) {
  monitorHost.value = h
  Object.assign(form, emptyPolicyForm(), {
    name: `监控 ${h.hostname || h.ip}`,
    // 主机入口天然是「按 MAC 匹配」，把 MAC 直接填进匹配规则，
    // 这样在策略页编辑时匹配范围不是空的。
    match_target_kind: 'mac',
    mac_patterns: h.mac || '',
  })
}

function closeMonitor() {
  monitorHost.value = null
}

async function addPolicy() {
  if (!monitorHost.value) return
  saving.value = true
  try {
    await api.createHostPolicy(monitorHost.value.id, {
      ...form,
      priority: Number(form.priority) || 0,
      min_port: Number(form.min_port) || 0,
      max_port: Number(form.max_port) || 0,
      auto_add: form.auto_action === 'add' || form.auto_add,
    })
    toastOk('已添加监控策略')
    Object.assign(form, emptyPolicyForm())
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
onBeforeUnmount(clearPoll)
</script>

<template>
  <div>
    <div class="toolbar">
      <span class="dim small">
        共 {{ hosts.length }} 台主机 · 默认网关 {{ gateway || '未知' }} · 监控按 MAC 绑定，IP 变动自动跟随
      </span>
      <div class="spacer" style="flex: 1" />
      <span v-if="selected.size" class="badge accent">已选 {{ selected.size }} 台</span>
      <button
        class="btn primary"
        :disabled="!selected.size || !canWrite() || scanning"
        :title="selected.size ? '扫描勾选的主机' : '请先勾选主机'"
        @click="openScan"
      >
        <span v-if="scanning" class="spin">◌</span>扫描所选主机
      </button>
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
                <th style="width: 34px">
                  <input type="checkbox" :checked="allSelected" @change="toggleSelectAll" />
                </th>
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
              <tr v-for="h in hosts" :key="h.id" :class="{ selected: selected.has(h.id) }">
                <td><input type="checkbox" :checked="selected.has(h.id)" @change="toggleSelect(h.id)" /></td>
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
                  <div class="cell-actions">
                    <button
                      class="btn sm ghost"
                      :disabled="!canWrite() || !h.mac"
                      :title="h.mac ? '为此主机添加监控策略' : '该主机没有 MAC 地址，无法按 MAC 绑定监控'"
                      @click="openMonitor(h)"
                    >
                      监控<span v-if="policyCount(h)" class="badge accent" style="margin-left: 6px">{{ policyCount(h) }}</span>
                    </button>
                    <button
                      class="btn sm ghost"
                      :disabled="!canWrite() || scanning"
                      :title="`扫描 ${h.ip} 的开放端口`"
                      @click="openScanFor(h)"
                    >
                      扫描端口
                    </button>
                  </div>
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

    <!-- 扫描所选主机 -->
    <Modal v-if="scanOpen" title="扫描所选主机" wide @close="closeScan">
      <div class="small dim" style="margin-bottom: 12px">
        目标（{{ scanTargets.length }} 台）：
        <span class="mono">{{ scanTargets.map((h) => h.ip).join(', ') }}</span>
      </div>

      <div class="row wrap" style="gap: 10px; align-items: flex-end">
        <label class="field" style="flex: 1 1 320px">
          <span>端口范围</span>
          <input v-model="scanForm.ports_spec" class="input" placeholder="留空使用默认端口集；例如 22,80,443,8000-8100" />
        </label>
        <button class="btn primary" :disabled="scanning || !canWrite()" @click="startScan">
          <span v-if="scanning" class="spin">◌</span>{{ scanning ? '扫描中…' : '开始扫描' }}
        </button>
      </div>

      <div v-if="scanRun" class="small dim" style="margin-top: 10px">
        扫描 #{{ scanRun.id }} · 状态 {{ scanRun.status }} · 开放 {{ scanRun.open_count || 0 }}
        <span v-if="scanRun.message"> · {{ scanRun.message }}</span>
      </div>

      <div v-if="scanPorts.length" style="margin-top: 14px">
        <div class="row" style="margin-bottom: 8px">
          <span class="small mute">发现 {{ scanPorts.length }} 个端口，已勾选 {{ portSel.size }} 个</span>
          <div class="spacer" style="flex: 1" />
          <button class="btn sm" @click="toggleAllPorts">{{ allPortsSelected ? '取消全选' : '全选' }}</button>
        </div>
        <div class="table-wrap" style="max-height: 46vh; overflow-y: auto">
          <table class="data">
            <thead>
              <tr>
                <th style="width: 34px"></th>
                <th>主机:端口</th>
                <th>协议</th>
                <th>服务</th>
                <th>状态</th>
                <th>指纹</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="p in scanPorts" :key="p.id" :class="{ selected: portSel.has(p.id) }">
                <td><input type="checkbox" :checked="portSel.has(p.id)" @change="togglePort(p.id)" /></td>
                <td class="mono small nowrap">{{ p.host }}:{{ p.port }}</td>
                <td>
                  <Badge :kind="protocolTag(p.protocol).kind">{{ protocolTag(p.protocol).label }}</Badge>
                </td>
                <td class="small">{{ p.service || '—' }}</td>
                <td>
                  <Badge :kind="portStatusInfo(p.status).kind">{{ portStatusInfo(p.status).label }}</Badge>
                </td>
                <td class="small mute">{{ truncate(p.banner, 40) || '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>

        <div class="row wrap" style="margin-top: 12px; gap: 10px; align-items: center">
          <select v-model="policyAction" class="select" style="width: 220px">
            <option value="watch">生成「仅观察」策略</option>
            <option value="add">生成「自动添加」策略</option>
            <option value="ignore">生成「忽略」策略</option>
            <option value="delete">生成「删除记录」策略</option>
          </select>
          <button class="btn" :disabled="actionBusy.policy || !portSel.size || !canWrite()" @click="createPolicies">
            <span v-if="actionBusy.policy" class="spin">◌</span>生成监控策略（{{ selectedPorts.length }}）
          </button>
          <button class="btn primary" :disabled="actionBusy.rules || !portSel.size || !canWrite()" @click="addToRules">
            <span v-if="actionBusy.rules" class="spin">◌</span>添加到映射规则（{{ selectedPorts.length }}）
          </button>
        </div>
        <div class="small dim" style="margin-top: 6px">
          生成监控策略会按 MAC 绑定主机（同一主机的多个端口合并为一条策略），IP 变化后仍有效；
          添加到映射规则会为每个端口立即创建一条映射。
        </div>
      </div>
      <div v-else-if="!scanning && scanRun && scanRun.status !== 'running'" class="mute small" style="margin-top: 14px">
        本次扫描没有发现开放端口。可以调整端口范围后重新扫描。
      </div>

      <template #footer>
        <button class="btn ghost" @click="closeScan">关闭</button>
        <button class="btn" :disabled="scanning || !canWrite()" @click="startScan">
          <span v-if="scanning" class="spin">◌</span>重新扫描
        </button>
      </template>
    </Modal>

    <!-- 单机监控策略 -->
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

      <PolicyFields :form="form" host-bound />

      <template #footer>
        <button class="btn ghost" @click="closeMonitor">关闭</button>
        <button class="btn primary" :disabled="saving || !canWrite()" @click="addPolicy">
          <span v-if="saving" class="spin">◌</span>添加策略
        </button>
      </template>
    </Modal>
  </div>
</template>
