<script setup>
defineProps({
  icon: { type: String, default: '∅' },
  title: { type: String, default: '暂无数据' },
  hint: { type: String, default: '' },
})
</script>

<template>
  <div class="empty">
    <div class="big">{{ icon }}</div>
    <div style="font-weight: 600; color: var(--text-dim)">{{ title }}</div>
    <div v-if="hint" class="small" style="margin-top: 6px">{{ hint }}</div>
    <div v-if="$slots.default" style="margin-top: 12px"><slot /></div>
  </div>
</template>
