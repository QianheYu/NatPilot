<script setup>
// 启动器设置（桌面版专用页）
//
// 与 Web 控制台的「系统设置」是两套配置：
//   - 系统设置  → Go 服务端 config.json（映射、扫描、日志…）
//   - 启动器设置 → Electron 壳的 launcher.json（怎么拉起服务、传什么参数）
// 这里通过 preload 暴露的 window.launcher 走 IPC，浏览器访问时会提示不可用。
import { computed, onMounted, reactive, ref } from 'vue'
import { toastOk, toastErr } from '../store'

const available = computed(() => typeof window !== 'undefined' && !!window.launcher)
const state = ref({ config: {}, url: '', binaryPath: '', dataDir: '', version: '' })
const saving = ref(false)
const busy = reactive({ start: false, stop: false, restart: false })

const form = reactive({
  autoStartService: true,
  serverBinary: '',
  dataDir: '',
  host: '127.0.0.1',
  port: 0,
  extraArgs: '',
  closeToTray: true,
  startHiddenOnAutostart: true,
  checkUpdatesOnStart: true,
})

const running = computed(() => Boolean(state.value.url))

function fill(cfg) {
  form.autoStartService = cfg.autoStartService !== false
  form.serverBinary = cfg.serverBinary || ''
  form.dataDir = cfg.dataDir || ''
  form.host = cfg.host || '127.0.0.1'
  form.port = Number(cfg.port) || 0
  form.extraArgs = (cfg.extraArgs || []).join(' ')
  form.closeToTray = cfg.closeToTray !== false
  form.startHiddenOnAutostart = cfg.startHiddenOnAutostart !== false
  form.checkUpdatesOnStart = cfg.checkUpdatesOnStart !== false
}

async function load() {
  if (!available.value) return
  try {
    state.value = await window.launcher.getState()
    fill(state.value.config || {})
  } catch (err) {
    toastErr(String((err && err.message) || err), '读取启动器配置失败')
  }
}

function payload() {
  return {
    autoStartService: form.autoStartService,
    serverBinary: form.serverBinary.trim(),
    dataDir: form.dataDir.trim(),
    host: form.host.trim() || '127.0.0.1',
    port: Number(form.port) || 0,
    extraArgs: form.extraArgs.split(/\s+/).filter(Boolean),
    closeToTray: form.closeToTray,
    startHiddenOnAutostart: form.startHiddenOnAutostart,
    checkUpdatesOnStart: form.checkUpdatesOnStart,
  }
}

async function save() {
  saving.value = true
  try {
    const res = await window.launcher.save(payload())
    if (res.ok) {
      toastOk('已保存；服务相关改动需重启服务生效')
      await load()
    } else {
      toastErr(res.error, '保存失败')
    }
  } finally {
    saving.value = false
  }
}

async function pickBinary() {
  const p = await window.launcher.pickBinary()
  if (p) form.serverBinary = p
}

async function pickDir() {
  const p = await window.launcher.pickDir()
  if (p) form.dataDir = p
}

async function resetDefaults() {
  const res = await window.launcher.resetDefaults()
  if (res && res.config) fill(res.config)
  toastOk('已恢复默认值，记得点保存')
}

async function startService() {
  busy.start = true
  try {
    const res = await window.launcher.startService()
    res.ok ? toastOk('服务已启动') : toastErr(res.error, '启动失败')
    await load()
  } finally {
    busy.start = false
  }
}

async function stopService() {
  busy.stop = true
  try {
    const res = await window.launcher.stopService()
    res.ok ? toastOk('服务已停止') : toastErr(res.error, '停止失败')
    await load()
  } finally {
    busy.stop = false
  }
}

function openDataDir() {
  window.launcher.openDataDir()
}

async function restartService() {
  busy.restart = true
  try {
    const res = await window.launcher.restartService()
    res.ok ? toastOk('服务已重启') : toastErr(res.error, '重启失败')
    await load()
  } finally {
    busy.restart = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <div v-if="!available" class="card">
      <div class="card-body">
        <h3 style="margin: 0 0 6px">启动器设置仅在桌面版可用</h3>
        <p class="small dim" style="margin: 0">
          当前是通过浏览器访问服务端的，没有 Electron 启动器可供配置。
          桌面版中此页可设置服务可执行文件、监听地址与端口、数据目录以及启动器行为。
        </p>
      </div>
    </div>

    <template v-else>
      <div class="card">
        <div class="card-head"><h3>运行状态</h3></div>
        <div class="card-body">
          <div class="grid cols-2">
            <label class="field">
              <span>服务状态</span>
              <input class="input" :value="running ? '运行中' : '已停止'" readonly />
            </label>
            <label class="field">
              <span>控制台地址</span>
              <input class="input mono" :value="state.url || '—'" readonly />
            </label>
            <label class="field">
              <span>服务可执行文件</span>
              <input class="input mono" :value="state.binaryPath || '（未找到）'" readonly />
            </label>
            <label class="field">
              <span>启动器版本</span>
              <input class="input mono" :value="state.version || '—'" readonly />
            </label>
          </div>
          <div class="row wrap">
            <button class="btn primary" :disabled="running || busy.start" @click="startService">
              <span v-if="busy.start" class="spin">◌</span>启动服务
            </button>
            <button class="btn" :disabled="!running || busy.stop" @click="stopService">
              <span v-if="busy.stop" class="spin">◌</span>停止服务
            </button>
            <button class="btn" :disabled="busy.restart" @click="restartService">
              <span v-if="busy.restart" class="spin">◌</span>重启服务
            </button>
            <button class="btn" @click="openDataDir">打开数据目录</button>
          </div>
        </div>
      </div>

      <div class="card">
        <div class="card-head"><h3>服务启动参数</h3></div>
        <div class="card-body">
          <label class="field row" style="gap: 10px">
            <input v-model="form.autoStartService" type="checkbox" />
            <span class="small dim">启动器启动时自动运行服务（取消后只显示启动面板，需手动启动）</span>
          </label>
          <div class="grid cols-2">
            <label class="field">
              <span>服务可执行文件</span>
              <div class="row" style="gap: 8px">
                <input v-model="form.serverBinary" class="input mono" placeholder="留空 = 自动探测" />
                <button class="btn" @click="pickBinary">浏览…</button>
              </div>
              <div class="hint">留空时按 resources/bin → 安装目录 → 仓库 bin 顺序自动查找。</div>
            </label>
            <label class="field">
              <span>数据目录</span>
              <div class="row" style="gap: 8px">
                <input v-model="form.dataDir" class="input mono" placeholder="留空 = 默认 userData/natpilot" />
                <button class="btn" @click="pickDir">浏览…</button>
              </div>
              <div class="hint">数据库与配置都放这里；切换后服务会用另一个数据库启动。</div>
            </label>
            <label class="field">
              <span>监听地址</span>
              <input v-model="form.host" class="input mono" placeholder="127.0.0.1" />
              <div class="hint">默认只绑定回环地址，控制台不会暴露到局域网。</div>
            </label>
            <label class="field">
              <span>端口（0 = 自动选择）</span>
              <input v-model.number="form.port" class="input" type="number" min="0" max="65535" />
              <div class="hint">固定端口便于书签或防火墙放行；被占用时会自动换端口重试。</div>
            </label>
          </div>
          <label class="field">
            <span>额外启动参数</span>
            <input v-model="form.extraArgs" class="input mono" placeholder="例如：--log-level info --log-format json" />
            <div class="hint">按空格分隔，追加在 <code>serve</code> 之前。</div>
          </label>
        </div>
      </div>

      <div class="card">
        <div class="card-head"><h3>启动器行为</h3></div>
        <div class="card-body">
          <label class="field row" style="gap: 10px">
            <input v-model="form.closeToTray" type="checkbox" />
            <span class="small dim">关闭窗口时驻留托盘（取消后关闭窗口即退出）</span>
          </label>
          <label class="field row" style="gap: 10px">
            <input v-model="form.startHiddenOnAutostart" type="checkbox" />
            <span class="small dim">开机自启时静默启动，只在托盘驻留</span>
          </label>
          <label class="field row" style="gap: 10px">
            <input v-model="form.checkUpdatesOnStart" type="checkbox" />
            <span class="small dim">启动约 10 秒后静默检查更新</span>
          </label>
        </div>
      </div>

      <div class="toolbar">
        <button class="btn primary" :disabled="saving" @click="save">
          <span v-if="saving" class="spin">◌</span>保存设置
        </button>
        <button class="btn" @click="resetDefaults">恢复默认</button>
        <span class="dim small">修改服务参数后需要「重启服务」才会生效。</span>
      </div>
    </template>
  </div>
</template>
