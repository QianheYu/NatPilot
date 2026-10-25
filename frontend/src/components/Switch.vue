<script setup>
const props = defineProps({
  modelValue: { type: [Boolean, String, Number], default: false },
  disabled: { type: Boolean, default: false },
  label: { type: String, default: '' },
})
const emit = defineEmits(['update:modelValue'])
function toggle(e) {
  if (props.disabled) return
  emit('update:modelValue', e.target.checked)
}
</script>

<template>
  <label class="switch">
    <input type="checkbox" :checked="!!modelValue" :disabled="disabled" @change="toggle" />
    <span class="track" />
    <span v-if="label" class="small dim">{{ label }}</span>
  </label>
</template>
