<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import api from '../api'
import Badge from '../components/Badge.vue'
import EmptyState from '../components/EmptyState.vue'
import Modal from '../components/Modal.vue'
import TagInput from '../components/TagInput.vue'
import Switch from '../components/Switch.vue'
import { toastOk, toastErr, toastWarn, canWrite } from '../store'
import { fmtRelative, portStatusInfo, protocolTag, truncate } from '../utils/format'
import { useLive } from '../composables/useLive'
import {
  buildHostIndex,
  groupBySubnet,
  readCollapsed,
  readGroupPref,
  rowKey,
  rowsForTree,
  writeCollapsed,
  writeGroupPref,
} from '../composables/useHostGroups'

const loading = ref(true)
const ports = ref([])
const total = ref(0)
const counts = ref({})
const selected = ref(new Set())
const filters = reactive({ q: '', status: '', host: '', limit: 500, open_only: true })
const showScan = ref(false)
const showEditor = ref(false)
const editing = ref(null)
const busy = reactive({})

// 按主机分组（MAC 优先，解析不到退化为 IP），偏好与折叠状态持久化。
// 树形分组：网段 → 主机 → 端口行；两级均可折叠，行内还能展开详情。
const hosts = ref([])
const grouped = ref(readGroupPref())
const collapsedSubnets = ref(readCollapsed('subnet'))
const collapsedHosts = ref(readCollapsed('host'))
const detailRows = ref(new Set())

const hostIndex = computed(() => buildHostIndex(hosts.value))
const tree = computed(() => groupBySubnet(ports.value, hostIndex.value, (p) => p.host, (p) => p.port))
const tableRows = computed(() =>
  grouped.value
    ? rowsForTree(tree.value, collapsedSubnets.value, collapsedHosts.value)
    : ports.value.map((p) => ({ type: 'row', item: p })),
)
const allCollapsed = computed(
  () => tree.value.length > 0 && tree.value.every((n) => collapsedSubnets.value.has(n.key)),
)

function toggleGrouped() {
  grouped.value = !grouped.value
  writeGroupPref(grouped.value)
}

function toggleSubnet(key) {
  const next = new Set(collapsedSubnets.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  collapsedSubnets.value = next
  writeCollapsed('subnet', next)
}

function toggleHost(key) {
  const next = new Set(collapsedHosts.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  collapsedHosts.value = next
  writeCollapsed('host', next)
}

function toggleDetail(id) {
  const next = new Set(detailRows.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  detailRows.value = next
}

// 「全部折叠」两级都收起；「全部展开」两级都放开。
const everythingCollapsed = computed(
  () =>
    allCollapsed.value &&
    tree.value.every((n) => n.hosts.every((h) => collapsedHosts.value.has(h.key))),
)

function toggleAllGroups() {
  const sn = new Set()
  const hn = new Set()
  if (!everythingCollapsed.value) {
    tree.value.forEach((n) => {
      sn.add(n.key)
      n.hosts.forEach((h) => hn.add(h.key))
    })
  }
  collapsedSubnets.value = sn
  collapsedHosts.value = hn
  writeCollapsed('subnet', sn)
  writeCollapsed('host', hn)
}

function reload() {
  load()
  loadHosts()
}const editor = reactive({ id: null, tags: [], note: '' })

const statusOptions = [
  { value: '', label: '全部状态' },
  { value: 'not_added', label: '未添加' },
  { value: 'open', label: '已开启（未管理）' },
  { value: 'added', label: '已添加' },
  { value: 'closed', label: '已关闭' },
  { value: 'source_gone', label: '源已删除' },
]

const scanForm = reactive({
  target_kind: 'local',
  target_ref: '',
  ports_spec: '',
  protocol: '',
  apply_policy: true,
  add_open: false,
  wait: true,
})
const scanPreview = ref(null)
const scanning = ref(false)

const statChips = computed(() =>
  Object.entries(counts.value || {}).map(([k, v]) => ({ status: k, count: v, info: portStatusInfo(k) })),
)

async function load() {
  loading.value = true
  try {
    const res = await api.ports({
      q: filters.q,
      status: filters.status,
      host: filters.host,
      limit: filters.limit,
      open_only: filters.open_only ? 'true' : '',
    })
    ports.value = res.ports || []
    total.value = res.total || 0
    counts.value = res.counts || {}
    selected.value = new Set()
  } catch (err) {
    toastErr(err.message, '加载端口失败')
  } finally {
    loading.value = false
  }
}

useLive((ev) => {
  if (ev.type === 'changed' || ev.type === 'scan-progress') load()
})

async function preview() {
  scanPreview.value = null
  try {
    scanPreview.value = await api.previewScan({ ...scanForm, ports_spec: scanForm.ports_spec || undefined })
  } catch (err) {
    toastErr(err.message, '预览失败')
  }
}

async function runScan() {
  scanning.value = true
  try {
    const body = {
      target_kind: scanForm.target_kind,
      target_ref: scanForm.target_ref,
      ports_spec: scanForm.ports_spec || undefined,
      apply_policy: scanForm.apply_policy,
      wait: scanForm.wait,
    }
    const res = await api.startScan(body)
    const run = res.scan
    if (scanForm.add_open && run) {
      await addOpenFromScan(run.id)
    }
    toastOk(`扫描 #${run.id} 完成：${run.open_count || 0} 个开放端口`)
    showScan.value = false
    load()
  } catch (err) {
    toastErr(err.message, '扫描失败')
  } finally {
    scanning.value = false
  }
}

async function addOpenFromScan(scanId) {
  try {
    const res = await api.promotePorts({ statuses: ['open', 'not_added'], tags: ['scan'] })
    toastOk(`已添加 ${res.added} 个端口，映射生效 ${res.applied} 个`)
  } catch (err) {
    toastWarn(err.message, '自动添加部分失败')
  }
}

async function decision(port, kind) {
  busy['p' + port.id + kind] = true
  try {
    await api.portAction(port.id, { decision: kind })
    const map = { add: '已加入管理列表', ignore: '已忽略', watch: '标记为待观察', delete: '已从列表删除' }
    toastOk(map[kind] || '已完成')
    load()
  } catch (err) {
    toastErr(err.message, '操作失败')
  } finally {
    busy['p' + port.id + kind] = false
  }
}

async function bulkAdd() {
  const ids = [...selected.value]
  if (!ids.length) return
  busy.bulk = true
  try {
    const res = await api.addPorts({ ids, tags: ['ui'], enabled: true })
    toastOk(`已添加 ${res.added} 个端口，映射生效 ${res.applied} 个`)
    if (res.failed) toastWarn(`${res.failed} 个端口处理失败`)
    load()
  } catch (err) {
    toastErr(err.message, '批量添加失败')
  } finally {
    busy.bulk = false
  }
}

async function bulkDelete() {
  const ids = [...selected.value]
  if (!ids.length) return
  busy.bulk = true
  try {
    const res = await api.bulkPorts({ ids, action: 'delete' })
    toastOk(`已删除 ${res.result.deleted} 个端口`)
    load()
  } catch (err) {
    toastErr(err.message, '批量删除失败')
  } finally {
    busy.bulk = false
  }
}

async function clearClosed() {
  busy.bulk = true
  try {
    const res = await api.bulkPorts({ statuses: ['closed', 'source_gone'], action: 'clear' })
    toastOk(`已清理 ${res.deleted} 条无效记录`)
    load()
  } catch (err) {
    toastErr(err.message, '清理失败')
  } finally {
    busy.bulk = false
  }
}

function openEditor(port) {
  editing.value = port
  editor.id = port.id
  editor.tags = [...(port.tags || [])]
  editor.note = port.note || ''
  showEditor.value = true
}

async function saveEditor() {
  try {
    await api.patchPort(editor.id, { tags: editor.tags, note: editor.note })
    toastOk('已保存端口标签与备注')
    showEditor.value = false
    load()
  } catch (err) {
    toastErr(err.message, '保存失败')
  }
}

function toggleSelect(id) {
  const s = new Set(selected.value)
  if (s.has(id)) s.delete(id)
  else s.add(id)
  selected.value = s
}

function toggleAll() {
  if (selected.value.size === ports.value.length) selected.value = new Set()
  else selected.value = new Set(ports.value.map((p) => p.id))
}

function applyStatusChip(status) {
  filters.status = filters.status === status ? '' : status
  load()
}

onMounted(() => {
  load()
  loadHosts()
})
</script>

<template>
  <div>
    <div class="toolbar">
      <input v-model="filters.q" class="input grow" style="max-width: 220px" placeholder="搜索主机 / 端口 / 进程 / 服务" @keydown.enter="load" />
      <select v-model="filters.status" class="select" style="width: 160px" @change="load">
        <option v-for="o in statusOptions" :key="o.value" :value="o.value">{{ o.label }}</option>
      </select>
      <label class="row" style="gap: 6px">
        <Switch v-model="filters.open_only" @update:model-value="load" />
        <span class="small dim">只看存活</span>
      </label>
      <button class="btn" :class="grouped ? 'primary' : ''" title="按主机（优先 MAC，其次 IP）分组显示" @click="toggleGrouped">
        {{ grouped ? '已按主机分组' : '按主机分组' }}
      </button>
      <button v-if="grouped" class="btn sm" @click="toggleAllGroups">{{ allCollapsed ? '全部展开' : '全部折叠' }}</button>
      <button class="btn" @click="reload"><span v-if="loading" class="spin">◌</span>刷新</button>
      <button class="btn" :disabled="busy.bulk" @click="clearClosed">清理无效记录</button>
      <div class="spacer" style="flex: 1" />
      <template v-if="selected.size">
        <span class="badge accent">已选 {{ selected.size }}</span>
        <button class="btn sm primary" :disabled="busy.bulk" @click="bulkAdd">批量添加</button>
        <button class="btn sm danger" :disabled="busy.bulk" @click="bulkDelete">批量删除</button>
      </template>
      <button v-if="canWrite()" class="btn primary" @click="showScan = true">开始扫描</button>
    </div>

    <div class="row wrap" style="margin-bottom: 12px">
      <span
        v-for="c in statChips"
        :key="c.status"
        class="badge"
        :class="[c.info.kind, { dot: filters.status === c.status }]"
        style="cursor: pointer"
        @click="applyStatusChip(c.status)"
      >
        {{ c.info.label }} {{ c.count }}
      </span>
    </div>

    <div class="card">
      <div class="card-body flush">
        <EmptyState v-if="!ports.length && !loading" icon="⌗" title="暂无端口记录" hint="点击“开始扫描”发现本机或网段内的开放端口">
          <button v-if="canWrite()" class="btn primary" @click="showScan = true">开始扫描</button>
        </EmptyState>
        <div v-else class="table-wrap">
          <table class="data">
            <thead>
              <tr>
                <th style="width: 62px">
                  <input type="checkbox" :checked="selected.size === ports.length && ports.length > 0" @change="toggleAll" />
                </th>
                <th>状态</th>
                <th>主机:端口</th>
                <th>协议</th>
                <th>服务</th>
                <th>进程</th>
                <th>指纹 / 说明</th>
                <th>标签</th>
                <th>最近发现</th>
                <th class="right" style="width: 210px">操作</th>
              </tr>
            </thead>
                        <tbody>
              <template v-for="row in tableRows" :key="rowKey(row)">
                <tr v-if="row.type === 'subnet'" class="group-row subnet-row" @click="toggleSubnet(row.subnet.key)">
                  <td :colspan="10">
                    <span class="g-chev">{{ collapsedSubnets.has(row.subnet.key) ? '▸' : '▾' }}</span>
                    <span class="g-title">{{ row.subnet.label }}</span>
                    <span class="badge">{{ row.subnet.hosts.length }} 台主机</span>
                    <span class="badge accent">{{ row.subnet.count }} 个端口</span>
                  </td>
                </tr>
                <tr v-else-if="row.type === 'host'" class="group-row host-row" @click="toggleHost(row.host.key)">
                  <td :colspan="10">
                    <span class="g-chev">{{ collapsedHosts.has(row.host.key) ? '▸' : '▾' }}</span>
                    <span class="g-title">{{ row.host.title }}</span>
                    <span v-if="row.host.mac" class="g-mac mono">{{ row.host.mac }}</span>
                    <span class="g-ip mono">{{ row.host.mac ? row.host.subtitle : row.host.ip }}</span>
                    <span class="badge accent">{{ row.host.items.length }}</span>
                  </td>
                </tr>
                <template v-else>
                  <tr class="data-row" :class="{ selected: selected.has(row.item.id) }">
                    <td class="cell-exp">
                      <button
                        class="btn sm ghost exp"
                        :title="detailRows.has(row.item.id) ? '收起详情' : '展开详情'"
                        @click.stop="toggleDetail(row.item.id)"
                      >
                        {{ detailRows.has(row.item.id) ? '▾' : '▸' }}
                      </button>
                      <input type="checkbox" :checked="selected.has(row.item.id)" @change="toggleSelect(row.item.id)" />
                    </td>
                    <td>
                      <Badge :kind="portStatusInfo(row.item.status).kind">{{ portStatusInfo(row.item.status).label }}</Badge>
                    </td>
                    <td class="mono small nowrap">{{ row.item.host }}:{{ row.item.port }}</td>
                    <td><Badge :kind="protocolTag(row.item.protocol).kind">{{ protocolTag(row.item.protocol).label }}</Badge></td>
                    <td class="small">{{ row.item.service || '—' }}</td>
                    <td class="small dim">{{ truncate(row.item.process, 30) || '—' }}</td>
                    <td class="small mute">{{ truncate(row.item.banner, 42) || '—' }}</td>
                    <td>
                      <span v-for="t in row.item.tags" :key="t" class="tag">{{ t }}</span>
                      <span v-if="!row.item.tags || !row.item.tags.length" class="mute small">—</span>
                    </td>
                    <td class="small mute nowrap">{{ fmtRelative(row.item.last_seen) }}</td>
                    <td>
                      <div class="cell-actions">
                        <button
                          v-if="!row.item.added"
                          class="btn sm primary"
                          :disabled="!canWrite() || busy['p' + row.item.id + 'add']"
                          @click="decision(row.item, 'add')"
                        >
                          添加到列表
                        </button>
                        <button
                          v-if="!row.item.ignored"
                          class="btn sm"
                          :disabled="!canWrite()"
                          @click="decision(row.item, 'ignore')"
                        >
                          忽略
                        </button>
                        <button class="btn sm ghost" :disabled="!canWrite()" @click="openEditor(row.item)">备注</button>
                        <button class="btn sm danger" :disabled="!canWrite()" @click="decision(row.item, 'delete')">删除</button>
                      </div>
                    </td>
                  </tr>
                  <tr v-if="detailRows.has(row.item.id)" class="detail-row">
                    <td :colspan="10">
                      <div class="row wrap" style="gap: 20px">
                        <span class="small"><span class="mute">进程</span> <span class="mono">{{ row.item.process || '—' }}</span></span>
                        <span class="small"><span class="mute">PID</span> <span class="mono">{{ row.item.pid || '—' }}</span></span>
                        <span class="small"><span class="mute">服务</span> {{ row.item.service || '—' }}</span>
                        <span class="small"><span class="mute">延迟</span> {{ row.item.latency_ms ? row.item.latency_ms + ' ms' : '—' }}</span>
                        <span class="small"><span class="mute">最近发现</span> {{ fmtRelative(row.item.last_seen) }}</span>
                        <span class="small"><span class="mute">来源规则</span> <span class="mono">{{ row.item.rule_id ? '#' + row.item.rule_id : '—' }}</span></span>
                      </div>
                      <div v-if="row.item.banner" class="small mono" style="margin-top: 6px">
                        <span class="mute">指纹</span> {{ row.item.banner }}
                      </div>
                      <div v-if="row.item.tags && row.item.tags.length" class="row wrap" style="margin-top: 6px; gap: 6px">
                        <span v-for="t in row.item.tags" :key="t" class="tag">{{ t }}</span>
                      </div>
                    </td>
                  </tr>
                </template>
              </template>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <div class="dim small" style="margin-top: 12px">
      共 {{ total }} 条 · “已添加”表示该端口已在映射管理列表中；“源已删除”表示规则仍存在但端口已不再监听。
    </div>

    <!-- scan dialog -->
    <Modal v-if="showScan" title="新建端口扫描" wide @close="showScan = false">
      <div class="grid cols-2">
        <label class="field">
          <span>扫描范围</span>
          <select v-model="scanForm.target_kind" class="select">
            <option value="local">本机（监听端口）</option>
            <option value="cidr">局域网 / 网段（CIDR）</option>
            <option value="mac">按 MAC 地址（IP 变动也能找到）</option>
          </select>
        </label>
        <label class="field">
          <span>{{ scanForm.target_kind === 'mac' ? 'MAC 地址 / 前缀' : '网段 CIDR' }}</span>
          <input
            v-model="scanForm.target_ref"
            class="input"
            :placeholder="scanForm.target_kind === 'mac' ? 'aa:bb:cc:dd:ee:ff 或 aa:bb:cc:*，多个用逗号分隔' : '192.168.1.0/24'"
            :disabled="scanForm.target_kind === 'local'"
          />
          <div v-if="scanForm.target_kind === 'mac'" class="hint">按主机清单中的 MAC 解析当前 IP 后再扫描，DHCP 换址不影响。</div>
        </label>
      </div>
      <label class="field">
        <span>端口范围</span>
        <input v-model="scanForm.ports_spec" class="input" placeholder="留空使用默认；例如 1-1024,3000,8000-8100" />
        <div class="hint">支持逗号分隔与区间；网段扫描请控制规模，避免占用过多带宽。</div>
      </label>
      <div class="grid cols-2">
        <label class="field row" style="gap: 10px">
          <Switch v-model="scanForm.apply_policy" />
          <span class="small dim">套用已启用的策略</span>
        </label>
        <label class="field row" style="gap: 10px">
          <Switch v-model="scanForm.wait" />
          <span class="small dim">同步等待结果</span>
        </label>
      </div>
      <label class="field row" style="gap: 10px">
        <Switch v-model="scanForm.add_open" />
        <span class="small dim">扫描后自动把开放端口加入映射列表</span>
      </label>

      <div v-if="scanPreview" class="card" style="background: var(--bg-elev-2)">
        <div class="card-body">
          <div class="row wrap">
            <span class="badge">目标 {{ scanPreview.target }}</span>
            <span class="badge">主机 {{ scanPreview.hosts }}</span>
            <span class="badge">端口 {{ scanPreview.ports }}</span>
            <span class="badge">预计 {{ scanPreview.estimated_seconds }} 秒</span>
            <span class="badge" :class="scanPreview.policies && scanPreview.policies.length ? 'accent' : 'warn'">
              策略 {{ (scanPreview.policies || []).length }}
            </span>
          </div>
          <div v-if="scanPreview.warnings && scanPreview.warnings.length" style="margin-top: 8px">
            <div v-for="w in scanPreview.warnings" :key="w" class="small" style="color: var(--warn)">⚠ {{ w }}</div>
          </div>
        </div>
      </div>

      <template #footer>
        <button class="btn" @click="preview">预览规模</button>
        <button class="btn" @click="showScan = false">取消</button>
        <button class="btn primary" :disabled="scanning" @click="runScan">
          <span v-if="scanning" class="spin">◌</span>开始扫描
        </button>
      </template>
    </Modal>

    <!-- port note/tag editor -->
    <Modal v-if="showEditor" :title="`端口标签与备注 #${editor.id}`" @close="showEditor = false">
      <label class="field">
        <span>标签</span>
        <TagInput v-model="editor.tags" />
      </label>
      <label class="field">
        <span>备注</span>
        <textarea v-model="editor.note" class="input"></textarea>
      </label>
      <template #footer>
        <button class="btn" @click="showEditor = false">取消</button>
        <button class="btn primary" @click="saveEditor">保存</button>
      </template>
    </Modal>
  </div>
</template>
