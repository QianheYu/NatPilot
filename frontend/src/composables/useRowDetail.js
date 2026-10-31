// 全局「点击表格行看详情」。
//
// 表格单元格统一单行 + 省略号，完整内容需要出口；与其在每个页面的每个表格里
// 重复写一次弹窗，这里用事件委托统一处理：点击 table.data 的数据行时，按表头
// 收集每列的完整文本（innerText 不受省略号影响），交给 RowDetailModal 展示。
//
// 不处理的点击：
//   - 行内的按钮/复选框/下拉/输入框/链接（这些是操作，不是"看详情"）
//   - 树形表格的分组行（点击是展开/折叠）
import { ref } from 'vue'

export const rowDetail = ref(null)

export function closeRowDetail() {
  rowDetail.value = null
}

const INTERACTIVE = 'button, a, input, select, textarea, label, .cell-actions, .exp, [role="button"]'

function headerLabels(table) {
  const heads = table.querySelectorAll('thead th')
  return [...heads].map((th) => (th.textContent || '').trim())
}

function cellValue(td) {
  // innerText 反映的是完整文本（CSS 的 text-overflow 只影响渲染，不改文本）。
  const raw = (td.innerText || '').replace(/\u00a0/g, ' ').trim()
  return raw
}

export function describeRow(tr) {
  const table = tr.closest('table')
  if (!table) return null
  const labels = headerLabels(table)
  const tds = [...tr.children]
  const rows = []
  tds.forEach((td, i) => {
    // 跨列单元格（树表的分组/详情行、空操作列）跳过
    if (td.colSpan > 1) return
    // 没有表头的列（勾选/展开箭头）不是数据，跳过
    const label = labels[i] || td.dataset.label || ''
    if (!label) return
    // 纯图标单元格(展开箭头、开关)也跳过
    if (td.classList.contains('cell-exp') || td.classList.contains('gt-sel')) return
    const value = cellValue(td)
    if (!value) return
    rows.push({ label, value, mono: /mono/.test(td.className) })
  })
  if (!rows.length) return null

  // 标题：优先用行里第一列的内容，其次用表名
  const first = rows[0]
  const title = first ? `${first.label}: ${first.value}`.slice(0, 80) : '行详情'
  return { title, rows }
}

let installed = false

// installRowDetail 在应用根组件挂载时调用一次。
export function installRowDetail(root) {
  if (installed) return () => {}
  installed = true
  const onClick = (ev) => {
    const target = ev.target
    if (!(target instanceof Element)) return
    // 交互元素优先：点按钮/复选框时不要弹详情
    if (target.closest(INTERACTIVE)) return
    const row = target.closest('table.data tbody tr')
    if (!row) return
    // 树形表格：分组行是折叠开关，详情行没有有效列
    if (row.classList.contains('group-row') || row.classList.contains('detail-row')) return
    const info = describeRow(row)
    if (info) rowDetail.value = info
  }
  root.addEventListener('click', onClick)
  return () => {
    root.removeEventListener('click', onClick)
    installed = false
  }
}
