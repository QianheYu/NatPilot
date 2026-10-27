<script setup>
// 三态复选框：支持 indeterminate（部分选中），用于分组行的"全选本组"。
// 原生 checkbox 的 indeterminate 只是 DOM 属性、不是 HTML 特性，所以这里显式同步。
import { onMounted, ref, watch } from 'vue'

const props = defineProps({
  checked: { type: Boolean, default: false },
  indeterminate: { type: Boolean, default: false },
  disabled: { type: Boolean, default: false },
  title: { type: String, default: '' },
})
const emit = defineEmits(['change'])

const el = ref(null)

function sync() {
  if (el.value) el.value.indeterminate = props.indeterminate && !props.checked
}

onMounted(sync)
watch(() => [props.checked, props.indeterminate], sync)

function onChange() {
  emit('change', !props.checked)
}
</script>

<template>
  <input
    ref="el"
    type="checkbox"
    :checked="checked"
    :disabled="disabled"
    :title="title"
    @click.stop
    @change="onChange"
  />
</template>
