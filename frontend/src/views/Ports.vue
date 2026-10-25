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
const editor = reactive({ id: null, tags: [], note: '' })

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

onMounted(load)
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
      <button class="btn" @click="load"><span v-if="loading" class="spin">◌</span>刷新</button>
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
                <th style="width: 34px">
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
              <tr v-for="p in ports" :key="p.id" :class="{ selected: selected.has(p.id) }">
                <td><input type="checkbox" :checked="selected.has(p.id)" @change="toggleSelect(p.id)" /></td>
                <td>
                  <Badge :kind="portStatusInfo(p.status).kind">{{ portStatusInfo(p.status).label }}</Badge>
                </td>
                <td class="mono small nowrap">{{ p.host }}:{{ p.port }}</td>
                <td><Badge :kind="protocolTag(p.protocol).kind">{{ protocolTag(p.protocol).label }}</Badge></td>
                <td class="small">{{ p.service || '—' }}</td>
                <td class="small dim">{{ truncate(p.process, 30) || '—' }}</td>
                <td class="small mute">{{ truncate(p.banner, 42) || '—' }}</td>
                <td>
                  <span v-for="t in p.tags" :key="t" class="tag">{{ t }}</span>
                  <span v-if="!p.tags || !p.tags.length" class="mute small">—</span>
                </td>
                <td class="small mute nowrap">{{ fmtRelative(p.last_seen) }}</td>
                <td>
                  <div class="cell-actions">
                    <button
                      v-if="!p.added"
                      class="btn sm primary"
                      :disabled="!canWrite() || busy['p' + p.id + 'add']"
                      @click="decision(p, 'add')"
                    >
                      添加到列表
                    </button>
                    <button
                      v-if="!p.ignored"
                      class="btn sm"
                      :disabled="!canWrite()"
                      @click="decision(p, 'ignore')"
                    >
                      忽略
                    </button>
                    <button class="btn sm ghost" :disabled="!canWrite()" @click="openEditor(p)">备注</button>
                    <button class="btn sm danger" :disabled="!canWrite()" @click="decision(p, 'delete')">删除</button>
                  </div>
                </td>
              </tr>
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
          </select>
        </label>
        <label class="field">
          <span>网段 CIDR</span>
          <input v-model="scanForm.target_ref" class="input" placeholder="192.168.1.0/24" :disabled="scanForm.target_kind === 'local'" />
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
