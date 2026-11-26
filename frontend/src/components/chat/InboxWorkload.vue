<script setup lang="ts">
import InboxAssignmentRate from './InboxAssignmentRate.vue'
import InboxLiveStatus from '@/components/shared/InboxLiveStatus.vue'
import { useInboxLive } from '@/composables/useInboxLive'
import { isInboxAccessFailure } from '@/lib/inboxAccess'
import { onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import { inboxService } from '@/services/inbox'
import { useAuthStore } from '@/stores/auth'
const props = defineProps<{ team?: string; account?: string }>()
const { t } = useI18n(),
  auth = useAuthStore(),
  rows = ref<
    Awaited<ReturnType<typeof inboxService.workload>>['data']['data']['agents']
  >([]),
  loading = ref(false),
  failed = ref(false),
  updated = ref(''),
  offset = ref(0),
  more = ref(false),
  uniqueAgents = ref(0)
let generation = 0,
  timer: ReturnType<typeof setInterval> | undefined
async function load() {
  if (document.visibilityState !== 'visible') return
  const request = ++generation
  loading.value = true
  try {
    const { data } = await inboxService.workload(
      props.team,
      props.account,
      offset.value,
    )
    if (request !== generation) return
    rows.value = data.data.agents ?? []
    more.value = data.data.has_more
    uniqueAgents.value = data.data.unique_agents ?? 0
    updated.value = new Date(data.data.updated_at).toLocaleTimeString()
    failed.value = false
  } catch (error) {
    if (request === generation) {
      failed.value = true
      if (isInboxAccessFailure(error)) {
        rows.value = []
        uniqueAgents.value = 0
        updated.value = ''
        more.value = false
      }
    }
  } finally {
    if (request === generation) loading.value = false
  }
}
const live = useInboxLive(
  () => ({
    kind: 'workload',
    team_id: props.team || undefined,
    account_id: props.account || undefined,
  }),
  {
    blocked: () => loading.value,
    refresh: load,
    clear: () => {
      generation++
      rows.value = []
      offset.value = 0
      uniqueAgents.value = 0
      updated.value = ''
      loading.value = false
    },
    denied: () => {
      generation++
      rows.value = []
      uniqueAgents.value = 0
      updated.value = ''
      loading.value = false
      failed.value = true
    },
  },
)
watch(
  () => [props.team, props.account],
  () => {
    generation++
    rows.value = []
    uniqueAgents.value = 0
    updated.value = ''
    failed.value = false
    if (offset.value !== 0) offset.value = 0
    else void load()
  },
)
watch(offset, load)
onMounted(() => {
  void load()
  timer = setInterval(load, 30000)
  document.addEventListener('visibilitychange', load)
})
onUnmounted(() => {
  generation++
  clearInterval(timer)
  document.removeEventListener('visibilitychange', load)
})
</script>
<template>
  <section class="inbox-panel my-5">
    <div
      class="inbox-subsection flex flex-wrap items-center justify-between gap-3"
    >
      <div>
        <h3 class="inbox-section-heading">
          {{ t('inboxQuality.liveWorkload') }}
        </h3>
        <InboxLiveStatus
          :state="live.state.value"
          :pending="live.pending.value"
          class="mt-1"
        />
        <p v-if="updated && !failed" class="inbox-caption mt-1">
          {{
            t(
              'inboxReview.workloadSummary',
              { count: uniqueAgents },
              uniqueAgents,
            )
          }}
        </p>
        <p class="inbox-caption mt-1">{{ t('inboxReview.workloadHelp') }}</p>
        <p v-if="updated" class="inbox-caption mt-1" role="status">
          {{ t('inboxQuality.lastUpdated', { time: updated }) }}
        </p>
      </div>
      <Button variant="outline" size="sm" :disabled="loading" @click="load">{{
        t('inboxQuality.refresh')
      }}</Button>
    </div>
    <div v-if="failed" class="inbox-notice" role="alert">
      {{ t('inboxCapacity.loadError')
      }}<Button size="sm" variant="outline" @click="load">{{
        t('common.retry')
      }}</Button>
    </div>
    <p v-if="loading && !rows.length" class="p-4" role="status">
      {{ t('common.loading') }}
    </p>
    <p v-else-if="!rows.length && !failed" class="inbox-empty">
      {{ t('inboxUX.capacityEmptyHelp') }}
    </p>
    <ul v-else class="inbox-list">
      <li
        v-for="row in rows"
        :key="[row.team_id, row.account_id, row.agent_id].join('/')"
        class="inbox-list-row"
      >
        <div class="inbox-list-copy">
          <p class="inbox-list-title">
            {{ row.name
            }}<span v-if="row.inactive" class="inbox-caption block">{{
              t('inboxQuality.inactiveAgent')
            }}</span>
          </p>
          <p class="inbox-caption">
            {{ row.team_name || t('inboxQuality.noTeam') }} ·
            {{ row.account_name }}
          </p>
          <p class="inbox-caption">
            {{
              t(
                row.available
                  ? 'inboxCapacity.available'
                  : 'inboxCapacity.unavailable',
              )
            }}
          </p>
          <InboxAssignmentRate :row="row" />
        </div>
        <span class="inbox-badge tabular-nums"
          >{{ row.active }} /
          {{ row.max_active || t('inboxCapacity.unlimited') }}</span
        ><RouterLink
          v-if="row.team_name && auth.hasPermission('teams', 'read')"
          :to="'/settings/teams/' + row.team_id"
          class="text-sm underline"
          >{{ t('inboxQuality.teamSettings') }}</RouterLink
        >
      </li>
    </ul>
    <div v-if="offset || more" class="inbox-subsection flex gap-2">
      <Button
        variant="outline"
        size="sm"
        :disabled="!offset || loading"
        @click="offset -= 100"
        >{{ t('inboxQueue.previous') }}</Button
      ><Button
        variant="outline"
        size="sm"
        :disabled="!more || loading"
        @click="offset += 100"
        >{{ t('inboxQueue.next') }}</Button
      >
    </div>
  </section>
</template>
