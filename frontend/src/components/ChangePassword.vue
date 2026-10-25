<script setup>
import { reactive, ref } from 'vue'
import api from '../api'
import { toastOk, toastErr } from '../store'

const state = reactive({ current_password: '', new_password: '' })
const busy = ref(false)

async function submit() {
  if (state.new_password.length < 8) {
    toastErr('新密码至少 8 位')
    return
  }
  busy.value = true
  try {
    await api.changePassword({ ...state })
    toastOk('密码已更新')
    state.current_password = ''
    state.new_password = ''
  } catch (err) {
    toastErr(err.message, '修改失败')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="grid cols-3">
    <label class="field">
      <span>当前密码</span>
      <input v-model="state.current_password" class="input" type="password" autocomplete="current-password" />
    </label>
    <label class="field">
      <span>新密码</span>
      <input v-model="state.new_password" class="input" type="password" autocomplete="new-password" />
    </label>
    <div class="field" style="align-self: end">
      <button class="btn primary" :disabled="busy" @click="submit">更新密码</button>
    </div>
  </div>
</template>
