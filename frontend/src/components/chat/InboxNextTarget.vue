<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { InboxSLA, InboxSLATarget } from '@/services/inbox'
const props = defineProps<{ sla: InboxSLA }>()
const { t, locale } = useI18n()
const next = computed(
  () =>
    Object.entries(props.sla)
      .filter(
        (entry): entry is [string, InboxSLATarget] =>
          ['first_reply', 'next_reply', 'resolution'].includes(entry[0]) &&
          !!entry[1] &&
          ['on_track', 'at_risk', 'breached'].includes(
            (entry[1] as InboxSLATarget).status,
          ) &&
          !!(entry[1] as InboxSLATarget).deadline,
      )
      .sort(
        (a, b) => Date.parse(a[1].deadline!) - Date.parse(b[1].deadline!),
      )[0],
)
const due = computed(() => {
  if (!next.value) return ''
  const value = next.value[1]
  try {
    return (
      new Date(value.deadline!).toLocaleString(locale.value, {
        timeZone: value.timezone || 'UTC',
        month: 'short',
        day: 'numeric',
        hour: '2-digit',
        minute: '2-digit',
      }) +
      ' · ' +
      (value.timezone || 'UTC')
    )
  } catch {
    return new Date(value.deadline!).toISOString() + ' · UTC'
  }
})
</script>
<template>
  <p
    v-if="next"
    class="inbox-caption inbox-next-target mt-2 flex flex-wrap gap-x-1"
    :data-state="next[1].status"
  >
    <strong class="font-medium"
      >{{ t('inboxSla.target.' + next[0]) }} ·
      {{ t('inboxSla.state.' + next[1].status) }}</strong
    >
    <time :datetime="next[1].deadline">{{ due }}</time>
  </p>
</template>
