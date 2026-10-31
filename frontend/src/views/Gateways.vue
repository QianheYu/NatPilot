<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import api from '../api'
import Badge from '../components/Badge.vue'
import EmptyState from '../components/EmptyState.vue'
import Modal from '../components/Modal.vue'
import Switch from '../components/Switch.vue'
import { toastOk, toastErr, canWrite } from '../store'
import { fmtRelative, gatewayKindInfo } from '../utils/format'

const loading = ref(true)
const gateways = ref([])
const preferred = ref('')
const lastDiscover = ref('')
const lastError = ref('')
const order = ref([])
const externalIP = ref('')
const gwMappings = ref([])
const showMappings = ref(false)
const busy = reactive({})

const protocolLabels = {
  igd2: 'UPnP IGD v2（首选）',
  igd1: 'UPnP IGD v1（兼容）',
  pcp: 'PCP RFC 6887',
  natpmp: 'NAT-PMP RFC 6886',
}

async function load() {
  loading.value = true
  try {
    const res = await api.gateways()
    const list = res.gateways || []
    list.sort((a, b) => gatewayKindInfo(a.kind).rank - gatewayKindInfo(b.kind).rank)
    gateways.value = list
    preferred.value = res.preferred || ''
    lastDiscover.value = res.last_discover
    lastError.value = res.last_error
    order.value = res.configured_order || []
  } catch (err) {
    toastErr(err.message, '加载网关失败')
  } finally {
    loading.value = false
  }
}

function gwKey(g) {
  return `${g.kind}|${g.address}|${g.port}`
}

async function discover() {
  busy.discover = true
  try {
    const res = await api.discover()
    externalIP.value = res.external_ip || ''
    toastOk(`发现 ${res.gateways.length} 个网关`)
    load()
  } catch (err) {
    toastErr(err.message, '探测失败')
  } finally {
    busy.discover = false
  }
}

async function prefer(g) {
  try {
    await api.preferGateway(g.id)
    toastOk(`已设为首选：${gatewayKindInfo(g.kind).label}`)
    load()
  } catch (err) {
    toastErr(err.message, '设置失败')
  }
}

async function toggleEnabled(g, v) {
  try {
    await api.enableGateway(g.id, v)
    load()
  } catch (err) {
    g.enabled = !v
    toastErr(err.message, '切换失败')
  }
}

async function removeGw(g) {
  try {
    await api.deleteGateway(g.id)
    toastOk('已移除网关记录')
    load()
  } catch (err) {
    toastErr(err.message, '删除失败')
  }
}

async function fetchExternal() {
  busy.external = true
  try {
    const res = await api.externalIP()
    if (res.ok) {
      externalIP.value = res.external_ip
      toastOk(`外部 IP：${res.external_ip}`)
    } else {
      toastErr(res.error || '获取失败')
    }
  } catch (err) {
    toastErr(err.message, '获取失败')
  } finally {
    busy.external = false
  }
}

async function loadMappings() {
  try {
    const res = await api.gatewayMappings()
    gwMappings.value = res.mappings || []
    showMappings.value = true
  } catch (err) {
    toastErr(err.message, '该网关可能不支持映射枚举')
  }
}

const hasIgd = computed(() => gateways.value.some((g) => g.kind === 'igd2' || g.kind === 'igd1'))

onMounted(load)
</script>

<template>
  <div>
    <div class="toolbar">
      <span class="dim small">
        协议优先级：{{ order.map((o) => protocolLabels[o] || o).join(' → ') || '默认（新协议优先）' }}
      </span>
      <div class="spacer" style="flex: 1" />
      <span v-if="externalIP" class="badge accent">外部 IP {{ externalIP }}</span>
      <button class="btn" :disabled="busy.external" @click="fetchExternal">刷新外部 IP</button>
      <button class="btn" v-if="hasIgd" @click="loadMappings">枚举网关映射</button>
      <button class="btn primary" :disabled="busy.discover || !canWrite()" @click="discover">
        <span v-if="busy.discover" class="spin">◌</span>发现网关
      </button>
    </div>

    <div class="card">
      <div class="card-head">
        <h3>已发现的 NAT 网关</h3>
        <div class="spacer" style="flex: 1" />
        <span class="mute small">上次探测 {{ fmtRelative(lastDiscover) }}</span>
      </div>
      <div class="card-body flush">
        <EmptyState v-if="!gateways.length" icon="⬡" title="尚未发现网关"
          hint="确保设备与路由器在同一网段；UPnP / PCP / NAT-PMP 需在路由器上开启">
          <button class="btn primary" :disabled="busy.discover || !canWrite()" @click="discover">立即探测</button>
        </EmptyState>
        <div v-else class="table-wrap">
          <table class="data">
            <thead>
              <tr>
                <th>协议</th>
                <th>名称 / 型号</th>
                <th>地址</th>
                <th>外部 IP</th>
                <th>能力</th>
                <th>最近发现</th>
                <th>启用</th>
                <th class="right">操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="g in gateways" :key="g.id">
                <td>
                  <Badge :kind="gatewayKindInfo(g.kind).kind" :dot="gwKey(g) === preferred">
                    {{ gatewayKindInfo(g.kind).label }}
                  </Badge>
                  <div v-if="gwKey(g) === preferred" class="small mute" style="margin-top: 3px">当前首选</div>
                </td>
                <td>
                  <div>{{ g.name || '(未命名)' }}</div>
                  <div class="small mute">{{ g.manufacturer }} {{ g.model_name }}</div>
                </td>
                <td class="mono small">{{ g.address }}:{{ g.port }}</td>
                <td class="mono small">{{ g.external_ip || '—' }}</td>
                <td>
                  <span v-for="c in g.capabilities" :key="c" class="tag">{{ c }}</span>
                </td>
                <td class="small mute nowrap">{{ fmtRelative(g.last_seen) }}</td>
                <td><Switch :model-value="g.enabled" :disabled="!canWrite()" @update:model-value="(v) => toggleEnabled(g, v)" /></td>
                <td class="right">
                  <div class="cell-actions">
                    <button class="btn sm" :disabled="gwKey(g) === preferred || !canWrite()" @click="prefer(g)">设为首选</button>
                    <button class="btn sm danger" :disabled="!canWrite()" @click="removeGw(g)">移除</button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
      <div v-if="lastError" class="card-body">
        <div class="badge warn" style="display: block; padding: 8px 10px">最近一次探测错误：{{ lastError }}</div>
      </div>
    </div>

    <div class="grid cols-2" style="margin-top: 16px">
      <div class="card">
        <div class="card-head"><h3>协议说明</h3></div>
        <div class="card-body">
          <div v-for="(label, id) in protocolLabels" :key="id" style="margin-bottom: 10px">
            <Badge :kind="gatewayKindInfo(id).kind">{{ gatewayKindInfo(id).label }}</Badge>
            <div class="small dim" style="margin-top: 4px">
              {{
                id === 'igd2'
                  ? '支持 AddAnyPortMapping 与租约，优先使用；可让网关自动分配可用外部端口。'
                  : id === 'igd1'
                  ? '传统 WANIPConnection / WANPPPConnection，兼容老路由器。'
                  : id === 'pcp'
                  ? 'RFC 6887，最新且语义最完整的端口控制协议，支持显式租约与删除。'
                  : 'RFC 6886，兼容性最好；仅支持 TCP/UDP 单端口映射。'
              }}
            </div>
          </div>
        </div>
      </div>
      <div class="card">
        <div class="card-head"><h3>工作方式</h3></div>
        <div class="card-body dim small">
          <p>
            NatPilot 会按“协议优先级”从上到下依次尝试，只有成功的映射才会写入记录；
            首选网关失败时会自动回退到其它可用网关与协议。
          </p>
          <p>新协议（IGD v2 / PCP）优先用于分配端口与租约；旧协议（IGD v1 / NAT-PMP）作为兼容层提供服务。</p>
          <p style="margin: 0">
            端口冲突处理策略为 <b>{{ 'skip' }}</b>：当两条启用的规则请求同一个外部端口时，后一条会被标记为冲突而不会覆盖先前的映射。
          </p>
        </div>
      </div>
    </div>

    <Modal v-if="showMappings" title="网关上的端口映射（含其它程序创建）" wide @close="showMappings = false">
      <div class="table-wrap" style="max-height: 420px; overflow-y: auto">
        <table class="data">
          <thead>
            <tr>
              <th>协议</th>
              <th>外部端口</th>
              <th>内部目标</th>
              <th>租约</th>
              <th>描述</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="(m, i) in gwMappings" :key="i">
              <td class="small">{{ m.protocol }}</td>
              <td class="mono small">:{{ m.external_port }}</td>
              <td class="mono small">{{ m.internal_host }}:{{ m.internal_port }}</td>
              <td class="small mute">{{ m.lease_seconds ? m.lease_seconds + 's' : '永久' }}</td>
              <td class="small">{{ m.description || '—' }}</td>
            </tr>
          </tbody>
        </table>
        <div v-if="!gwMappings.length" class="mute small" style="padding: 14px">
          该网关未返回映射列表（PCP / NAT-PMP 不支持枚举，IGD 可能也未实现）。
        </div>
      </div>
    </Modal>
  </div>
</template>
