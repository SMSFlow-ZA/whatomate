<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { InboxSLA, InboxSLATarget } from '@/services/inbox'

const props = defineProps<{ sla: InboxSLA; compact?: boolean }>()
const { t, locale } = useI18n()
const now = ref(Date.now())
let timer: ReturnType<typeof setInterval> | undefined
onMounted(() => {
  timer = setInterval(() => {
    now.value = Date.now()
  }, 30000)
})
onUnmounted(() => clearInterval(timer))
function dueLabel(deadline: Date | null, timezone?: string) {
  if (!deadline) return ''
  try {
    return (
      deadline.toLocaleString(locale.value, {
        timeZone: timezone || 'UTC',
        dateStyle: 'full',
        timeStyle: 'short',
      }) +
      ' · ' +
      (timezone || 'UTC')
    )
  } catch {
    return (
      deadline.toLocaleString(locale.value, {
        timeZone: 'UTC',
        dateStyle: 'full',
        timeStyle: 'short',
      }) + ' · UTC'
    )
  }
}
const allTargets = computed(() =>
  Object.entries(props.sla)
    .filter(
      (entry): entry is [string, InboxSLATarget] =>
        ['first_reply', 'next_reply', 'resolution'].includes(entry[0]) &&
        !!entry[1],
    )
    .map(([kind, target]) => {
      const deadline = target.deadline ? new Date(target.deadline) : null
      const minutes = Math.max(0, Math.ceil(target.remaining_seconds / 60))
      return {
        kind,
        status:
          deadline &&
          deadline.getTime() <= now.value &&
          ['on_track', 'at_risk'].includes(target.status)
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
        title: dueLabel(deadline, target.timezone),
      }
    }),
)

const targets = computed(() =>
  props.compact
    ? [...allTargets.value]
        .sort((a, b) => {
          const rank: Record<string, number> = {
            breached: 0,
            at_risk: 1,
            on_track: 2,
            paused: 3,
            unanswered: 4,
            met: 5,
          }
          return (
            (rank[a.status] ?? 6) - (rank[b.status] ?? 6) ||
            (Date.parse(
              props.sla[a.kind as 'first_reply' | 'next_reply' | 'resolution']
                ?.deadline ?? '',
            ) || Infinity) -
              (Date.parse(
                props.sla[b.kind as 'first_reply' | 'next_reply' | 'resolution']
                  ?.deadline ?? '',
              ) || Infinity)
          )
        })
        .slice(0, 1)
    : allTargets.value,
)
</script>

<template>
  <div>
    <p v-if="sla.legacy" class="inbox-caption">
      {{ t('inboxQuality.legacy') }}
    </p>
    <ul class="flex flex-wrap gap-1" :aria-label="t('inboxSla.title')">
      <li
        v-for="target in targets"
        :key="target.kind"
        class="rounded px-1.5 py-0.5 text-xs"
        :class="
          target.status === 'breached'
            ? 'bg-red-500/10 text-red-300 light:text-red-700'
            : target.status === 'at_risk'
              ? 'bg-amber-500/10 text-amber-300 light:text-amber-800'
              : 'bg-muted text-muted-foreground'
        "
      >
        <details>
          <summary
            class="min-h-8 cursor-pointer py-1 focus-visible:outline-2 focus-visible:outline-ring"
          >
            {{ t('inboxSla.target.' + target.kind) }} ·
            {{ t('inboxSla.state.' + target.status) }}
          </summary>
          <p v-if="target.countdown" class="py-1">
            {{ t('inboxQuality.workingTime') }}: {{ target.countdown }}
          </p>
          <p
            v-if="
              sla[target.kind as 'first_reply' | 'next_reply' | 'resolution']
                ?.deadline
            "
            class="max-w-64 py-1"
          >
            {{ target.title }}
          </p>
        </details>
      </li>
    </ul>
  </div>
</template>
