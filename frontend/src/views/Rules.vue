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

const loading = ref(true)
const rules = ref([])
const total = ref(0)
const allTags = ref({})
const selected = ref(new Set())
const filters = reactive({ q: '', tags: [], enabled: '', protocol: '', sort: 'id', order: 'desc', limit: 200 })
const showEditor = ref(false)
const editing = ref(null)
const busy = reactive({})
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

onMounted(load)
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
      <button class="btn" @click="load"><span v-if="loading" class="spin">◌</span>刷新</button>
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
                <th style="width: 34px">
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
              <tr v-for="r in rules" :key="r.id" :class="{ selected: selected.has(r.id) }">
                <td><input type="checkbox" :checked="selected.has(r.id)" @change="toggleSelect(r.id)" /></td>
                <td class="mono small mute">{{ r.id }}</td>
                <td>
                  <Badge :kind="statusInfo(r.mapping_status).kind" :dot="r.mapping_status === 'active'">
                    {{ statusInfo(r.mapping_status).label }}
                  </Badge>
                  <div v-if="r.last_error" class="small" style="color: var(--danger); margin-top: 3px" :title="r.last_error">
                    {{ truncate(r.last_error, 28) }}
                  </div>
                  <div v-else-if="r.conflict_with" class="small mute" style="margin-top: 3px">与 #{{ r.conflict_with }} 冲突</div>
                </td>
                <td class="mono small nowrap">{{ r.host }}:{{ r.port }}</td>
                <td class="mono small nowrap">
                  <span v-if="r.external_port">{{ r.external_ip || '?' }}:{{ r.external_port }}</span>
                  <span v-else class="mute">—</span>
                  <div v-if="r.provider" class="small mute">{{ shortProvider(r.provider) }}</div>
                </td>
                <td>
                  <Badge :kind="protocolTag(r.protocol).kind">{{ protocolTag(r.protocol).label }}</Badge>
                </td>
                <td>
                  <span v-for="t in r.tags" :key="t" class="tag">{{ t }}</span>
                  <span v-if="!r.tags || !r.tags.length" class="mute small">—</span>
                </td>
                <td>
                  <div>{{ r.name || '(未命名)' }}</div>
                  <div v-if="r.note" class="small mute">{{ truncate(r.note, 40) }}</div>
                </td>
                <td>
                  <Switch :model-value="r.enabled" :disabled="!canWrite()" @update:model-value="(v) => toggleEnabled(r, v)" />
                </td>
                <td>
                  <div class="cell-actions">
                    <button class="btn sm" :disabled="busy['apply' + r.id]" @click="apply(r)">同步</button>
                    <button v-if="canWrite()" class="btn sm ghost" @click="openEdit(r)">编辑</button>
                    <button v-if="canWrite()" class="btn sm danger" @click="remove(r)">删除</button>
                  </div>
                </td>
              </tr>
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
