<script setup>
import { computed, nextTick, ref, watch } from 'vue'

const props = defineProps({
  open: { type: Boolean, default: false },
  pages: { type: Array, default: () => [] },
})

const emit = defineEmits(['close', 'pick'])

const q = ref('')
const sel = ref(0)
const inputEl = ref(null)

const matches = computed(() => {
  const needle = q.value.trim().toLowerCase()
  if (!needle) return props.pages
  return props.pages.filter((p) =>
    [p.title, p.desc, p.module]
      .filter(Boolean)
      .some((s) => s.toLowerCase().includes(needle)),
  )
})

// Group the matches by module so the list mirrors the sidebar.
const grouped = computed(() => {
  const out = []
  for (const p of matches.value) {
    let g = out.find((x) => x.module === p.module)
    if (!g) {
      g = { module: p.module, items: [] }
      out.push(g)
    }
    g.items.push(p)
  }
  return out
})

const flat = computed(() => grouped.value.flatMap((g) => g.items))

watch(
  () => props.open,
  async (v) => {
    if (v) {
      q.value = ''
      sel.value = 0
      await nextTick()
      if (inputEl.value) inputEl.value.focus()
    }
  },
)

watch(matches, () => {
  sel.value = 0
})

function pick(page) {
  if (!page) return
  emit('pick', page)
  emit('close')
}

function move(delta) {
  const n = flat.value.length
  if (!n) return
  sel.value = (sel.value + delta + n) % n
}

function onKey(e) {
  if (e.key === 'ArrowDown') {
    e.preventDefault()
    move(1)
  } else if (e.key === 'ArrowUp') {
    e.preventDefault()
    move(-1)
  } else if (e.key === 'Enter') {
    e.preventDefault()
    pick(flat.value[sel.value])
  } else if (e.key === 'Escape') {
    e.preventDefault()
    emit('close')
  }
}

function indexOf(page) {
  return flat.value.indexOf(page)
}
</script>

<template>
  <div v-if="open" class="cp-overlay" @click.self="emit('close')" @keydown="onKey">
    <div class="cp-box">
      <div class="cp-input-row">
        <span class="ico">⌕</span>
        <input
          ref="inputEl"
          v-model="q"
          class="cp-input"
          placeholder="搜索功能页面，例如 规则 / 扫描 / 网关…"
          autocomplete="off"
          spellcheck="false"
          @keydown="onKey"
        />
        <span class="kbd dim">Esc</span>
      </div>
      <div class="cp-list">
        <template v-for="g in grouped" :key="g.module">
          <div class="cp-group">{{ g.module }}</div>
          <button
            v-for="p in g.items"
            :key="p.name"
            class="cp-item"
            :class="{ sel: indexOf(p) === sel }"
            @mouseenter="sel = indexOf(p)"
            @click="pick(p)"
          >
            <span class="cp-ico">{{ p.icon }}</span>
            <span class="cp-text">
              {{ p.title }}
              <span class="cp-desc">{{ p.desc }}</span>
            </span>
          </button>
        </template>
        <div v-if="!flat.length" class="popover-empty">没有匹配的页面</div>
      </div>
      <div class="cp-foot">
        <span><span class="kbd">↑</span> <span class="kbd">↓</span> 选择</span>
        <span><span class="kbd">Enter</span> 打开</span>
        <span><span class="kbd">Esc</span> 关闭</span>
      </div>
    </div>
  </div>
</template>
