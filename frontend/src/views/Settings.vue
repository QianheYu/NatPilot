<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import api from '../api'
import Badge from '../components/Badge.vue'
import Switch from '../components/Switch.vue'
import ChangePassword from '../components/ChangePassword.vue'
import { store, toastOk, toastErr, toastWarn } from '../store'
import { fmtTime, fmtUptime } from '../utils/format'

const loading = ref(true)
const info = ref({})
const cfg = ref({})
const overrides = ref({})
const busy = reactive({})
const draft = reactive({
  // 默认与后端一致：不记录。API 返回当前值时会被覆盖。
  'log.level': 'off',
  'retention.events_days': 30,
  'retention.events_max': 5000,
  'retention.scans_days': 30,
  'retention.scans_max': 500,
  'mapper.dry_run': false,
  'mapper.enabled': true,
  'mapper.default_lease_seconds': 86400,
  'mapper.conflict_policy': 'skip',
  'mapper.external_ip_check': true,
  'scanner.max_concurrency': 256,
  'scanner.timeout_ms': 800,
  'scanner.default_ports': '',
  'scanner.grab_banners': true,
  'scanner.enable_lan_scan': true,
  'scanner.auto_monitor_local': false,
  'scanner.auto_interval_seconds': 300,
  'scanner.auto_apply_policy': true,
  'auth.enabled': false,
  'auth.require_for_web': true,
})

// 保存时只提交"改过的键"：以前把整份 draft 全量 PATCH，会把默认值也写成
// 数据库里的持久化覆盖项，之后再改镜像/默认值都不生效（典型症状：
// "换了新镜像，server 模式却仍然免登录"）。
const snapshot = ref('')
function takeSnapshot() {
  snapshot.value = JSON.stringify(draft)
}
function changedKeys() {
  let base = {}
  try { base = JSON.parse(snapshot.value || '{}') } catch { base = {} }
  return Object.keys(draft).filter((k) => JSON.stringify(draft[k]) !== JSON.stringify(base[k]))
}

// 鉴权开关不能因为"当前免登录"就被禁用——否则一旦关了鉴权就再也打不开，
// 形成死锁。规则改成单向：打开随时可以；关闭是降权操作，要求当前会话已认证。
const authCurrentlyOn = computed(() => !!(cfg.value.auth && cfg.value.auth.enabled))
const authToggleDisabled = computed(() => authCurrentlyOn.value && !store.identity.authenticated)

async function clearOverride(key) {
  try {
    const res = await api.patchSettings({ [key]: '' })
    if ((res.cleared || []).includes(key)) {
      toastOk(`${key} 的持久化覆盖已清除，重启服务后按配置/默认值生效`)
    } else {
      toastWarn(`${key} 没有可清除的覆盖项`, '无需清理')
    }
    await load()
  } catch (err) {
    toastErr(err.message, '清除覆盖项失败')
  }
}

async function load() {
  loading.value = true
  try {
    const [i, s] = await Promise.all([api.systemInfo(), api.settings()])
    info.value = i
    cfg.value = s.config || {}
    overrides.value = s.overrides || {}
    applyDraft(s.config || {})
    takeSnapshot()
  } catch (err) {
    toastErr(err.message, '加载设置失败')
  } finally {
    loading.value = false
  }
}

function applyDraft(c) {
  for (const key of Object.keys(draft)) {
    const [section, name] = key.split('.')
    const val = c[section] ? c[section][name] : undefined
    if (val !== undefined && val !== null) draft[key] = val
  }
}

async function save() {
  busy.save = true
  try {
    const changed = changedKeys()
    if (!changed.length) {
      toastWarn('没有需要保存的改动', '未修改')
      return
    }
    const payload = {}
    for (const k of changed) payload[k] = draft[k]
    const res = await api.patchSettings(payload)
    // 安全/网络边界类配置（http.mode、auth.enabled 等）只落库，重启后才生效。
    const applied = res.applied || []
    const pending = res.restart_required || []
    if (pending.length) {
      toastOk(`已保存 ${applied.length} 项配置；${pending.join('、')} 需重启后生效`)
    } else {
      toastOk(`已保存 ${applied.length} 项配置`)
    }
    load()
  } catch (err) {
    toastErr(err.message, '保存失败')
  } finally {
    busy.save = false
  }
}

async function reloadMapper() {
  busy.recon = true
  try {
    const res = await api.reconcile()
    toastOk(res.ok ? '映射同步完成' : res.error)
  } catch (err) {
    toastErr(err.message, '同步失败')
  } finally {
    busy.recon = false
  }
}

const protocols = computed(() => (cfg.value.mapper && cfg.value.mapper.protocols) || store.meta.protocols || [])

onMounted(load)
</script>

<template>
  <div>
    <div class="grid cols-4" style="margin-bottom: 16px">
      <div class="stat">
        <div class="label">版本</div>
        <div class="value" style="font-size: 20px">{{ info.version || '—' }}</div>
        <div class="sub">{{ info.mode }} 模式</div>
      </div>
      <div class="stat">
        <div class="label">运行时长</div>
        <div class="value" style="font-size: 20px">{{ fmtUptime(info.uptime_s) }}</div>
        <div class="sub">启动于 {{ fmtTime(info.started_at) }}</div>
      </div>
      <div class="stat">
        <div class="label">本机地址</div>
        <div class="value mono" style="font-size: 20px">{{ info.primary_ip || '—' }}</div>
        <div class="sub">网关 {{ info.gateway || '未知' }}</div>
      </div>
      <div class="stat">
        <div class="label">账号 / 鉴权</div>
        <div class="value" style="font-size: 20px">{{ info.users || 0 }}</div>
        <div class="sub">{{ info.auth ? '需要登录' : '免登录（桌面模式）' }}</div>
      </div>
    </div>

    <div class="grid cols-2" style="align-items: start">
      <div class="card">
        <div class="card-head">
          <h3>映射引擎</h3>
          <div class="spacer" style="flex: 1" />
          <Badge :kind="draft['mapper.dry_run'] ? 'warn' : 'ok'">{{ draft['mapper.dry_run'] ? '演练模式' : '实际生效' }}</Badge>
        </div>
        <div class="card-body">
          <label class="field row" style="gap: 10px">
            <Switch v-model="draft['mapper.enabled']" />
            <span class="small dim">启用映射引擎（定期同步启用规则）</span>
          </label>
          <label class="field row" style="gap: 10px">
            <Switch v-model="draft['mapper.dry_run']" />
            <span class="small dim">演练模式（只记录不真正下发到网关）</span>
          </label>
          <label class="field row" style="gap: 10px">
            <Switch v-model="draft['mapper.external_ip_check']" />
            <span class="small dim">允许通过公网服务校验外部 IP</span>
          </label>
          <div class="grid cols-2">
            <label class="field">
              <span>默认租约（秒）</span>
              <input v-model.number="draft['mapper.default_lease_seconds']" class="input" type="number" />
            </label>
            <label class="field">
              <span>冲突处理策略</span>
              <select v-model="draft['mapper.conflict_policy']" class="select">
                <option value="skip">跳过冲突规则</option>
                <option value="error">报错</option>
              </select>
            </label>
          </div>
          <div class="divider" />
          <div class="row wrap" style="gap: 6px">
            <span class="mute small">启用的协议顺序：</span>
            <Badge v-for="p in protocols" :key="p" kind="accent">{{ p }}</Badge>
          </div>
          <div class="row" style="margin-top: 12px">
            <button class="btn" :disabled="busy.recon" @click="reloadMapper">立即同步映射</button>
          </div>
        </div>
      </div>

      <div class="card">
        <div class="card-head"><h3>扫描与自动监控</h3></div>
        <div class="card-body">
          <label class="field row" style="gap: 10px">
            <Switch v-model="draft['scanner.enable_lan_scan']" />
            <span class="small dim">允许局域网 / 网段扫描</span>
          </label>
          <label class="field row" style="gap: 10px">
            <Switch v-model="draft['scanner.grab_banners']" />
            <span class="small dim">抓取服务指纹（Banner）</span>
          </label>
          <div class="grid cols-2">
            <label class="field">
              <span>并发数</span>
              <input v-model.number="draft['scanner.max_concurrency']" class="input" type="number" />
            </label>
            <label class="field">
              <span>超时（毫秒）</span>
              <input v-model.number="draft['scanner.timeout_ms']" class="input" type="number" />
            </label>
          </div>
          <label class="field">
            <span>默认端口集</span>
            <input v-model="draft['scanner.default_ports']" class="input mono" />
          </label>
          <div class="divider" />
          <label class="field row" style="gap: 10px">
            <Switch v-model="draft['scanner.auto_monitor_local']" />
            <span class="small dim">启动时自动开启本机监控</span>
          </label>
          <div class="grid cols-2">
            <label class="field">
              <span>监控间隔（秒）</span>
              <input v-model.number="draft['scanner.auto_interval_seconds']" class="input" type="number" />
            </label>
            <label class="field row" style="gap: 10px; align-items: end; padding-bottom: 6px">
              <Switch v-model="draft['scanner.auto_apply_policy']" />
              <span class="small dim">自动套用策略</span>
            </label>
          </div>
        </div>
      </div>
    </div>

    <div class="grid cols-2" style="align-items: start; margin-top: 16px">
      <div class="card">
        <div class="card-head"><h3>安全与登录</h3></div>
        <div class="card-body">
          <label class="field row" style="gap: 10px; flex-wrap: wrap">
            <Switch v-model="draft['auth.enabled']" :disabled="authToggleDisabled" />
            <span class="small dim">
              启用登录鉴权（web / 服务端部署建议开启；桌面模式免登录）
              <template v-if="authToggleDisabled">· 关闭鉴权属于降权操作，需先以管理员登录</template>
              <template v-else-if="!authCurrentlyOn">· 当前免登录，可以直接打开</template>
            </span>
            <span
              v-if="overrides['auth.enabled'] !== undefined"
              class="small"
              style="color: var(--warn); flex-basis: 100%"
            >
              数据库里存在持久化覆盖 auth.enabled={{ overrides['auth.enabled'] }}（优先级高于环境变量与默认值）
              <button class="btn sm" style="margin-left: 8px" @click="clearOverride('auth.enabled')">清除该覆盖项</button>
            </span>
          </label>
          <label class="field row" style="gap: 10px">
            <Switch v-model="draft['auth.require_for_web']" />
            <span class="small dim">Web 端强制登录</span>
          </label>
          <p class="hint">
            修改鉴权开关后需要重启进程才会完全生效。会话使用加密 Cookie，密码以 bcrypt 存储。
          </p>
          <div class="divider" />
          <h4 style="margin: 0 0 10px; font-size: 13px">修改我的密码</h4>
          <ChangePassword />
        </div>
      </div>

      <div class="card">
        <div class="card-head"><h3>数据保留</h3></div>
        <div class="card-body">
          <div class="grid cols-2">
            <label class="field">
              <span>日志审计 · 最多保留条数</span>
              <input v-model.number="draft['retention.events_max']" class="input" type="number" min="0" placeholder="0 = 不限制" />
              <div class="hint">超出后自动删除最旧的记录。</div>
            </label>
            <label class="field">
              <span>日志审计 · 保留天数</span>
              <input v-model.number="draft['retention.events_days']" class="input" type="number" min="0" placeholder="0 = 永久保留" />
              <div class="hint">早于该天数的记录会被清理。</div>
            </label>
            <label class="field">
              <span>扫描任务 · 最多保留条数</span>
              <input v-model.number="draft['retention.scans_max']" class="input" type="number" min="0" placeholder="0 = 不限制" />
              <div class="hint">包含其开放端口记录一起清理。</div>
            </label>
            <label class="field">
              <span>扫描任务 · 保留天数</span>
              <input v-model.number="draft['retention.scans_days']" class="input" type="number" min="0" placeholder="0 = 永久保留" />
            </label>
          </div>
          <p class="hint">清理任务每 5 分钟运行一次，修改后无需重启即可生效。</p>
        </div>
      </div>

      <div class="card">
        <div class="card-head"><h3>日志与路径</h3></div>
        <div class="card-body">
          <div class="grid cols-2">
            <label class="field">
              <span>日志级别</span>
              <select v-model="draft['log.level']" class="select">
                <option value="off">off（不记录）</option>
                <option value="debug">debug</option>
                <option value="info">info</option>
                <option value="warn">warn</option>
                <option value="error">error</option>
              </select>
            </label>
            <label class="field">
              <span>数据目录</span>
              <input class="input mono" :value="info.data_dir" readonly />
            </label>
          </div>
          <label class="field">
            <span>数据库路径</span>
            <input class="input mono" :value="info.db_path" readonly />
          </label>
          <div class="row wrap">
            <a class="btn" :href="api.exportURL('json')" target="_blank">导出配置 (JSON)</a>
            <a class="btn" :href="api.exportURL('csv')" target="_blank">导出规则 (CSV)</a>
          </div>
          <p class="hint">导出的 JSON 包含规则、策略、计划与主机清单，可在其它实例中导入。</p>
        </div>
      </div>
    </div>

    <div class="toolbar" style="margin-top: 16px">
      <span class="dim small">部分配置项保存后需重启服务端处理；修改将同时写入 SQLite 设置表。</span>
      <div class="spacer" style="flex: 1" />
      <button class="btn" @click="load">重置</button>
      <button class="btn primary" :disabled="busy.save" @click="save">保存设置</button>
    </div>
  </div>
</template>
