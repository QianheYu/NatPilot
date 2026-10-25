<script setup>
import { onMounted, reactive, ref } from 'vue'
import api from '../api'
import Badge from '../components/Badge.vue'
import EmptyState from '../components/EmptyState.vue'
import Modal from '../components/Modal.vue'
import Switch from '../components/Switch.vue'
import { toastOk, toastErr, canWrite } from '../store'
import { fmtRelative, fmtTime } from '../utils/format'

const loading = ref(true)
const schedules = ref([])
const showEditor = ref(false)
const editing = ref(null)
const busy = reactive({})

const presets = [
  { label: '每 5 分钟', cron: '*/5 * * * *' },
  { label: '每 30 分钟', cron: '*/30 * * * *' },
  { label: '每小时', cron: '0 * * * *' },
  { label: '每天 3:00', cron: '0 3 * * *' },
  { label: '每周一 4:30', cron: '30 4 * * 1' },
]

const kinds = [
  { value: 'scan', label: '端口扫描' },
  { value: 'reconcile', label: '同步映射' },
  { value: 'cleanup', label: '清理失效映射' },
  { value: 'discover', label: '网关探测' },
]

const form = reactive({
  name: '',
  kind: 'scan',
  enabled: true,
  cron: '*/30 * * * *',
  target_kind: 'local',
  target_ref: '',
  ports_spec: '',
  policy_id: 0,
})

async function load() {
  loading.value = true
  try {
    const res = await api.schedules()
    schedules.value = res.schedules || []
  } catch (err) {
    toastErr(err.message, '加载计划任务失败')
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editing.value = null
  Object.assign(form, {
    name: '',
    kind: 'scan',
    enabled: true,
    cron: '*/30 * * * *',
    target_kind: 'local',
    target_ref: '',
    ports_spec: '',
    policy_id: 0,
  })
  showEditor.value = true
}

function openEdit(s) {
  editing.value = s
  Object.assign(form, {
    name: s.name,
    kind: s.kind,
    enabled: s.enabled,
    cron: s.cron,
    target_kind: s.target_kind,
    target_ref: s.target_ref,
    ports_spec: s.ports_spec,
    policy_id: s.policy_id,
  })
  showEditor.value = true
}

async function save() {
  busy.save = true
  try {
    if (editing.value) {
      await api.updateSchedule(editing.value.id, form)
      toastOk('计划任务已更新')
    } else {
      await api.createSchedule(form)
      toastOk('计划任务已创建')
    }
    showEditor.value = false
    load()
  } catch (err) {
    toastErr(err.message, '保存失败')
  } finally {
    busy.save = false
  }
}

async function toggle(s, v) {
  try {
    await api.updateSchedule(s.id, { ...s, enabled: v })
    load()
  } catch (err) {
    s.enabled = !v
    toastErr(err.message, '切换失败')
  }
}

async function remove(s) {
  try {
    await api.deleteSchedule(s.id)
    toastOk('已删除')
    load()
  } catch (err) {
    toastErr(err.message, '删除失败')
  }
}

const kindLabel = (k) => (kinds.find((x) => x.value === k) || {}).label || k

onMounted(load)
</script>

<template>
  <div>
    <div class="toolbar">
      <span class="dim small">共 {{ schedules.length }} 条计划任务 · 使用 5 段 cron 表达式（分 时 日 月 周）</span>
      <div class="spacer" style="flex: 1" />
      <button class="btn" @click="load"><span v-if="loading" class="spin">◌</span>刷新</button>
      <button v-if="canWrite()" class="btn primary" @click="openCreate">+ 新增计划</button>
    </div>

    <div class="card">
      <div class="card-body flush">
        <EmptyState v-if="!schedules.length && !loading" icon="◷" title="暂无计划任务"
          hint="例如“每 30 分钟扫描一次本机端口”或“每天凌晨同步映射”" />
        <div v-else class="table-wrap">
          <table class="data">
            <thead>
              <tr>
                <th>启用</th>
                <th>名称</th>
                <th>类型</th>
                <th>表达式</th>
                <th>目标</th>
                <th>上次运行</th>
                <th>状态</th>
                <th>运行次数</th>
                <th class="right">操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="s in schedules" :key="s.id">
                <td><Switch :model-value="s.enabled" :disabled="!canWrite()" @update:model-value="(v) => toggle(s, v)" /></td>
                <td>{{ s.name }}</td>
                <td><Badge>{{ kindLabel(s.kind) }}</Badge></td>
                <td class="mono small">{{ s.cron }}</td>
                <td class="small">
                  {{ s.kind === 'scan' ? (s.target_kind === 'local' ? '本机' : s.target_ref) : '—' }}
                  <div v-if="s.ports_spec" class="mute small mono">{{ s.ports_spec }}</div>
                </td>
                <td class="small mute nowrap">{{ s.last_run_at ? fmtTime(s.last_run_at, true) : '尚未运行' }}</td>
                <td class="small">
                  <span class="badge" :class="s.last_status && s.last_status.startsWith('error') ? 'danger' : 'ok'">
                    {{ s.last_status || '—' }}
                  </span>
                </td>
                <td class="small mute">{{ s.run_count }}</td>
                <td>
                  <div class="cell-actions">
                    <button class="btn sm ghost" :disabled="!canWrite()" @click="openEdit(s)">编辑</button>
                    <button class="btn sm danger" :disabled="!canWrite()" @click="remove(s)">删除</button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <Modal v-if="showEditor" :title="editing ? `编辑计划 #${editing.id}` : '新增计划任务'" @close="showEditor = false">
      <div class="grid cols-2">
        <label class="field">
          <span>名称</span>
          <input v-model="form.name" class="input" placeholder="如 每半小时本机扫描" />
        </label>
        <label class="field">
          <span>类型</span>
          <select v-model="form.kind" class="select">
            <option v-for="k in kinds" :key="k.value" :value="k.value">{{ k.label }}</option>
          </select>
        </label>
      </div>
      <label class="field">
        <span>Cron 表达式</span>
        <input v-model="form.cron" class="input mono" placeholder="*/30 * * * *" />
        <div class="row wrap" style="margin-top: 6px">
          <button v-for="p in presets" :key="p.cron" class="btn sm" type="button" @click="form.cron = p.cron">{{ p.label }}</button>
        </div>
      </label>
      <template v-if="form.kind === 'scan'">
        <div class="grid cols-2">
          <label class="field">
            <span>扫描范围</span>
            <select v-model="form.target_kind" class="select">
              <option value="local">本机</option>
              <option value="cidr">网段（CIDR）</option>
            </select>
          </label>
          <label class="field">
            <span>CIDR</span>
            <input v-model="form.target_ref" class="input" placeholder="192.168.1.0/24" :disabled="form.target_kind === 'local'" />
          </label>
        </div>
        <label class="field">
          <span>端口范围</span>
          <input v-model="form.ports_spec" class="input" placeholder="留空 = 默认端口集" />
        </label>
      </template>
      <label class="field row" style="gap: 10px">
        <Switch v-model="form.enabled" />
        <span class="small dim">创建后立即启用</span>
      </label>
      <template #footer>
        <button class="btn" @click="showEditor = false">取消</button>
        <button class="btn primary" :disabled="busy.save" @click="save">保存</button>
      </template>
    </Modal>
  </div>
</template>
