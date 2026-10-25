// Electron main process for the NatPilot desktop edition.
//
// Behaviour (identical on Windows, macOS and Linux):
//
//   * boots the Go server on a free loopback port in desktop mode (no login),
//   * shows the console in a native window,
//   * lives in the system tray: closing the window only hides it,
//   * the only way to really stop it is "退出 NatPilot" in the tray menu
//     (⌘Q on macOS is honoured as an explicit quit request),
//   * the tray also exposes 系统设置 (the runtime configuration page),
//     重启服务 (needed after settings that require a new process: listen port,
//     TLS, ...), 在浏览器中打开, 打开数据目录, 开机自启 and 检查更新,
//   * started as a login item (`--autostart`) it stays in the tray without
//     opening a window, and still boots the server,
//   * killing the app always stops the server child process.
//
// Run:  npm install && npm start      (development)
//       npm run dist                  (package with electron-builder)

const { app, BrowserWindow, Tray, Menu, nativeImage, shell, dialog } = require('electron')
const { spawn, spawnSync } = require('node:child_process')
const http = require('node:http')
const https = require('node:https')
const net = require('node:net')
const path = require('node:path')
const fs = require('node:fs')
const autostart = require('./autostart')
const { isNewer } = require('./version')

let serverProcess = null
let mainWindow = null
let tray = null
let serverURL = null
let quitting = false

const TRAY_TOOLTIP = 'NatPilot — NAT 端口映射控制台'

// AUTOSTART_ARG is appended to the login item command; when the shell is started
// with it (i.e. by the OS at login) no window is opened, only the tray appears.
const STARTED_HIDDEN = process.argv.includes(autostart.AUTOSTART_ARG)

// UPDATE_REPO is the GitHub repository the "检查更新" entry asks for the latest
// release. It points at the upstream project, so forks should change it.
const UPDATE_REPO = 'ekko-studio/natpilot'
const UPDATE_TIMEOUT_MS = 8000

// ---------------------------------------------------------------- window state

function statePath() {
  return path.join(app.getPath('userData'), 'window-state.json')
}

function loadWindowState() {
  try {
    const raw = JSON.parse(fs.readFileSync(statePath(), 'utf8'))
    if (typeof raw.width === 'number' && raw.width >= 800 && typeof raw.height === 'number') {
      return raw
    }
  } catch {
    /* first run or a corrupt file */
  }
  return null
}

function saveWindowState() {
  if (!mainWindow || mainWindow.isDestroyed()) return
  try {
    const bounds = mainWindow.getNormalBounds ? mainWindow.getNormalBounds() : mainWindow.getBounds()
    fs.mkdirSync(path.dirname(statePath()), { recursive: true })
    fs.writeFileSync(statePath(), JSON.stringify(bounds))
  } catch {
    /* never fail on a cosmetic feature */
  }
}

// ---------------------------------------------------------------- server side

// serverBinary resolves the natpilot server executable this shell spawns.
//
// Layouts the project actually ships:
//
//   development       <repo>/bin/natpilot[.exe]
//                     main.js lives in <repo>/desktop/electron, so the repo root
//                     is two levels up — looking only one level up (desktop/bin)
//                     is why the binary was never found.
//   electron-builder  <app>/resources/bin/natpilot[.exe]
//                     (extraResources maps ../bin/ → bin/ in resources)
//   portable / zip    <directory of the NatPilot executable>/natpilot[.exe]
//
// Returns { path, probed }: probed lists every location that was examined so a
// failed start can say exactly where the server was expected.
function serverBinary() {
  const name = process.platform === 'win32' ? 'natpilot.exe' : 'natpilot'
  const resources = process.resourcesPath || ''
  const exeDir = path.dirname(process.execPath)
  let appPath = ''
  try {
    appPath = app.getAppPath() || ''
  } catch {
    /* not available outside the Electron runtime */
  }
  const dirs = [
    resources && path.join(resources, 'bin'), // packaged: resources/bin
    resources, // packed next to app.asar
    exeDir, // bundled next to the Electron executable
    path.join(exeDir, 'bin'),
    appPath && path.join(appPath, 'bin'),
    appPath,
    path.join(__dirname, '..', '..', 'bin'), // repo root bin/ (npm start)
    path.join(__dirname, '..', 'bin'), // desktop/bin
    path.join(__dirname, 'bin'), // desktop/electron/bin
    path.join(process.cwd(), 'bin'),
    process.cwd(),
  ]
  const probed = []
  for (const dir of dirs) {
    if (!dir) continue
    const candidate = path.resolve(path.join(dir, name))
    if (probed.includes(candidate)) continue
    probed.push(candidate)
    try {
      if (fs.existsSync(candidate) && fs.statSync(candidate).isFile()) return { path: candidate, probed }
    } catch {
      /* unreadable location: keep looking */
    }
  }
  return { path: name, probed } // fall back to PATH
}

// freePort probes a free loopback port.
//
// A probe-and-release port can always be stolen between close() and the server's
// bind (classic TOCTOU), so this is only a hint: startServer retries with a fresh
// port whenever the child loses that race. Correctness does not depend on the
// window being closed.
function freePort() {
  return new Promise((resolve, reject) => {
    const srv = net.createServer()
    srv.unref()
    srv.on('error', reject)
    srv.listen(0, '127.0.0.1', () => {
      const { port } = srv.address()
      srv.close(() => resolve(port))
    })
  })
}

// waitForHealth polls /api/health until it answers 200. isCanceled aborts the poll
// as soon as the child it waits for dies, so a failed attempt does not keep polling
// for the whole timeout.
function waitForHealth(url, timeoutMs = 20000, isCanceled = () => false) {
  const started = Date.now()
  return new Promise((resolve, reject) => {
    let done = false
    let timer = null
    const finish = (fn, err) => {
      if (done) return
      done = true
      if (timer) {
        clearTimeout(timer)
        timer = null
      }
      fn(err)
    }
    const tick = () => {
      if (done) return
      if (isCanceled()) return finish(reject, new Error('server exited before becoming ready'))
      const req = http.get(url, (res) => {
        res.resume()
        if (res.statusCode === 200) return finish(resolve)
        retry()
      })
      req.on('error', retry)
      req.setTimeout(1500, () => req.destroy())
    }
    const retry = () => {
      if (done) return
      if (isCanceled()) return finish(reject, new Error('server exited before becoming ready'))
      if (Date.now() - started > timeoutMs) {
        return finish(reject, new Error(`server did not become ready within ${timeoutMs} ms`))
      }
      timer = setTimeout(tick, 350)
    }
    tick()
  })
}

const SERVER_START_ATTEMPTS = 5
const SERVER_START_DEADLINE_MS = 45000
const SERVER_READY_TIMEOUT_MS = 20000
const SERVER_STOP_GRACE_MS = 3000

function serverArgs(port, dataDir) {
  return ['--host', '127.0.0.1', '--port', String(port), '--mode', 'desktop', '--data-dir', dataDir, 'serve']
}

function spawnServer(binPath, port, dataDir) {
  return spawn(binPath, serverArgs(port, dataDir), {
    env: { ...process.env, NATPILOT_DESKTOP: '1', NATPILOT_PARENT_PID: String(process.pid) },
    stdio: ['ignore', 'pipe', 'pipe'],
    windowsHide: true,
  })
}

// handleServerExit cleans up after the server exited on its own.
function handleServerExit(child, code, signal) {
  if (serverProcess !== child) return // a stop was requested, not a crash
  serverProcess = null
  serverURL = null
  updateTray()
  if (quitting) return
  const why =
    code === null || code === undefined ? `信号 ${signal || '未知'}` : `退出码 ${code}${signal ? `，信号 ${signal}` : ''}`
  dialog.showErrorBox(
    'NatPilot 服务已停止',
    `后台服务意外退出（${why}）。\n可以从托盘菜单退出后重新启动。`
  )
}

// launchAndWait spawns one server instance on `port` and waits until /api/health
// answers. A child that dies early (a lost port race, a broken data dir, ...) or
// never becomes ready is terminated before the result is reported, so the next
// attempt never runs next to a half-started server.
function launchAndWait(binPath, port, dataDir, url, readyTimeoutMs) {
  return new Promise((resolve) => {
    let child
    try {
      child = spawnServer(binPath, port, dataDir)
    } catch (err) {
      resolve({ ok: false, permanent: true, reason: `spawn ${binPath}: ${err.message}` })
      return
    }
    let exited = null
    let accepted = false
    let settled = false
    const settle = (result) => {
      if (settled) return
      settled = true
      if (result.ok) {
        accepted = true
        serverProcess = child
        updateTray()
        resolve(result)
        return
      }
      if (serverProcess === child) {
        serverProcess = null
        updateTray()
      }
      terminateChild(child, 800)
        .catch(() => {})
        .then(() => resolve(result))
    }
    child.on('error', (err) => {
      if (accepted) {
        console.error(`[natpilot] server process error: ${err.message}`)
        return
      }
      // A missing, non-executable or blocked binary never fixes itself: do not retry it.
      const permanent =
        err.code === 'ENOENT' || err.code === 'EACCES' || err.code === 'ENOEXEC' || err.code === 'EPERM'
      settle({ ok: false, permanent, reason: `spawn ${binPath}: ${err.message}` })
    })
    child.on('exit', (code, signal) => {
      exited = { code, signal }
      if (accepted) {
        handleServerExit(child, code, signal)
        return
      }
      settle({ ok: false, reason: `exited before it was ready (code=${code}, signal=${signal})` })
    })
    child.stdout.on('data', (d) => process.stdout.write(`[natpilot] ${d}`))
    child.stderr.on('data', (d) => process.stderr.write(`[natpilot] ${d}`))
    serverProcess = child
    updateTray()
    waitForHealth(`${url}/api/health`, readyTimeoutMs, () => exited !== null).then(
      () => {
        if (exited) {
          settle({ ok: false, reason: `exited right after becoming ready (code=${exited.code})` })
          return
        }
        settle({ ok: true })
      },
      (err) => settle({ ok: false, reason: err.message })
    )
  })
}

// startServer boots the Go server and waits for it to become healthy.
//
// The port from freePort() is only a guess (see freePort), so every attempt uses a
// fresh port and a child that dies early or never answers /api/health is retried a
// bounded number of times — inside one overall deadline — before startup fails
// with a message that lists what was tried. It never loops forever.
// dataDirPath is the directory the Go server keeps its database in. The desktop
// shell owns it so the tray can reveal it and so every start uses the same path.
function dataDirPath() {
  return path.join(app.getPath('userData'), 'natpilot')
}

async function startServer() {
  const { path: binPath, probed } = serverBinary()
  const dataDir = dataDirPath()
  const deadline = Date.now() + SERVER_START_DEADLINE_MS
  const failures = []
  for (let attempt = 1; attempt <= SERVER_START_ATTEMPTS; attempt++) {
    if (quitting) throw new Error('应用正在退出，已取消启动 NatPilot 服务')
    const remaining = deadline - Date.now()
    if (remaining <= 1000) break
    let port
    try {
      port = await freePort()
    } catch (err) {
      throw new Error(`无法分配本地端口: ${err.message}`)
    }
    // A quit may have started while the port was being probed; never spawn a child
    // that the shell is no longer going to manage.
    if (quitting) throw new Error('应用正在退出，已取消启动 NatPilot 服务')
    const url = `http://127.0.0.1:${port}`
    const result = await launchAndWait(binPath, port, dataDir, url, Math.min(SERVER_READY_TIMEOUT_MS, remaining))
    if (result.ok) {
      serverURL = url
      updateTray()
      return url
    }
    failures.push(`${url}: ${result.reason}`)
    if (result.permanent) break
    console.warn(`[natpilot] start attempt ${attempt}/${SERVER_START_ATTEMPTS} failed (${result.reason}); retrying on another port`)
  }
  serverProcess = null
  serverURL = null
  updateTray()
  const details = failures.length ? failures.join('\n  ') : 'no attempt could be made'
  const where = path.isAbsolute(binPath)
    ? ''
    : `\n\n在以下位置都没有找到 ${path.basename(binPath)}:\n  ${probed.join('\n  ')}`
  throw new Error(`无法启动 NatPilot 服务:\n  ${details}${where}`)
}

// waitForExit resolves true when the child exits before timeoutMs, false otherwise.
function waitForExit(child, timeoutMs) {
  return new Promise((resolve) => {
    if (child.exitCode !== null || child.signalCode !== null) {
      resolve(true)
      return
    }
    const onExit = () => {
      if (timer) clearTimeout(timer)
      resolve(true)
    }
    const timer = setTimeout(() => {
      child.removeListener('exit', onExit)
      resolve(false)
    }, timeoutMs)
    child.once('exit', onExit)
  })
}

// taskkillTree terminates a Windows process tree. Without /F it asks the process to
// close (the closest thing to SIGTERM on Windows); with /F it is a hard kill. The
// caller still waits for the child's own 'exit' event, so a failed taskkill is
// detected rather than assumed.
function taskkillTree(child, force) {
  return new Promise((resolve) => {
    const args = ['/pid', String(child.pid), '/T']
    if (force) args.push('/F')
    let killer
    try {
      killer = spawn('taskkill', args, { stdio: 'ignore', windowsHide: true })
    } catch {
      resolve(false)
      return
    }
    killer.on('error', () => resolve(false))
    killer.on('exit', () => resolve(true))
  })
}

// terminateChild asks the server to shut down and escalates to a forced kill:
//
//   Windows: taskkill /pid <pid> /T (graceful, no /F) → taskkill /pid <pid> /T /F
//   POSIX:   SIGTERM → SIGKILL after the grace period
//
// It resolves once the child is gone (or the final timeout expired), so callers
// wait for the port to be released instead of racing the OS.
async function terminateChild(child, graceMs = SERVER_STOP_GRACE_MS) {
  const pid = child && child.pid
  if (!pid || child.exitCode !== null || child.signalCode !== null) return true
  try {
    if (process.platform === 'win32') {
      await taskkillTree(child, false)
    } else {
      child.kill('SIGTERM')
    }
  } catch {
    /* already gone */
  }
  // A graceful taskkill is usually refused for a console child, so keep that window
  // short on Windows; POSIX gets the full grace period for Go's own shutdown.
  const grace = process.platform === 'win32' ? Math.min(graceMs, 1000) : graceMs
  if (await waitForExit(child, grace)) return true
  console.warn(`[natpilot] server ${pid} ignored the graceful shutdown; forcing it to exit`)
  try {
    if (process.platform === 'win32') {
      await taskkillTree(child, true)
      // Safety net when taskkill itself is unavailable: child.kill() is
      // TerminateProcess on Windows, i.e. the same hard kill.
      if (child.exitCode === null && child.signalCode === null) child.kill()
    } else {
      child.kill('SIGKILL')
    }
  } catch {
    /* already gone */
  }
  const gone = await waitForExit(child, 5000)
  if (!gone) console.warn(`[natpilot] server ${pid} is still running; its port may stay occupied`)
  return gone
}

// stopServer stops the server child and resolves once it has really exited. The
// promise is shared so concurrent quit paths (tray menu, ⌘Q, before-quit) never
// stop the same child twice, and before-quit waits for it so the port is freed
// before Electron exits.
let serverStopPromise = null

function stopServer() {
  if (serverStopPromise) return serverStopPromise
  const child = serverProcess
  serverProcess = null
  serverURL = null
  updateTray()
  if (!child || !child.pid) return Promise.resolve()
  serverStopPromise = terminateChild(child, SERVER_STOP_GRACE_MS)
    .catch((err) => {
      console.warn(`[natpilot] failed to stop the server: ${err}`)
    })
    .finally(() => {
      serverStopPromise = null
    })
  return serverStopPromise
}

// ensureServer starts the server at most once at a time: a second window/show
// request during boot would otherwise spawn a second server and leak the first
// one (the tray owns the process, so it could no longer be stopped).
let serverStartPromise = null

function ensureServer() {
  if (serverStartPromise) return serverStartPromise
  serverStartPromise = startServer().finally(() => {
    serverStartPromise = null
  })
  return serverStartPromise
}

// ------------------------------------------------------- autostart + updates

// appVersion is the version the update check compares against. It is read from
// the packaged package.json instead of app.getVersion() so it stays correct even
// when the executable's version resource was not stamped (electron-builder's
// signAndEditExecutable=false, used for offline builds) — otherwise the check
// could compare against Electron's own version.
let cachedAppVersion = null

function appVersion() {
  if (cachedAppVersion) return cachedAppVersion
  try {
    const pkg = JSON.parse(fs.readFileSync(path.join(__dirname, '..', 'package.json'), 'utf8'))
    if (pkg && pkg.version) {
      cachedAppVersion = String(pkg.version)
      return cachedAppVersion
    }
  } catch {
    /* fall back to whatever Electron reports */
  }
  try {
    cachedAppVersion = app.getVersion()
  } catch {
    cachedAppVersion = '0.0.0'
  }
  return cachedAppVersion
}

// canAutoStart: registering a login item only makes sense for an installed build.
// Under `npm start` the "executable" would be electron.exe, so the entry is shown
// but disabled instead of registering something that cannot work.
function canAutoStart() {
  return app.isPackaged
}

function autoStartEnabled() {
  if (!canAutoStart()) return false
  return autostart.autoStartEnabled(app, { execPath: autostartExecPath() })
}

// autostartExecPath: an AppImage re-mounts to a random directory on every run, so
// the login entry has to point at the AppImage file itself, not at execPath.
function autostartExecPath() {
  return process.env.APPIMAGE || process.execPath
}

// toggleAutoStart is the tray checkbox handler.
function toggleAutoStart(enabled) {
  try {
    autostart.applyAutoStart(app, enabled, autostartExecPath())
  } catch (err) {
    dialog.showErrorBox('无法修改开机自启', String((err && err.message) || err))
  }
  // The menu is rebuilt from scratch: do it after the click has been handled so
  // the tick state comes from the OS instead of the item that was just clicked.
  setImmediate(() => updateTray())
}

// fetchLatestRelease asks GitHub for the newest published release.
function fetchLatestRelease(repo, timeoutMs = UPDATE_TIMEOUT_MS) {
  return new Promise((resolve, reject) => {
    const url = `https://api.github.com/repos/${repo}/releases/latest`
    const req = https.get(
      url,
      {
        headers: {
          'User-Agent': `NatPilot-Desktop/${appVersion()}`,
          Accept: 'application/vnd.github+json',
        },
        timeout: timeoutMs,
      },
      (res) => {
        let body = ''
        res.setEncoding('utf8')
        res.on('data', (chunk) => {
          body += chunk
          if (body.length > 512 * 1024) {
            req.destroy(new Error('版本信息过大'))
          }
        })
        res.on('end', () => {
          // 404 means the repository has no published release yet (or is private):
          // that is not a failure, it just means there is nothing to update to.
          if (res.statusCode === 404) {
            resolve(null)
            return
          }
          if (res.statusCode !== 200) {
            reject(new Error(`HTTP ${res.statusCode}`))
            return
          }
          try {
            resolve(JSON.parse(body))
          } catch {
            reject(new Error('无法解析版本信息'))
          }
        })
      }
    )
    req.on('timeout', () => req.destroy(new Error('请求超时')))
    req.on('error', reject)
  })
}

let updateChecking = false

// checkForUpdates reports the result. interactive=false (the check after login)
// stays silent unless there really is a newer release.
async function checkForUpdates(interactive = false) {
  if (updateChecking) return
  updateChecking = true
  updateTray()
  const current = appVersion()
  try {
    const release = await fetchLatestRelease(UPDATE_REPO)
    if (!release) {
      if (interactive) {
        await dialog.showMessageBox({
          type: 'info',
          title: '检查更新',
          message: `已是最新版本 v${current}`,
          detail: '该项目还没有发布可用的更新版本。',
          noLink: true,
        })
      }
      return
    }
    const latest = String(release.tag_name || release.name || '')
    if (isNewer(latest, current)) {
      const { response } = await dialog.showMessageBox({
        type: 'info',
        title: '发现新版本',
        message: `发现新版本 ${latest}（当前 v${current}）`,
        detail: release.name ? String(release.name) : '可以打开发布页获取新版本。',
        buttons: ['打开发布页', '稍后'],
        defaultId: 0,
        cancelId: 1,
        noLink: true,
      })
      if (response === 0 && release.html_url) shell.openExternal(String(release.html_url))
    } else if (interactive) {
      await dialog.showMessageBox({
        type: 'info',
        title: '检查更新',
        message: `已是最新版本 v${current}`,
        noLink: true,
      })
    }
  } catch (err) {
    if (interactive) {
      await dialog.showMessageBox({
        type: 'warning',
        title: '检查更新失败',
        message: '无法获取版本信息',
        detail: `${(err && err.message) || err}\n\n请检查网络连接后重试。`,
        noLink: true,
      })
    } else {
      console.warn(`[natpilot] update check skipped: ${(err && err.message) || err}`)
    }
  } finally {
    updateChecking = false
    updateTray()
  }
}

// ---------------------------------------------------------------- window

async function createWindow() {
  if (mainWindow) {
    showWindow()
    return
  }
  let url = 'about:blank'
  try {
    url = serverURL || (await ensureServer())
  } catch (err) {
    if (!quitting) {
      dialog.showErrorBox('无法启动 NatPilot 服务', `${err}\n\n请确认 natpilot 可执行文件与桌面壳位于同一目录。`)
    }
  }

  const saved = loadWindowState()
  mainWindow = new BrowserWindow({
    width: saved?.width || 1440,
    height: saved?.height || 920,
    x: saved?.x,
    y: saved?.y,
    minWidth: 1024,
    minHeight: 680,
    show: false,
    backgroundColor: '#0b0f17',
    title: 'NatPilot',
    autoHideMenuBar: true,
    // Keep the shell alive in the tray when the window is closed.
    webPreferences: {
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
    },
  })
  mainWindow.loadURL(url)
  mainWindow.once('ready-to-show', () => mainWindow.show())

  // External links open in the system browser.
  mainWindow.webContents.setWindowOpenHandler(({ url: target }) => {
    shell.openExternal(target)
    return { action: 'deny' }
  })

  // The window only ever shows the local console; anything else is handed to the
  // system browser instead of navigating the app shell.
  mainWindow.webContents.on('will-navigate', (event, target) => {
    const local = serverURL && target.startsWith(serverURL)
    if (local) return
    event.preventDefault()
    if (/^https?:/i.test(target)) shell.openExternal(target)
  })

  // Closing the window hides it; the app keeps running in the tray.
  mainWindow.on('close', (event) => {
    saveWindowState()
    if (quitting) return
    event.preventDefault()
    mainWindow.hide()
    if (process.platform === 'darwin' && app.dock) app.dock.hide()
  })
  mainWindow.on('closed', () => {
    mainWindow = null
  })
}

function showWindow() {
  if (process.platform === 'darwin' && app.dock) app.dock.show()
  if (!mainWindow) {
    createWindow()
    return
  }
  if (mainWindow.isMinimized()) mainWindow.restore()
  mainWindow.show()
  mainWindow.focus()
}

// ---------------------------------------------------------------- tray

function trayImage() {
  const assets = path.join(__dirname, 'assets')
  if (process.platform === 'darwin') {
    const img = nativeImage.createFromPath(path.join(assets, 'trayTemplate.png'))
    if (!img.isEmpty()) {
      img.setTemplateImage(true)
      return img
    }
  }
  const img = nativeImage.createFromPath(path.join(assets, 'tray.png'))
  return img.isEmpty() ? null : img
}

function updateTray() {
  if (!tray) return
  const running = Boolean(serverProcess)
  tray.setToolTip(`${TRAY_TOOLTIP}${running ? '' : '（服务未运行）'}`)
  tray.setContextMenu(Menu.buildFromTemplate(trayTemplate()))
}

// openSettings brings the console up on the 系统设置 page, which is where the
// runtime configuration lives (settings that need a new process, such as the
// listening port or TLS, take effect after 托盘 → 重启服务).
async function openSettings() {
  try {
    const url = serverURL || (await ensureServer())
    showWindow()
    if (mainWindow && !mainWindow.isDestroyed()) {
      await mainWindow.loadURL(`${url}/#/settings`)
    }
  } catch (err) {
    if (!quitting) {
      dialog.showErrorBox('无法打开系统设置', String((err && err.message) || err))
    }
  }
}

// restartService stops the child and boots a fresh one, then reloads the window
// onto the new URL (the port may change). This is what makes settings edits that
// need a restart usable from inside the desktop shell.
let restarting = false

async function restartService() {
  if (restarting) return
  restarting = true
  updateTray()
  try {
    await stopServer()
    const url = await ensureServer()
    if (mainWindow && !mainWindow.isDestroyed()) {
      await mainWindow.loadURL(url)
    }
    showWindow()
  } catch (err) {
    if (!quitting) {
      dialog.showErrorBox('重启 NatPilot 服务失败', String((err && err.message) || err))
    }
  } finally {
    restarting = false
    updateTray()
  }
}

function openDataDir() {
  shell.openPath(dataDirPath()).then((msg) => {
    if (msg) dialog.showErrorBox('无法打开数据目录', `${dataDirPath()}\n\n${msg}`)
  })
}

function createTray() {
  const image = trayImage()
  if (!image) {
    console.warn('[natpilot] tray icon not found; the window will quit on close')
    return false
  }
  tray = new Tray(image)
  tray.setToolTip(TRAY_TOOLTIP)
  tray.setContextMenu(Menu.buildFromTemplate(trayTemplate()))
  // double click re-opens the window on windows/linux; macOS opens the menu.
  tray.on('double-click', () => showWindow())
  return true
}

// trayTemplate builds the tray menu. It is rebuilt whenever the service state
// changes (see updateTray) so the entries reflect reality: the console entry is
// enabled only once there is a server to show, "重启服务" is disabled while a
// start or restart is already running, and the status line carries the URL.
function trayTemplate() {
  const running = Boolean(serverProcess) && Boolean(serverURL)
  const autoStartLabel = canAutoStart() ? '开机自启' : '开机自启（仅安装版可用）'
  return [
    {
      label: running ? `NatPilot 运行中 — ${serverURL}` : 'NatPilot 服务未运行',
      enabled: false,
    },
    { type: 'separator' },
    { label: '打开控制台', click: () => showWindow() },
    { label: '系统设置…', click: () => openSettings() },
    { label: '在浏览器中打开', enabled: running, click: () => serverURL && shell.openExternal(serverURL) },
    { type: 'separator' },
    {
      label: autoStartLabel,
      type: 'checkbox',
      checked: autoStartEnabled(),
      enabled: canAutoStart(),
      click: (item) => toggleAutoStart(item.checked),
    },
    {
      label: updateChecking ? '正在检查更新…' : '检查更新…',
      enabled: !updateChecking,
      click: () => checkForUpdates(true),
    },
    { type: 'separator' },
    { label: '重启服务', enabled: !restarting, click: () => restartService() },
    { label: '打开数据目录', click: () => openDataDir() },
    { type: 'separator' },
    { label: `NatPilot v${appVersion()}`, enabled: false },
    { label: '退出 NatPilot', click: () => quitApp() },
  ]
}

// ---------------------------------------------------------------- lifecycle

// quitApp starts a real quit. The server is stopped in before-quit, which defers
// the quit until the child has actually exited.
function quitApp() {
  quitting = true
  app.quit()
}

// killServerOnExit is the last line of defence against an orphaned server: when
// the main process dies from a signal or an unexpected exit (crash, "结束任务"
// from the task manager, a failed quit path), the Go child would otherwise keep
// the loopback port bound and stay visible in the process list. spawnSync is
// used on purpose — the 'exit' hook cannot await an async kill.
function killServerOnExit() {
  const child = serverProcess
  if (!child || !child.pid) return
  serverProcess = null
  try {
    if (process.platform === 'win32') {
      spawnSync('taskkill', ['/pid', String(child.pid), '/T', '/F'], {
        stdio: 'ignore',
        windowsHide: true,
      })
    } else {
      child.kill('SIGKILL')
    }
  } catch {
    /* the process is already gone */
  }
}

process.on('exit', killServerOnExit)
process.on('SIGINT', () => quitApp())
process.on('SIGTERM', () => quitApp())

const gotLock = app.requestSingleInstanceLock()
if (!gotLock) {
  // Another instance already owns the tray; just bring it forward and exit.
  app.quit()
} else {
  app.on('second-instance', () => showWindow())

  app.whenReady().then(async () => {
    app.setName('NatPilot')
    // Windows groups taskbar entries and notifications by AppUserModelID.
    if (process.platform === 'win32') app.setAppUserModelId('com.natpilot.desktop')
    createTray()
    if (STARTED_HIDDEN) {
      // Launched by the OS at login: stay in the tray, but boot the server anyway
      // so the console is ready the moment the user opens it.
      try {
        await ensureServer()
      } catch (err) {
        console.warn(`[natpilot] autostart could not start the server: ${(err && err.message) || err}`)
      }
      updateTray()
    } else {
      await createWindow()
    }
    // One silent update check shortly after boot; the tray entry can be used for
    // an on-demand check with visible feedback.
    const timer = setTimeout(() => checkForUpdates(false), 10000)
    if (timer && typeof timer.unref === 'function') timer.unref()
  })

  // Stay resident: closing every window must not quit the app.
  app.on('window-all-closed', () => {
    /* intentionally empty — the tray owns the lifecycle */
  })

  app.on('activate', () => showWindow())

  // Reached for the tray "退出" item, ⌘Q, and system shutdown. The quit is held
  // back until the server (or the start attempt) has finished, so no orphan child
  // keeps the port occupied after the shell is gone.
  app.on('before-quit', (event) => {
    quitting = true
    if (tray) {
      tray.destroy()
      tray = null
    }
    if (serverProcess || serverStopPromise || serverStartPromise) {
      event.preventDefault()
      stopServer()
      Promise.resolve(serverStartPromise)
        .catch(() => {})
        .then(() => stopServer())
        .finally(() => app.quit())
    }
  })
}
