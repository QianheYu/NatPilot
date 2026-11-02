<script setup>
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import api from '../api'
import Badge from '../components/Badge.vue'
import EmptyState from '../components/EmptyState.vue'
import Modal from '../components/Modal.vue'
import PolicyFields from '../components/PolicyFields.vue'
import Switch from '../components/Switch.vue'
import { toastOk, toastErr, toastWarn, canWrite } from '../store'
import { fmtRelative, portStatusInfo, protocolTag, truncate } from '../utils/format'
import { addressLabel, addrFamily, fmtHost, fmtHostPort } from '../utils/netaddr'

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
// 主机名手动编辑：名称挂在主机记录（即 MAC）上 —— 用 PATCH /api/hosts/{id}
// 保存后，后端把该名称写回这条 MAC 记录，IP 变化（DHCP 换址）也不会丢。
const renameHost = ref(null)
const hostnameDraft = ref('')
const savingName = ref(false)
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

// ---- 搜索：地址（任意一条）/ 主机名 / MAC / 厂商 / 接口 ----
// 双栈主机有多条地址，只要任意一条命中就该出现在结果里。
const filterQ = ref('')

function hostMatches(h, q) {
  const fields = [h.hostname, h.mac, h.vendor, h.interface, h.kind, h.note, h.ip]
  for (const a of hostAddresses(h)) {
    fields.push(a.ip, a.zone, a.source)
  }
  return fields.some((v) => v && String(v).toLowerCase().includes(q))
}

const filteredHosts = computed(() => {
  const q = filterQ.value.trim().toLowerCase()
  if (!q) return hosts.value
  return hosts.value.filter((h) => hostMatches(h, q))
})

// ---- 选择的「主机」与「扫描结果端口」----
const selected = ref(new Set())
const allSelected = computed(
  () => filteredHosts.value.length > 0 && filteredHosts.value.every((h) => selected.value.has(h.id)),
)

function toggleSelect(id) {
  const next = new Set(selected.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  selected.value = next
}

function toggleSelectAll() {
  const next = new Set(selected.value)
  const on = !allSelected.value
  for (const h of filteredHosts.value) {
    if (on) next.add(h.id)
    else next.delete(h.id)
  }
  selected.value = next
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
// 正在提交「添加到映射规则」的端口：期间显示「添加中…」，等接口确认后再改状态，
// 不做乐观更新（用户明确要求以服务端确认为准）。
const pendingPorts = ref(new Set())
// 生成策略对话框：扫描结果可能跨多台主机，先弹窗让用户确认/修改字段。
const policyTargets = ref([]) // [{ host, ports: number[] }]
const applyToAll = ref(false)
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

// ---- 主机名：绑定到 MAC，可手动修正 ----
// 主机名不是由 IP 决定的：相同的 MAC 换到新地址后仍沿用同一个名称。没名称时
// 用它显示占位提示，并允许就地修改（名称会写回这条主机记录）。
function hostnameTitle(h) {
  const mac = h && h.mac ? h.mac : '（无 MAC）'
  return `名称绑定到 MAC ${mac}，通过 PTR 或手动设置；IP 变化后仍沿用该名称`
}

function openRename(h) {
  renameHost.value = h
  hostnameDraft.value = h.hostname || ''
}

function closeRename() {
  renameHost.value = null
  hostnameDraft.value = ''
}

async function saveHostname() {
  const h = renameHost.value
  if (!h) return
  const name = hostnameDraft.value.trim()
  savingName.value = true
  try {
    await api.patchHost(h.id, { hostname: name })
    toastOk(name ? `已将主机名设为 ${name}` : '已清除主机名，下次发现/扫描时再按 PTR 解析')
    closeRename()
    await load()
  } catch (err) {
    toastErr(err.message, '主机名保存失败')
  } finally {
    savingName.value = false
  }
}


// familyBadge renders the optional interface family field. Older payloads do
// not send it, so an absent (or unexpected) value simply yields no badge.
function familyBadge(family) {
  if (family === 'ipv6') return 'IPv6'
  if (family === 'ipv4') return 'IPv4'
  return ''
}

// 接口条目的地址族标签：老版本响应可能没有 family，按 IP 反推。
function ifaceFamily(i) {
  if (!i) return ''
  return familyBadge(i.family) || familyBadge(addrFamily(i.ip))
}

// ---- 地址列：一台主机可能同时有 IPv4 / IPv6 多个地址 ----
// addrKey 去掉方括号：`[fd00::20]` 与 `fd00::20` 要落到同一条主机记录。
function addrKey(value) {
  return String(value == null ? '' : value).trim().replace(/^\[/, '').replace(/\]$/, '')
}

// hostAddresses 把一条主机记录摊平成地址数组。老版本载荷没有 addresses，
// 就退化成一如既往的单条 host.ip，所以下面的渲染逻辑不必分叉。
function hostAddresses(h) {
  const out = []
  const seen = new Set()
  const push = (value) => {
    if (!value) return
    const entry = typeof value === 'string' ? { ip: value } : value
    const ip = String(entry.ip == null ? '' : entry.ip).trim()
    if (!ip || seen.has(ip)) return
    seen.add(ip)
    out.push({ ...entry, ip })
  }
  for (const a of Array.isArray(h && h.addresses) ? h.addresses : []) push(a)
  if (!out.length && h && h.ip) push({ ip: h.ip, primary: true })
  return out
}

// addrFam 优先用后端给的 family，缺省时按地址字面量反推。
function addrFam(a) {
  const fam = String((a && a.family) || '').toLowerCase()
  if (fam === 'ipv6' || fam === 'ipv4') return fam
  return addrFamily(a && a.ip)
}

function famLabel(a) {
  const fam = addrFam(a)
  if (fam === 'ipv6') return 'IPv6'
  if (fam === 'ipv4') return 'IPv4'
  return ''
}

// 主地址 = 后端标记 primary 的那条；没有任何标记时退回 hosts[].ip。
function isPrimaryAddr(h, a) {
  if (!a) return false
  if (a.primary === true) return true
  const list = Array.isArray(h && h.addresses) ? h.addresses : []
  if (list.some((x) => x && x.primary === true)) return false
  return String(a.ip) === String((h && h.ip) || '')
}

// 链路本地地址的 zone 后缀：地址本身已带 %zone 时不再重复拼接。
function addrZone(a) {
  const zone = String((a && a.zone) || '').trim()
  if (!zone) return ''
  return String((a && a.ip) || '').indexOf('%') >= 0 ? '' : zone
}

function addrTitle(a) {
  const parts = [famLabel(a), addressLabel(a)]
  if (a && a.source) parts.push(`来源 ${a.source}`)
  if (a && a.last_seen) parts.push(`最近 ${fmtRelative(a.last_seen)}`)
  if (a && a.primary) parts.push('主地址')
  return parts.filter(Boolean).join(' · ')
}

// hostFamilyLabel 是行内「按主机扫描」的家庭指示器：v4 / v6 / v4·v6。
// 只读当前已加载的主机记录，不额外发请求。
function hostFamilyLabel(h) {
  const fams = new Set(hostAddresses(h).map((a) => addrFam(a)))
  const v4 = fams.has('ipv4')
  const v6 = fams.has('ipv6')
  if (v4 && v6) return 'v4·v6'
  return v6 ? 'v6' : v4 ? 'v4' : ''
}

function hostFamilyTitle(h) {
  const list = hostAddresses(h).map((a) => addressLabel(a))
  if (list.length > 1) {
    return `双栈主机，共 ${list.length} 个地址（${list.join('、')}）：扫描会覆盖全部地址`
  }
  return `该主机地址：${list.join('、') || '未知'}`
}

// 扫描目标一律用主地址：后端会据此解析出该主机的全部地址，请求形状不变。
function primaryAddress(h) {
  const list = hostAddresses(h)
  const primary = list.find((a) => isPrimaryAddr(h, a)) || list[0]
  return primary ? String(primary.ip) : ''
}

// targetLabel 在扫描弹窗里展示「实际要扫的主机」，顺带列出它的其它地址。
function targetLabel(h) {
  const addrs = hostAddresses(h)
  const primary = primaryAddress(h)
  const others = addrs.filter((a) => String(a.ip) !== primary).map((a) => addressLabel(a))
  const head = fmtHost(primary)
  return others.length ? `${head}（另有 ${others.join('、')}）` : head
}

function addrSummary(h) {
  return hostAddresses(h).map((a) => addressLabel(a)).join(' · ')
}

// ---- 扫描主机（指定网段 / 网卡）----
const hostScanOpen = ref(false)
const hostScanBusy = ref(false)
const hostScanResult = ref(null)
// cidr 是可选项：留空表示「扫所选网卡所在网段」，由后端按 iface 计算。
// iface 存复合值 "<name>|<ip>"：同一块网卡现在有 IPv4 / IPv6 两个条目，
// 只用名字会永远选中 IPv4 那条。
const hostScanForm = reactive({
  iface: '', cidr: '', ports: '80,443,22,445,3389', timeout_ms: 400,
  // 多目标：可同时选多块网卡（各自扫自己的网段），与手填网段组合使用。
  targets: [],
  autoEnabled: false, autoInterval: 900, autoBusy: false,
})

// ifaceKey 是网卡下拉里每个条目的唯一值。
function ifaceKey(i) {
  return `${i.name}|${i.ip}`
}

// 回环网卡不提供：扫自己没有意义，还会把 127.0.0.0/8、::1 当成局域网。
// 同一 (name, ip) 只保留一条，避免接口列表重复上报同一地址时出现重复选项。
const scanIfaces = computed(() => {
  const out = []
  const seen = new Set()
  for (const i of interfaces.value || []) {
    if (i.loopback || !i.up) continue
    const key = ifaceKey(i)
    if (seen.has(key)) continue
    seen.add(key)
    out.push(i)
  }
  return out
})

const scanIface = computed(
  () => scanIfaces.value.find((i) => ifaceKey(i) === hostScanForm.iface) || null,
)

// 网卡自带的网段。IPv6 必须优先 network（真实网段，如 fdfe:dcba:9876::/126），
// ip_net 是「地址/前缀」（fdfe:dcba:9876::1/126），直接拿去扫是错的。
function ifaceDefaultCidr(i) {
  if (!i) return ''
  return String(i.network || i.ip_net || '').trim()
}

// 「用网卡网段」按钮恢复的默认值。
const scanIfaceCidr = computed(() => ifaceDefaultCidr(scanIface.value))

// 按当前选择算出「将扫描什么」，让用户按开始前就能看到结果。
const scanTarget = computed(() => {
  const cidr = hostScanForm.cidr.trim()
  const i = scanIface.value
  if (cidr) return { cidr, label: `将扫描 ${cidr}${i ? `（${i.name}）` : '（手填网段）'}` }
  if (!i) return { cidr: '', label: '尚未选择网卡，也没有填写网段' }
  const def = ifaceDefaultCidr(i)
  return {
    cidr: def,
    label: def
      ? `将扫描 ${def}（${i.name} 所在网段）`
      : `将扫描所选网卡 ${i.name} 所在网段（由后端按网卡计算）`,
  }
})

function openHostScan() {
  loadAutoScan()
  hostScanResult.value = null
  // 首次打开、或接口列表变化导致旧选择失效时，默认选第一块可用网卡并带出网段。
  // 之后不再覆盖用户已填的网段（用户可能故意清空它）。
  if (!scanIfaces.value.some((i) => ifaceKey(i) === hostScanForm.iface)) {
    const first = scanIfaces.value[0] || null
    hostScanForm.iface = first ? ifaceKey(first) : ''
    hostScanForm.cidr = ifaceDefaultCidr(first)
  }
  hostScanOpen.value = true
}

function pickScanIface(value) {
  hostScanForm.iface = value
  // 换网卡时把网段跟着换成该网卡的子网，避免「扫 A 网卡却用 B 网段」。
  // 这只是默认值：填入后仍可手动清空，留空即交给后端按网卡计算。
  const i = scanIfaces.value.find((x) => ifaceKey(x) === value)
  if (!i) return // 「默认路由」：保留用户手填的网段
  hostScanForm.cidr = ifaceDefaultCidr(i)
}

function useIfaceSubnet() {
  if (!scanIface.value) {
    toastErr('请先选择网卡', '没有可用的网段')
    return
  }
  hostScanForm.cidr = scanIfaceCidr.value
  if (!hostScanForm.cidr) {
    toastWarn('所选网卡没有上报网段，留空时由后端按网卡计算', '网段未知')
  }
}

// 响应里的 iface 可能是复合值 "<name>|<ip>"，展示时只保留网卡名。
function ifaceName(value) {
  const s = String(value || '').trim()
  if (!s) return ''
  const idx = s.indexOf('|')
  return idx >= 0 ? s.slice(0, idx) : s
}

// scanDoneMessage 用响应回显的 mode/cidr 描述「实际扫了什么」，而不是复述表单。
function scanDoneMessage(res, sentMode, sentCidr) {
  const r = res || {}
  const subnet = String(r.cidr || sentCidr || '').trim()
  const mode = r.mode || sentMode
  const scanned = subnet ? `扫描 ${subnet}` : mode === 'subnet' ? '扫描所选网卡网段' : '扫描'
  let msg = `${scanned} 完成：${r.alive || 0} 台在线，新增/更新 ${r.discovered || 0} 条记录`
  const skipped = Number(r.skipped) || 0
  if (skipped > 0) msg += `（已跳过 ${skipped} 个无 zone 的链路本地地址）`
  return msg
}

async function loadAutoScan() {
  try {
    const res = await api.getAutoScan()
    const auto = res.auto || res
    hostScanForm.autoEnabled = !!auto.enabled
    if (auto.interval_seconds) hostScanForm.autoInterval = auto.interval_seconds
  } catch {
    /* 自动扫描状态读不到不影响手动扫描 */
  }
}

async function saveAutoScan() {
  const cidrs = []
  for (const key of hostScanForm.targets.filter(Boolean)) {
    const it = scanIfaces.value.find((i) => ifaceKey(i) === key)
    const def = ifaceDefaultCidr(it)
    if (def) cidrs.push(def)
  }
  const manual = hostScanForm.cidr.trim()
  if (manual) cidrs.push(manual)
  if (hostScanForm.autoEnabled && !cidrs.length) {
    toastErr('自动扫描需要至少一个目标：勾选网卡或填写网段', '缺少目标')
    return
  }
  hostScanForm.autoBusy = true
  try {
    const res = await api.patchAutoScan({
      enabled: hostScanForm.autoEnabled,
      interval_seconds: Number(hostScanForm.autoInterval) || 900,
      cidrs,
      ports: hostScanForm.ports,
    })
    const auto = res.auto || res
    hostScanForm.autoEnabled = !!auto.enabled
    hostScanForm.autoInterval = auto.interval_seconds || hostScanForm.autoInterval
    toastOk(auto.enabled ? `已开启自动扫描，每 ${auto.interval_seconds} 秒一次` : '已关闭自动扫描')
  } catch (err) {
    toastErr(err.message, '保存自动扫描设置失败')
  } finally {
    hostScanForm.autoBusy = false
  }
}

async function runHostScan() {
  const cidr = hostScanForm.cidr.trim()
  const iface = hostScanForm.iface
  // 网段可选，但「网段 + 网卡」全空时后端只能返回 400，这里提前说清楚。
  if (!cidr && !iface) {
    toastErr('请选择网卡，或填写要扫描的网段，例如 192.168.1.0/24', '缺少扫描目标')
    return
  }
  // 手填网段 = 扫该网段；留空 = 扫所选网卡所在网段；两者都没有才退化为 cidr。
  const mode = cidr ? 'cidr' : iface ? 'subnet' : 'cidr'
  hostScanBusy.value = true
  try {
    const picked = hostScanForm.targets.filter(Boolean)
    const payload = {
      ports: hostScanForm.ports,
      timeout_ms: Number(hostScanForm.timeout_ms) || 400,
    }
    if (picked.length || (cidr && picked.length === 0 && false)) {
      // 多目标：每块网卡一个 iface 目标，手填网段作为一个 cidr 目标。
      payload.targets = picked.map((key) => ({ kind: 'iface', iface: key }))
      if (cidr) payload.targets.push({ kind: 'cidr', cidr })
    } else {
      payload.mode = mode
      payload.iface = iface
      if (cidr) payload.cidr = cidr
    }
    const res = await api.hostScan(payload)
    hostScanResult.value = res
    toastOk(scanDoneMessage(res, mode, cidr))
    await load()
  } catch (err) {
    toastErr(err.message, '扫描主机失败')
  } finally {
    hostScanBusy.value = false
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
      // 双栈主机只送主地址（v4 优先）；后端据此解析该主机的全部地址，
      // 请求形状与之前完全一致。
      target_ref: scanTargets.value.map(primaryAddress).filter(Boolean).join(','),
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

// refreshScanResults reloads the result rows of the current scan so that the
// 状态 column reflects what just happened (已添加 / 已忽略 …). Without it the
// list keeps showing the snapshot taken when the scan finished.
async function refreshScanResults() {
  const id = scanRun.value && scanRun.value.id
  if (!id) return
  try {
    const res = await api.scanResults(id, { limit: 500 })
    scanPorts.value = res.ports || []
    // Keep the previous selection, but drop rows that disappeared.
    const ids = new Set(scanPorts.value.map((p) => p.id))
    portSel.value = new Set([...portSel.value].filter((x) => ids.has(x)))
  } catch {
    /* the list is advisory; a failed refresh must not break the action result */
  }
}

// ---- 对选中的端口执行动作 ----

async function addToRules() {
  const ports = selectedPorts.value
  if (!ports.length) {
    toastErr('请先勾选端口', '未选择端口')
    return
  }
  actionBusy.rules = true
  const ids = ports.map((p) => p.id)
  // 请求在飞：只标记「添加中」，不改状态。状态一律以接口返回后的服务端数据为准。
  pendingPorts.value = new Set(ids)
  try {
    const res = await api.addPorts({ ids })
    toastOk(`已添加 ${res.added} 条映射规则${res.failed ? `，${res.failed} 条失败` : ''}`)
    portSel.value = new Set()
    // 成功后才刷新：此时服务端已把端口状态写成 added。
    await Promise.all([load(), refreshScanResults()])
  } catch (err) {
    // 失败不改状态：这些端口仍处于原来的「未添加」。
    toastErr(err.message, '添加映射规则失败')
  } finally {
    pendingPorts.value = new Set()
    actionBusy.rules = false
  }
}

// openPolicyDialog prepares one host-bound policy per host that owns a selected
// port, then shows the same field set as the policy page so the user can review
// and adjust everything before it is created (defaults are pre-filled).
function openPolicyDialog() {
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
  // 扫描结果的 p.host 可能来自该主机的任意一条地址（v4 或 v6），
  // 所以按「全部地址」建索引，不能只认 hosts[].ip。
  const hostByIP = new Map()
  for (const h of hosts.value) {
    for (const a of hostAddresses(h)) hostByIP.set(addrKey(a.ip), h)
  }
  const targets = []
  let missingMac = 0
  for (const [ip, portList] of byHost) {
    const host = hostByIP.get(addrKey(ip)) || { id: 0, ip, mac: '', hostname: '' }
    if (!host.mac) missingMac++
    targets.push({ host, ports: [...new Set(portList)].sort((a, b) => a - b) })
  }
  if (!targets.length) {
    toastErr('没有可用的主机信息', '无法生成策略')
    return
  }
  if (missingMac) {
    // 仍然打开对话框：让用户自己填 MAC 前缀（例如主机尚未做发现、或回环地址），
    // 否则这里直接拒绝，用户就没有任何补救入口。
    toastWarn(
      `${missingMac} 台主机没有 MAC 地址，请在对话框里填写「MAC 地址 / 前缀」，否则该策略不会匹配任何主机`,
      '需要补充匹配条件',
    )
  }
  policyTargets.value = targets
  applyToAll.value = targets.length > 1
  const first = targets[0]
  monitorHost.value = first.host
  Object.assign(form, emptyPolicyForm(), {
    name: targets.length > 1 ? `监控 ${targets.length} 台主机` : `监控 ${first.host.hostname || first.host.ip}`,
    match_target_kind: 'mac',
    mac_patterns: first.host.mac || '',
    port_ranges: first.ports.join(','),
    protocol: 'tcp',
  })
}

// ---- 单机监控策略弹窗 ----

function openMonitor(h) {
  monitorHost.value = h
  policyTargets.value = []
  applyToAll.value = false
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
  const targets =
    applyToAll.value && policyTargets.value.length > 1
      ? policyTargets.value
      : [{ host: monitorHost.value, ports: [] }]
  saving.value = true
  actionBusy.policy = true
  try {
    for (const t of targets) {
      // 每台主机沿用同一份字段值，只把"端口范围"换成该主机被选中的端口，
      // 避免把 A 主机的端口写进 B 主机的策略。
      const portRanges = t.ports.length ? t.ports.join(',') : form.port_ranges
      const payload = {
        ...form,
        port_ranges: portRanges,
        mac_patterns: form.mac_patterns || t.host.mac || '',
        priority: Number(form.priority) || 0,
        min_port: Number(form.min_port) || 0,
        max_port: Number(form.max_port) || 0,
        auto_add: form.auto_action === 'add' || form.auto_add,
      }
      if (t.host.mac) {
        // 有 MAC：按主机绑定（hosts/{id}/policies），设备换 IP 也不会失配。
        await api.createHostPolicy(t.host.id, payload)
      } else {
        // 没有 MAC：退化为按 MAC 前缀匹配的普通策略，由用户在对话框里填前缀。
        delete payload.match_target_kind
        await api.createPolicy({ ...payload, match_target_kind: 'mac' })
      }
    }
    toastOk(targets.length > 1 ? `已为 ${targets.length} 台主机生成监控策略` : '已添加监控策略')
    monitorHost.value = null
    policyTargets.value = []
    Object.assign(form, emptyPolicyForm())
    portSel.value = new Set()
    await Promise.all([load(), refreshScanResults()])
  } catch (err) {
    toastErr(err.message, '添加失败')
  } finally {
    saving.value = false
    actionBusy.policy = false
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
      <input
        v-model="filterQ"
        class="input grow"
        style="max-width: 240px"
        placeholder="搜索地址 / 主机名 / MAC / 厂商"
      />
      <span class="dim small">
        共 {{ hosts.length }} 台主机<template v-if="filterQ.trim()">（匹配 {{ filteredHosts.length }} 台）</template>
        · 默认网关 {{ gateway || '未知' }} · 监控按 MAC 绑定，IP 变动自动跟随
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
      <button class="btn primary" :disabled="hostScanBusy || !canWrite()" @click="openHostScan">
        <span v-if="hostScanBusy" class="spin">◌</span>扫描主机…
      </button>
      <button class="btn" :disabled="busy || !canWrite()" @click="discoverHosts">发现局域网主机</button>
    </div>

    <div class="grid cols-4" style="margin-bottom: 16px">
      <div v-for="i in interfaces" :key="i.name + i.ip" class="stat">
        <div class="label">
          {{ i.name }}
          <span v-if="familyBadge(i.family)" class="badge" style="margin-left: 4px">{{ familyBadge(i.family) }}</span>
        </div>
        <div class="value mono" style="font-size: 18px">{{ fmtHost(i.ip) }}</div>
        <div class="sub mono small">{{ i.network }}</div>
        <div class="sub">
          <span v-if="i.gateway">网关 {{ fmtHost(i.gateway) }}</span>
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
                <th>地址</th>
                <th>MAC</th>
                <th>厂商</th>
                <th>主机名</th>
                <th>类型</th>
                <th>接口</th>
                <th>次数</th>
                <th>最近</th>
                <th class="right">操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="h in filteredHosts" :key="h.id" :class="{ selected: selected.has(h.id) }">
                <td><input type="checkbox" :checked="selected.has(h.id)" @change="toggleSelect(h.id)" /></td>
                <td data-label="地址">
                  <div class="addr-cell">
                    <span
                      v-for="a in hostAddresses(h)"
                      :key="a.ip + '|' + (a.zone || '')"
                      class="addr-chip"
                      :class="{ primary: isPrimaryAddr(h, a), v6: addrFam(a) === 'ipv6' }"
                      :title="addrTitle(a)"
                    >
                      <span class="addr-fam">{{ famLabel(a) }}</span>
                      <span class="addr-ip">{{ fmtHost(a.ip) }}<span v-if="addrZone(a)" class="addr-zone">%{{ addrZone(a) }}</span></span>
                      <span v-if="isPrimaryAddr(h, a)" class="addr-main">主</span>
                    </span>
                  </div>
                </td>
                <td class="mono small mute">{{ h.mac || '—' }}</td>
                <td class="small">{{ h.vendor || '—' }}</td>
                <td class="small" data-label="主机名" :title="hostnameTitle(h)">
                  <div class="hostname-cell">
                    <span
                      class="hostname-text"
                      :class="{ empty: !h.hostname }"
                      :title="hostnameTitle(h)"
                    >{{ h.hostname || '未命名（点击 ✎ 设置）' }}</span>
                    <button
                      class="btn sm ghost hostname-edit"
                      :disabled="!canWrite()"
                      :title="`${h.hostname ? '修改' : '设置'}主机名（绑定到 MAC ${h.mac || '—'}）`"
                      @click.stop="openRename(h)"
                    >
                      ✎
                    </button>
                  </div>
                </td>
                <td>
                  <Badge :kind="h.is_local ? 'accent' : h.kind === 'gateway' ? 'purple' : 'default'">
                    {{ h.is_local ? '本机' : h.kind || '对端' }}
                  </Badge>
                </td>
                <td class="small mute">{{ h.interface || '—' }}</td>
                <td class="small mute">{{ h.seen_count }}</td>
                <td class="small mute nowrap">{{ fmtRelative(h.last_seen) }}</td>
                <td class="right">
                  <div class="cell-actions">
                    <span v-if="hostFamilyLabel(h)" class="badge fam" :title="hostFamilyTitle(h)">{{ hostFamilyLabel(h) }}</span>
                    <button
                      class="btn sm ghost"
                      :disabled="!canWrite() || !h.mac"
                      :title="h.mac ? '为此主机添加监控策略' : '该主机没有 MAC 地址，无法按 MAC 绑定监控'"
                      @click="openMonitor(h)"
                    >
                      监控<span v-if="policyCount(h)" class="badge accent" style="margin-left: 4px">{{ policyCount(h) }}</span>
                    </button>
                    <button
                      class="btn sm ghost"
                      :disabled="!canWrite() || scanning"
                      :title="`扫描 ${targetLabel(h)} 的开放端口`"
                      @click="openScanFor(h)"
                    >
                      扫描端口
                    </button>
                    <button class="btn sm ghost danger" :disabled="!canWrite()" @click="removeHost(h)">移除</button>
                  </div>
                </td>
              </tr>
              <tr v-if="filterQ.trim() && !filteredHosts.length">
                <td colspan="10" class="dim small">没有匹配“{{ filterQ.trim() }}”的主机（地址、主机名、MAC、厂商均可搜索）。</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <!-- 扫描主机：网段可选，留空即扫所选网卡所在网段 -->
    <Modal v-if="hostScanOpen" title="扫描主机（网段可选 / 指定网卡）" wide @close="hostScanOpen = false">
      <div class="grid cols-2">
        <label class="field">
          <span>网卡</span>
          <select class="select" :value="hostScanForm.iface" @change="pickScanIface($event.target.value)">
            <option value="">默认路由（不绑定网卡）</option>
            <option v-for="i in scanIfaces" :key="ifaceKey(i)" :value="ifaceKey(i)">
              {{ i.name }} · {{ fmtHost(i.ip) }}<template v-if="ifaceFamily(i)"> · {{ ifaceFamily(i) }}</template><template v-if="i.gateway"> · 网关 {{ fmtHost(i.gateway) }}</template>
            </option>
          </select>
          <div class="hint">选中网卡后，探测报文会绑定该网卡的地址发出，多网卡机器不会走错链路。</div>
          <div class="hint">IPv6 链路本地地址（fe80::/10）会使用该网卡的 zone 扫描。</div>
        </label>
        <label class="field">
          <span>多目标（可组合多选）</span>
          <select v-model="hostScanForm.targets" class="select" multiple size="5">
            <option v-for="i in scanIfaces" :key="ifaceKey(i)" :value="ifaceKey(i)">
              {{ i.name }} · {{ fmtHost(i.ip) }}<template v-if="ifaceFamily(i)"> · {{ ifaceFamily(i) }}</template>
            </option>
          </select>
          <div class="hint">
            选中多块网卡时，每块网卡各扫自己的网段；与上方网段组合使用（网段作为额外目标）。留空则按单选网卡扫描。
          </div>
        </label>
        <label class="field">
          <span>网段（CIDR，可选）</span>
          <div class="row" style="gap: 8px; align-items: center">
            <input
              v-model="hostScanForm.cidr"
              class="input mono"
              style="flex: 1 1 auto"
              placeholder="留空 = 扫描所选网卡所在网段"
            />
            <button
              class="btn sm"
              type="button"
              :disabled="!scanIface"
              :title="scanIfaceCidr ? `填入 ${scanIfaceCidr}` : '所用网卡没有上报网段'"
              @click="useIfaceSubnet"
            >
              用网卡网段
            </button>
          </div>
          <div class="hint">
            留空时由后端按所选网卡计算网段；也可手动填其它网段（最多解析 1024 个地址），填入后以这里的值为准。
          </div>
          <div class="hint">
            <span class="badge accent" v-if="!hostScanForm.cidr.trim() && scanIface">自动</span>
            {{ scanTarget.label }}
          </div>
        </label>
        <label class="field">
          <span>自动扫描</span>
          <div class="row" style="gap: 10px; align-items: center">
            <Switch v-model="hostScanForm.autoEnabled" />
            <span class="small dim">自动运行</span>
            <input
              v-model.number="hostScanForm.autoInterval"
              class="input"
              type="number"
              min="60"
              max="86400"
              step="60"
              style="width: 120px"
            />
            <span class="small dim">秒/次</span>
            <button class="btn sm" type="button" :disabled="hostScanForm.autoBusy" @click="saveAutoScan">
              保存自动扫描
            </button>
          </div>
          <div class="hint">
            自动扫描使用「多目标 + 网段」里选中的目标；间隔 60–86400 秒，改完立即生效，无需重启。
          </div>
        </label>
        <label class="field">
          <span>探测端口（任一响应即视为在线）</span>
          <input v-model="hostScanForm.ports" class="input mono" placeholder="80,443,22,445,3389" />
        </label>
        <label class="field">
          <span>单次超时（毫秒）</span>
          <input v-model.number="hostScanForm.timeout_ms" class="input" type="number" min="50" max="5000" />
          <div class="hint">超时越小扫得越快，但可能漏掉响应慢的主机。</div>
        </label>
      </div>

      <div v-if="hostScanResult" class="table-wrap" style="max-height: 320px; overflow-y: auto; margin-top: 8px">
        <table class="data">
          <thead>
            <tr><th>IP</th><th>MAC</th><th>主机名</th><th>网卡</th></tr>
          </thead>
          <tbody>
            <tr v-for="h in hostScanResult.hosts" :key="h.ip">
              <td class="mono small">{{ fmtHost(h.ip) }}</td>
              <td class="mono small">{{ h.mac || '—' }}</td>
              <td class="small">{{ h.hostname || '—' }}</td>
              <td class="small mute">{{ ifaceName(h.iface || hostScanResult.iface) || '—' }}</td>
            </tr>
            <tr v-if="!hostScanResult.hosts || !hostScanResult.hosts.length">
              <td colspan="4" class="dim small">该网段没有主机响应所选端口（可尝试换端口或加大超时）。</td>
            </tr>
          </tbody>
        </table>
      </div>
      <div v-if="hostScanResult" class="small dim" style="margin-top: 8px">
        扫描方式 {{ hostScanResult.mode === 'subnet' ? '所选网卡网段' : '指定网段' }}<template v-if="hostScanResult.cidr"> · 目标 <span class="mono">{{ hostScanResult.cidr }}</span></template>
        · 在线 {{ hostScanResult.alive }} 台 · 写入/更新 {{ hostScanResult.discovered }} 条
        <span v-if="Number(hostScanResult.skipped) > 0"> · 已跳过 {{ hostScanResult.skipped }} 个无 zone 的链路本地地址</span>
      </div>

      <template #footer>
        <button class="btn" @click="hostScanOpen = false">关闭</button>
        <button class="btn primary" :disabled="hostScanBusy" @click="runHostScan">
          <span v-if="hostScanBusy" class="spin">◌</span>开始扫描
        </button>
      </template>
    </Modal>

    <Modal v-if="scanOpen" title="扫描所选主机" wide @close="closeScan">
      <div class="small dim" style="margin-bottom: 12px">
        目标（{{ scanTargets.length }} 台）：
        <span class="mono">{{ scanTargets.map(targetLabel).join(' · ') }}</span>
        <div style="margin-top: 4px">双栈主机按主地址提交，后端会解析出该主机的全部地址后再扫描。</div>
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
                <td class="mono small nowrap">{{ fmtHostPort(p.host, p.port) }}</td>
                <td>
                  <Badge :kind="protocolTag(p.protocol).kind">{{ protocolTag(p.protocol).label }}</Badge>
                </td>
                <td class="small">{{ p.service || '—' }}</td>
                <td>
                  <span v-if="pendingPorts.has(p.id)" class="badge accent">
                    <span class="spin">◌</span> 添加中…
                  </span>
                  <Badge v-else :kind="portStatusInfo(p.status).kind">{{ portStatusInfo(p.status).label }}</Badge>
                </td>
                <td class="small mute">{{ truncate(p.banner, 40) || '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>

        <div class="row wrap" style="margin-top: 12px; gap: 10px; align-items: center">
          <button class="btn" :disabled="actionBusy.policy || !portSel.size || !canWrite()" @click="openPolicyDialog">
            <span v-if="actionBusy.policy" class="spin">◌</span>生成监控策略…（{{ selectedPorts.length }}）
          </button>
          <button class="btn primary" :disabled="actionBusy.rules || !portSel.size || !canWrite()" @click="addToRules">
            <span v-if="actionBusy.rules" class="spin">◌</span>
            {{ actionBusy.rules ? '正在添加…' : `添加到映射规则（${selectedPorts.length}）` }}
          </button>
        </div>
        <div class="small dim" style="margin-top: 6px">
          生成监控策略会先弹出表单（已预填端口与主机 MAC），可修改全部字段后再保存；
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
        按 MAC <span class="mono">{{ monitorHost.mac }}</span> 绑定，当前地址
        <span class="mono">{{ addrSummary(monitorHost) }}</span>。IP 变化后策略会自动跟随，无需重设。
      </div>

      <label v-if="policyTargets.length > 1" class="field row" style="gap: 10px; margin-bottom: 6px">
        <Switch v-model="applyToAll" />
        <span class="small dim">
          应用到所选全部 {{ policyTargets.length }} 台主机（每台主机使用各自被选中的端口）
        </span>
      </label>

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
                    <button class="btn sm ghost danger" :disabled="!canWrite()" @click="removePolicy(p)">删除</button>
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

    <!-- 主机名：绑定到 MAC，可手动修正（PATCH /api/hosts/{id}） -->
    <Modal v-if="renameHost" title="设置主机名" @close="closeRename">
      <div class="small dim" style="margin-bottom: 12px">
        名称绑定到 MAC <span class="mono">{{ renameHost.mac || '（无 MAC）' }}</span>：
        同一块网卡换 IP 后仍沿用这个名称。后端会先尝试 PTR 解析，这里手动填写的值优先并固定下来。
      </div>
      <label class="field">
        <span>主机名</span>
        <input
          v-model="hostnameDraft"
          class="input"
          placeholder="例如 router.lan、nas 或 路由器"
          @keydown.enter.prevent="saveHostname"
        />
        <div class="hint">留空保存 = 清除手动名称，回到 PTR 解析结果。</div>
      </label>
      <template #footer>
        <button class="btn" @click="closeRename">取消</button>
        <button class="btn primary" :disabled="savingName" @click="saveHostname">
          <span v-if="savingName" class="spin">◌</span>保存
        </button>
      </template>
    </Modal>
  </div>
</template>
