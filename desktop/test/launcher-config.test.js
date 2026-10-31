'use strict'
// sanitize 是启动器配置的唯一入口：一份坏配置绝不能让启动器起不来。
const test = require('node:test')
const assert = require('node:assert')
const { DEFAULTS, sanitize } = require('../electron/launcher-config')

test('empty or non-object input yields defaults', () => {
  assert.deepStrictEqual(sanitize(null), DEFAULTS)
  assert.deepStrictEqual(sanitize('nope'), DEFAULTS)
  assert.deepStrictEqual(sanitize(42), DEFAULTS)
})

test('unknown keys are dropped and known keys kept', () => {
  const out = sanitize({ autoStartService: false, evil: 'x', host: '0.0.0.0' })
  assert.strictEqual(out.autoStartService, false)
  assert.strictEqual(out.host, '0.0.0.0')
  assert.strictEqual(out.evil, undefined)
})

test('wrong types fall back to defaults instead of crashing', () => {
  const out = sanitize({ autoStartService: 'yes', port: 'abc', extraArgs: 'x', host: 5 })
  assert.strictEqual(out.autoStartService, DEFAULTS.autoStartService)
  assert.strictEqual(out.port, DEFAULTS.port)
  assert.deepStrictEqual(out.extraArgs, DEFAULTS.extraArgs)
  assert.strictEqual(out.host, DEFAULTS.host)
})

test('port is clamped to a valid range', () => {
  assert.strictEqual(sanitize({ port: 8080 }).port, 8080)
  assert.strictEqual(sanitize({ port: -1 }).port, DEFAULTS.port)
  assert.strictEqual(sanitize({ port: 70000 }).port, DEFAULTS.port)
  assert.strictEqual(sanitize({ port: 12.5 }).port, DEFAULTS.port)
})

test('extraArgs drops empties and keeps order', () => {
  const out = sanitize({ extraArgs: ['--log-level', ' ', 'info', 7] })
  assert.deepStrictEqual(out.extraArgs, ['--log-level', 'info'])
})

test('paths are trimmed so a stray space cannot break spawn', () => {
  const out = sanitize({ serverBinary: '  C:\\x\\natpilot.exe  ', dataDir: '  D:\\data ' })
  assert.strictEqual(out.serverBinary, 'C:\\x\\natpilot.exe')
  assert.strictEqual(out.dataDir, 'D:\\data')
})