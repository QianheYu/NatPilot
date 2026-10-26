<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import api from '../api'
import Badge from '../components/Badge.vue'
import EmptyState from '../components/EmptyState.vue'
import Modal from '../components/Modal.vue'
import TagInput from '../components/TagInput.vue'
import Switch from '../components/Switch.vue'
import { toastOk, toastErr, toastWarn, canWrite } from '../store'
import { fmtRelative, protocolTag, shortProvider, statusInfo, truncate } from '../utils/format'
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
const rules = ref([])
const total = ref(0)
const allTags = ref({})
const selected = ref(new Set())
const filters = reactive({ q: '', tags: [], enabled: '', protocol: '', sort: 'id', order: 'desc', limit: 200 })
const showEditor = ref(false)
const editing = ref(null)
const busy = reactive({})

// 树形分组：网段 → 主机 → 端口行；两级均可折叠，行内还能展开详情。
const hosts = ref([])
const grouped = ref(readGroupPref())
const collapsedSubnets = ref(readCollapsed('subnet'))
const collapsedHosts = ref(readCollapsed('host'))
const detailRows = ref(new Set())

const hostIndex = computed(() => buildHostIndex(hosts.value))
const tree = computed(() => groupBySubnet(rules.value, hostIndex.value, (r) => r.host, (r) => r.port))
const tableRows = computed(() =>
  grouped.value
    ? rowsForTree(tree.value, collapsedSubnets.value, collapsedHosts.value)
    : rules.value.map((r) => ({ type: 'row', item: r })),
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

// 主机表用于 IP→MAC 解析；失败时分组自动退化为按 IP。
async function loadHosts() {
  try {
    const res = await api.hosts({ limit: 2000 })
    hosts.value = res.hosts || []
  } catch {
    hosts.value = []
  }
}

function reload() {
  load()
  loadHosts()
}
const confirmState = ref(null)

const form = reactive({
  name: '',
  host: '127.0.0.1',
  port: '',
  ext_port: '',
  protocol: 'tcp',
  tags: [],
  note: '',
  enabled: true,
})

const tagSuggestions = computed(() => Object.keys(allTags.value || {}))

async function load() {
  loading.value = true
  try {
    const res = await api.rules({
      q: filters.q,
      tag: filters.tags,
      enabled: filters.enabled,
      protocol: filters.protocol,
      sort: filters.sort,
      order: filters.order,
      limit: filters.limit,
    })
    rules.value = res.rules || []
    total.value = res.total || 0
    allTags.value = res.all_tags || {}
    selected.value = new Set()
  } catch (err) {
    toastErr(err.message, '加载规则失败')
  } finally {
    loading.value = false
  }
}

useLive((ev) => {
  if (ev.type === 'changed') load()
})

function openCreate() {
  editing.value = null
  Object.assign(form, {
    name: '',
    host: '127.0.0.1',
    port: '',
    ext_port: '',
    protocol: 'tcp',
    tags: [],
    note: '',
    enabled: true,
  })
  showEditor.value = true
}

function openEdit(rule) {
  editing.value = rule
  Object.assign(form, {
    name: rule.name,
    host: rule.host,
    port: rule.port,
    ext_port: rule.ext_port || '',
    protocol: rule.protocol,
    tags: [...(rule.tags || [])],
    note: rule.note || '',
    enabled: rule.enabled,
  })
  showEditor.value = true
}

async function save() {
  const payload = {
    name: form.name,
    host: form.host || '127.0.0.1',
    port: Number(form.port),
    ext_port: form.ext_port === '' ? 0 : Number(form.ext_port),
    protocol: form.protocol,
    tags: form.tags,
    note: form.note,
    enabled: form.enabled,
  }
  if (!payload.port || payload.port < 1 || payload.port > 65535) {
    toastErr('内部端口必须在 1-65535 之间')
    return
  }
  busy.save = true
  try {
    if (editing.value) {
      const res = await api.updateRule(editing.value.id, payload)
      toastOk(`已更新 #${editing.value.id}`)
      if (res.result && res.result.apply_error) toastWarn(res.result.apply_error, '网关返回')
    } else {
      const res = await api.createRule(payload)
      toastOk(`已创建 #${res.rule.id}`)
      if (res.result && res.result.apply_error) toastWarn(res.result.apply_error, '网关返回')
    }
    showEditor.value = false
    load()
  } catch (err) {
    toastErr(err.message, '保存失败')
  } finally {
    busy.save = false
  }
}

async function toggleEnabled(rule, value) {
  const prev = rule.enabled
  rule.enabled = value
  try {
    await api.toggleRule(rule.id, value)
    toastOk(`#${rule.id} 已${value ? '启用' : '停用'}`)
  } catch (err) {
    rule.enabled = prev
    toastErr(err.message, '切换失败')
  }
}

async function apply(rule) {
  busy['apply' + rule.id] = true
  try {
    const res = await api.applyRule(rule.id)
    if (res.error) toastWarn(res.error, '映射失败')
    else toastOk(`#${rule.id} 映射已生效`)
    load()
  } catch (err) {
    toastErr(err.message, '同步失败')
  } finally {
    busy['apply' + rule.id] = false
  }
}

async function remove(rule) {
  confirmState.value = { type: 'rule', item: rule }
}

async function doRemove() {
  const rule = confirmState.value && confirmState.value.item
  if (!rule) return
  try {
    const res = await api.deleteRule(rule.id)
    toastOk(`已删除 #${rule.id}`)
    if (res.gateway_error) toastWarn(res.gateway_error, '网关清理提示')
    confirmState.value = null
    load()
  } catch (err) {
    toastErr(err.message, '删除失败')
  }
}

function toggleSelect(id) {
  const s = new Set(selected.value)
  if (s.has(id)) s.delete(id)
  else s.add(id)
  selected.value = s
}

function toggleSelectAll() {
  if (selected.value.size === rules.value.length) selected.value = new Set()
  else selected.value = new Set(rules.value.map((r) => r.id))
}

async function bulk(action, extra = {}) {
  const ids = [...selected.value]
  if (!ids.length) return
  busy.bulk = true
  try {
    const res = await api.bulkRules({ ids, action, ...extra })
    toastOk(`批量操作完成：${JSON.stringify(res.result)}`)
    load()
  } catch (err) {
    toastErr(err.message, '批量操作失败')
  } finally {
    busy.bulk = false
  }
}

onMounted(() => {
  load()
  loadHosts()
})
</script>

<template>
  <div>
    <div class="toolbar">
      <input v-model="filters.q" class="input grow" style="max-width: 240px" placeholder="搜索名称 / 主机 / 端口 / 备注" @keydown.enter="load" />
      <select v-model="filters.enabled" class="select" style="width: 118px" @change="load">
        <option value="">全部状态</option>
        <option value="true">已启用</option>
        <option value="false">已停用</option>
      </select>
      <select v-model="filters.protocol" class="select" style="width: 108px" @change="load">
        <option value="">全部协议</option>
        <option value="tcp">TCP</option>
        <option value="udp">UDP</option>
      </select>
      <select v-model="filters.tags" class="select" style="width: 150px" multiple size="1" @change="load">
        <option v-for="(c, t) in allTags" :key="t" :value="t">{{ t }} ({{ c }})</option>
      </select>
      <button class="btn" :class="grouped ? 'primary' : ''" title="按主机（优先 MAC，其次 IP）分组显示" @click="toggleGrouped">
        {{ grouped ? '已按主机分组' : '按主机分组' }}
      </button>
      <button v-if="grouped" class="btn sm" @click="toggleAllGroups">{{ allCollapsed ? '全部展开' : '全部折叠' }}</button>
      <button class="btn" @click="reload"><span v-if="loading" class="spin">◌</span>刷新</button>
      <div class="spacer" style="flex: 1" />
      <template v-if="selected.size">
        <span class="badge accent">已选 {{ selected.size }}</span>
        <button class="btn sm" :disabled="busy.bulk" @click="bulk('enable')">批量启用</button>
        <button class="btn sm" :disabled="busy.bulk" @click="bulk('disable')">批量停用</button>
        <button class="btn sm danger" :disabled="busy.bulk" @click="bulk('delete')">批量删除</button>
      </template>
      <button v-if="canWrite()" class="btn primary" @click="openCreate">+ 新增映射</button>
    </div>

    <div class="card">
      <div class="card-body flush">
        <EmptyState v-if="!rules.length && !loading" icon="⇄" title="还没有端口映射" hint="点击右上角“新增映射”，或先在“端口发现”里扫描后一键添加">
          <button v-if="canWrite()" class="btn primary" @click="openCreate">新增映射</button>
        </EmptyState>
        <div v-else class="table-wrap">
          <table class="data">
            <thead>
              <tr>
                <th style="width: 62px">
                  <input type="checkbox" :checked="selected.size === rules.length && rules.length > 0" @change="toggleSelectAll" />
                </th>
                <th style="width: 46px">ID</th>
                <th>状态</th>
                <th>目标 (内网)</th>
                <th>外部映射</th>
                <th>协议</th>
                <th>标签</th>
                <th>名称 / 备注</th>
                <th style="width: 66px">启用</th>
                <th class="right" style="width: 150px">操作</th>
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
                    <td class="mono small mute">{{ row.item.id }}</td>
                    <td>
                      <Badge :kind="statusInfo(row.item.mapping_status).kind" :dot="row.item.mapping_status === 'active'">
                        {{ statusInfo(row.item.mapping_status).label }}
                      </Badge>
                    </td>
                    <td class="mono small nowrap">{{ row.item.host }}:{{ row.item.port }}</td>
                    <td class="mono small nowrap">
                      <span v-if="row.item.external_port">{{ row.item.external_ip || '?' }}:{{ row.item.external_port }}</span>
                      <span v-else class="mute">—</span>
                    </td>
                    <td>
                      <Badge :kind="protocolTag(row.item.protocol).kind">{{ protocolTag(row.item.protocol).label }}</Badge>
                    </td>
                    <td>
                      <span v-for="t in row.item.tags" :key="t" class="tag">{{ t }}</span>
                      <span v-if="!row.item.tags || !row.item.tags.length" class="mute small">—</span>
                    </td>
                    <td><div>{{ row.item.name || '(未命名)' }}</div></td>
                    <td>
                      <Switch :model-value="row.item.enabled" :disabled="!canWrite()" @update:model-value="(v) => toggleEnabled(row.item, v)" />
                    </td>
                    <td>
                      <div class="cell-actions">
                        <button class="btn sm" :disabled="busy['apply' + row.item.id]" @click="apply(row.item)">同步</button>
                        <button v-if="canWrite()" class="btn sm ghost" @click="openEdit(row.item)">编辑</button>
                        <button v-if="canWrite()" class="btn sm danger" @click="remove(row.item)">删除</button>
                      </div>
                    </td>
                  </tr>
                  <tr v-if="detailRows.has(row.item.id)" class="detail-row">
                    <td :colspan="10">
                      <div class="row wrap" style="gap: 20px">
                        <span class="small"><span class="mute">规则 ID</span> <span class="mono">#{{ row.item.id }}</span></span>
                        <span class="small"><span class="mute">目标</span> <span class="mono">{{ row.item.host }}:{{ row.item.port }}/{{ row.item.protocol }}</span></span>
                        <span class="small"><span class="mute">外部映射</span> <span class="mono">{{ row.item.external_port ? (row.item.external_ip || '?') + ':' + row.item.external_port : '—' }}</span></span>
                        <span class="small"><span class="mute">网关</span> {{ shortProvider(row.item.provider) || '—' }}</span>
                        <span class="small"><span class="mute">来源</span> {{ row.item.source || '—' }}</span>
                        <span class="small"><span class="mute">更新</span> {{ fmtRelative(row.item.updated_at) }}</span>
                      </div>
                      <div v-if="row.item.note" class="small" style="margin-top: 6px">
                        <span class="mute">备注</span> {{ row.item.note }}
                      </div>
                      <div v-if="row.item.last_error" class="small" style="margin-top: 6px; color: var(--danger)">
                        最近错误 {{ row.item.last_error }}
                      </div>
                      <div v-if="row.item.conflict_with" class="small" style="margin-top: 6px; color: var(--warn)">
                        与规则 #{{ row.item.conflict_with }} 冲突
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
      共 {{ total }} 条规则 · 只有“启用”的规则会向网关请求端口映射 · 最近刷新 {{ fmtRelative(new Date()) }}
    </div>

    <Modal v-if="showEditor" :title="editing ? `编辑规则 #${editing.id}` : '新增端口映射'" @close="showEditor = false">
      <div class="grid cols-2">
        <label class="field">
          <span>名称</span>
          <input v-model="form.name" class="input" placeholder="如 NAS 管理后台" />
        </label>
        <label class="field">
          <span>内网主机 (目标)</span>
          <input v-model="form.host" class="input" placeholder="127.0.0.1 或 192.168.1.20" />
          <div class="hint">留空或写 self 表示本机</div>
        </label>
      </div>
      <div class="grid cols-3">
        <label class="field">
          <span>内网端口 *</span>
          <input v-model="form.port" class="input" type="number" min="1" max="65535" placeholder="8080" />
        </label>
        <label class="field">
          <span>外部端口</span>
          <input v-model="form.ext_port" class="input" type="number" min="0" max="65535" placeholder="留空=与内网相同" />
        </label>
        <label class="field">
          <span>协议</span>
          <select v-model="form.protocol" class="select">
            <option value="tcp">TCP</option>
            <option value="udp">UDP</option>
          </select>
        </label>
      </div>
      <label class="field">
        <span>标签</span>
        <TagInput v-model="form.tags" :suggestions="tagSuggestions" />
      </label>
      <label class="field">
        <span>备注</span>
        <textarea v-model="form.note" class="input" placeholder="用途、负责人、到期时间等"></textarea>
      </label>
      <label class="field row" style="gap: 10px">
        <Switch v-model="form.enabled" />
        <span class="small dim">保存后立即启用并请求映射</span>
      </label>
      <template #footer>
        <button class="btn" @click="showEditor = false">取消</button>
        <button class="btn primary" :disabled="busy.save" @click="save">
          <span v-if="busy.save" class="spin">◌</span>保存
        </button>
      </template>
    </Modal>

    <Modal v-if="confirmState" title="确认删除" @close="confirmState = null">
      <p>
        确定要删除规则
        <b class="mono">#{{ confirmState.item.id }} {{ confirmState.item.host }}:{{ confirmState.item.port }}</b>
        吗？该操作会同时向网关撤销映射（若已开启）。
      </p>
      <template #footer>
        <button class="btn" @click="confirmState = null">取消</button>
        <button class="btn danger" @click="doRemove">删除</button>
      </template>
    </Modal>
  </div>
</template>
