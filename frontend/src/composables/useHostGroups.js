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

import { groupIPv6, isIPv6 } from '../utils/netaddr'

const GROUP_PREF_KEY = 'natpilot-group-by-host'
const COLLAPSED_PREFIX = 'natpilot-collapsed-'

export function normMAC(mac) {
  const hex = String(mac || '')
    .toLowerCase()
    .replace(/[^0-9a-f]/g, '')
  return hex.length === 12 ? hex : ''
}

// buildHostIndex maps an address to the host record that currently owns it.
export function buildHostIndex(hosts) {
  const index = new Map()
  for (const h of hosts || []) {
    if (h && h.ip) index.set(h.ip, h)
  }
  return index
}

// hostKeyFor returns the level-2 key for one row.
export function hostKeyFor(ip, index) {
  const host = index && index.get(String(ip || ''))
  const mac = host ? normMAC(host.mac) : ''
  return mac ? `mac:${mac}` : `ip:${ip || '?'}`
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
export function groupBySubnet(items, index, getIP, getPort) {
  const nets = new Map()
  for (const item of items || []) {
    const ip = getIP(item)
    const subnet = subnetOf(ip)
    const hostKey = hostKeyFor(ip, index)

    let net = nets.get(subnet.key)
    if (!net) {
      net = { key: subnet.key, label: subnet.label, rank: subnet.rank, hosts: new Map(), count: 0 }
      nets.set(subnet.key, net)
    }
    let host = net.hosts.get(hostKey)
    if (!host) {
      const record = index && index.get(String(ip || ''))
      host = {
        key: hostKey,
        ip: ip || '?',
        ips: new Set(),
        mac: record ? record.mac || '' : '',
        hostname: record ? record.hostname || '' : '',
        vendor: record ? record.vendor || '' : '',
        items: [],
      }
      net.hosts.set(hostKey, host)
    }
    host.ips.add(ip || '?')
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
      h.ipList = [...h.ips].sort()
      h.title = h.hostname || h.vendor || h.mac || h.ipList[0]
      h.subtitle = h.mac && h.ipList.length ? h.ipList.join(', ') : ''
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
