<script setup>
import { ref, watch } from 'vue'

const props = defineProps({
  modelValue: { type: Array, default: () => [] },
  placeholder: { type: String, default: '输入后按回车添加标签' },
  suggestions: { type: Array, default: () => [] },
})
const emit = defineEmits(['update:modelValue'])
const draft = ref('')

function add(tag) {
  const t = String(tag || draft.value).trim()
  if (!t) return
  const list = props.modelValue || []
  if (!list.includes(t)) emit('update:modelValue', [...list, t])
  draft.value = ''
}
function remove(tag) {
  emit('update:modelValue', (props.modelValue || []).filter((x) => x !== tag))
}
watch(
  () => props.modelValue,
  () => {},
)
</script>

<template>
  <div>
    <div class="row wrap" style="margin-bottom: 6px">
      <span v-for="t in modelValue" :key="t" class="tag">
        {{ t }}
        <button type="button" @click="remove(t)">✕</button>
      </span>
      <span v-if="!modelValue || !modelValue.length" class="mute small">暂无标签</span>
    </div>
    <input
      v-model="draft"
      class="input"
      :placeholder="placeholder"
      list="np-tag-suggestions"
      @keydown.enter.prevent="add()"
      @keydown.,.prevent="add()"
    />
    <datalist id="np-tag-suggestions">
      <option v-for="s in suggestions" :key="s" :value="s" />
    </datalist>
  </div>
</template>
