<script setup>
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import api from '../api'
import { store, toastOk, toastErr } from '../store'

const router = useRouter()
const username = ref('')
const password = ref('')
const busy = ref(false)
const error = ref('')

onMounted(() => {
  document.documentElement.setAttribute('data-theme', store.theme)
})

async function submit() {
  error.value = ''
  if (!username.value || !password.value) {
    error.value = '请输入用户名和密码'
    return
  }
  busy.value = true
  try {
    const res = await api.login(username.value, password.value)
    if (res.identity) {
      store.identity = res.identity
    }
    toastOk('登录成功，欢迎回来')
    router.replace({ name: 'dashboard' })
  } catch (err) {
    error.value = err.message
    toastErr(err.message, '登录失败')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="login-wrap">
    <form class="login-card" @submit.prevent="submit">
      <div class="brand" style="border: 0">
        <div class="logo" style="width: 42px; height: 42px; font-size: 18px">NP</div>
        <div>
          <div class="title" style="font-size: 18px">NatPilot</div>
          <div class="sub">NAT 端口映射控制台</div>
        </div>
      </div>
      <p class="dim small" style="text-align: center; margin-top: 0">
        登录以管理端口映射、网关协议与端口扫描
      </p>
      <label class="field">
        <span>用户名</span>
        <input v-model="username" class="input" autocomplete="username" placeholder="admin" />
      </label>
      <label class="field">
        <span>密码</span>
        <input v-model="password" type="password" class="input" autocomplete="current-password" placeholder="••••••••" />
      </label>
      <div v-if="error" class="badge danger" style="display: block; height: auto; padding: 8px 10px; margin-bottom: 12px">
        {{ error }}
      </div>
      <button class="btn primary" style="width: 100%; height: 38px" :disabled="busy">
        <span v-if="busy" class="spin">◌</span>
        {{ busy ? '登录中…' : '登录' }}
      </button>
      <p class="mute small" style="text-align: center; margin: 14px 0 0">
        首次部署请使用启动日志中打印的管理员账号；忘记密码可在服务端使用
        <code>natpilot user passwd</code> 重置。
      </p>
    </form>
  </div>
</template>
