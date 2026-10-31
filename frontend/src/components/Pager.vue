<script setup>
// 通用分页控件：共 N 条 / 每页条数 / 上一页 下一页 / 页码。
// 两个 v-model 分别对应 limit（每页条数）与 offset（起始偏移）。
import { computed } from 'vue'

const props = defineProps({
  total: { type: Number, default: 0 },
  limit: { type: Number, default: 50 },
  offset: { type: Number, default: 0 },
  // 每页可选条数
  sizes: { type: Array, default: () => [20, 50, 100, 200] },
})

const emit = defineEmits(['update:limit', 'update:offset', 'change'])

const pages = computed(() => Math.max(1, Math.ceil(props.total / Math.max(1, props.limit))))
const page = computed(() => Math.floor(props.offset / Math.max(1, props.limit)) + 1)

function go(next) {
  const target = Math.min(Math.max(1, next), pages.value)
  const offset = (target - 1) * props.limit
  if (offset === props.offset) return
  emit('update:offset', offset)
  emit('change')
}

function setLimit(e) {
  const limit = Number(e.target.value) || 50
  emit('update:limit', limit)
  emit('update:offset', 0)
  emit('change')
}
</script>

<template>
  <div class="pager">
    <span class="dim small">共 {{ total }} 条</span>
    <span class="pager-sep">·</span>
    <label class="pager-size">
      <span class="dim small">每页</span>
      <select class="select select-sm" :value="limit" @change="setLimit">
        <option v-for="s in sizes" :key="s" :value="s">{{ s }}</option>
      </select>
    </label>
    <span class="pager-nav">
      <button class="btn sm" :disabled="page <= 1" @click="go(page - 1)">上一页</button>
      <span class="small dim mono">{{ page }} / {{ pages }}</span>
      <button class="btn sm" :disabled="page >= pages" @click="go(page + 1)">下一页</button>
    </span>
  </div>
</template>

<style scoped>
.pager {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px;
  padding: 10px 12px;
  border-top: 1px solid var(--border-soft);
}

.pager-sep {
  color: var(--text-mute);
}

.pager-size {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.select-sm {
  min-height: 26px;
  padding: 2px 6px;
  font-size: 12px;
}

.pager-nav {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  margin-left: auto;
}
</style>
