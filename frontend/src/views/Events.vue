<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import api from '../api'
import Badge from '../components/Badge.vue'
import EmptyState from '../components/EmptyState.vue'
import { store, toastOk, toastErr } from '../store'
import { fmtTime } from '../utils/format'
import { useLive } from '../composables/useLive'

const loading = ref(true)
const events = ref([])
const total = ref(0)
const unread = ref(0)
const liveLog = ref([])
const tab = ref('events')
const busy = reactive({})
const filters = reactive({ level: '', category: '', q: '', limit: 200 })

const categories = ['gateway', 'mapping', 'scan', 'scanner', 'policy', 'auth', 'api', 'scheduler', 'app']

async function loadEvents() {
  loading.value = true
  try {
    const res = await api.events({ level: filters.level, category: filters.category, q: filters.q, limit: filters.limit })
    events.value = res.events || []
    total.value = res.total || 0
    unread.value = res.unread || 0
  } catch (err) {
    toastErr(err.message, '加载日志失败')
  } finally {
    loading.value = false
  }
}

useLive((ev) => {
  if (ev.type === 'log') {
    liveLog.value.unshift({ ...ev.data, at: new Date().toISOString(), id: Math.random() })
    if (liveLog.value.length > 400) liveLog.value.pop()
  }
})

async function markRead() {
  try {
    await api.markEventsRead([])
    toastOk('已全部标记为已读')
    loadEvents()
  } catch (err) {
    toastErr(err.message, '操作失败')
  }
}

async function clearAll() {
  busy.clear = true
  try {
    const res = await api.clearEvents(0)
    toastOk(`已清空 ${res.deleted} 条日志`)
    liveLog.value = []
    loadEvents()
  } catch (err) {
    toastErr(err.message, '清空失败')
  } finally {
    busy.clear = false
  }
}

const levelKind = (l) => ({ info: 'info', warn: 'warn', error: 'danger', debug: 'default' })[l] || 'default'

const liveCount = computed(() => liveLog.value.length)

onMounted(loadEvents)
</script>

<template>
  <div>
    <div class="toolbar">
      <div class="row" style="gap: 0">
        <button class="btn" :class="tab === 'events' ? 'primary' : ''" @click="tab = 'events'">系统事件</button>
        <button class="btn" :class="tab === 'live' ? 'primary' : ''" style="margin-left: 6px" @click="tab = 'live'">
          实时日志 <span class="badge" style="margin-left: 6px">{{ liveCount }}</span>
        </button>
      </div>
      <div class="spacer" style="flex: 1" />
      <template v-if="tab === 'events'">
        <input v-model="filters.q" class="input" style="max-width: 200px" placeholder="搜索日志内容" @keydown.enter="loadEvents" />
        <select v-model="filters.level" class="select" style="width: 110px" @change="loadEvents">
          <option value="">全部级别</option>
          <option value="info">info</option>
          <option value="warn">warn</option>
          <option value="error">error</option>
        </select>
        <select v-model="filters.category" class="select" style="width: 130px" @change="loadEvents">
          <option value="">全部来源</option>
          <option v-for="c in categories" :key="c" :value="c">{{ c }}</option>
        </select>
        <button class="btn" @click="loadEvents">刷新</button>
        <button class="btn" @click="markRead">全部已读</button>
        <button class="btn danger" :disabled="busy.clear" @click="clearAll">清空</button>
      </template>
    </div>

    <div v-if="tab === 'live'" class="card">
      <div class="card-head">
        <h3>实时日志流（SSE）</h3>
        <div class="spacer" style="flex: 1" />
        <span class="mute small">最多保留最近 400 条</span>
      </div>
      <div class="card-body">
        <div class="log-tail">
          <div v-if="!liveLog.length" class="mute small">等待日志…执行一次扫描或同步即可看到输出。</div>
          <div v-for="l in liveLog" :key="l.id" class="ln">
            <span class="t">{{ fmtTime(l.at, true) }}</span>
            <span class="lv" :class="l.level">{{ (l.level || 'info').toUpperCase() }}</span>
            <span class="cat">{{ l.category }}</span>
            <span>{{ l.message }}</span>
          </div>
        </div>
      </div>
    </div>

    <div v-else class="card">
      <div class="card-head">
        <h3>系统事件</h3>
        <div class="spacer" style="flex: 1" />
        <span v-if="unread" class="badge warn">{{ unread }} 条未读</span>
        <span class="mute small">共 {{ total }} 条</span>
      </div>
      <div class="card-body flush">
        <EmptyState v-if="!events.length && !loading" icon="≡" title="暂无事件" />
        <div v-else class="table-wrap">
          <table class="data">
            <thead>
              <tr>
                <th style="width: 160px">时间</th>
                <th style="width: 90px">级别</th>
                <th style="width: 110px">来源</th>
                <th>内容</th>
                <th style="width: 60px"></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="e in events" :key="e.id" :style="{ opacity: e.read ? 0.65 : 1 }">
                <td class="small mute nowrap">{{ fmtTime(e.at, true) }}</td>
                <td><Badge :kind="levelKind(e.level)">{{ e.level }}</Badge></td>
                <td class="small">{{ e.category }}</td>
                <td class="small">
                  {{ e.message }}
                  <details v-if="e.meta && Object.keys(e.meta).length" style="margin-top: 4px">
                    <summary class="mute" style="cursor: pointer; font-size: 11px">详情</summary>
                    <pre class="mono small" style="white-space: pre-wrap; margin: 4px 0 0">{{ JSON.stringify(e.meta, null, 2) }}</pre>
                  </details>
                </td>
                <td class="right">
                  <span v-if="!e.read" class="badge accent">未读</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>
  </div>
</template>
