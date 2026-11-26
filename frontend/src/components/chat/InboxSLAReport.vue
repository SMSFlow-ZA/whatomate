<script setup lang="ts">
import '@/assets/inbox.css'
import InboxLiveStatus from '@/components/shared/InboxLiveStatus.vue'
import { useInboxLive } from '@/composables/useInboxLive'
import { isInboxAccessFailure } from '@/lib/inboxAccess'
import { Clock3, Settings2 } from 'lucide-vue-next'
import { useAuthStore } from '@/stores/auth'
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useTeamsStore } from '@/stores/teams'
import { Button } from '@/components/ui/button'
import { accountsService } from '@/services/api'
import { inboxService, type QueueCase } from '@/services/inbox'
import InboxTeamCapacity from '@/components/settings/InboxTeamCapacity.vue'
import InboxSLAIndicators from './InboxSLAIndicators.vue'
import InboxTabs from '@/components/shared/InboxTabs.vue'
import InboxWorkload from './InboxWorkload.vue'
import InboxSLAHistory from './InboxSLAHistory.vue'

const { t } = useI18n()
const auth = useAuthStore()
const teamsStore = useTeamsStore()
const rows = ref<QueueCase[]>([])
const account = ref('')
const team = ref('')
const accounts = ref<{ id: string; name: string }[]>([])
const teams = ref<{ id: string; name: string }[]>([])
const offset = ref(0)
const hasMore = ref(false)
const available = ref(false)
const failed = ref(false)
const loading = ref(false)
const settingsFailed = ref(false)
const lastUpdated = ref('')
const lookupFailed = ref(false)
const all = ref(false)
const section = ref('live')
let timer: ReturnType<typeof setInterval> | undefined
let sequence = 0
const visible = computed(() => rows.value)
const summary = ref({ total: 0, breached: 0, at_risk: 0 })
async function load() {
  if (document.visibilityState !== 'visible') return
  if (section.value !== 'live') return
  const request = ++sequence
  loading.value = true
  try {
    const { data } = await inboxService.slaReport(
      account.value,
      team.value,
      offset.value,
      all.value,
    )
    if (request !== sequence) return
    summary.value = data.data.summary ?? { total: 0, breached: 0, at_risk: 0 }
    rows.value = data.data.cases ?? []
    hasMore.value = data.data.has_more
    failed.value = false
    lastUpdated.value = new Date(
      data.data.updated_at || Date.now(),
    ).toLocaleTimeString()
  } catch (error) {
    if (request === sequence) {
      failed.value = true
      if (isInboxAccessFailure(error)) {
        rows.value = []
        summary.value = { total: 0, breached: 0, at_risk: 0 }
        hasMore.value = false
        lastUpdated.value = ''
      }
    }
  } finally {
    if (request === sequence) loading.value = false
  }
}
watch([team, account, all, section], () => {
  sequence++
  rows.value = []
  summary.value = { total: 0, breached: 0, at_risk: 0 }
  hasMore.value = false
  lastUpdated.value = ''
  loading.value = false
  if (offset.value !== 0) offset.value = 0
  else void load()
})
watch(offset, load)
async function loadLookups() {
  lookupFailed.value = false
  const results = await Promise.allSettled([
    accountsService.list().then((reply) => {
      accounts.value = reply.data.data?.accounts ?? []
    }),
    teamsStore.fetchTeams().then((result) => {
      teams.value = result.teams
    }),
  ])
  lookupFailed.value = results.some((result) => result.status === 'rejected')
}
function refreshVisible() {
  if (!loading.value) void load()
}
async function initialize() {
  settingsFailed.value = false
  try {
    available.value = (await inboxService.settings()).data.data.enabled
  } catch {
    settingsFailed.value = true
    return
  }
  if (!available.value) return
  void loadLookups()
  await load()
  if (!timer) timer = setInterval(refreshVisible, 30000)
}
onMounted(() => {
  void initialize()
  document.addEventListener('visibilitychange', refreshVisible)
})
onUnmounted(() => {
  sequence++
  clearInterval(timer)
  document.removeEventListener('visibilitychange', refreshVisible)
})
const live = useInboxLive(
  () => ({
    kind: 'supervisor',
    account_id: account.value || undefined,
    team_id: team.value || undefined,
  }),
  {
    active: () => available.value && section.value === 'live',
    context: () => JSON.stringify([account.value, team.value]),
    blocked: () => loading.value,
    refresh: load,
    clear: () => {
      sequence++
      rows.value = []
      offset.value = 0
      summary.value = { total: 0, breached: 0, at_risk: 0 }
      lastUpdated.value = ''
      hasMore.value = false
      loading.value = false
    },
    denied: () => {
      sequence++
      rows.value = []
      summary.value = { total: 0, breached: 0, at_risk: 0 }
      lastUpdated.value = ''
      hasMore.value = false
      loading.value = false
      failed.value = true
    },
  },
)
</script>

<template>
  <div v-if="settingsFailed" role="alert" class="inbox-ui inbox-notice">
    {{ t('inboxQuality.settingsLoadError')
    }}<Button size="sm" variant="outline" @click="initialize">{{
      t('common.retry')
    }}</Button>
  </div>
  <section v-if="available" class="inbox-ui" :aria-label="t('inboxSla.report')">
    <div class="inbox-toolbar">
      <div>
        <h2 class="inbox-section-heading">{{ t('inboxSla.report') }}</h2>
        <InboxLiveStatus
          :state="live.state.value"
          :pending="live.pending.value"
          class="mt-1"
        />
        <p class="inbox-caption mt-1">{{ t('inboxUX.slaDescription') }}</p>
        <p v-if="lastUpdated" class="inbox-caption mt-1" role="status">
          {{ t('inboxQuality.lastUpdated', { time: lastUpdated }) }}
        </p>
      </div>
      <RouterLink
        :to="{ name: 'inbox-notices' }"
        class="inline-flex min-h-[44px] items-center text-sm font-medium underline-offset-4 hover:underline"
        >{{ t('inboxNotice.title') }}</RouterLink
      >
      <RouterLink
        v-if="
          auth.hasPermission('settings.general', 'write') &&
          auth.hasPermission('accounts', 'write')
        "
        to="/settings/inbox-workflows?tab=sla"
        ><Button variant="outline" size="sm"
          ><Settings2 class="mr-2 h-4 w-4" aria-hidden="true" />{{
            t('inboxUX.manageTargets')
          }}</Button
        ></RouterLink
      >
    </div>
    <p v-if="lookupFailed" role="alert" class="inbox-notice text-destructive">
      {{ t('inboxQueue.loadFiltersError')
      }}<Button size="sm" variant="outline" @click="loadLookups">{{
        t('common.retry')
      }}</Button>
    </p>
    <InboxTabs
      v-model="section"
      :label="t('inboxSla.report')"
      :items="[
        { value: 'live', label: t('inboxQuality.liveTargets') },
        { value: 'workload', label: t('inboxQuality.liveWorkload') },
        { value: 'history', label: t('inboxQuality.historicalTargets') },
      ]"
    />
    <div
      v-if="section === 'live' && !loading"
      class="mb-4 flex flex-wrap gap-3"
    >
      <span class="inbox-badge">{{
        t(
          'inboxQuality.summaryBreached',
          { count: summary.breached },
          summary.breached,
        )
      }}</span
      ><span class="inbox-badge">{{
        t(
          'inboxQuality.summaryRisk',
          { count: summary.at_risk },
          summary.at_risk,
        )
      }}</span
      ><span class="inbox-badge">{{
        t('inboxQuality.summaryOpen', { count: summary.total }, summary.total)
      }}</span
      ><Button size="sm" variant="outline" @click="load">{{
        t('inboxQuality.refresh')
      }}</Button>
    </div>
    <div class="inbox-toolbar">
      <label class="inbox-label min-w-40"
        >{{ t('inboxQueue.account')
        }}<select v-model="account">
          <option value="">{{ t('inboxSla.any') }}</option>
          <option v-for="entry in accounts" :key="entry.id" :value="entry.id">
            {{ entry.name }}
          </option>
        </select></label
      ><label class="inbox-label min-w-40"
        >{{ t('inboxQueue.team')
        }}<select v-model="team">
          <option value="">{{ t('inboxSla.any') }}</option>
          <option v-for="entry in teams" :key="entry.id" :value="entry.id">
            {{ entry.name }}
          </option>
        </select></label
      ><label v-if="section === 'live'" class="inbox-check self-end pb-2"
        ><input v-model="all" type="checkbox" />{{
          t('inboxSla.allCases')
        }}</label
      >
    </div>
    <InboxSLAHistory
      v-if="section === 'history'"
      :account="account"
      :team="team"
    /><InboxWorkload
      v-else-if="section === 'workload'"
      :account="account"
      :team="team"
    /><template v-else>
      <InboxTeamCapacity
        v-if="team && account && auth.hasPermission('teams', 'read')"
        class="mb-5"
        :team="team"
        :account="account"
      />
      <p v-if="loading" role="status" class="inbox-notice">
        {{ t('inboxReview.updatingTargets') }}
      </p>
      <div v-else-if="failed" role="alert" class="inbox-notice">
        {{ t('inboxSla.loadError')
        }}<Button variant="outline" size="sm" @click="load">{{
          t('common.retry')
        }}</Button>
      </div>
      <div v-else class="inbox-panel">
        <div v-if="!visible.length" class="inbox-empty">
          <Clock3 class="h-8 w-8" aria-hidden="true" />
          <h3>{{ t('inboxSla.empty') }}</h3>
          <p>{{ t('inboxUX.slaEmptyHelp') }}</p>
        </div>
        <ul v-else class="inbox-list">
          <li v-for="row in visible" :key="row.id" class="inbox-list-row">
            <div class="inbox-list-copy">
              <RouterLink
                class="inbox-list-title inline-flex min-h-[44px] items-center underline-offset-4 hover:underline sm:min-h-0"
                :to="{
                  name: 'chat-conversation',
                  params: { contactId: row.contact_id },
                  query: {
                    inbox_account: row.account_name,
                    inbox_account_id: row.account_id,
                    case_id: row.id,
                  },
                }"
                >{{ row.contact_name || t('inboxQueue.unnamed') }}</RouterLink
              >
              <p class="inbox-caption mt-1">
                {{ row.account_name }} /
                {{ t('inboxWorkflow.status.' + row.status) }} /
                {{ t('inboxWorkflow.priority.' + row.priority) }}
              </p>
            </div>
            <InboxSLAIndicators v-if="row.sla" :sla="row.sla" />
          </li>
        </ul>
      </div>
      <div v-if="offset || hasMore" class="mt-4 flex gap-2">
        <Button
          :disabled="!offset || loading"
          variant="outline"
          size="sm"
          @click="offset -= 50"
          >{{ t('inboxQueue.previous') }}</Button
        ><Button
          :disabled="!hasMore || loading"
          variant="outline"
          size="sm"
          @click="offset += 50"
          >{{ t('inboxQueue.next') }}</Button
        >
      </div>
    </template>
  </section>
</template>
