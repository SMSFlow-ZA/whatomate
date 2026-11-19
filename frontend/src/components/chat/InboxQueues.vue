<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { accountsService, tagsService, usersService } from '@/services/api'
import {
  inboxService,
  type BulkInboxAction, type BulkInboxResult, type CasePriority, type CaseStatus,
  type InboxFilters, type InboxQueue, type QueueCase, type SavedInboxView
} from '@/services/inbox'
import { useAuthStore } from '@/stores/auth'
import { useTeamsStore } from '@/stores/teams'
import { toast } from '@/lib/toast'

const props = defineProps<{ active: boolean }>()
const teamsStore = useTeamsStore()
const lookupFailed = ref(false)
let lookupsLoaded = false
const emit = defineEmits<{ open: [row: QueueCase]; changed: [] }>()
const { t } = useI18n()
const auth = useAuthStore()
const filters = ref<InboxFilters>({ tags: [] })
const rows = ref<QueueCase[]>([])
const views = ref<SavedInboxView[]>([])
const accounts = ref<{ id: string; name: string }[]>([])
const teams = ref<{ id: string; name: string }[]>([])
const agents = ref<{ id: string; full_name: string }[]>([])
const tags = ref<{ name: string }[]>([])
const selected = ref<string[]>([])
const results = ref<Record<string, BulkInboxResult>>({})
const resultContacts = ref<Record<string, string>>({})
const loading = ref(false)
const failed = ref(false)
const offset = ref(0)
const hasMore = ref(false)
const saving = ref(false)
const viewName = ref('')
const viewTeam = ref('')
const activeView = ref('')
const action = ref<BulkInboxAction>('resolve')
const actionValue = ref('')
const overrideReason = ref('')
const reviewing = ref(false)
const queues: InboxQueue[] = ['', 'mine', 'unassigned', 'awaiting_reply', 'follow_ups']
const statuses: CaseStatus[] = ['open', 'pending', 'waiting_customer', 'snoozed', 'resolved']
const priorities: CasePriority[] = ['low', 'normal', 'high', 'urgent']
const canEdit = computed(() => auth.hasPermission('chat', 'write'))
const canAssignOrTag = computed(() => auth.hasPermission('contacts', 'write'))
const selectedCases = computed(() => rows.value.filter(row => selected.value.includes(row.id)))
const actionReady = computed(() => selectedCases.value.length > 0 && (action.value === 'resolve' || actionValue.value !== ''))
let revision = 0

function selectedFilters() {
  return Object.fromEntries(Object.entries(filters.value).filter(([, value]) => value)) as InboxFilters
}

async function refresh() {
  if (!props.active || document.visibilityState !== 'visible') return
  const id = ++revision
  loading.value = true
  failed.value = false
  try {
    const { data } = await inboxService.queue(selectedFilters(), offset.value)
    if (id !== revision) return
    rows.value = data.data.cases
    hasMore.value = data.data.has_more
    selected.value = selected.value.filter(selectedId => rows.value.some(row => row.id === selectedId))
  } catch {
    if (id === revision) failed.value = true
  } finally {
    if (id === revision) loading.value = false
  }
}

async function refreshViews() {
  try { views.value = (await inboxService.views()).data.data.views }
  catch { toast.error(t('inboxQueue.loadViewsError')) }
}

watch(filters, () => {
  offset.value = 0
  selected.value = []
  results.value = {}
  reviewing.value = false
  void refresh()
}, { deep: true })

async function loadLookups() {
  lookupFailed.value = false
  const results = await Promise.allSettled([
    accountsService.list().then(res => { accounts.value = res.data.data?.accounts ?? [] }),
    teamsStore.fetchTeams({ limit: 100 }).then(result => { teams.value = result.teams }),
    usersService.list({ limit: 100 }).then(res => { agents.value = res.data.data?.users ?? [] }),
    tagsService.list({ limit: 100 }).then(res => { tags.value = (res.data as any).data?.tags ?? [] }),
  ])
  lookupsLoaded = results.every(result => result.status === 'fulfilled')
  lookupFailed.value = !lookupsLoaded
}
function activate() {
  if (!props.active || document.visibilityState !== 'visible') return
  void refresh()
  void refreshViews()
  if (!lookupsLoaded) void loadLookups()
}
onMounted(() => { activate(); document.addEventListener('visibilitychange', activate) })
watch(() => props.active, active => {
  if (active) activate()
  else { revision++; loading.value = false }
})
watch(selected, () => { reviewing.value = false })
const refreshTimer = setInterval(() => { if (!loading.value && !reviewing.value && !saving.value) void refresh() }, 20_000)
onUnmounted(() => { revision++; clearInterval(refreshTimer); document.removeEventListener('visibilitychange', activate) })

function chooseView() {
  const view = views.value.find(view => view.id === activeView.value)
  filters.value = view ? { tags: [], ...view.filters } : { tags: [] }
}

function pageBy(delta: number) {
  offset.value = Math.max(0, offset.value + delta)
  selected.value = []
  results.value = {}
  void refresh()
}

async function saveView() {
  if (!viewName.value.trim() || saving.value) return
  saving.value = true
  try {
    const { data } = await inboxService.saveView(viewName.value.trim(), selectedFilters(), viewTeam.value || undefined)
    viewName.value = ''
    viewTeam.value = ''
    activeView.value = data.data.view.id
    await refreshViews()
    chooseView()
  } catch { toast.error(t('inboxQueue.saveError')) }
  finally { saving.value = false }
}

async function removeView() {
  try {
    await inboxService.deleteView(activeView.value)
    await refreshViews()
    activeView.value = ''
  } catch { toast.error(t('inboxQueue.saveError')) }
}

async function applyBulk() {
  if (!actionReady.value || saving.value) return
  saving.value = true
  try {
    const selection = selectedCases.value.map(row => ({ id: row.id, version: row.version }))
    const { data } = await inboxService.bulk(action.value, selection, actionValue.value, action.value === 'assign' ? overrideReason.value.trim() || undefined : undefined)
    results.value = Object.fromEntries(data.data.results.map(item => [item.id, item.result]))
    resultContacts.value = Object.fromEntries(selectedCases.value.map(row => [row.id, row.contact_name || t('inboxQueue.unnamed')]))
    reviewing.value = false
    overrideReason.value = ''
    selected.value = []
    emit('changed')
    await refresh()
  } catch { toast.error(t('inboxQueue.bulkError')) }
  finally { saving.value = false }
}
</script>

<template>
  <div v-show="active" class="vstack gap-2 min-h-0">
    <select v-model="filters.queue" class="small" :aria-label="t('inboxQueue.title')">
      <option v-for="queue in queues" :key="queue" :value="queue">{{ t('inboxQueue.queue.' + (queue || 'all')) }}</option>
    </select>
    <p v-if="lookupFailed" role="alert">
      {{ t('inboxQueue.loadFiltersError') }}
      <button type="button" @click="loadLookups">{{ t('common.retry') }}</button>
    </p>
    <details>
      <summary>{{ t('inboxQueue.filters') }}</summary>
      <div class="vstack gap-2 mt-2">
        <select v-model="filters.account_id" :aria-label="t('inboxQueue.account')">
          <option :value="undefined">{{ t('inboxQueue.allAccounts') }}</option>
          <option v-for="item in accounts" :key="item.id" :value="item.id">{{ item.name }}</option>
        </select>
        <select v-model="filters.team_id" :aria-label="t('inboxQueue.team')">
          <option :value="undefined">{{ t('inboxQueue.allTeams') }}</option>
          <option v-for="item in teams" :key="item.id" :value="item.id">{{ item.name }}</option>
        </select>
        <select v-model="filters.assignee_id" :aria-label="t('inboxQueue.assignee')">
          <option :value="undefined">{{ t('inboxQueue.allAssignees') }}</option>
          <option v-for="item in agents" :key="item.id" :value="item.id">{{ item.full_name }}</option>
        </select>
        <select v-model="filters.status" :aria-label="t('inboxWorkflow.caseStatus')">
          <option :value="undefined">{{ t('inboxQueue.allStatuses') }}</option>
          <option v-for="status in statuses" :key="status" :value="status">{{ t('inboxWorkflow.status.' + status) }}</option>
        </select>
        <select v-model="filters.priority" :aria-label="t('inboxWorkflow.casePriority')">
          <option :value="undefined">{{ t('inboxQueue.allPriorities') }}</option>
          <option v-for="priority in priorities" :key="priority" :value="priority">{{ t('inboxWorkflow.priority.' + priority) }}</option>
        </select>
        <fieldset v-if="tags.length">
          <legend>{{ t('contacts.tags') }}</legend>
          <label v-for="tag in tags" :key="tag.name">
            <input v-model="filters.tags" type="checkbox" :value="tag.name" /> {{ tag.name }}
          </label>
        </fieldset>
      </div>
    </details>
    <details>
      <summary>{{ t('inboxQueue.savedViews') }}</summary>
      <div class="vstack gap-2 mt-2">
        <select v-model="activeView" :aria-label="t('inboxQueue.savedViews')" @change="chooseView">
          <option value="">{{ t('inboxQueue.chooseView') }}</option>
          <option v-for="view in views" :key="view.id" :value="view.id">{{ view.name }}</option>
        </select>
        <button v-if="activeView" type="button" class="small outline" @click="removeView">{{ t('inboxQueue.deleteView') }}</button>
        <form class="vstack gap-2" @submit.prevent="saveView">
          <input v-model="viewName" :aria-label="t('inboxQueue.viewName')" :placeholder="t('inboxQueue.viewName')" maxlength="100" required />
          <select v-if="teams.length" v-model="viewTeam" :aria-label="t('inboxQueue.visibility')">
            <option value="">{{ t('inboxQueue.personal') }}</option>
            <option v-for="team in teams" :key="team.id" :value="team.id">{{ team.name }}</option>
          </select>
          <button type="submit" class="small outline" :disabled="saving">{{ t('inboxQueue.saveView') }}</button>
        </form>
      </div>
    </details>
    <div v-if="canEdit && selectedCases.length" class="vstack gap-2">
      <select v-model="action" :aria-label="t('inboxQueue.bulkAction')" @change="actionValue = ''; reviewing = false">
        <option value="resolve">{{ t('inboxQueue.resolve') }}</option>
        <option v-if="canAssignOrTag" value="assign">{{ t('inboxQueue.assign') }}</option>
        <option v-if="canAssignOrTag" value="tag">{{ t('inboxQueue.addTag') }}</option>
      </select>
      <select v-if="action === 'assign'" v-model="actionValue" :aria-label="t('inboxQueue.assignee')">
        <option value="">{{ t('inboxQueue.chooseAgent') }}</option>
        <option v-for="agent in agents" :key="agent.id" :value="agent.id">{{ agent.full_name }}</option>
      </select>
      <select v-if="action === 'tag'" v-model="actionValue" :aria-label="t('contacts.tags')">
        <option value="">{{ t('inboxQueue.chooseTag') }}</option>
        <option v-for="tag in tags" :key="tag.name" :value="tag.name">{{ tag.name }}</option>
      </select>
      <button v-if="!reviewing" type="button" class="small outline" :disabled="!actionReady" @click="reviewing = true">{{ t('inboxQueue.review') }}</button>
      <template v-else>
        <p>{{ t('inboxQueue.confirmCount', { count: selectedCases.length }) }}</p>
        <label v-if="action === 'assign' && auth.hasPermission('teams', 'write')">{{ t('inboxCapacity.override') }}<input v-model="overrideReason" maxlength="500" class="block w-full" /></label>
        <button type="button" class="small" :disabled="saving || !actionReady" @click="applyBulk">{{ t('inboxQueue.apply') }}</button>
        <button type="button" class="small ghost" @click="reviewing = false">{{ t('common.cancel') }}</button>
      </template>
    </div>
    <details v-if="Object.keys(results).length">
      <summary role="status">{{ t('inboxQueue.bulkSummary', { updated: Object.values(results).filter(v => v === 'updated').length, total: Object.keys(results).length }) }}</summary>
      <ul>
        <li v-for="(result, id) in results" :key="id">{{ resultContacts[id] }} · {{ t('inboxQueue.result.' + result) }}</li>
      </ul>
    </details>
    <p v-if="failed" role="alert">{{ t('inboxQueue.loadError') }} <button type="button" class="small outline" @click="refresh">{{ t('inboxQueue.retry') }}</button></p>
    <p v-else-if="loading" role="status">{{ t('common.loading') }}</p>
    <p v-else-if="!rows.length" class="text-light">{{ t('inboxQueue.empty') }}</p>
    <ul v-else class="vstack gap-2 scroll-y">
      <li v-for="row in rows" :key="row.id" class="flex gap-2 items-start">
        <input
          v-if="canEdit && row.status !== 'resolved'"
          v-model="selected" type="checkbox" :value="row.id"
          :aria-label="t('inboxQueue.selectCase', { name: row.contact_name || t('inboxQueue.unnamed') })"
        />
        <button type="button" class="ghost flex-1 min-w-0 text-start" @click="emit('open', row)">
          <span class="vstack min-w-0">
            <strong class="truncate">{{ row.contact_name || t('inboxQueue.unnamed') }}</strong>
            <small class="text-light truncate">{{ row.account_name }}</small>
            <small>{{ t('inboxWorkflow.status.' + row.status) }} · {{ t('inboxWorkflow.priority.' + row.priority) }}</small>
            <small v-if="!row.assignee_id && row.routing_reason">{{ t('inboxCapacity.reason.' + row.routing_reason) }}</small>
            <small v-if="results[row.id]">{{ t('inboxQueue.result.' + results[row.id]) }}</small>
          </span>
        </button>
      </li>
    </ul>
    <div v-if="offset || hasMore" class="hstack gap-2">
      <button type="button" class="small outline" :disabled="!offset || loading" @click="pageBy(-50)">{{ t('inboxQueue.previous') }}</button>
      <button type="button" class="small outline" :disabled="!hasMore || loading" @click="pageBy(50)">{{ t('inboxQueue.next') }}</button>
    </div>
  </div>
</template>
