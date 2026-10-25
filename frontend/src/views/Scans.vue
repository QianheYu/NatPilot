<script setup>
import { computed, onMounted, ref } from 'vue'
import api from '../api'
import Badge from '../components/Badge.vue'
import EmptyState from '../components/EmptyState.vue'
import Modal from '../components/Modal.vue'
import { toastOk, toastErr } from '../store'
import { fmtRelative, fmtTime, scanStatusInfo } from '../utils/format'
import { useLive } from '../composables/useLive'

const loading = ref(true)
const scans = ref([])
const total = ref(0)
const running = ref(0)
const detail = ref(null)
const detailPorts = ref([])

async function load() {
  loading.value = true
  try {
    const res = await api.scans({ limit: 60 })
    scans.value = res.scans || []
    total.value = res.total || 0
    running.value = res.running || 0
  } catch (err) {
    toastErr(err.message, '加载扫描记录失败')
  } finally {
    loading.value = false
  }
}

useLive((ev) => {
  if (ev.type === 'scan-progress' || ev.type === 'changed') load()
})

async function openDetail(scan) {
  detail.value = scan
  detailPorts.value = []
  try {
    const res = await api.scanResults(scan.id, { limit: 500 })
    detailPorts.value = res.ports || []
  } catch (err) {
    toastErr(err.message, '加载结果失败')
  }
}

async function cancel(scan) {
  try {
    await api.cancelScan(scan.id)
    toastOk(`已请求取消 #${scan.id}`)
    load()
  } catch (err) {
    toastErr(err.message, '取消失败')
  }
}

async function remove(scan) {
  try {
    await api.deleteScan(scan.id)
    toastOk(`已删除扫描 #${scan.id}`)
    load()
  } catch (err) {
    toastErr(err.message, '删除失败')
  }
}

async function promoteAll(scan) {
  try {
    const res = await api.promotePorts({ statuses: ['open', 'not_added'] })
    toastOk(`已添加 ${res.added} 个端口，映射生效 ${res.applied} 个`)
    load()
  } catch (err) {
    toastErr(err.message, '批量添加失败')
  }
}

const progress = computed(() => (s) => (s.progress ? Math.min(100, s.progress) : s.status === 'done' ? 100 : 0))

onMounted(load)
</script>

<template>
  <div>
    <div class="toolbar">
      <span class="dim small">共 {{ total }} 次扫描，{{ running }} 个进行中</span>
      <div class="spacer" style="flex: 1" />
      <button class="btn" @click="load"><span v-if="loading" class="spin">◌</span>刷新</button>
      <router-link class="btn primary" :to="{ name: 'ports' }">新建扫描</router-link>
    </div>

    <div class="card">
      <div class="card-body flush">
        <EmptyState v-if="!scans.length && !loading" icon="◎" title="还没有扫描记录" hint="在“端口发现”页面发起第一次扫描" />
        <div v-else class="table-wrap">
          <table class="data">
            <thead>
              <tr>
                <th>ID</th>
                <th>状态</th>
                <th>范围</th>
                <th>端口规格</th>
                <th>触发</th>
                <th>结果</th>
                <th>耗时</th>
                <th>开始时间</th>
                <th class="right">操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="s in scans" :key="s.id">
                <td class="mono small mute">#{{ s.id }}</td>
                <td>
                  <Badge :kind="scanStatusInfo(s.status).kind">{{ scanStatusInfo(s.status).label }}</Badge>
                  <div v-if="s.status === 'running'" class="progress" style="margin-top: 5px; width: 90px">
                    <i :style="{ width: progress(s) + '%' }" />
                  </div>
                </td>
                <td class="small">
                  {{ s.target_kind === 'local' ? '本机' : s.target_kind === 'mac' ? 'MAC ' + s.target_ref : s.target_ref }}
                  <div v-if="s.hosts_count" class="mute small">{{ s.hosts_count }} 台主机</div>
                </td>
                <td class="mono small mute">{{ s.ports_spec }}</td>
                <td><span class="badge">{{ s.trigger }}</span></td>
                <td class="small">
                  开放 {{ s.open_count || 0 }} · 新增 {{ s.new_count || 0 }}
                  <div class="mute small">{{ s.message }}</div>
                </td>
                <td class="small mute">{{ (s.duration_ms / 1000).toFixed(1) }}s</td>
                <td class="small mute nowrap">{{ fmtTime(s.started_at, true) }}</td>
                <td>
                  <div class="cell-actions">
                    <button class="btn sm" @click="openDetail(s)">结果</button>
                    <button class="btn sm" @click="promoteAll(s)">全部添加</button>
                    <button v-if="s.status === 'running'" class="btn sm danger" @click="cancel(s)">取消</button>
                    <button v-else class="btn sm ghost" @click="remove(s)">删除</button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <Modal v-if="detail" :title="`扫描 #${detail.id} 结果`" wide @close="detail = null">
      <div class="row wrap" style="margin-bottom: 12px">
        <span class="badge">范围 {{ detail.target_kind === 'local' ? '本机' : detail.target_kind === 'mac' ? 'MAC ' + detail.target_ref : detail.target_ref }}</span>
        <span class="badge">端口 {{ detail.ports_spec }}</span>
        <span class="badge accent">开放 {{ detail.open_count || 0 }}</span>
        <span class="badge">新增 {{ detail.new_count || 0 }}</span>
        <span class="badge">耗时 {{ (detail.duration_ms / 1000).toFixed(1) }}s</span>
      </div>
      <div class="table-wrap" style="max-height: 420px; overflow-y: auto">
        <table class="data">
          <thead>
            <tr>
              <th>主机</th>
              <th>端口</th>
              <th>协议</th>
              <th>服务</th>
              <th>进程</th>
              <th>指纹</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="p in detailPorts" :key="p.id">
              <td class="mono small">{{ p.host }}</td>
              <td class="mono small">{{ p.port }}</td>
              <td class="small">{{ p.protocol }}</td>
              <td class="small">{{ p.service || '—' }}</td>
              <td class="small dim">{{ p.process || '—' }}</td>
              <td class="small mute">{{ p.banner || '—' }}</td>
            </tr>
          </tbody>
        </table>
        <div v-if="!detailPorts.length" class="mute small" style="padding: 14px">本次扫描没有发现开放端口。</div>
      </div>
    </Modal>
  </div>
</template>
