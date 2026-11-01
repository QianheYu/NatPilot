// 「端口发现」/「映射规则」的树形分组：网段 → 主机 → 端口行（→ 行内详情）。
//
// 两处可折叠层级：
//   一级 网段（IPv4 取 /24；127.x → 本机回环；IPv6 交给 groupIPv6：
//         ::1 → 本机回环、fe80::/10 → 链路本地、其余取 /64 桶；
//         主机名 → 其它主机）
//   二级 主机（优先 MAC，解析不到退化为 IP —— 见 hostKeyFor 的说明）
// 第三层就是原来的数据行，行内可再展开详情（由页面自己维护展开集合）。
//
// 为什么二级键优先 MAC：规则与端口记录里只存 IP，而 IP 会随 DHCP 变化；hosts 表
// 以 MAC 为身份，端口↔策略绑定也走 MAC。所以能从 /api/hosts 解析出该 IP 当前所属
// 主机的 MAC 时就用 mac:<mac> 作键 —— 设备换地址后仍留在同一台主机下，标题里的
// 「当前 IP」跟着更新；解析不到则退化为 ip:<ip>，否则这些行会从界面上消失。
//
// 双栈（同一设备既有 IPv4 又有 IPv6）：主机记录现在带 addresses[]，索引把每条
// 地址都映射到同一条记录，一级网段按该记录的「主地址」（hosts[].ip，v4 优先）算，
// 于是同一台设备的 v4 / v6 行落在同一个网段节点、同一个主机节点下，而不是被拆成
// 两段。没有 hosts 数据时自然退化回「按地址本身分组」。

import { fmtHost, groupIPv6, isIPv6 } from '../utils/netaddr'

const GROUP_PREF_KEY = 'natpilot-group-by-host'
const COLLAPSED_PREFIX = 'natpilot-collapsed-'

export function normMAC(mac) {
  const hex = String(mac || '')
    .toLowerCase()
    .replace(/[^0-9a-f]/g, '')
  return hex.length === 12 ? hex : ''
}

// addrKey normalises one address for map lookups: brackets are display sugar,
// so `[fd00::20]` and `fd00::20` must resolve to the same host record.
function addrKey(value) {
  const s = String(value == null ? '' : value).trim()
  if (s.length > 1 && s[0] === '[' && s[s.length - 1] === ']') return s.slice(1, -1).trim()
  return s
}

// hostAddresses flattens one host record into its addresses, primary first.
// `addresses` is optional: older payloads only carry the single `ip`.
export function hostAddresses(host) {
  const out = []
  const seen = new Set()
  const push = (value) => {
    const ip = addrKey(value && typeof value === 'object' ? value.ip : value)
    if (!ip || seen.has(ip)) return
    seen.add(ip)
    out.push(ip)
  }
  if (!host) return out
  push(host.ip)
  for (const a of host.addresses || []) push(a)
  return out
}

// lookupHost resolves one address to its host record, tolerating brackets.
export function lookupHost(index, ip) {
  if (!index) return null
  return index.get(addrKey(ip)) || null
}

// buildHostIndex maps every address of a host (primary + all `addresses[]`) to
// that host record, so a dual-stack device resolves from either family. Both
// the raw and the bracket-free spelling are registered, because callers hand us
// addresses straight from API payloads without normalising them first.
export function buildHostIndex(hosts) {
  const index = new Map()
  for (const h of hosts || []) {
    if (!h) continue
    const raw = [h.ip]
    for (const a of h.addresses || []) raw.push(a && typeof a === 'object' ? a.ip : a)
    for (const value of raw) {
      const ip = String(value == null ? '' : value).trim()
      if (!ip) continue
      index.set(ip, h)
      index.set(addrKey(ip), h)
    }
  }
  return index
}

// hostKeyFor returns the level-2 key for one row.
export function hostKeyFor(ip, index) {
  const host = lookupHost(index, ip)
  const mac = host ? normMAC(host.mac) : ''
  return mac ? `mac:${mac}` : `ip:${addrKey(ip) || '?'}`
}

// subnetKey derives the level-1 grouping for one address.
// IPv4 behaviour is unchanged (127.x → loopback, /24 otherwise); IPv6 is
// delegated to groupIPv6 so `net6:`-prefixed keys can never collide with the
// IPv4 keys above. A hostname still falls through to the "other" bucket.
export function subnetOf(ip) {
  const addr = String(ip || '').trim()
  if (!addr) return { key: 'unknown', label: '未识别网段', rank: 9 }
  if (isIPv6(addr)) {
    const v6 = groupIPv6(addr)
    if (v6) return v6
  }
  if (/^127\./.test(addr)) return { key: 'loopback', label: '本机回环 (127.0.0.0/8)', rank: 0 }
  const m = addr.match(/^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.\d{1,3}$/)
  if (m) {
    const cidr = `${Number(m[1])}.${Number(m[2])}.${Number(m[3])}.0/24`
    return { key: `net:${cidr}`, label: cidr, rank: 1 }
  }
  return { key: 'other', label: '其它主机', rank: 8 }
}

// groupBySubnet builds the two-level tree from a flat row list.
//
// options.hostKeyFor(item, record) is optional: a view that already knows how
// to identify a host (e.g. by its own host-keyed cache) can pass it and the
// returned non-empty string becomes the level-2 key. The built-in MAC/IP key is
// used whenever it returns nothing, so existing callers are unaffected. Even
// when a caller key is supplied a row is never split across two host nodes:
// the first subnet assigned to that key wins for every later row.
export function groupBySubnet(items, index, getIP, getPort, options = {}) {
  const callerKey = typeof options.hostKeyFor === 'function' ? options.hostKeyFor : null
  const nets = new Map()
  const keyNet = new Map() // caller host key -> subnet key already used for it

  for (const item of items || []) {
    const ip = getIP(item)
    const record = lookupHost(index, ip)

    // A resolved host is grouped by its primary address (hosts[].ip, v4 first):
    // that is what keeps a dual-stack device's v4 and v6 rows in one subnet
    // node instead of two. Unresolved rows keep grouping by their own address.
    const groupAddr = record && record.ip ? record.ip : ip
    const subnet = subnetOf(groupAddr)

    let hostKey = ''
    if (callerKey) hostKey = String(callerKey(item, record) || '').trim()
    if (!hostKey) hostKey = hostKeyFor(ip, index)

    let netKey = subnet.key
    if (hostKey) {
      const prior = keyNet.get(hostKey)
      if (prior) netKey = prior
      else keyNet.set(hostKey, netKey)
    }

    let net = nets.get(netKey)
    if (!net) {
      net = { key: netKey, label: subnet.label, rank: subnet.rank, hosts: new Map(), count: 0 }
      nets.set(netKey, net)
    }
    let host = net.hosts.get(hostKey)
    if (!host) {
      host = {
        key: hostKey,
        // Primary address first: this is what the node title falls back to.
        ip: addrKey((record && record.ip) || ip) || '?',
        primary: addrKey((record && record.ip) || ip),
        // Seed with every address the host record knows about, so a v4-only
        // row list still advertises the device's IPv6 address.
        ips: new Set(record ? hostAddresses(record) : []),
        mac: record ? record.mac || '' : '',
        hostname: record ? record.hostname || '' : '',
        vendor: record ? record.vendor || '' : '',
        items: [],
      }
      net.hosts.set(hostKey, host)
    }
    const rowIP = addrKey(ip)
    if (rowIP) host.ips.add(rowIP)
    host.items.push(item)
    net.count++
  }

  const out = [...nets.values()].map((net) => {
    const hosts = [...net.hosts.values()]
    for (const h of hosts) {
      h.items.sort((a, b) => {
        const pa = Number(getPort(a)) || 0
        const pb = Number(getPort(b)) || 0
        return pa !== pb ? pa - pb : String(getIP(a)).localeCompare(String(getIP(b)))
      })
      // Primary address first, then the rest in numeric order. The label is
      // rendered compactly as `192.168.1.20 · [fd00::20]`.
      const all = [...h.ips].filter(Boolean)
      all.sort((a, b) => {
        if (a === h.primary) return -1
        if (b === h.primary) return 1
        return String(a).localeCompare(String(b), undefined, { numeric: true })
      })
      h.ipList = all
      h.addrText = all.map((ip) => fmtHost(ip)).join(' · ')
      h.title = h.hostname || h.vendor || h.mac || h.ipList[0] || '?'
      h.subtitle = h.addrText
    }
    // Bigger hosts first, then by title.
    hosts.sort((a, b) => {
      if (b.items.length !== a.items.length) return b.items.length - a.items.length
      return String(a.title).localeCompare(String(b.title))
    })
    return { ...net, hosts }
  })

  out.sort((a, b) => {
    if (a.rank !== b.rank) return a.rank - b.rank
    if (b.count !== a.count) return b.count - a.count
    return a.label.localeCompare(b.label)
  })
  return out
}

// rowsForTree flattens the tree into the rows the table renders. A level whose
// key is in the corresponding collapsed set hides everything below it.
export function rowsForTree(tree, collapsedSubnets, collapsedHosts) {
  const out = []
  for (const subnet of tree || []) {
    out.push({ type: 'subnet', subnet })
    if (collapsedSubnets.has(subnet.key)) continue
    for (const host of subnet.hosts) {
      out.push({ type: 'host', host, subnet })
      if (collapsedHosts.has(host.key)) continue
      for (const item of host.items) out.push({ type: 'row', item, host, subnet })
    }
  }
  return out
}

// rowKey is the v-for key for a flattened row.
export function rowKey(row) {
  if (row.type === 'subnet') return `s-${row.subnet.key}`
  if (row.type === 'host') return `h-${row.host.key}`
  return `r-${row.item.id}`
}

// ---- persistence -----------------------------------------------------------

export function readGroupPref() {
  try {
    return localStorage.getItem(GROUP_PREF_KEY) !== '0'
  } catch {
    return true
  }
}

export function writeGroupPref(on) {
  try {
    localStorage.setItem(GROUP_PREF_KEY, on ? '1' : '0')
  } catch {
    /* storage blocked: the toggle still works for this session */
  }
}

// readCollapsed loads the collapsed-key set for one level ('subnet' | 'host').
export function readCollapsed(level) {
  try {
    const raw = localStorage.getItem(COLLAPSED_PREFIX + level)
    const list = raw ? JSON.parse(raw) : []
    return new Set(Array.isArray(list) ? list : [])
  } catch {
    return new Set()
  }
}

export function writeCollapsed(level, set) {
  try {
    localStorage.setItem(COLLAPSED_PREFIX + level, JSON.stringify([...set].slice(0, 800)))
  } catch {
    /* ignore */
  }
}
