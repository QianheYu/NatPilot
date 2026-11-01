// IPv4 / IPv6 address helpers shared by the views and the host-grouping tree.
//
// Everything here is pure: no DOM, no Vue, no globals. That keeps it easy to
// exercise from Node (the package is ESM):
//
//   node --input-type=module -e "import('./src/utils/netaddr.js').then(m =>
//     console.assert(m.fmtHostPort('::1', 8080) === '[::1]:8080'))"
//
// IPv6 input encountered by these helpers may be a bare literal (`::1`), a
// bracketed literal (`[::1]`) or carry a zone suffix (`fe80::1%eth0`,
// `[fe80::1%eth0]`). The backend is expected to hand us zone-free strings with
// the zone in a separate field, but tolerating both here keeps rendering safe
// if that ever slips through.
//
// Display rules:
//   - only an IPv6 literal gets brackets; hostnames and IPv4 stay untouched;
//   - an empty host renders as an em dash, an empty port omits `:port`.

const IPV4_RE = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/
const HEX_GROUP_RE = /^[0-9a-fA-F]{1,4}$/

// normalizeAddr strips whitespace, one or two layers of [] and a %zone suffix.
// Brackets may sit either side of the zone (`[fe80::1%eth0]` / `[fe80::1]%eth0`).
function normalizeAddr(value) {
  let s = String(value == null ? '' : value).trim()
  if (s.length > 1 && s[0] === '[' && s[s.length - 1] === ']') s = s.slice(1, -1).trim()
  const pct = s.indexOf('%')
  if (pct >= 0) s = s.slice(0, pct)
  if (s.length > 1 && s[0] === '[' && s[s.length - 1] === ']') s = s.slice(1, -1).trim()
  return s
}

function isIPv4Literal(s) {
  const m = IPV4_RE.exec(s)
  if (!m) return false
  for (let i = 1; i <= 4; i++) {
    if (Number(m[i]) > 255) return false
  }
  return true
}

// parseIPv6 returns the eight 16-bit groups of an IPv6 literal, or null when
// `s` is not one. It is deliberately strict: a colon-only string with six
// groups (a MAC address) is rejected, because only `::` compression may yield
// fewer than eight groups.
function parseIPv6(s) {
  if (!s || s.indexOf(':') < 0) return null
  const pieces = s.split('::')
  if (pieces.length > 2) return null
  const compressed = pieces.length === 2

  const headText = pieces[0]
  const tailText = compressed ? pieces[1] : ''
  const tokens = []
  if (headText) tokens.push(...headText.split(':'))
  if (compressed) tokens.push('') // marks where the zero run belongs
  if (tailText) tokens.push(...tailText.split(':'))

  const groups = []
  let gap = -1
  for (let i = 0; i < tokens.length; i++) {
    const t = tokens[i]
    if (t === '') {
      if (!compressed || gap >= 0) return null
      gap = groups.length
      continue
    }
    if (t.indexOf('.') >= 0) {
      // An embedded IPv4 tail is only allowed as the very last token and
      // contributes two 16-bit groups.
      if (i !== tokens.length - 1 || !isIPv4Literal(t)) return null
      const [a, b, c, d] = t.split('.').map(Number)
      groups.push((a << 8) | b, (c << 8) | d)
      continue
    }
    if (!HEX_GROUP_RE.test(t)) return null
    groups.push(parseInt(t, 16))
  }

  if (gap >= 0) {
    if (groups.length >= 8) return null
    groups.splice(gap, 0, ...new Array(8 - groups.length).fill(0))
  } else if (groups.length !== 8) {
    return null
  }
  return groups
}

// compressGroups renders eight groups in canonical lowercase compressed form.
function compressGroups(groups) {
  const hex = groups.map((g) => g.toString(16))
  let bestStart = -1
  let bestLen = 0
  for (let i = 0; i < hex.length; ) {
    if (hex[i] !== '0') {
      i++
      continue
    }
    let j = i
    while (j < hex.length && hex[j] === '0') j++
    if (j - i > bestLen) {
      bestStart = i
      bestLen = j - i
    }
    i = j
  }
  if (bestLen < 2) return hex.join(':')
  return `${hex.slice(0, bestStart).join(':')}::${hex.slice(bestStart + bestLen).join(':')}`
}

// isIPv6 reports whether `s` is an IPv6 literal, tolerating surrounding
// brackets and a trailing %zone.
export function isIPv6(s) {
  return parseIPv6(normalizeAddr(s)) !== null
}

// addrFamily classifies one bare address.
export function addrFamily(s) {
  const addr = normalizeAddr(s)
  if (!addr) return 'other'
  if (parseIPv6(addr)) return 'ipv6'
  if (isIPv4Literal(addr)) return 'ipv4'
  return 'other'
}

// fmtHost renders a bare address for display: IPv6 in brackets, everything
// else unchanged. An empty address becomes an em dash.
export function fmtHost(host) {
  const s = String(host == null ? '' : host).trim()
  if (!s) return '—'
  // Already bracketed: leave it alone, including a possible %zone.
  if (s.length > 1 && s[0] === '[' && s[s.length - 1] === ']') return s
  return isIPv6(s) ? `[${s}]` : s
}

// fmtHostPort renders host:port unambiguously. IPv6 gets `[addr]:port`, an
// empty port falls back to the plain host (or an em dash when there is none).
export function fmtHostPort(host, port) {
  const h = String(host == null ? '' : host).trim()
  const p = String(port == null ? '' : port).trim()
  if (!p) return fmtHost(h)
  return `${h ? fmtHost(h) : ''}:${p}`
}

// groupIPv6 derives the tree node for one IPv6 address:
//   ::1        → 本机回环 (::1)       rank 0
//   fc00::/7   → /64 bucket           rank 1 (ULA)
//   anything   → /64 bucket           rank 1 (global)
//   fe80::/10  → fe80::/10            rank 2 (link-local; zone ignored)
// Keys are prefixed with `net6:` so they can never collide with the IPv4 tree
// keys (`loopback`, `net:<cidr>`, `unknown`, `other`).
export function groupIPv6(ip) {
  const addr = normalizeAddr(ip)
  const groups = parseIPv6(addr)
  if (!groups) return null

  // ::1 (any textual spelling) is the local loopback.
  if (groups[0] === 0 && groups[1] === 0 && groups[2] === 0 && groups[3] === 0 &&
      groups[4] === 0 && groups[5] === 0 && groups[6] === 0 && groups[7] === 1) {
    return { key: 'net6:loopback', label: '本机回环 (::1)', rank: 0 }
  }

  // fe80::/10 — link-local addresses have no routable prefix to group by, so
  // every one of them shares the single bucket the task asks for.
  if ((groups[0] & 0xffc0) === 0xfe80) {
    return { key: 'net6:fe80::/10', label: 'fe80::/10', rank: 2 }
  }

  const label = `${compressGroups([...groups.slice(0, 4), 0, 0, 0, 0])}/64`
  return { key: `net6:${label}`, label, rank: 1 }
}
