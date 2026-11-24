<script setup lang="ts">
import { computed, getCurrentInstance, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import { useAuthStore } from '@/stores/auth'
import { useInboxViewerContext } from '@/composables/useInboxViewerContext'
import type { IntegrationControlReview } from '@/services/inboxIntegrations'

defineProps<{ disabled?: boolean }>()
const emit = defineEmits<{ review: [request: IntegrationControlReview] }>()
const { t } = useI18n(),
  auth = useAuthStore(),
  viewer = useInboxViewerContext()
const runId = ref(''),
  touched = ref(false)
const prefix = 'inbox-workflow-stop-' + getCurrentInstance()!.uid
const operator = computed(() =>
  ['settings.general', 'chat', 'custom_actions'].every((resource) =>
    auth.hasPermission(resource, 'write'),
  ),
)
const normalized = computed(() => runId.value.trim().toLowerCase())
const valid = computed(
  () =>
    /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(
      normalized.value,
    ) && normalized.value !== '00000000-0000-0000-0000-000000000000',
)
watch(
  () => JSON.stringify([viewer.value, operator.value]),
  () => {
    runId.value = ''
    touched.value = false
  },
  { flush: 'sync' },
)
function review() {
  touched.value = true
  if (!operator.value || !valid.value) return
  emit('review', { kind: 'cancel', run: { id: normalized.value } })
}
</script>
<template>
  <details
    v-if="operator"
    class="border-b border-border pb-4"
    data-inbox-workflow-stop
  >
    <summary class="min-h-[44px] cursor-pointer py-3 font-medium">
      {{ t('inboxStop.title') }}
    </summary>
    <form
      class="grid max-w-2xl gap-3 pt-2"
      @submit.prevent="!disabled && review()"
    >
      <p class="inbox-caption">{{ t('inboxStop.help') }}</p>
      <label class="inbox-label" :for="prefix"
        >{{ t('inboxStop.runId') }} ({{ t('common.required') }})</label
      >
      <input
        :id="prefix"
        v-model="runId"
        type="text"
        class="inbox-input"
        required
        aria-required="true"
        autocomplete="off"
        spellcheck="false"
        :disabled="disabled"
        :aria-invalid="touched && !valid"
        :aria-describedby="
          prefix + '-help' + (touched && !valid ? ' ' + prefix + '-error' : '')
        "
        @blur="touched = true"
      />
      <p :id="prefix + '-help'" class="inbox-caption">
        {{ t('inboxStop.identifierHelp') }}
      </p>
      <p v-if="touched && !valid" :id="prefix + '-error'" role="alert">
        {{ t('inboxStop.invalidId') }}
      </p>
      <Button
        type="submit"
        variant="outline"
        class="justify-self-start"
        :disabled="disabled || !valid"
        >{{ t('inboxStop.review') }}</Button
      >
    </form>
  </details>
</template>
