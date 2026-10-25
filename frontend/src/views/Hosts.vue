<script setup>
import { onMounted, ref } from 'vue'
import api from '../api'
import Badge from '../components/Badge.vue'
import EmptyState from '../components/EmptyState.vue'
import { toastOk, toastErr, canWrite } from '../store'
import { fmtRelative } from '../utils/format'

const loading = ref(true)
const hosts = ref([])
const interfaces = ref([])
const gateway = ref('')
const busy = ref(false)

async function load() {
  loading.value = true
  try {
    const [h, n] = await Promise.all([api.hosts({ limit: 800 }), api.interfaces()])
    hosts.value = h.hosts || []
    interfaces.value = n.interfaces || []
    gateway.value = n.gateway || ''
  } catch (err) {
    toastErr(err.message, '加载主机失败')
  } finally {
    loading.value = false
  }
}

async function discoverHosts() {
  busy.value = true
  try {
    const res = await api.hostScan()
    toastOk(`发现 ${res.discovered} 台主机`)
    load()
  } catch (err) {
    toastErr(err.message, '主机发现失败')
  } finally {
    busy.value = false
  }
}

async function removeHost(h) {
  try {
    await api.deleteHost(h.id)
    load()
  } catch (err) {
    toastErr(err.message, '删除失败')
  }
}

onMounted(load)
</script>

<template>
  <div>
    <div class="toolbar">
      <span class="dim small">共 {{ hosts.length }} 台主机 · 默认网关 {{ gateway || '未知' }}</span>
      <div class="spacer" style="flex: 1" />
      <button class="btn" @click="load"><span v-if="loading" class="spin">◌</span>刷新</button>
      <button class="btn primary" :disabled="busy || !canWrite()" @click="discoverHosts">发现局域网主机</button>
    </div>

    <div class="grid cols-4" style="margin-bottom: 16px">
      <div v-for="i in interfaces" :key="i.name + i.ip" class="stat">
        <div class="label">{{ i.name }}</div>
        <div class="value" style="font-size: 18px">{{ i.ip }}</div>
        <div class="sub mono small">{{ i.network }}</div>
        <div class="sub">
          <span v-if="i.gateway">网关 {{ i.gateway }}</span>
          <span v-else-if="i.loopback" class="mute">回环</span>
        </div>
      </div>
    </div>

    <div class="card">
      <div class="card-body flush">
        <EmptyState v-if="!hosts.length && !loading" icon="▤" title="暂无主机记录" hint="点击“发现局域网主机”读取 ARP 邻居表" />
        <div v-else class="table-wrap">
          <table class="data">
            <thead>
              <tr>
                <th>IP</th>
                <th>MAC</th>
                <th>厂商</th>
                <th>主机名</th>
                <th>类型</th>
                <th>接口</th>
                <th>次数</th>
                <th>最近</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="h in hosts" :key="h.id">
                <td class="mono small">{{ h.ip }}</td>
                <td class="mono small mute">{{ h.mac || '—' }}</td>
                <td class="small">{{ h.vendor || '—' }}</td>
                <td class="small">{{ h.hostname || '—' }}</td>
                <td>
                  <Badge :kind="h.is_local ? 'accent' : h.kind === 'gateway' ? 'purple' : 'default'">
                    {{ h.is_local ? '本机' : h.kind || '对端' }}
                  </Badge>
                </td>
                <td class="small mute">{{ h.interface || '—' }}</td>
                <td class="small mute">{{ h.seen_count }}</td>
                <td class="small mute nowrap">{{ fmtRelative(h.last_seen) }}</td>
                <td class="right">
                  <button class="btn sm ghost" :disabled="!canWrite()" @click="removeHost(h)">移除</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>
  </div>
</template>
