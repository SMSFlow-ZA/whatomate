<script setup lang="ts">
import { computed } from 'vue'
import { Radio, RefreshCw, Clock3, ShieldAlert } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import type { InboxLiveState } from '@/services/inboxLive'
const props = defineProps<{ state: InboxLiveState; pending?: boolean }>()
const { t } = useI18n()
const label = computed(() =>
  t('inboxLive.' + (props.pending ? 'pending' : props.state)),
)
const icon = computed(() =>
  props.pending
    ? Clock3
    : props.state === 'live'
      ? Radio
      : props.state === 'denied'
        ? ShieldAlert
        : RefreshCw,
)
</script>
<template>
  <span
    class="inbox-caption inline-flex max-w-full items-center gap-1.5"
    role="status"
    aria-live="polite"
    aria-atomic="true"
  >
    <component :is="icon" class="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
    <span>{{ label }}</span>
    <span class="sr-only">{{ t('inboxLive.help') }}</span>
  </span>
</template>
