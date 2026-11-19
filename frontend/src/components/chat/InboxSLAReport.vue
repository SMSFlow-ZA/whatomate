<script setup lang="ts">
import { Clock, Settings } from '@/lib/icons'
import { useAuthStore } from '@/stores/auth'
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useTeamsStore } from '@/stores/teams'
import { accountsService } from '@/services/api'
import { inboxService, type QueueCase } from '@/services/inbox'
import InboxSLAIndicators from './InboxSLAIndicators.vue'

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
const lookupFailed = ref(false)
const all = ref(false)
let timer: ReturnType<typeof setInterval> | undefined
let sequence = 0
const visible = computed(() =>
  rows.value.filter(
    (row) =>
      all.value ||
      Object.values(row.sla ?? {}).some((target) =>
        ['at_risk', 'breached'].includes(target.status),
      ),
  ),
)
async function load() {
  if (document.visibilityState !== 'visible') return
  const request = ++sequence
  try {
    const { data } = await inboxService.slaReport(
      account.value,
      team.value,
      offset.value,
    )
    if (request !== sequence) return
    rows.value = data.data.cases ?? []
    hasMore.value = data.data.has_more
    failed.value = false
  } catch {
    if (request === sequence) failed.value = true
  }
}
watch([team, account], () => {
  offset.value = 0
  void load()
})
watch(offset, load)
onMounted(async () => {
  try {
    available.value = (await inboxService.settings()).data.data.enabled
  } catch {
    return
  }
  if (!available.value) return
  void accountsService
    .list()
    .then((reply) => {
      accounts.value = reply.data.data?.accounts ?? []
    })
    .catch(() => {
      lookupFailed.value = true
    })
  void teamsStore
    .fetchTeams()
    .then((result) => {
      teams.value = result.teams
    })
    .catch(() => {
      lookupFailed.value = true
    })
  await load()
  timer = setInterval(load, 30000)
})
onUnmounted(() => clearInterval(timer))
</script>

<template>
  <section v-if="available" class="inbox-ui" :aria-label="t('inboxSla.report')">
    <div class="inbox-toolbar">
      <div>
        <h2 class="inbox-section-heading">{{ t('inboxSla.report') }}</h2>
        <p class="inbox-caption mt-1">{{ t('inboxUX.slaDescription') }}</p>
      </div>
      <RouterLink
        v-if="
          auth.hasPermission('settings.general', 'write') &&
          auth.hasPermission('accounts', 'write')
        "
        to="/settings/inbox-response-targets"
        class="button outline small"
        ><Settings class="mr-2 h-4 w-4" aria-hidden="true" />{{
          t('inboxUX.manageTargets')
        }}</RouterLink
      >
    </div>
    <p v-if="lookupFailed" role="alert" class="inbox-notice text-destructive">
      {{ t('inboxQueue.loadFiltersError') }}
    </p>
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
      ><label class="inbox-check self-end pb-2"
        ><input v-model="all" type="checkbox" />{{
          t('inboxSla.allCases')
        }}</label
      >
    </div>

    <div v-if="failed" role="alert" class="inbox-notice">
      {{ t('inboxSla.loadError')
      }}<button @click="load" class="outline small" type="button">
        {{ t('common.retry') }}
      </button>
    </div>
    <div v-else class="inbox-panel">
      <div v-if="!visible.length" class="inbox-empty">
        <Clock class="h-8 w-8" aria-hidden="true" />
        <h3>{{ t('inboxSla.empty') }}</h3>
        <p>{{ t('inboxUX.slaEmptyHelp') }}</p>
      </div>
      <ul v-else class="inbox-list">
        <li v-for="row in visible" :key="row.id" class="inbox-list-row">
          <div class="inbox-list-copy">
            <RouterLink
              class="inbox-list-title underline-offset-4 inbox-link"
              :to="{
                name: 'chat-conversation',
                params: { contactId: row.contact_id },
                query: {
                  inbox_account: row.account_name,
                  inbox_account_id: row.account_id,
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
      <button
        :disabled="!offset"
        @click="offset -= 50"
        class="outline small"
        type="button"
      >
        {{ t('inboxQueue.previous') }}</button
      ><button
        :disabled="!hasMore"
        @click="offset += 50"
        class="outline small"
        type="button"
      >
        {{ t('inboxQueue.next') }}
      </button>
    </div>
  </section>
</template>
