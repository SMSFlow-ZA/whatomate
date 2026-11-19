<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { FieldDefinition, FieldValue } from '@/services/inbox'
const props = defineProps<{
  definition: FieldDefinition
  disabled?: boolean
  label?: string
}>()
const value = defineModel<FieldValue>({ required: true })
const { t } = useI18n()
function update(event: Event) {
  const raw = (event.target as HTMLInputElement).value
  if (!raw) value.value = null
  else if (props.definition.kind === 'number') value.value = Number(raw)
  else if (props.definition.kind === 'boolean') value.value = raw === 'true'
  else value.value = raw
}
</script>

<template>
  <select
    class="inbox-control"
    v-if="['select', 'boolean'].includes(definition.kind)"
    :value="value === null ? '' : String(value)"
    :aria-label="label || definition.label"
    :disabled="disabled"
    @change="update"
  >
    <option value="">—</option>
    <template v-if="definition.kind === 'boolean'">
      <option value="true">{{ t('common.yes') }}</option>
      <option value="false">{{ t('common.no') }}</option>
    </template>
    <option v-for="option in definition.options" :key="option" :value="option">
      {{ option }}
    </option>
  </select>
  <input
    class="inbox-control"
    v-else
    :type="definition.kind === 'text' ? 'text' : definition.kind"
    :step="definition.kind === 'number' ? 'any' : undefined"
    :value="value ?? ''"
    :aria-label="label || definition.label"
    :disabled="disabled"
    maxlength="4096"
    @input="update"
  />
</template>
