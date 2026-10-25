<script setup>
defineProps({
  kind: { type: String, default: 'default' }, // default|ok|warn|danger|info|accent|purple
  dot: { type: Boolean, default: false },
})
</script>

<template>
  <span class="badge" :class="[kind, { dot }]"><slot /></span>
</template>
