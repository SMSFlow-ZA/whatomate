<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { InboxSLA } from '@/services/inbox'

const props = defineProps<{ sla: InboxSLA }>()
const { t, locale } = useI18n()
const now = ref(Date.now())
let timer: ReturnType<typeof setInterval> | undefined
onMounted(() => {
  timer = setInterval(() => {
    now.value = Date.now()
  }, 30000)
})
onUnmounted(() => clearInterval(timer))
const targets = computed(() =>
  Object.entries(props.sla).map(([kind, target]) => {
    const deadline = target.deadline ? new Date(target.deadline) : null
    const minutes = deadline
      ? Math.ceil((deadline.getTime() - now.value) / 60000)
      : 0
    return {
      kind,
      status:
        deadline &&
        minutes <= 0 &&
        ['on_track', 'at_risk', 'breached'].includes(target.status)
          ? 'breached'
          : target.status,
      countdown:
        minutes > 0
          ? new Intl.RelativeTimeFormat(locale.value, {
              numeric: 'always',
            }).format(
              minutes >= 120 ? Math.ceil(minutes / 60) : minutes,
              minutes >= 120 ? 'hour' : 'minute',
            )
          : '',
      title: deadline?.toLocaleString(locale.value),
    }
  }),
)
</script>

<template>
  <ul class="inbox-deadlines" :aria-label="t('inboxSla.title')">
    <li
      v-for="target in targets"
      :key="target.kind"
      :title="target.title"
      :data-state="target.status"
    >
      {{ t('inboxSla.target.' + target.kind) }} /
      {{ t('inboxSla.state.' + target.status) }}
      <span v-if="target.countdown"> / {{ target.countdown }}</span>
    </li>
  </ul>
</template>
