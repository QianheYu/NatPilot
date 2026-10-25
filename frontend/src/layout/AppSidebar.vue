<script setup>
import { useRoute } from 'vue-router'

const props = defineProps({
  modules: { type: Array, default: () => [] },
  collapsed: { type: Boolean, default: false },
  version: { type: String, default: '' },
  mode: { type: String, default: '' },
  live: { type: Boolean, default: false },
  badges: { type: Object, default: () => ({}) },
})

const route = useRoute()

function badgeFor(item) {
  const n = props.badges[item.name]
  return n > 0 ? n : 0
}
</script>

<template>
  <aside class="app-sidebar">
    <div class="sb-brand">
      <div class="sb-logo">NP</div>
      <div v-if="!collapsed" class="sb-brand-text">
        <div class="sb-name">NatPilot</div>
        <div class="sb-sub">NAT 映射控制台</div>
      </div>
    </div>

    <nav class="sb-nav">
      <div v-for="m in modules" :key="m.key" class="sb-module">
        <div v-if="!collapsed" class="sb-section">{{ m.title }}</div>
        <div v-else class="sb-divider" />
        <router-link
          v-for="item in m.items"
          :key="item.name"
          :to="{ name: item.name }"
          class="sb-item"
          :class="{ active: route.name === item.name }"
          :title="collapsed ? item.title : item.desc || item.title"
        >
          <span class="sb-ico">{{ item.icon }}</span>
          <span v-if="!collapsed" class="sb-label">{{ item.title }}</span>
          <span v-if="!collapsed && badgeFor(item)" class="badge accent">{{ badgeFor(item) }}</span>
        </router-link>
      </div>
    </nav>

    <div class="sb-foot">
      <span class="sb-dot" :class="live ? 'on' : 'off'" :title="live ? '实时通道已连接' : '实时通道已断开'" />
      <span v-if="!collapsed">{{ live ? '实时连接' : '实时断开' }}</span>
      <span class="tb-spacer" />
      <span v-if="!collapsed" class="dim">{{ mode || 'auto' }} · v{{ version || '0.1.0' }}</span>
    </div>
  </aside>
</template>
