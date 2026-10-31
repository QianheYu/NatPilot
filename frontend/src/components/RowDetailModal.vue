<script setup>
// 表格行详情弹窗。
//
// 表格单元格统一单行显示、超出省略号；完整内容通过点击行在此弹窗里查看。
// 内容通过 useRowDetail 提供：{ title, rows: [{ label, value }] }
import Modal from './Modal.vue'
import { rowDetail, closeRowDetail, runRowAction } from '../composables/useRowDetail'
</script>

<template>
  <Modal v-if="rowDetail" :title="rowDetail.title" wide @close="closeRowDetail">
    <div class="kv-list">
      <div v-for="(r, i) in rowDetail.rows" :key="i" class="kv-row">
        <span class="kv-key">{{ r.label }}</span>
        <span class="kv-value" :class="{ mono: r.mono }">{{ r.value || '—' }}</span>
      </div>
    </div>
    <template #footer>
      <span v-if="rowDetail.actions && rowDetail.actions.length" class="detail-actions">
        <button
          v-for="(a, i) in rowDetail.actions"
          :key="i"
          class="btn sm"
          :class="{ danger: /删除|移除/.test(a.label) }"
          :disabled="a.disabled"
          @click="runRowAction(i)"
        >
          {{ a.label }}
        </button>
      </span>
      <button class="btn" @click="closeRowDetail">关闭</button>
    </template>
  </Modal>
</template>

<style scoped>
.detail-actions {
  display: inline-flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-right: auto;
}

.detail-actions .btn.danger {
  color: var(--danger);
  border-color: color-mix(in srgb, var(--danger) 45%, var(--border));
}

.detail-actions .btn.danger:hover:not(:disabled) {
  color: #fff;
  background: var(--danger);
  border-color: var(--danger);
}
.kv-list {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.kv-row {
  display: grid;
  grid-template-columns: 148px minmax(0, 1fr);
  gap: 12px;
  padding: 8px 2px;
  border-bottom: 1px solid var(--border-soft);
}

.kv-row:last-child {
  border-bottom: 0;
}

.kv-key {
  color: var(--text-mute);
  font-size: 12px;
}

.kv-value {
  min-width: 0;
  word-break: break-word;
  white-space: pre-wrap;
  font-size: 13px;
}
</style>
