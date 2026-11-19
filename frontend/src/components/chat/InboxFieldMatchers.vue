<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import InboxFieldInput from './InboxFieldInput.vue'
import {
  inboxService,
  type FieldDefinition,
  type FieldMatch,
  type FieldValue,
} from '@/services/inbox'

const matches = defineModel<FieldMatch[]>({ required: true })
const { t } = useI18n()
const definitions = ref<FieldDefinition[]>([])
const failed = ref(false)
const chosen = ref('')
const value = ref<FieldValue>(null)
const definition = (id: string) => definitions.value.find((d) => d.id === id)
async function loadDefinitions() {
  failed.value = false
  try {
    definitions.value = (
      await inboxService.fieldDefinitions()
    ).data.data.definitions
  } catch {
    failed.value = true
  }
}
onMounted(loadDefinitions)
function add() {
  if (
    !chosen.value ||
    value.value === null ||
    value.value === '' ||
    matches.value.length >= 8
  )
    return
  matches.value = [
    ...matches.value.filter((m) => m.definition_id !== chosen.value),
    { definition_id: chosen.value, value: value.value },
  ]
  chosen.value = ''
  value.value = null
}
</script>

<template>
  <p v-if="failed" role="alert" class="text-sm text-destructive">
    {{ t('inboxFields.unavailable') }}
    <button type="button" class="underline" @click="loadDefinitions">
      {{ t('common.retry') }}
    </button>
  </p>
  <fieldset
    v-if="definitions.length || matches.length"
    class="grid min-w-0 gap-3"
  >
    <legend class="mb-2 text-xs font-medium">
      {{ t('inboxFields.matches') }}
    </legend>
    <div
      v-for="match in matches"
      :key="match.definition_id"
      class="flex items-center justify-between gap-2 break-all text-sm"
    >
      <span
        >{{
          definition(match.definition_id)?.label || t('inboxFields.unavailable')
        }}
        ·
        {{
          typeof match.value === 'boolean'
            ? t(match.value ? 'common.yes' : 'common.no')
            : match.value
        }}</span
      >
      <button
        type="button"
        @click="
          matches = matches.filter(
            (m) => m.definition_id !== match.definition_id,
          )
        "
        class="ghost small"
      >
        {{ t('common.remove') }}
      </button>
    </div>
    <select
      v-model="chosen"
      class="rounded-md border border-input bg-background p-1"
      :aria-label="t('inboxFields.choose')"
      @change="value = null"
    >
      <option value="">{{ t('inboxFields.choose') }}</option>
      <option
        v-for="d in definitions.filter((item) => !item.archived_at)"
        :key="d.id"
        :value="d.id"
      >
        {{ d.label }} · {{ t('inboxFields.scope.' + d.scope) }}
      </option>
    </select>
    <template
      v-for="d in definitions.filter((item) => item.id === chosen)"
      :key="d.id"
    >
      <InboxFieldInput v-model="value" :definition="d" />
      <button
        type="button"
        :disabled="value === null || value === '' || matches.length >= 8"
        @click="add"
        class="outline small"
      >
        {{ t('inboxFields.addMatch') }}
      </button>
    </template>
  </fieldset>
</template>
