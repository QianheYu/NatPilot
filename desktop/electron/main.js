// Electron main process for the NatPilot desktop edition.
//
// Behaviour (identical on Windows, macOS and Linux):
//
//   * boots the Go server on a free loopback port in desktop mode (no login),
//   * shows the console in a native window,
//   * lives in the system tray: closing the window only hides it,
//   * the only way to really stop it is "退出 NatPilot" in the tray menu
//     (⌘Q on macOS is honoured as an explicit quit request),
//   * killing the app always stops the server child process.
//
// Run:  npm install && npm start      (development)
//       npm run dist                  (package with electron-builder)

const { app, BrowserWindow, Tray, Menu, nativeImage, shell, dialog } = require('electron')
const { spawn } = require('node:child_process')
const http = require('node:http')
const net = require('node:net')
const path = require('node:path')
const fs = require('node:fs')

let serverProcess = null
let mainWindow = null
let tray = null
let serverURL = null
let quitting = false

const TRAY_TOOLTIP = 'NatPilot — NAT 端口映射控制台'

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
    env: { ...process.env, NATPILOT_DESKTOP: '1' },
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
async function startServer() {
  const { path: binPath, probed } = serverBinary()
  const dataDir = path.join(app.getPath('userData'), 'natpilot')
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
}

function createTray() {
  const image = trayImage()
  if (!image) {
    console.warn('[natpilot] tray icon not found; the window will quit on close')
    return false
  }
  tray = new Tray(image)
  tray.setToolTip(TRAY_TOOLTIP)
  tray.setContextMenu(
    Menu.buildFromTemplate([
      { label: '打开控制台', click: () => showWindow() },
      { label: '在浏览器中打开', click: () => serverURL && shell.openExternal(serverURL) },
      { type: 'separator' },
      { label: '退出 NatPilot', click: () => quitApp() },
    ])
  )
  // double click re-opens the window on windows/linux; macOS opens the menu.
  tray.on('double-click', () => showWindow())
  return true
}

// ---------------------------------------------------------------- lifecycle

// quitApp starts a real quit. The server is stopped in before-quit, which defers
// the quit until the child has actually exited.
function quitApp() {
  quitting = true
  app.quit()
}

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
    await createWindow()
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
