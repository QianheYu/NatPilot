'use strict'

const test = require('node:test')
const assert = require('node:assert/strict')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')

const {
  AUTOSTART_ARG,
  applyAutoStart,
  autoStartEnabled,
  linuxAutostartPath,
  linuxDesktopEntry,
  linuxEnabled,
  quoteExec,
  setLinuxAutoStart,
} = require('../electron/autostart')

// tempConfig creates an isolated XDG_CONFIG_HOME so the tests never touch the
// real ~/.config of whoever runs them.
function tempConfig() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'natpilot-autostart-'))
  return { dir, options: { env: { XDG_CONFIG_HOME: dir }, home: dir } }
}

test('linuxAutostartPath honours XDG_CONFIG_HOME', () => {
  const { dir, options } = tempConfig()
  assert.equal(linuxAutostartPath(options), path.join(dir, 'autostart', 'natpilot.desktop'))
})

test('linuxAutostartPath falls back to ~/.config', () => {
  const home = path.join(os.tmpdir(), 'np-home')
  assert.equal(
    linuxAutostartPath({ env: {}, home }),
    path.join(home, '.config', 'autostart', 'natpilot.desktop')
  )
})

test('linuxAutostartPath ignores a blank XDG_CONFIG_HOME', () => {
  const home = path.join(os.tmpdir(), 'np-home2')
  assert.equal(
    linuxAutostartPath({ env: { XDG_CONFIG_HOME: '   ' }, home }),
    path.join(home, '.config', 'autostart', 'natpilot.desktop')
  )
})

test('quoteExec only quotes paths that need it', () => {
  assert.equal(quoteExec('/opt/natpilot/NatPilot'), '/opt/natpilot/NatPilot')
  assert.equal(quoteExec('/opt/My Apps/NatPilot'), '"/opt/My Apps/NatPilot"')
  assert.equal(quoteExec('/opt/a"b/NatPilot'), '"/opt/a\\"b/NatPilot"')
})

test('linuxDesktopEntry carries the command and the tray flag', () => {
  const entry = linuxDesktopEntry('/opt/natpilot/NatPilot')
  assert.match(entry, /^\[Desktop Entry\]\n/)
  assert.match(entry, /^Type=Application$/m)
  assert.match(entry, /^Name=NatPilot$/m)
  assert.match(entry, /^Terminal=false$/m)
  assert.ok(
    entry.includes(`Exec=/opt/natpilot/NatPilot ${AUTOSTART_ARG}`),
    `Exec line missing: ${entry}`
  )
  assert.ok(entry.endsWith('\n'), 'a .desktop file must end with a newline')
})

test('setLinuxAutoStart writes, reports and removes the entry', () => {
  const { options } = tempConfig()
  const file = linuxAutostartPath(options)

  assert.equal(linuxEnabled(options), false, 'nothing installed yet')

  const written = setLinuxAutoStart(true, '/opt/natpilot/NatPilot', options)
  assert.equal(written.enabled, true)
  assert.equal(written.file, file)
  assert.equal(linuxEnabled(options), true, 'file must exist after enabling')
  assert.match(fs.readFileSync(file, 'utf8'), /Exec=.*--autostart/)

  const removed = setLinuxAutoStart(false, '/opt/natpilot/NatPilot', options)
  assert.equal(removed.enabled, false)
  assert.equal(fs.existsSync(file), false, 'file must be gone after disabling')
  assert.equal(linuxEnabled(options), false)
})

test('setLinuxAutoStart tolerates a second disable', () => {
  const { options } = tempConfig()
  assert.doesNotThrow(() => setLinuxAutoStart(false, '/opt/np', options))
  assert.doesNotThrow(() => setLinuxAutoStart(false, '/opt/np', options))
})

test('setLinuxAutoStart creates missing parent directories', () => {
  const { dir, options } = tempConfig()
  const nested = path.join(dir, 'deep', 'nested')
  const nestedOptions = { env: { XDG_CONFIG_HOME: nested }, home: dir }
  setLinuxAutoStart(true, '/opt/np', nestedOptions)
  assert.equal(linuxEnabled(nestedOptions), true)
})

// A fake Electron app: records what was registered and answers read-backs the way
// the real API does on Windows (openAtLogin stays false for a custom path).
function fakeElectron(initial = {}) {
  const calls = []
  const state = { openAtLogin: false, executableWillLaunchAtLogin: false, ...initial }
  return {
    calls,
    state,
    setLoginItemSettings(settings) {
      calls.push(settings)
      state.openAtLogin = Boolean(settings.openAtLogin)
      state.executableWillLaunchAtLogin = Boolean(settings.openAtLogin)
    },
    getLoginItemSettings(query) {
      state.query = query
      return { ...state }
    },
  }
}

test('applyAutoStart registers the executable with the tray flag', () => {
  const fake = fakeElectron()
  applyAutoStart(fake, true, '/opt/natpilot/NatPilot.exe', { platform: 'win32' })

  assert.equal(fake.calls.length, 1)
  assert.deepEqual(fake.calls[0], {
    openAtLogin: true,
    openAsHidden: false,
    path: '/opt/natpilot/NatPilot.exe',
    args: ['--autostart'],
  })
})

test('applyAutoStart clears the args when disabling', () => {
  const fake = fakeElectron()
  applyAutoStart(fake, false, '/opt/natpilot/NatPilot.exe', { platform: 'darwin' })
  assert.equal(fake.calls[0].openAtLogin, false)
  assert.deepEqual(fake.calls[0].args, [])
})

test('autoStartEnabled trusts executableWillLaunchAtLogin over openAtLogin', () => {
  // This is the Windows case that used to read back as "disabled" forever.
  const fake = fakeElectron({ openAtLogin: false, executableWillLaunchAtLogin: true })
  assert.equal(autoStartEnabled(fake, { execPath: '/opt/NatPilot.exe' }), true)
})

test('autoStartEnabled passes the executable path to the query', () => {
  const fake = fakeElectron({ openAtLogin: true })
  autoStartEnabled(fake, { execPath: '/opt/NatPilot.exe' })
  assert.equal(fake.state.query.path, '/opt/NatPilot.exe')
})

test('autoStartEnabled is false when the app throws', () => {
  const broken = {
    getLoginItemSettings() {
      throw new Error('nope')
    },
  }
  assert.equal(autoStartEnabled(broken), false)
})

test('applyAutoStart on linux never touches the Electron API', () => {
  const { options } = tempConfig()
  const fake = fakeElectron()
  const result = applyAutoStart(fake, true, '/opt/np', { ...options, platform: 'linux' })
  assert.equal(fake.calls.length, 0, 'the login item API is Windows/macOS only')
  assert.equal(result.platform, 'linux')
  assert.equal(result.enabled, true)
})
