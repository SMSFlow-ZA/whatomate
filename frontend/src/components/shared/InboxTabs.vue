<script setup lang="ts">
const model = defineModel<string>({ required: true })
const props = defineProps<{
  items: { value: string; label: string }[]
  label: string
}>()
function navigate(event: KeyboardEvent, index: number) {
  let next = index
  if (event.key === 'ArrowRight') next = (index + 1) % props.items.length
  else if (event.key === 'ArrowLeft')
    next = (index + props.items.length - 1) % props.items.length
  else if (event.key === 'Home') next = 0
  else if (event.key === 'End') next = props.items.length - 1
  else return
  event.preventDefault()
  const buttons = (
    event.currentTarget as HTMLElement
  ).parentElement?.querySelectorAll<HTMLButtonElement>('[role="tab"]')
  model.value = props.items[next].value
  buttons?.[next]?.focus()
}
</script>
<template>
  <nav class="inbox-tabs" :aria-label="label" role="tablist">
    <button
      v-for="(item, index) in items"
      :key="item.value"
      type="button"
      role="tab"
      :aria-selected="model === item.value"
      :tabindex="model === item.value ? 0 : -1"
      @click="model = item.value"
      @keydown="navigate($event, index)"
    >
      {{ item.label }}
    </button>
  </nav>
</template>
