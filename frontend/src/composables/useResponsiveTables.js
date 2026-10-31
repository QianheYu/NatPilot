// useResponsiveTables keeps data tables usable on narrow windows.
//
// Below 720px `responsive.css` lays every table out as a stack of labelled
// cards, which is far more readable on a phone than a horizontally scrolling
// grid. That layout needs to know each cell's column name, and the views render
// plain `<td>` cells, so the label is derived from the table header here.
//
// A MutationObserver is used because the views re-render their rows whenever
// data arrives over SSE; re-labelling on change keeps the cards correct without
// touching any of the 12 views.

import { onBeforeUnmount, onMounted, watch } from 'vue'

// STACK_ATTR marks a table as eligible for the card layout.
const STACK_ATTR = 'data-stack'
const LABEL_ATTR = 'data-label'

// labelTable copies the header text of each column onto the matching cells.
function labelTable(table) {
  if (table.classList.contains('tree')) {
    table.removeAttribute(STACK_ATTR)
    return
  }

  const headers = Array.from(table.querySelectorAll('thead th')).map((th) =>
    (th.textContent || '').trim().replace(/\s+/g, ' '),
  )
  if (!headers.length) {
    // Without a header row there is nothing to label cells with, so the table
    // keeps the horizontal-scroll layout instead of becoming unlabelled cards.
    table.removeAttribute(STACK_ATTR)
    return
  }
  table.setAttribute(STACK_ATTR, '1')
  for (const row of table.querySelectorAll('tbody tr')) {
    Array.from(row.children).forEach((cell, index) => {
      const label = headers[index] || ''
      const current = cell.getAttribute(LABEL_ATTR)
      if (label && current !== label) {
        cell.setAttribute(LABEL_ATTR, label)
      } else if (!label && current !== null) {
        cell.removeAttribute(LABEL_ATTR)
      }
    })
  }
}

// labelAll labels every table under root.
export function labelAllTables(root) {
  if (!root || !root.querySelectorAll) return 0
  const tables = root.querySelectorAll('table.data')
  tables.forEach(labelTable)
  return tables.length
}

// useResponsiveTables watches the page container and labels tables as they
// appear or are updated. Pass the ref of the element that holds <router-view>.
export function useResponsiveTables(contentRef) {
  let observer = null
  let pending = 0

  const run = () => {
    const root = contentRef && contentRef.value
    if (!root) return
    const tables = root.querySelectorAll('table.data')
    if (!tables.length) return
    tables.forEach(labelTable)
  }

  // Rows can arrive in bursts (one per SSE event); coalesce them into one pass
  // per animation frame so the observer never becomes the bottleneck.
  const schedule = () => {
    if (pending) return
    pending = window.setTimeout(() => {
      pending = 0
      run()
    }, 40)
  }

  // The page container only exists once boot has finished (App.vue renders a
  // loading screen first), so the observer is attached whenever the ref first
  // becomes available instead of in onMounted.
  let started = false

  const start = () => {
    const root = contentRef && contentRef.value
    if (started || !root) return
    started = true
    run()
    if (typeof MutationObserver === 'undefined') return
    observer = new MutationObserver(schedule)
    observer.observe(root, { childList: true, subtree: true })
  }

  onMounted(() => {
    start()
  })

  // Fire once the container appears (and again if the layout swaps it out).
  watch(
    () => (contentRef && contentRef.value ? contentRef.value : null),
    (el) => {
      if (el) start()
    },
    { immediate: true },
  )

  // A route change swaps the whole view; the observer stays attached to the
  // container, so a re-run is all that is needed.
  watch(
    () => (contentRef && contentRef.value ? contentRef.value.children.length : 0),
    schedule,
  )

  onBeforeUnmount(() => {
    if (pending) {
      window.clearTimeout(pending)
      pending = 0
    }
    if (observer) {
      observer.disconnect()
      observer = null
    }
  })

  return { relabel: run }
}
