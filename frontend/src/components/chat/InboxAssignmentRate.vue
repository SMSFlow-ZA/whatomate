<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { InboxCapacity } from '@/services/inbox'
const props = defineProps<{ row: InboxCapacity }>()
const { t } = useI18n()
const visible = computed(
  () =>
    !!props.row.max_assignments ||
    [
      'capacity',
      'permission_required',
      'invalid_config',
      'rate_limited',
    ].includes(props.row.assignment_reason ?? ''),
)
const window = computed(() => {
  const seconds = props.row.window_seconds ?? 0
  return seconds % 60 === 0
    ? t('inboxRouting.minutes', { count: seconds / 60 })
    : t('inboxRouting.seconds', { count: seconds })
})
const resumes = computed(() => {
  const value = props.row.rate_resumes_at
  if (
    !value ||
    props.row.rate_details_hidden ||
    !Number.isFinite(Date.parse(value))
  )
    return ''
  return new Intl.DateTimeFormat(undefined, {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    timeZoneName: 'short',
  }).format(new Date(value))
})
</script>
<template>
  <span v-if="visible" class="inbox-caption mt-1 block">
    <span v-if="row.max_assignments" class="block">{{
      row.rate_details_hidden
        ? t('inboxRouting.rateSummary', { count: row.max_assignments, window })
        : t('inboxRouting.rateUsage', {
            used: row.assignments ?? 0,
            count: row.max_assignments,
            window,
          })
    }}</span>
    <span v-if="row.max_assignments && row.rate_details_hidden" class="block">{{
      t('inboxRouting.hiddenActivity')
    }}</span>
    <span
      v-if="row.assignment_reason === 'permission_required'"
      class="block"
      >{{ t('inboxRouting.agentPermission') }}</span
    >
    <span
      v-else-if="row.assignment_reason === 'invalid_config'"
      class="block"
      >{{ t('inboxRouting.automaticSetup') }}</span
    >
    <span v-else-if="row.assignment_reason === 'rate_limited'" class="block">{{
      t('inboxRouting.atRateLimit')
    }}</span>
    <span v-if="resumes" class="block">{{
      t('inboxRouting.rateSlot', { time: resumes })
    }}</span>
  </span>
</template>
