<script setup>
import { onMounted, reactive, ref } from 'vue'
import api from '../api'
import Badge from '../components/Badge.vue'
import Modal from '../components/Modal.vue'
import Switch from '../components/Switch.vue'
import ChangePassword from '../components/ChangePassword.vue'
import { store, toastOk, toastErr } from '../store'
import { fmtTime } from '../utils/format'

const loading = ref(true)
const users = ref([])
const showEditor = ref(false)
const editing = ref(null)
const tokenResult = ref(null)
const busy = reactive({})
const error = ref('')

const form = reactive({ username: '', display_name: '', email: '', role: 'operator', enabled: true, password: '' })

async function load() {
  loading.value = true
  try {
    const res = await api.users()
    users.value = res.users || []
  } catch (err) {
    toastErr(err.message, '加载用户失败')
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editing.value = null
  error.value = ''
  Object.assign(form, { username: '', display_name: '', email: '', role: 'operator', enabled: true, password: '' })
  showEditor.value = true
}

function openEdit(u) {
  editing.value = u
  error.value = ''
  Object.assign(form, { username: u.username, display_name: u.display_name, email: u.email, role: u.role, enabled: u.enabled, password: '' })
  showEditor.value = true
}

async function save() {
  error.value = ''
  busy.save = true
  try {
    if (editing.value) {
      const body = { display_name: form.display_name, email: form.email, role: form.role, enabled: form.enabled }
      if (form.password) body.password = form.password
      await api.updateUser(editing.value.id, body)
      toastOk('用户已更新')
    } else {
      await api.createUser(form)
      toastOk('用户已创建')
    }
    showEditor.value = false
    load()
  } catch (err) {
    error.value = err.message
    toastErr(err.message, '保存失败')
  } finally {
    busy.save = false
  }
}

async function remove(u) {
  try {
    await api.deleteUser(u.id)
    toastOk('用户已删除')
    load()
  } catch (err) {
    toastErr(err.message, '删除失败')
  }
}

async function issueToken(u) {
  try {
    const res = await api.userToken(u.id)
    tokenResult.value = { username: u.username, token: res.token }
  } catch (err) {
    toastErr(err.message, '生成令牌失败')
  }
}

const roleKind = (r) => ({ admin: 'danger', operator: 'accent', viewer: 'default' })[r] || 'default'

onMounted(load)
</script>

<template>
  <div>
    <div class="toolbar">
      <span class="dim small">共 {{ users.length }} 个账号 · 权限：admin 全权 / operator 可写 / viewer 只读</span>
      <div class="spacer" style="flex: 1" />
      <button class="btn" @click="load"><span v-if="loading" class="spin">◌</span>刷新</button>
      <button class="btn primary" @click="openCreate">+ 新增用户</button>
    </div>

    <div class="card">
      <div class="card-body flush">
        <div class="table-wrap">
          <table class="data">
            <thead>
              <tr>
                <th>ID</th>
                <th>用户名</th>
                <th>显示名</th>
                <th>邮箱</th>
                <th>角色</th>
                <th>状态</th>
                <th>最近登录</th>
                <th class="right">操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="u in users" :key="u.id">
                <td class="mono small mute">{{ u.id }}</td>
                <td>
                  {{ u.username }}
                  <span v-if="store.identity.username === u.username" class="badge accent" style="margin-left: 6px">我</span>
                </td>
                <td class="small">{{ u.display_name }}</td>
                <td class="small mute">{{ u.email || '—' }}</td>
                <td><Badge :kind="roleKind(u.role)">{{ u.role }}</Badge></td>
                <td>
                  <Badge :kind="u.enabled ? 'ok' : 'default'">{{ u.enabled ? '启用' : '停用' }}</Badge>
                </td>
                <td class="small mute nowrap">{{ u.last_login_at ? fmtTime(u.last_login_at, true) : '从未登录' }}</td>
                <td>
                  <div class="cell-actions">
                    <button class="btn sm" @click="issueToken(u)">API 令牌</button>
                    <button class="btn sm ghost" @click="openEdit(u)">编辑</button>
                    <button class="btn sm danger" :disabled="store.identity.username === u.username" @click="remove(u)">删除</button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <div class="card" style="margin-top: 16px">
      <div class="card-head"><h3>修改我的密码</h3></div>
      <div class="card-body">
        <ChangePassword />
      </div>
    </div>

    <Modal v-if="showEditor" :title="editing ? `编辑用户 ${editing.username}` : '新增用户'" @close="showEditor = false">
      <div v-if="error" class="badge danger" style="display: block; padding: 8px 10px; margin-bottom: 12px">{{ error }}</div>
      <div class="grid cols-2">
        <label class="field">
          <span>用户名 *</span>
          <input v-model="form.username" class="input" :disabled="!!editing" placeholder="operator1" />
        </label>
        <label class="field">
          <span>显示名</span>
          <input v-model="form.display_name" class="input" placeholder="运维小王" />
        </label>
      </div>
      <div class="grid cols-2">
        <label class="field">
          <span>邮箱</span>
          <input v-model="form.email" class="input" type="email" placeholder="ops@example.com" />
        </label>
        <label class="field">
          <span>角色</span>
          <select v-model="form.role" class="select">
            <option value="admin">admin · 管理员</option>
            <option value="operator">operator · 运维</option>
            <option value="viewer">viewer · 只读</option>
          </select>
        </label>
      </div>
      <label class="field">
        <span>{{ editing ? '重置密码（留空则不修改）' : '密码 *（至少 8 位）' }}</span>
        <input v-model="form.password" class="input" type="password" autocomplete="new-password" />
      </label>
      <label class="field row" style="gap: 10px">
        <Switch v-model="form.enabled" />
        <span class="small dim">启用该账号</span>
      </label>
      <template #footer>
        <button class="btn" @click="showEditor = false">取消</button>
        <button class="btn primary" :disabled="busy.save" @click="save">保存</button>
      </template>
    </Modal>

    <Modal v-if="tokenResult" title="API 令牌" @close="tokenResult = null">
      <p class="small dim">为 <b>{{ tokenResult.username }}</b> 生成的 JWT 令牌（请妥善保管，可用于 <code>Authorization: Bearer</code>）：</p>
      <textarea class="input mono" rows="5" readonly>{{ tokenResult.token }}</textarea>
      <template #footer>
        <button class="btn" @click="tokenResult = null">关闭</button>
        <button class="btn primary" @click="navigator.clipboard && navigator.clipboard.writeText(tokenResult.token)">复制</button>
      </template>
    </Modal>
  </div>
</template>
