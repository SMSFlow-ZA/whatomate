<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import { inboxService, type InboxSLAHistoryRow } from '@/services/inbox'
const props = defineProps<{ account?: string; team?: string }>()
const { t } = useI18n()
const today = new Date(),
  prior = new Date()
prior.setDate(prior.getDate() - 30)
const localDate = (date: Date) =>
  [
    date.getFullYear(),
    String(date.getMonth() + 1).padStart(2, '0'),
    String(date.getDate()).padStart(2, '0'),
  ].join('-')
const from = ref(localDate(prior)),
  to = ref(localDate(today)),
  agent = ref(''),
  policy = ref(''),
  kind = ref(''),
  offset = ref(0),
  more = ref(false),
  rows = ref<InboxSLAHistoryRow[]>([]),
  busy = ref(false),
  failed = ref(false),
  exporting = ref(false)
const summary = ref({
  total: 0,
  met: 0,
  breached: 0,
  unanswered: 0,
  legacy: 0,
})
const agents = ref<{ id: string; name: string }[]>([]),
  versions = ref<{ id: string; created_at: string; legacy: boolean }[]>([])
const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone
const applied = ref<Record<string, string | number | undefined>>(),
  exportError = ref(false)
let generation = 0
function filters() {
  const end = new Date(to.value + 'T00:00:00')
  end.setDate(end.getDate() + 1)
  return {
    from: new Date(from.value + 'T00:00:00').toISOString(),
    to: end.toISOString(),
    account_id: props.account || undefined,
    team_id: props.team || undefined,
    agent_id: agent.value || undefined,
    policy_id: policy.value || undefined,
    kind: kind.value || undefined,
    offset: offset.value,
  }
}
function applyFilters() {
  offset.value = 0
  try {
    applied.value = filters()
    void load()
  } catch {
    failed.value = true
  }
}
async function load() {
  if (!applied.value) return
  const request = ++generation
  busy.value = true
  try {
    const { data } = await inboxService.slaHistory({
      ...applied.value,
      offset: offset.value,
    })
    if (request !== generation) return
    rows.value = data.data.results
    summary.value = data.data.summary
    more.value = data.data.has_more
    agents.value = data.data.filters?.agents ?? []
    versions.value = data.data.filters?.policy_versions ?? []
    failed.value = false
  } catch {
    if (request === generation) failed.value = true
  } finally {
    if (request === generation) busy.value = false
  }
}
async function download() {
  if (!applied.value) return
  exporting.value = true
  exportError.value = false
  try {
    const { data } = await inboxService.exportSlaHistory(applied.value)
    const url = URL.createObjectURL(data)
    const link = document.createElement('a')
    link.href = url
    link.download = 'inbox-response-targets.csv'
    link.click()
    setTimeout(() => URL.revokeObjectURL(url), 1000)
  } catch {
    exportError.value = true
  } finally {
    exporting.value = false
  }
}
watch(
  () => [props.account, props.team],
  () => {
    offset.value = 0
    agent.value = ''
    policy.value = ''
    applyFilters()
  },
)
onMounted(applyFilters)
onUnmounted(() => generation++)
function pageBy(delta: number) {
  offset.value = Math.max(0, offset.value + delta)
  void load()
}
</script>
<template>
  <section class="inbox-panel my-5">
    <div class="inbox-subsection">
      <h3 class="inbox-section-heading">
        {{ t('inboxQuality.historicalTargets') }}
      </h3>
      <p class="inbox-caption mt-2">{{ t('inboxQuality.historicalHelp') }}</p>
      <form
        class="mt-4 grid gap-3 sm:grid-cols-2 lg:grid-cols-3"
        @submit.prevent="applyFilters"
      >
        <label class="inbox-label"
          >{{ t('inboxSla.from') }} ({{ timezone }})<input
            v-model="from"
            type="date"
            required /></label
        ><label class="inbox-label"
          >{{ t('inboxSla.to') }} ({{ timezone }})<input
            v-model="to"
            type="date"
            required /></label
        ><label class="inbox-label"
          >{{ t('inboxQueue.assignee')
          }}<select v-model="agent">
            <option value="">{{ t('inboxSla.any') }}</option>
            <option
              v-for="option in agents"
              :key="option.id"
              :value="option.id"
            >
              {{ option.name }}
            </option>
          </select></label
        ><label class="inbox-label"
          >{{ t('inboxQuality.policyVersion')
          }}<select v-model="policy">
            <option value="">{{ t('inboxSla.any') }}</option>
            <option
              v-for="option in versions"
              :key="option.id"
              :value="option.id"
            >
              {{ new Date(option.created_at).toLocaleDateString() }} ·
              {{ option.id.slice(0, 8)
              }}{{ option.legacy ? ' · ' + t('inboxQuality.legacy') : '' }}
            </option>
          </select></label
        ><label class="inbox-label"
          >{{ t('inboxQuality.target')
          }}<select v-model="kind">
            <option value="">{{ t('inboxSla.any') }}</option>
            <option
              v-for="target in ['first_reply', 'next_reply', 'resolution']"
              :key="target"
              :value="target"
            >
              {{ t('inboxSla.target.' + target) }}
            </option>
          </select></label
        >
        <div class="flex items-end gap-2">
          <Button :disabled="busy">{{ t('inboxQuality.applyFilters') }}</Button
          ><Button
            type="button"
            variant="outline"
            :disabled="exporting || busy"
            @click="download"
            >{{ t('inboxQuality.exportCsv') }}</Button
          >
        </div>
      </form>
      <p v-if="exportError" role="alert" class="inbox-notice mt-3">
        {{ t('inboxQuality.exportError') }}
      </p>
      <p class="inbox-caption mt-4" role="status">
        {{ t('inboxQuality.historySummary', summary) }}
      </p>
      <p v-if="summary.legacy" class="inbox-caption mt-2">
        {{ t('inboxQuality.legacyHelp') }}
      </p>
    </div>
    <p v-if="busy" role="status" class="p-4">{{ t('common.loading') }}</p>
    <div v-else-if="failed" role="alert" class="inbox-notice">
      {{ t('inboxSla.loadError') }}
      <Button variant="outline" size="sm" @click="load">{{
        t('common.retry')
      }}</Button>
    </div>
    <p v-else-if="!rows.length" class="inbox-empty">
      {{ t('inboxQuality.historyEmpty') }}
    </p>
    <ul v-else class="inbox-list">
      <li v-for="row in rows" :key="row.id" class="inbox-list-row">
        <div class="inbox-list-copy">
          <RouterLink
            class="inbox-list-title underline"
            :to="{
              name: 'chat-conversation',
              params: { contactId: row.contact_id },
              query: {
                inbox_account: row.account_name,
                inbox_account_id: row.account_id,
                case_id: row.case_id,
              },
            }"
            >{{ row.contact_name || t('inboxQueue.unnamed') }}</RouterLink
          >
          <p class="inbox-caption mt-1">
            {{ row.account_name }} · {{ row.team_name }} · {{ row.agent_name }}
          </p>
          <p class="inbox-caption">
            {{ t('inboxSla.target.' + row.kind) }} ·
            {{ new Date(row.completed_at).toLocaleString() }} ·
            {{ row.budget_minutes }} {{ t('inboxSla.minutes') }}
          </p>
          <p class="inbox-caption">
            {{ t('inboxQuality.policyVersion') }} {{ row.policy_id.slice(0, 8)
            }}<span v-if="row.legacy"> · {{ t('inboxQuality.legacy') }}</span>
          </p>
        </div>
        <span class="inbox-badge">{{
          t('inboxSla.state.' + row.outcome)
        }}</span>
      </li>
    </ul>
    <div v-if="offset || more" class="inbox-subsection flex gap-2">
      <Button
        variant="outline"
        size="sm"
        :disabled="!offset || busy"
        @click="pageBy(-50)"
        >{{ t('inboxQueue.previous') }}</Button
      ><Button
        variant="outline"
        size="sm"
        :disabled="!more || busy"
        @click="pageBy(50)"
        >{{ t('inboxQueue.next') }}</Button
      >
    </div>
  </section>
</template>
