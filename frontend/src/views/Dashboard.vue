<script setup>
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import api from '../api'
import Badge from '../components/Badge.vue'
import EmptyState from '../components/EmptyState.vue'
import { toastOk, toastErr, toastWarn } from '../store'
import { fmtRelative, fmtTime, gatewayKindInfo, portStatusInfo, scanStatusInfo, statusInfo, truncate } from '../utils/format'

const router = useRouter()
const loading = ref(true)
const data = ref({ stats: {}, gateways: [], recent_rules: [], pending_ports: [], recent_events: [], monitor: {} })
const busy = ref({})

const stats = computed(() => data.value.stats || {})
const activeRatio = computed(() => {
  const total = stats.value.rules || 0
  if (!total) return 0
  return Math.round(((stats.value.mapped || 0) / total) * 100)
})

async function load() {
  loading.value = true
  try {
    data.value = await api.dashboard()
  } catch (err) {
    toastErr(err.message, '加载总览失败')
  } finally {
    loading.value = false
  }
}

async function discover() {
  busy.value.discover = true
  try {
    const res = await api.discover()
    if (res.external_ip) toastOk(`发现 ${res.gateways.length} 个网关，外部 IP：${res.external_ip}`)
    else toastOk(`发现 ${res.gateways.length} 个网关`)
    load()
  } catch (err) {
    toastErr(err.message, '网关探测失败')
  } finally {
    busy.value.discover = false
  }
}

async function reconcile() {
  busy.value.reconcile = true
  try {
    const res = await api.reconcile()
    if (res.ok) toastOk(`同步完成：${summaryText(res.summary)}`)
    else toastWarn(res.error || '同步出现问题')
    load()
  } catch (err) {
    toastErr(err.message, '同步失败')
  } finally {
    busy.value.reconcile = false
  }
}

function summaryText(s) {
  if (!s) return ''
  return `新建 ${s.created}，续约 ${s.renewed}，正常 ${s.ok}，失败 ${s.failed}，清理 ${s.removed}`
}

async function toggleMonitor() {
  const active = data.value.monitor && data.value.monitor.active
  busy.value.monitor = true
  try {
    if (active) {
      await api.stopMonitor()
      toastOk('已停止自动监控')
    } else {
      await api.startMonitor()
      toastOk('已启动自动监控')
    }
    load()
  } catch (err) {
    toastErr(err.message, '切换自动监控失败')
  } finally {
    busy.value.monitor = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <div class="toolbar">
      <div class="dim small">
        最近同步 {{ fmtRelative(stats.updated_at) }} · 外部 IP
        <span class="mono">{{ data.external_ip || '未知' }}</span>
      </div>
      <div class="spacer" style="flex: 1" />
      <button class="btn" :disabled="busy.discover" @click="discover">
        <span v-if="busy.discover" class="spin">◌</span>发现网关
      </button>
      <button class="btn" :disabled="busy.reconcile" @click="reconcile">
        <span v-if="busy.reconcile" class="spin">◌</span>立即同步映射
      </button>
      <button class="btn" :class="data.monitor && data.monitor.active ? 'danger' : 'primary'" :disabled="busy.monitor" @click="toggleMonitor">
        {{ data.monitor && data.monitor.active ? '停止自动监控' : '启动自动监控' }}
      </button>
    </div>

    <div class="grid cols-4" style="margin-bottom: 16px">
      <div class="stat accent">
        <div class="label">管理端口</div>
        <div class="value">{{ stats.rules || 0 }}</div>
        <div class="sub">已启用 {{ stats.rules_enabled || 0 }} · 已映射 {{ stats.mapped || 0 }}</div>
      </div>
      <div class="stat ok">
        <div class="label">映射成功率</div>
        <div class="value">{{ activeRatio }}%</div>
        <div class="sub">映射总数 {{ stats.mappings_total || 0 }}</div>
      </div>
      <div class="stat warn">
        <div class="label">发现待处理端口</div>
        <div class="value">{{ stats.ports_open || 0 }}</div>
        <div class="sub">源已删除 {{ stats.gone_sources || 0 }}</div>
      </div>
      <div class="stat danger">
        <div class="label">异常</div>
        <div class="value">{{ (stats.failures || 0) + (stats.conflicts || 0) }}</div>
        <div class="sub">失败 {{ stats.failures || 0 }} · 冲突 {{ stats.conflicts || 0 }}</div>
      </div>
    </div>

    <div class="grid cols-2" style="grid-template-columns: 1.4fr 1fr; align-items: start">
      <div class="card">
        <div class="card-head">
          <h3>映射状态分布</h3>
          <div class="spacer" style="flex: 1" />
          <span class="badge">{{ stats.gateways || 0 }} 个网关</span>
        </div>
        <div class="card-body">
          <div v-if="!Object.keys(stats.by_status || {}).length" class="mute small">暂无数据</div>
          <div v-for="(count, st) in stats.by_status || {}" :key="st" style="margin-bottom: 10px">
            <div class="row" style="justify-content: space-between; margin-bottom: 4px">
              <Badge :kind="statusInfo(st).kind">{{ statusInfo(st).label }}</Badge>
              <span class="mono small">{{ count }}</span>
            </div>
            <div class="progress">
              <i :style="{ width: Math.max(3, Math.round((count / (stats.rules || 1)) * 100)) + '%' }" />
            </div>
          </div>
          <div class="divider" />
          <div class="row wrap">
            <span v-for="(c, p) in stats.by_protocol || {}" :key="p" class="badge">{{ p.toUpperCase() }}: {{ c }}</span>
            <span v-for="(c, t) in stats.by_tag || {}" :key="t" class="tag">{{ t }} · {{ c }}</span>
          </div>
        </div>
      </div>

      <div class="card">
        <div class="card-head"><h3>网关与协议</h3></div>
        <div class="card-body flush">
          <EmptyState v-if="!data.gateways.length" icon="⬡" title="尚未发现网关" hint="点击“发现网关”开始探测" />
          <table v-else class="data">
            <tbody>
              <tr v-for="g in data.gateways" :key="g.id">
                <td>
                  <Badge :kind="gatewayKindInfo(g.kind).kind">{{ gatewayKindInfo(g.kind).label }}</Badge>
                  <div class="small dim">{{ truncate(g.name, 26) }}</div>
                </td>
                <td class="mono small">{{ g.address }}:{{ g.port }}</td>
                <td class="mono small">{{ g.external_ip || '—' }}</td>
                <td class="right">
                  <Badge v-if="g.preferred" kind="accent">首选</Badge>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <div class="grid cols-2" style="margin-top: 16px; align-items: start">
      <div class="card">
        <div class="card-head">
          <h3>最近变更的映射</h3>
          <div class="spacer" style="flex: 1" />
          <router-link class="small" :to="{ name: 'rules' }">查看全部 →</router-link>
        </div>
        <div class="card-body flush">
          <EmptyState v-if="!data.recent_rules.length" icon="⇄" title="还没有映射规则" hint="在“端口映射管理”中新增" />
          <table v-else class="data">
            <tbody>
              <tr v-for="r in data.recent_rules" :key="r.id">
                <td>
                  <Badge :kind="statusInfo(r.mapping_status).kind" :dot="r.mapping_status === 'active'">
                    {{ statusInfo(r.mapping_status).label }}
                  </Badge>
                </td>
                <td class="mono small">{{ r.host }}:{{ r.port }}</td>
                <td class="mono small dim">{{ r.external_ip || '' }}{{ r.external_port ? ':' + r.external_port : '' }}</td>
                <td class="small">{{ truncate(r.name, 22) }}</td>
                <td class="right small mute">{{ fmtRelative(r.updated_at) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <div class="card">
        <div class="card-head">
          <h3>待处理端口</h3>
          <div class="spacer" style="flex: 1" />
          <router-link class="small" :to="{ name: 'ports' }">去处理 →</router-link>
        </div>
        <div class="card-body flush">
          <EmptyState v-if="!data.pending_ports.length" icon="⌗" title="没有待处理端口" hint="可以运行一次本机扫描" />
          <table v-else class="data">
            <tbody>
              <tr v-for="p in data.pending_ports" :key="p.id">
                <td>
                  <Badge :kind="portStatusInfo(p.status).kind">{{ portStatusInfo(p.status).label }}</Badge>
                </td>
                <td class="mono small">{{ p.host }}:{{ p.port }}</td>
                <td class="small dim">{{ p.service || p.process || '—' }}</td>
                <td class="right small mute">{{ fmtRelative(p.last_seen) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <div class="card" style="margin-top: 16px">
      <div class="card-head">
        <h3>最近事件</h3>
        <div class="spacer" style="flex: 1" />
        <router-link class="small" :to="{ name: 'events' }">全部日志 →</router-link>
      </div>
      <div class="card-body">
        <div v-if="!data.recent_events.length" class="mute small">暂无事件</div>
        <div v-else class="log-tail">
          <div v-for="e in data.recent_events" :key="e.id" class="ln">
            <span class="t">{{ fmtTime(e.at, true) }}</span>
            <span class="lv" :class="e.level">{{ e.level.toUpperCase() }}</span>
            <span class="cat">{{ e.category }}</span>
            <span>{{ e.message }}</span>
          </div>
        </div>
      </div>
    </div>

    <div v-if="loading" class="mute small" style="margin-top: 14px">正在加载…</div>
  </div>
</template>
