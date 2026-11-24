<script setup lang="ts">
import { computed, nextTick, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import type { InboxQueueDiscovery } from '@/services/inbox'
import {
  applyInboxDates,
  emptyInboxDates,
  inboxDateDraft,
} from '@/lib/inboxDiscovery'

const props = defineProps<{
  modelValue: InboxQueueDiscovery
  disabled?: boolean
}>()
const emit = defineEmits<{
  'update:modelValue': [value: InboxQueueDiscovery]
}>()
const { t } = useI18n()
const id = useId(),
  root = ref<HTMLElement>()
const initial = computed(() => inboxDateDraft(props.modelValue))
const draft = ref({ ...initial.value.draft }),
  exact = ref(initial.value.exact),
  error = ref('')
const zones = [
  'UTC',
  ...((
    Intl as typeof Intl & { supportedValuesOf?: (key: string) => string[] }
  ).supportedValuesOf?.('timeZone') ?? ['Africa/Johannesburg']),
]
function cancel() {
  draft.value = { ...initial.value.draft }
  exact.value = initial.value.exact
  error.value = ''
}
watch(() => props.modelValue, cancel, { deep: true })
function replaceExact() {
  draft.value = emptyInboxDates(draft.value.timezone)
  exact.value = false
  nextTick(() =>
    root.value?.querySelector<HTMLInputElement>('input[type=date]')?.focus(),
  )
}
function apply() {
  try {
    emit('update:modelValue', applyInboxDates(draft.value, props.modelValue))
    error.value = ''
  } catch (cause) {
    error.value = ['timezone', 'range', 'skippedDay'].includes(
      (cause as Error).message,
    )
      ? (cause as Error).message
      : 'dates'
    nextTick(() =>
      root.value?.querySelector<HTMLElement>('[role=alert]')?.focus(),
    )
  }
}
function reset() {
  emit('update:modelValue', {
    order: props.modelValue.order || 'priority_waiting',
  })
  error.value = ''
  nextTick(() =>
    root.value?.querySelector<HTMLInputElement>('input[type=date]')?.focus(),
  )
}
</script>

<template>
  <section
    ref="root"
    class="border-t border-border pt-4 [color-scheme:light] dark:[color-scheme:dark]"
    :aria-labelledby="id + '-title'"
  >
    <h3 :id="id + '-title'" class="mb-2 text-sm font-medium">
      {{ t('inboxDiscovery.dates') }}
    </h3>
    <p :id="id + '-help'" class="inbox-caption mb-3">
      {{ t('inboxDiscovery.dateHelp') }}
    </p>
    <template v-if="exact">
      <p class="inbox-caption mb-2">{{ t('inboxDiscovery.exactHelp') }}</p>
      <dl class="mb-3 space-y-2 text-xs">
        <template
          v-for="key in [
            'opened_from',
            'opened_before',
            'activity_from',
            'activity_before',
          ] as const"
          :key="key"
        >
          <div v-if="modelValue[key]" class="break-words">
            <dt class="font-medium">{{ t('inboxDiscovery.' + key) }}</dt>
            <dd>{{ modelValue[key] }} · {{ modelValue.timezone }}</dd>
          </div>
        </template>
      </dl>
      <Button variant="outline" :disabled="disabled" @click="replaceExact">{{
        t('inboxDiscovery.replaceDates')
      }}</Button>
    </template>
    <form
      v-else
      class="grid gap-3"
      :aria-describedby="id + '-help'"
      @submit.prevent="apply"
    >
      <label class="inbox-label"
        >{{ t('inboxDiscovery.timezone')
        }}<input
          v-model="draft.timezone"
          :list="id + '-zones'"
          maxlength="128"
          :disabled="disabled"
          autocomplete="off"
          :aria-invalid="error === 'timezone' || undefined"
          :aria-describedby="error ? id + '-error' : id + '-help'" /><datalist
          :id="id + '-zones'"
        >
          <option v-for="zone in zones" :key="zone" :value="zone" /></datalist
      ></label>
      <fieldset
        v-for="group in ['opened', 'activity'] as const"
        :key="group"
        :disabled="disabled"
      >
        <legend class="mb-2 text-xs font-medium">
          {{ t('inboxDiscovery.' + group) }}
        </legend>
        <div class="grid grid-cols-2 gap-3">
          <label class="inbox-label"
            >{{ t('inboxDiscovery.from')
            }}<input
              v-model="
                draft[group === 'opened' ? 'openedFrom' : 'activityFrom']
              "
              type="date"
              min="0001-01-01"
              max="9999-12-31"
              :aria-invalid="(!!error && error !== 'timezone') || undefined"
              :aria-describedby="error ? id + '-error' : id + '-help'"
              :aria-label="
                t('inboxDiscovery.' + group) + ' — ' + t('inboxDiscovery.from')
              "
          /></label>
          <label class="inbox-label"
            >{{ t('inboxDiscovery.through')
            }}<input
              v-model="
                draft[group === 'opened' ? 'openedThrough' : 'activityThrough']
              "
              type="date"
              min="0001-01-01"
              max="9999-12-30"
              :aria-invalid="(!!error && error !== 'timezone') || undefined"
              :aria-describedby="error ? id + '-error' : id + '-help'"
              :aria-label="
                t('inboxDiscovery.' + group) +
                ' — ' +
                t('inboxDiscovery.through')
              "
          /></label>
        </div>
      </fieldset>
      <p
        v-if="error"
        :id="id + '-error'"
        role="alert"
        tabindex="-1"
        class="text-sm text-destructive"
      >
        {{ t('inboxDiscovery.error.' + error) }}
      </p>
      <div class="flex flex-wrap gap-2">
        <Button type="submit" size="sm" :disabled="disabled">{{
          t('inboxDiscovery.applyDates')
        }}</Button
        ><Button
          type="button"
          variant="ghost"
          size="sm"
          :disabled="disabled"
          @click="cancel"
          >{{ t('common.cancel') }}</Button
        >
      </div>
    </form>
    <Button
      v-if="
        modelValue.opened_from ||
        modelValue.opened_before ||
        modelValue.activity_from ||
        modelValue.activity_before
      "
      type="button"
      variant="ghost"
      size="sm"
      class="mt-2"
      :disabled="disabled"
      @click="reset"
      >{{ t('inboxDiscovery.clearDates') }}</Button
    >
  </section>
</template>
