<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { computed } from 'vue'
import type { IntegrationIntent } from '@/services/inboxIntegrations'
const props = defineProps<{ intent: IntegrationIntent }>()
const { t, te } = useI18n()
const unavailableReason = computed(() => {
  const key = 'inboxIntegrations.reason.' + props.intent.reason
  return te?.(key) ? t(key) : t('inboxIntegrations.unavailable')
})
function display(value: unknown) {
  return typeof value === 'string' ? value : JSON.stringify(value)
}
</script>
<template>
  <div class="mt-2 grid min-w-0 gap-2 text-sm">
    <p class="break-words font-medium">{{ intent.name }}</p>
    <p class="inbox-caption break-words">
      {{ intent.method }} · {{ intent.destination }}
    </p>
    <p v-if="!intent.available" role="status" class="inbox-caption">
      {{ unavailableReason }}
    </p>
    <dl v-if="Object.keys(intent.inputs ?? {}).length" class="grid gap-2">
      <div v-for="(value, name) in intent.inputs" :key="name" class="min-w-0">
        <dt class="inbox-caption break-words">{{ name }}</dt>
        <dd class="whitespace-pre-wrap break-words">{{ display(value) }}</dd>
      </div>
    </dl>
    <dl
      v-if="Object.keys(intent.deferred_inputs ?? {}).length"
      class="grid gap-2"
    >
      <div v-for="(keys, name) in intent.deferred_inputs" :key="name">
        <dt class="font-medium break-words">{{ name }}</dt>
        <dd class="inbox-caption break-words">
          {{ t('inboxIntegrations.deferredInput') }} {{ keys.join(', ') }}
        </dd>
      </div>
    </dl>
    <p
      v-if="Object.keys(intent.deferred_inputs ?? {}).length"
      class="inbox-notice"
    >
      {{ t('inboxIntegrations.deferredHelp') }}
    </p>
    <div v-if="intent.outputs?.length">
      <p class="inbox-caption">{{ t('inboxIntegrations.mappedFields') }}</p>
      <ul class="mt-1 grid gap-1">
        <li v-for="field in intent.outputs" :key="field.id" class="break-words">
          {{ field.label }} · {{ t('inboxIntegrations.scope.' + field.scope) }}
        </li>
      </ul>
    </div>
  </div>
</template>
