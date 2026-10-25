<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { fmtRelative } from '../utils/format'

const props = defineProps({
  title: { type: String, default: '' },
  module: { type: String, default: '' },
  collapsed: { type: Boolean, default: false },
  unread: { type: Number, default: 0 },
  events: { type: Array, default: () => [] },
  monitorActive: { type: Boolean, default: false },
  live: { type: Boolean, default: false },
  theme: { type: String, default: 'dark' },
  authEnabled: { type: Boolean, default: false },
  identity: { type: Object, default: () => ({ username: '', name: '', role: 'viewer' }) },
})

const emit = defineEmits([
  'toggle-sidebar',
  'open-search',
  'toggle-theme',
  'mark-read',
  'goto-events',
  'change-password',
  'logout',
])

const open = ref('') // '' | 'notif' | 'user'

const roleLabel = computed(
  () => ({ admin: '管理员', operator: '操作员', viewer: '只读' })[props.identity.role] || '只读',
)

const initials = computed(() => {
  const src = props.identity.name || props.identity.username || 'NP'
  return src.slice(0, 2)
})

const displayName = computed(
  () => props.identity.name || props.identity.username || (props.authEnabled ? '未登录' : '本地操作员'),
)

const recent = computed(() => props.events.slice(0, 8))

function toggle(which) {
  open.value = open.value === which ? '' : which
}

function closeAll() {
  open.value = ''
}

function onDocClick(e) {
  if (!e.target.closest || !e.target.closest('.tb-pop-wrap')) closeAll()
}

function markRead() {
  emit('mark-read')
  closeAll()
}

function gotoEvents() {
  emit('goto-events')
  closeAll()
}

function changePassword() {
  emit('change-password')
  closeAll()
}

function logout() {
  emit('logout')
  closeAll()
}

onMounted(() => document.addEventListener('click', onDocClick))
onBeforeUnmount(() => document.removeEventListener('click', onDocClick))
</script>

<template>
  <header class="app-topbar">
    <button
      class="tb-toggle"
      :title="collapsed ? '展开侧边栏' : '收起侧边栏'"
      aria-label="切换侧边栏"
      @click="emit('toggle-sidebar')"
    >
      <!-- Below 860px the sidebar is a drawer, so the glyph becomes a menu
           button; responsive.css picks which one is shown. -->
      <span class="tg-wide">{{ collapsed ? '»' : '«' }}</span>
      <span class="tg-narrow">☰</span>
    </button>

    <div class="crumbs">
      <span class="crumb-module">{{ module }}</span>
      <span class="crumb-sep">/</span>
      <h1 class="crumb-title">{{ title }}</h1>
    </div>

    <div class="tb-spacer" />

    <button class="tb-search" title="搜索功能页面（Ctrl+K）" @click="emit('open-search')">
      <span>⌕</span>
      <span class="tb-search-text">搜索页面…</span>
      <span class="kbd">Ctrl K</span>
    </button>

    <span v-if="monitorActive" class="tb-chip ok" title="后台自动监控正在运行">
      <span class="pulse" />监控中
    </span>
    <span v-if="!live" class="tb-chip warn" title="实时事件通道已断开，正在重连">实时断开</span>

    <div class="tb-pop-wrap">
      <button
        class="tb-icon"
        :class="{ 'is-open': open === 'notif' }"
        title="通知"
        @click.stop="toggle('notif')"
      >
        🔔
        <span v-if="unread > 0" class="tb-badge-dot">{{ unread > 99 ? '99+' : unread }}</span>
      </button>
      <div v-if="open === 'notif'" class="popover popover-notif" @click.stop>
        <div class="popover-head">
          <span>通知</span>
          <span class="small dim">{{ unread > 0 ? unread + ' 条未读' : '全部已读' }}</span>
        </div>
        <div class="popover-body">
          <button v-for="ev in recent" :key="ev.id" class="notif" @click="gotoEvents">
            <div class="notif-top">
              <span class="notif-level" :class="ev.level" />
              <span class="notif-cat">{{ ev.category || 'system' }}</span>
              <span class="notif-time">{{ fmtRelative(ev.at) }}</span>
            </div>
            <div class="notif-msg">{{ ev.message }}</div>
          </button>
          <div v-if="!recent.length" class="popover-empty">暂无通知</div>
        </div>
        <div class="popover-foot">
          <button class="btn ghost sm" :disabled="!unread" @click="markRead">全部标为已读</button>
          <span class="tb-spacer" />
          <button class="btn ghost sm" @click="gotoEvents">查看全部</button>
        </div>
      </div>
    </div>

    <button
      class="tb-icon"
      :title="theme === 'dark' ? '切换到浅色主题' : '切换到深色主题'"
      @click="emit('toggle-theme')"
    >
      {{ theme === 'dark' ? '☀' : '☾' }}
    </button>

    <div class="tb-pop-wrap">
      <button
        class="tb-avatar"
        :class="{ 'is-open': open === 'user' }"
        title="账号"
        @click.stop="toggle('user')"
      >
        <span class="avatar-circle">{{ initials }}</span>
        <span v-if="!collapsed" class="tb-user-name">{{ displayName }}</span>
        <span class="dim">▾</span>
      </button>
      <div v-if="open === 'user'" class="popover popover-user" @click.stop>
        <div class="pop-user">
          <div class="pop-user-name">{{ displayName }}</div>
          <div class="pop-user-meta">
            {{ roleLabel }}
            <template v-if="authEnabled"> · {{ identity.username || '未登录' }}</template>
            <template v-else> · 免登录模式</template>
          </div>
        </div>
        <div class="popover-body">
          <button v-if="authEnabled" class="pop-item" @click="changePassword">
            <span class="ico">⚿</span>修改密码
          </button>
          <button class="pop-item" @click="gotoEvents">
            <span class="ico">≡</span>日志与审计
          </button>
          <div v-if="authEnabled" class="pop-sep" />
          <button v-if="authEnabled" class="pop-item danger" @click="logout">
            <span class="ico">⏻</span>退出登录
          </button>
        </div>
      </div>
    </div>
  </header>
</template>
