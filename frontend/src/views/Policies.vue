<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import api from '../api'
import Badge from '../components/Badge.vue'
import EmptyState from '../components/EmptyState.vue'
import Modal from '../components/Modal.vue'
import Switch from '../components/Switch.vue'
import TagInput from '../components/TagInput.vue'
import { toastOk, toastErr, canWrite } from '../store'
import { specPortCount } from '../utils/format'

const loading = ref(true)
const policies = ref([])
const monitor = ref({ active: false, interval_seconds: 0, policy_count: 0, targets: [] })
const showEditor = ref(false)
const editing = ref(null)
const busy = reactive({})

const form = reactive({
  name: '',
  description: '',
  enabled: true,
  match_target_kind: 'any',
  mac_patterns: '',
  port_ranges: '',
  exclude_ranges: '',
  protocol: 'any',
  auto_action: 'watch',
  auto_add: false,
  min_port: 0,
  max_port: 0,
  priority: 10,
  tags: [],
  note: '',
})

async function load() {
  loading.value = true
  try {
    const [p, m] = await Promise.all([api.policies(), api.monitor()])
    policies.value = p.policies || []
    monitor.value = m.monitor || {}
  } catch (err) {
    toastErr(err.message, '加载策略失败')
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editing.value = null
  Object.assign(form, {
    name: '',
    description: '',
    enabled: true,
    match_target_kind: 'any',
    mac_patterns: '',
    port_ranges: '',
    exclude_ranges: '',
    protocol: 'any',
    auto_action: 'watch',
    auto_add: false,
    min_port: 0,
    max_port: 0,
    priority: 10,
    tags: [],
    note: '',
  })
  showEditor.value = true
}

function openEdit(p) {
  editing.value = p
  Object.assign(form, { ...p, tags: [...(p.tags || [])] })
  showEditor.value = true
}

async function save() {
  if (!form.name) {
    toastErr('策略名称不能为空')
    return
  }
  busy.save = true
  try {
    if (editing.value) {
      await api.updatePolicy(editing.value.id, form)
      toastOk('策略已更新')
    } else {
      await api.createPolicy(form)
      toastOk('策略已创建')
    }
    showEditor.value = false
    load()
  } catch (err) {
    toastErr(err.message, '保存失败')
  } finally {
    busy.save = false
  }
}

async function remove(p) {
  try {
    await api.deletePolicy(p.id)
    toastOk('策略已删除')
    load()
  } catch (err) {
    toastErr(err.message, '删除失败')
  }
}

async function toggle(p, value) {
  try {
    await api.updatePolicy(p.id, { ...p, enabled: value })
    load()
  } catch (err) {
    p.enabled = !value
    toastErr(err.message, '切换失败')
  }
}

async function toggleMonitor() {
  busy.monitor = true
  try {
    if (monitor.value.active) {
      await api.stopMonitor()
      toastOk('已停止自动监控')
    } else {
      await api.startMonitor()
      toastOk('已启动自动监控')
    }
    load()
  } catch (err) {
    toastErr(err.message, err.message.includes('策略') ? '需要先启用至少一条默认策略' : '切换失败')
  } finally {
    busy.monitor = false
  }
}

const autoActionLabel = (a) => ({ add: '自动添加', watch: '仅观察', ignore: '忽略', delete: '删除记录' })[a] || a

onMounted(load)
</script>

<template>
  <div>
    <div class="card" style="margin-bottom: 16px">
      <div class="card-head">
        <h3>自动监控</h3>
        <div class="spacer" style="flex: 1" />
        <Badge :kind="monitor.active ? 'ok' : 'default'" :dot="monitor.active">
          {{ monitor.active ? '运行中' : '已停止' }}
        </Badge>
      </div>
      <div class="card-body">
        <div class="row wrap" style="gap: 18px">
          <div>
            <div class="label mute small">扫描间隔</div>
            <div>{{ monitor.interval_seconds || 300 }} 秒</div>
          </div>
          <div>
            <div class="label mute small">启用策略</div>
            <div>{{ monitor.policy_count || 0 }} 条</div>
          </div>
          <div>
            <div class="label mute small">监控目标</div>
            <div class="mono small">{{ (monitor.targets || ['local']).join(', ') }}</div>
          </div>
        </div>
        <p class="hint" style="margin-top: 12px">
          自动监控需要至少一条“已启用”的默认策略；开启后按固定间隔扫描本机（及配置的网段），并按策略自动添加 / 忽略 / 标记端口。
        </p>
        <div class="row" style="margin-top: 12px">
          <button class="btn" :class="monitor.active ? 'danger' : 'primary'" :disabled="busy.monitor || !canWrite()" @click="toggleMonitor">
            {{ monitor.active ? '停止自动监控' : '启动自动监控' }}
          </button>
        </div>
      </div>
    </div>

    <div class="toolbar">
      <span class="dim small">共 {{ policies.length }} 条策略（按优先级从高到低匹配第一条命中的规则）</span>
      <div class="spacer" style="flex: 1" />
      <button class="btn" @click="load"><span v-if="loading" class="spin">◌</span>刷新</button>
      <button v-if="canWrite()" class="btn primary" @click="openCreate">+ 新增策略</button>
    </div>

    <div class="card">
      <div class="card-body flush">
        <EmptyState v-if="!policies.length && !loading" icon="⚑" title="暂无策略" hint="新增一条默认策略后即可开启自动监控" />
        <div v-else class="table-wrap">
          <table class="data">
            <thead>
              <tr>
                <th>启用</th>
                <th>名称</th>
                <th>匹配范围</th>
                <th>端口范围</th>
                <th>协议</th>
                <th>自动动作</th>
                <th>优先级</th>
                <th>标签</th>
                <th class="right">操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="p in policies" :key="p.id">
                <td><Switch :model-value="p.enabled" :disabled="!canWrite()" @update:model-value="(v) => toggle(p, v)" /></td>
                <td>
                  <div>{{ p.name }}</div>
                  <div class="small mute">{{ p.description || '' }}</div>
                </td>
                <td>
                  <Badge>{{ p.match_target_kind }}</Badge>
                  <div v-if="p.mac_patterns" class="mono small mute">{{ p.mac_patterns }}</div>
                  <div v-else-if="p.mac" class="mono small mute">绑定 {{ p.mac }}</div>
                </td>
                <td class="mono small">
                  {{ p.port_ranges || '全部' }}
                  <span v-if="p.port_ranges" class="mute">({{ specPortCount(p.port_ranges) }})</span>
                  <div v-if="p.exclude_ranges" class="mute">排除 {{ p.exclude_ranges }}</div>
                </td>
                <td class="small">{{ p.protocol }}</td>
                <td><Badge :kind="p.auto_add ? 'ok' : 'info'">{{ autoActionLabel(p.auto_action) }}</Badge></td>
                <td class="mono small">{{ p.priority }}</td>
                <td>
                  <span v-for="t in p.tags" :key="t" class="tag">{{ t }}</span>
                </td>
                <td>
                  <div class="cell-actions">
                    <button class="btn sm ghost" :disabled="!canWrite()" @click="openEdit(p)">编辑</button>
                    <button class="btn sm danger" :disabled="!canWrite()" @click="remove(p)">删除</button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <Modal v-if="showEditor" :title="editing ? `编辑策略 #${editing.id}` : '新增策略'" wide @close="showEditor = false">
      <div class="grid cols-2">
        <label class="field">
          <span>名称 *</span>
          <input v-model="form.name" class="input" placeholder="如 家庭服务白名单" />
        </label>
        <label class="field">
          <span>优先级（越大越先匹配）</span>
          <input v-model.number="form.priority" class="input" type="number" />
        </label>
      </div>
      <label class="field">
        <span>说明</span>
        <input v-model="form.description" class="input" />
      </label>
      <div class="grid cols-2">
        <label class="field">
          <span>匹配范围</span>
          <select v-model="form.match_target_kind" class="select">
            <option value="any">任意</option>
            <option value="local">本机</option>
            <option value="cidr">网段 / 局域网</option>
            <option value="mac">MAC 地址 / 前缀</option>
          </select>
        </label>
        <label v-if="form.match_target_kind === 'mac'" class="field">
          <span>MAC 地址 / 前缀</span>
          <input v-model="form.mac_patterns" class="input" placeholder="aa:bb:cc:dd:ee:ff, aa:bb:cc:* （逗号分隔，可用 OUI 前缀）" />
          <div class="hint">按主机 MAC 匹配，IP 随 DHCP 变化时依然命中；前缀至少 3 字节。</div>
        </label>
        <label class="field">
          <span>协议</span>
          <select v-model="form.protocol" class="select">
            <option value="any">任意</option>
            <option value="tcp">TCP</option>
            <option value="udp">UDP</option>
          </select>
        </label>
      </div>
      <div class="grid cols-2">
        <label class="field">
          <span>端口范围</span>
          <input v-model="form.port_ranges" class="input" placeholder="3000-3010,8080,9000" />
        </label>
        <label class="field">
          <span>排除端口</span>
          <input v-model="form.exclude_ranges" class="input" placeholder="22,3389" />
        </label>
      </div>
      <div class="grid cols-3">
        <label class="field">
          <span>最小端口</span>
          <input v-model.number="form.min_port" class="input" type="number" />
        </label>
        <label class="field">
          <span>最大端口</span>
          <input v-model.number="form.max_port" class="input" type="number" />
        </label>
        <label class="field">
          <span>自动动作</span>
          <select v-model="form.auto_action" class="select">
            <option value="watch">仅观察</option>
            <option value="add">自动添加</option>
            <option value="ignore">忽略</option>
            <option value="delete">删除记录</option>
          </select>
        </label>
      </div>
      <label class="field row" style="gap: 10px">
        <Switch v-model="form.auto_add" />
        <span class="small dim">命中时自动加入映射列表（等同于“自动添加”）</span>
      </label>
      <label class="field">
        <span>命中后附加标签</span>
        <TagInput v-model="form.tags" />
      </label>
      <template #footer>
        <button class="btn" @click="showEditor = false">取消</button>
        <button class="btn primary" :disabled="busy.save" @click="save">保存</button>
      </template>
    </Modal>
  </div>
</template>
