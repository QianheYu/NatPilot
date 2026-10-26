// 端口发现 / 映射规则 的「按主机分组」支持。
//
// 分组键的选择（这是本文件的核心决定）：
//
//   * 规则与端口记录里只存 IP（rule.host / port.host），IP 会随 DHCP 变化；
//   * hosts 表以 MAC 为身份（ux_hosts_mac），端口↔策略的绑定也走 MAC；
//   * 所以能用 IP 解析出所属主机的 MAC 时，就以 `mac:...` 作键 —— 同一台设备
//     换地址后仍留在同一组；
//   * 解析不到（远程主机、尚未发现、已被清理）就退化为 `ip:...`，否则条目
//     会从界面上消失，这是不可接受的。
//
// 组标题同时展示主机名、MAC 与当前 IP：组本身是稳定的，组内 IP 会随实际
// 情况更新。

const GROUP_PREF_KEY = 'natpilot-group-by-host'
const COLLAPSED_PREF_KEY = 'natpilot-collapsed-hosts'

// normMAC 与后端 model.NormalizeMAC 保持一致。
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

// hostKeyFor returns the grouping key for one row.
export function hostKeyFor(ip, index) {
  const host = index && index.get(String(ip || ''))
  const mac = host ? normMAC(host.mac) : ''
  return mac ? `mac:${mac}` : `ip:${ip || '?'}`
}

// groupByHost groups rows by host key, keeping the order of first appearance
// for groups and sorting rows inside a group by port.
export function groupByHost(items, index, getIP, getPort) {
  const groups = new Map()
  for (const item of items || []) {
    const ip = getIP(item)
    const key = hostKeyFor(ip, index)
    let g = groups.get(key)
    if (!g) {
      const host = index && index.get(String(ip || ''))
      g = {
        key,
        ip: ip || '?',
        ips: new Set(),
        mac: host ? host.mac || '' : '',
        hostname: host ? host.hostname || '' : '',
        vendor: host ? host.vendor || '' : '',
        items: [],
      }
      groups.set(key, g)
    }
    g.ips.add(ip || '?')
    g.items.push(item)
  }
  const out = [...groups.values()]
  for (const g of out) {
    g.items.sort((a, b) => {
      const pa = Number(getPort(a)) || 0
      const pb = Number(getPort(b)) || 0
      if (pa !== pb) return pa - pb
      return String(getIP(a)).localeCompare(String(getIP(b)))
    })
    // A group is either one device (MAC known, address may change) or a single
    // address that could not be resolved to a device.
    g.ipList = [...g.ips].sort()
    g.title = g.hostname || g.vendor || g.mac || g.ipList[0]
    g.subtitle = g.mac && g.ipList.length ? g.ipList.join(', ') : ''
  }
  // Bigger groups first, then by title for a stable order.
  out.sort((a, b) => {
    if (b.items.length !== a.items.length) return b.items.length - a.items.length
    return String(a.title).localeCompare(String(b.title))
  })
  return out
}

// useHostGroups provides the persisted "group by host" toggle plus the set of
// collapsed group keys. Both live in localStorage so the choice survives reloads
// and is shared by the two pages that use it.
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

export function readCollapsed() {
  try {
    const raw = localStorage.getItem(COLLAPSED_PREF_KEY)
    const list = raw ? JSON.parse(raw) : []
    return new Set(Array.isArray(list) ? list : [])
  } catch {
    return new Set()
  }
}

export function writeCollapsed(set) {
  try {
    localStorage.setItem(COLLAPSED_PREF_KEY, JSON.stringify([...set].slice(0, 500)))
  } catch {
    /* ignore */
  }
}

// rowsFor flattens groups into the row list the table renders: a `group` entry
// followed by its rows unless the group is collapsed.
export function rowsFor(groups, collapsed) {
  const out = []
  for (const g of groups) {
    out.push({ type: 'group', group: g })
    if (!collapsed.has(g.key)) {
      for (const item of g.items) out.push({ type: 'row', item, group: g })
    }
  }
  return out
}
