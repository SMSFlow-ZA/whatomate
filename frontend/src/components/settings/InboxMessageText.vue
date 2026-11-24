<script setup lang="ts">
import { nextTick, ref } from 'vue'
import InboxMessageVariables from '@/components/chat/InboxMessageVariables.vue'
const model = defineModel<string>({ required: true })
defineProps<{ label: string; variables?: boolean }>()
const emit = defineEmits<{ personalised: [] }>()
const input = ref<HTMLTextAreaElement>()
async function insert(token: string) {
  const start = input.value?.selectionStart ?? model.value.length
  const end = input.value?.selectionEnd ?? start
  if (
    Array.from(model.value.slice(0, start) + token + model.value.slice(end))
      .length > 4096
  )
    return
  model.value = model.value.slice(0, start) + token + model.value.slice(end)
  emit('personalised')
  await nextTick()
  input.value?.focus()
  input.value?.setSelectionRange(start + token.length, start + token.length)
}
</script>
<template>
  <div class="grid min-w-0 gap-2">
    <label class="inbox-label"
      >{{ label }}
      <textarea ref="input" v-model="model" rows="3" maxlength="4096" />
    </label>
    <InboxMessageVariables v-if="variables" @insert="insert" />
  </div>
</template>
