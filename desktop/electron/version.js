'use strict'

// Version comparison for the update check.
//
// Kept free of Electron so it can be unit tested with plain `node --test`:
// "is this release newer than what I am running" is exactly the kind of logic
// that breaks silently, and a wrong answer either nags the user forever or never
// tells them about a security release.

// parseVersion turns "v1.2.3", "1.2.3", "1.2.3-beta.1" or "1.2.3+build" into
// [major, minor, patch], and returns null when the string is not a version.
function parseVersion(raw) {
  if (typeof raw !== 'string') return null
  const match = raw.trim().match(/^v?(\d+)\.(\d+)\.(\d+)/i)
  if (!match) return null
  const parts = [Number(match[1]), Number(match[2]), Number(match[3])]
  if (parts.some((n) => !Number.isFinite(n) || n < 0)) return null
  return parts
}

// compareVersions returns 1 when a > b, -1 when a < b, 0 when equal and null when
// either side is not a parseable version.
function compareVersions(a, b) {
  const left = parseVersion(a)
  const right = parseVersion(b)
  if (!left || !right) return null
  for (let i = 0; i < 3; i++) {
    if (left[i] > right[i]) return 1
    if (left[i] < right[i]) return -1
  }
  return 0
}

// isNewer reports whether `remote` is a strictly newer version than `local`.
// Unparseable input is never "newer": a malformed tag must not pop up a dialog.
function isNewer(remote, local) {
  return compareVersions(remote, local) === 1
}

module.exports = { parseVersion, compareVersions, isNewer }
