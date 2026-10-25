'use strict'

const test = require('node:test')
const assert = require('node:assert/strict')

const { parseVersion, compareVersions, isNewer } = require('../electron/version')

test('parseVersion accepts the tag shapes GitHub actually returns', () => {
  assert.deepEqual(parseVersion('v0.1.0'), [0, 1, 0])
  assert.deepEqual(parseVersion('1.2.3'), [1, 2, 3])
  assert.deepEqual(parseVersion('  V10.20.30  '), [10, 20, 30])
  assert.deepEqual(parseVersion('1.2.3-beta.1'), [1, 2, 3])
  assert.deepEqual(parseVersion('1.2.3+build.7'), [1, 2, 3])
})

test('parseVersion rejects everything that is not a version', () => {
  for (const raw of ['', '   ', 'v', 'latest', '1.2', 'x.y.z', null, undefined, 42, {}]) {
    assert.equal(parseVersion(raw), null, `expected null for ${JSON.stringify(raw)}`)
  }
})

test('compareVersions orders numerically, not lexicographically', () => {
  // The classic bug: "0.10.0" < "0.9.0" as strings.
  assert.equal(compareVersions('0.10.0', '0.9.0'), 1)
  assert.equal(compareVersions('0.9.0', '0.10.0'), -1)
  assert.equal(compareVersions('v1.0.0', '1.0.0'), 0)
  assert.equal(compareVersions('1.0.1', '1.0.0'), 1)
  assert.equal(compareVersions('2.0.0', '1.9.9'), 1)
})

test('compareVersions returns null when either side is unparseable', () => {
  assert.equal(compareVersions('latest', '1.0.0'), null)
  assert.equal(compareVersions('1.0.0', 'nope'), null)
})

test('isNewer only fires for a strictly newer, parseable release', () => {
  assert.equal(isNewer('v0.2.0', '0.1.0'), true)
  assert.equal(isNewer('v0.1.0', '0.1.0'), false)
  assert.equal(isNewer('v0.0.9', '0.1.0'), false)
  // A tag we cannot read must never pop up an "update available" dialog.
  assert.equal(isNewer('nightly', '0.1.0'), false)
  assert.equal(isNewer('', '0.1.0'), false)
})
