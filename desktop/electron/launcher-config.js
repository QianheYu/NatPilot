'use strict'
// 启动器配置：控制"启动器自身"的行为，与 Go 服务端的 config.json 是两回事。
//
// 存在 <userData>/launcher.json，刻意与 Web 控制台的配置分开：即使服务没起来，
// 用户也要能改这里（比如把服务路径指错、端口被占用、数据目录换了位置）。
const fs = require('node:fs')
const path = require('node:path')

const DEFAULTS = {
  // 启动器启动时是否自动拉起 Go 服务；关掉后窗口会显示启动面板，由用户手动启动。
  autoStartService: true,
  // 服务可执行文件路径；空 = 自动探测（resources/bin、exe 同目录、仓库 bin…）。
  serverBinary: '',
  // 数据目录；空 = <userData>/natpilot。
  dataDir: '',
  // 监听地址：默认只绑定回环，避免把控制台暴露到局域网。
  host: '127.0.0.1',
  // 固定端口；0 = 由系统分配空闲端口（失败会自动换端口重试）。
  port: 0,
  // 追加给 `serve` 的额外参数，每项一个 token，例如 ["--log-level", "info"]。
  extraArgs: [],
  // 关闭窗口时隐藏到托盘（false = 关闭即退出）。
  closeToTray: true,
  // 开机自启时不弹窗口，只在托盘驻留。
  startHiddenOnAutostart: true,
  // 启动后静默检查一次更新。
  checkUpdatesOnStart: true,
}

function configPath() {
  // electron 只在真正需要路径时加载，这样 sanitize 这类纯函数可以被 node --test
  // 直接覆盖（不需要启动 Electron 运行时）。
  const { app } = require('electron')
  return path.join(app.getPath('userData'), 'launcher.json')
}

// sanitize 把任意输入规范成合法配置：类型错误回退默认值，避免一份坏配置
// 让启动器再也起不来（这正是设置界面最不该出现的结果）。
function sanitize(raw) {
  const out = { ...DEFAULTS }
  if (!raw || typeof raw !== 'object') return out
  if (typeof raw.autoStartService === 'boolean') out.autoStartService = raw.autoStartService
  if (typeof raw.serverBinary === 'string') out.serverBinary = raw.serverBinary.trim()
  if (typeof raw.dataDir === 'string') out.dataDir = raw.dataDir.trim()
  if (typeof raw.host === 'string' && raw.host.trim()) out.host = raw.host.trim()
  const port = Number(raw.port)
  if (Number.isInteger(port) && port >= 0 && port <= 65535) out.port = port
  if (Array.isArray(raw.extraArgs)) {
    out.extraArgs = raw.extraArgs.filter((a) => typeof a === 'string' && a.trim()).map((a) => a.trim())
  }
  if (typeof raw.closeToTray === 'boolean') out.closeToTray = raw.closeToTray
  if (typeof raw.startHiddenOnAutostart === 'boolean') out.startHiddenOnAutostart = raw.startHiddenOnAutostart
  if (typeof raw.checkUpdatesOnStart === 'boolean') out.checkUpdatesOnStart = raw.checkUpdatesOnStart
  return out
}

function loadConfig() {
  try {
    const text = fs.readFileSync(configPath(), 'utf8')
    return sanitize(JSON.parse(text))
  } catch {
    // 不存在或损坏：用默认值，并且不覆盖原文件（留给用户自己修）。
    return { ...DEFAULTS }
  }
}

function saveConfig(raw) {
  const cfg = sanitize(raw)
  const file = configPath()
  fs.mkdirSync(path.dirname(file), { recursive: true })
  // 先写临时文件再 rename：避免写一半断电留下半份 JSON。
  const tmp = `${file}.tmp`
  fs.writeFileSync(tmp, JSON.stringify(cfg, null, 2), 'utf8')
  fs.renameSync(tmp, file)
  return cfg
}

module.exports = { DEFAULTS, configPath, loadConfig, saveConfig, sanitize }
