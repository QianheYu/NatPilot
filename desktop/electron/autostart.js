'use strict'

// 开机自启 (start at login) support.
//
// Windows and macOS are handled by Electron: setLoginItemSettings writes the
// HKCU Run entry / login item. Linux has no Electron API for this, so the same
// effect is produced the standard way: an XDG autostart .desktop file.
//
// The platform logic is injectable (fs / env / home) so the tests can drive both
// branches on any host without touching the real registry or ~/.config.

const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')

// AUTOSTART_ARG is appended to the command that the OS runs at login. The shell
// starts in the tray with this flag instead of popping a window on every boot.
const AUTOSTART_ARG = '--autostart'

// LINUX_DESKTOP_FILE is the file name inside $XDG_CONFIG_HOME/autostart.
const LINUX_DESKTOP_FILE = 'natpilot.desktop'

// linuxAutostartPath resolves <config>/autostart/natpilot.desktop.
function linuxAutostartPath(options = {}) {
  const env = options.env || process.env
  const home = options.home || os.homedir()
  const configured = String(env.XDG_CONFIG_HOME || '').trim()
  const base = configured || path.join(home, '.config')
  return path.join(base, 'autostart', LINUX_DESKTOP_FILE)
}

// quoteExec quotes an Exec= value per the Desktop Entry spec: paths containing
// spaces (very common on Linux, e.g. "/opt/My Apps/...") must be quoted.
function quoteExec(execPath) {
  if (!/[\s"']/.test(execPath)) return execPath
  return '"' + String(execPath).replace(/\\/g, '\\\\').replace(/"/g, '\\"') + '"'
}

// linuxDesktopEntry renders the autostart entry. Only the fields that every
// desktop environment reads are included; the X-GNOME key is understood by
// GNOME and ignored elsewhere.
function linuxDesktopEntry(execPath, arg = AUTOSTART_ARG) {
  const command = [quoteExec(execPath), arg].filter(Boolean).join(' ')
  return (
    [
      '[Desktop Entry]',
      'Type=Application',
      'Version=1.0',
      'Name=NatPilot',
      'Comment=NAT 端口映射控制台',
      'Comment[en]=NAT port mapping console',
      'Exec=' + command,
      'Terminal=false',
      'X-GNOME-Autostart-enabled=true',
      '',
    ].join('\n')
  )
}

function linuxEnabled(options = {}) {
  const fsImpl = options.fs || fs
  try {
    return fsImpl.statSync(linuxAutostartPath(options)).isFile()
  } catch {
    return false
  }
}

// setLinuxAutoStart writes or removes the autostart entry and returns what it did.
function setLinuxAutoStart(enabled, execPath, options = {}) {
  const fsImpl = options.fs || fs
  const file = linuxAutostartPath(options)
  if (!enabled) {
    try {
      fsImpl.unlinkSync(file)
    } catch (err) {
      if (err.code !== 'ENOENT') throw err
    }
    return { platform: 'linux', file, enabled: false }
  }
  fsImpl.mkdirSync(path.dirname(file), { recursive: true })
  fsImpl.writeFileSync(file, linuxDesktopEntry(execPath), { mode: 0o644 })
  return { platform: 'linux', file, enabled: true }
}

// setLoginItem enables the entry through Electron (Windows/macOS).
// `path` + `args` are passed explicitly so the registered command points at the
// packaged executable and not at whatever launched this process.
function setLoginItem(electronApp, enabled, execPath, arg = AUTOSTART_ARG) {
  electronApp.setLoginItemSettings({
    openAtLogin: Boolean(enabled),
    openAsHidden: false,
    path: execPath,
    args: enabled && arg ? [arg] : [],
  })
  return { platform: process.platform, file: execPath, enabled: Boolean(enabled) }
}

// autoStartEnabled reports the current state for the menu checkbox.
//
// On Windows `openAtLogin` is only true when the registry value matches the
// default command, so a custom `path` (which is what a packaged app registers)
// reads back as false. `executableWillLaunchAtLogin` answers the actual question
// — "will this executable start at login" — so both are consulted.
function autoStartEnabled(electronApp, options = {}) {
  const platform = options.platform || process.platform
  if (platform === 'linux') return linuxEnabled(options)
  try {
    const query = options.execPath ? { path: options.execPath } : undefined
    const state = electronApp.getLoginItemSettings(query) || {}
    return Boolean(state.openAtLogin || state.executableWillLaunchAtLogin)
  } catch {
    return false
  }
}

// applyAutoStart is the single entry point used by the tray menu.
function applyAutoStart(electronApp, enabled, execPath, options = {}) {
  const platform = options.platform || process.platform
  if (platform === 'linux') return setLinuxAutoStart(enabled, execPath, options)
  return setLoginItem(electronApp, enabled, execPath, options.arg || AUTOSTART_ARG)
}

module.exports = {
  AUTOSTART_ARG,
  applyAutoStart,
  autoStartEnabled,
  linuxAutostartPath,
  linuxDesktopEntry,
  linuxEnabled,
  quoteExec,
  setLinuxAutoStart,
}
