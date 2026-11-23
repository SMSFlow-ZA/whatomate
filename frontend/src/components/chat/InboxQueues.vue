<script setup lang="ts">
import InboxAgentSelect from '@/components/shared/InboxAgentSelect.vue'
import InboxFilterSummary from './InboxFilterSummary.vue'
import { Button } from '@/components/ui/button'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import { Filter, Bookmark, Inbox, RotateCw } from 'lucide-vue-next'
import '@/assets/inbox.css'
import InboxFieldMatchers from './InboxFieldMatchers.vue'
import InboxConditionBuilder from './InboxConditionBuilder.vue'
import InboxSLAIndicators from './InboxSLAIndicators.vue'
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { accountsService, tagsService, usersService } from '@/services/api'
import {
  inboxService,
  type BulkInboxAction,
  type BulkInboxResult,
  type CasePriority,
  type CaseStatus,
  type InboxFilters,
  type InboxQueue,
  type QueueCase,
  type SavedInboxView,
} from '@/services/inbox'
import { useAuthStore } from '@/stores/auth'
import { useTeamsStore } from '@/stores/teams'
import { toast } from 'vue-sonner'

const props = defineProps<{
  contextKey?: string
  active: boolean
  selectedContact?: string
  selectedAccount?: string
  selectedCase?: string
}>()
const teamsStore = useTeamsStore()
const lookupFailed = ref(false)
let lookupsLoaded = false
const emit = defineEmits<{ open: [row: QueueCase]; changed: [] }>()
const { t } = useI18n()
const auth = useAuthStore()
const filters = ref<InboxFilters>({ tags: [], queue: 'mine' })
const rows = ref<QueueCase[]>([])
const views = ref<SavedInboxView[]>([])
const publishTeams = ref<{ id: string; name: string }[]>([])
const accounts = ref<{ id: string; name: string }[]>([])
const teams = ref<{ id: string; name: string }[]>([])
const agents = ref<{ id: string; full_name: string }[]>([])
const tags = ref<{ name: string }[]>([])
const selected = ref<string[]>([])
const results = ref<Record<string, BulkInboxResult>>({})
const resultContacts = ref<Record<string, string>>({})
const resultRows = ref<Record<string, QueueCase>>({})
const counts = ref<Record<string, number>>({})
const updatedAt = ref('')
const editingView = ref(false)
const currentView = computed(() =>
  views.value.find((view) => view.id === activeView.value),
)
const storageKey = computed(() =>
  props.contextKey ? 'inbox-navigation:' + props.contextKey : '',
)
function restoreNavigation() {
  try {
    const raw = storageKey.value ? localStorage.getItem(storageKey.value) : null
    const state = raw ? JSON.parse(raw) : null
    filters.value =
      state?.filters && typeof state.filters === 'object'
        ? state.filters
        : { tags: [], queue: 'mine' }
    activeView.value = typeof state?.view === 'string' ? state.view : ''
  } catch {
    filters.value = { tags: [], queue: 'mine' }
  }
}
const loading = ref(false)
const failed = ref(false)
const offset = ref(0)
const hasMore = ref(false)
const saving = ref(false)
const viewName = ref('')
const viewTeam = ref('')
const activeView = ref('')
restoreNavigation()
const action = ref<BulkInboxAction>('resolve')
const actionValue = ref('')
const overrideReason = ref('')
const reviewing = ref(false)
function resetBulkReview() {
  actionValue.value = ''
  reviewing.value = false
}
const queues: InboxQueue[] = [
  '',
  'mine',
  'unassigned',
  'awaiting_reply',
  'follow_ups',
]
const statuses: CaseStatus[] = [
  'open',
  'pending',
  'waiting_customer',
  'snoozed',
  'resolved',
]
const priorities: CasePriority[] = ['low', 'normal', 'high', 'urgent']
const canEdit = computed(() => auth.hasPermission('chat', 'write'))
const canAssignOrTag = computed(() => auth.hasPermission('contacts', 'write'))
const filterCount = computed(
  () =>
    [
      'account_id',
      'team_id',
      'assignee_id',
      'status',
      'priority',
      'search',
    ].filter((key) => !!filters.value[key as keyof InboxFilters]).length +
    (filters.value.tags?.length ?? 0) +
    (filters.value.fields?.length ?? 0) +
    (filters.value.predicates?.length ?? 0),
)
const selectable = computed(() =>
  rows.value.filter((row) => row.status !== 'resolved'),
)
const pageSelected = computed(
  () =>
    !!selectable.value.length &&
    selectable.value.every((row) => selected.value.includes(row.id)),
)
function selectPage() {
  selected.value = pageSelected.value
    ? []
    : selectable.value.map((row) => row.id)
}
function clearFilters() {
  activeView.value = ''
  filters.value = { tags: [], queue: filters.value.queue }
}
const clock = ref(Date.now())
const ageTimer = setInterval(() => {
  clock.value = Date.now()
}, 30000)
onUnmounted(() => clearInterval(ageTimer))
function waitingAge(row: QueueCase) {
  const since =
    row.next_reply_from ??
    (!row.first_human_reply_at
      ? row.last_inbound_at
        ? row.opened_at
        : undefined
      : undefined)
  if (!since || row.status === 'resolved') return ''
  const minutes = Math.max(
    0,
    Math.floor((clock.value - Date.parse(since)) / 60000),
  )
  return t('inboxReview.waitingAge', {
    age: new Intl.RelativeTimeFormat(undefined, { numeric: 'always' }).format(
      -Math.max(
        1,
        Math.floor(minutes / (minutes >= 1440 ? 1440 : minutes >= 60 ? 60 : 1)),
      ),
      minutes >= 1440 ? 'day' : minutes >= 60 ? 'hour' : 'minute',
    ),
  })
}
const selectedCases = computed(() =>
  rows.value.filter((row) => selected.value.includes(row.id)),
)
const actionReady = computed(
  () =>
    selectedCases.value.length > 0 &&
    (action.value === 'resolve' || actionValue.value !== ''),
)
let revision = 0
let searchTimer: ReturnType<typeof setTimeout> | undefined
let lastFilterShape = JSON.stringify({ ...filters.value, search: undefined })

function isCurrent(row: QueueCase) {
  return (
    (!props.selectedCase || row.id === props.selectedCase) &&
    row.contact_id === props.selectedContact &&
    (!props.selectedAccount || row.account_id === props.selectedAccount)
  )
}

function selectedFilters() {
  return Object.fromEntries(
    Object.entries(filters.value).filter(([, value]) => value),
  ) as InboxFilters
}

async function refresh() {
  if (!props.active || document.visibilityState !== 'visible') return
  const id = ++revision
  loading.value = true
  failed.value = false
  try {
    const { data } = await inboxService.queue(
      selectedFilters(),
      offset.value,
      true,
      activeView.value || undefined,
    )
    if (id !== revision) return
    counts.value = data.data.counts ?? {}
    updatedAt.value = new Date().toLocaleTimeString()
    rows.value = data.data.cases
    hasMore.value = data.data.has_more
    selected.value = selected.value.filter((selectedId) =>
      rows.value.some((row) => row.id === selectedId),
    )
  } catch {
    if (id === revision) failed.value = true
  } finally {
    if (id === revision) loading.value = false
  }
}

async function refreshViews() {
  try {
    const response = (await inboxService.views()).data.data
    views.value = response.views
    publishTeams.value = response.publish_teams ?? []
    if (
      activeView.value &&
      !views.value.some((view) => view.id === activeView.value)
    ) {
      activeView.value = ''
      filters.value = { tags: [], queue: 'mine' }
    }
  } catch {
    toast.error(t('inboxQueue.loadViewsError'))
  }
}

watch(
  filters,
  () => {
    if (storageKey.value)
      try {
        localStorage.setItem(
          storageKey.value,
          JSON.stringify({ filters: filters.value, view: activeView.value }),
        )
      } catch {
        /* Storage can be disabled by the browser. */
      }
    offset.value = 0
    selected.value = []
    results.value = {}
    reviewing.value = false
    revision++
    clearTimeout(searchTimer)
    const shape = JSON.stringify({ ...filters.value, search: undefined })
    const searching = shape === lastFilterShape && !!filters.value.search
    lastFilterShape = shape
    rows.value = []
    counts.value = {}
    updatedAt.value = ''
    if (searching) {
      loading.value = true
      searchTimer = setTimeout(() => void refresh(), 250)
    } else void refresh()
  },
  { deep: true },
)

async function loadLookups() {
  lookupFailed.value = false
  const results = await Promise.allSettled([
    accountsService.list().then((res) => {
      accounts.value = res.data.data?.accounts ?? []
      if (
        filters.value.account_id &&
        !accounts.value.some((item) => item.id === filters.value.account_id)
      ) {
        filters.value.account_id = undefined
        activeView.value = ''
      }
    }),
    teamsStore.fetchTeams({ limit: 100 }).then((result) => {
      teams.value = result.teams
      if (
        filters.value.team_id &&
        !teams.value.some((item) => item.id === filters.value.team_id)
      ) {
        filters.value.team_id = undefined
        activeView.value = ''
      }
    }),
    usersService.list({ limit: 100 }).then((res) => {
      agents.value = res.data.data?.users ?? []
    }),
    tagsService.list({ limit: 100 }).then((res) => {
      tags.value = (res.data as any).data?.tags ?? []
    }),
  ])
  if (
    accounts.value.length &&
    filters.value.account_id &&
    !accounts.value.some((item) => item.id === filters.value.account_id)
  )
    filters.value.account_id = undefined
  if (
    teams.value.length &&
    filters.value.team_id &&
    !teams.value.some((item) => item.id === filters.value.team_id)
  )
    filters.value.team_id = undefined
  lookupsLoaded = results.every((result) => result.status === 'fulfilled')
  lookupFailed.value = !lookupsLoaded
}
function activate() {
  if (!props.active || document.visibilityState !== 'visible') return
  void refresh()
  void refreshViews()
  if (!lookupsLoaded) void loadLookups()
}
onMounted(() => {
  activate()
  document.addEventListener('visibilitychange', activate)
})
watch(
  () => props.active,
  (active) => {
    if (active) activate()
    else {
      revision++
      loading.value = false
    }
  },
)
watch(
  () => props.contextKey,
  () => {
    revision++
    rows.value = []
    selected.value = []
    offset.value = 0
    lookupsLoaded = false
    restoreNavigation()
    activate()
  },
)
watch(selected, () => {
  reviewing.value = false
})
const refreshTimer = setInterval(() => {
  if (!loading.value && !reviewing.value && !saving.value) void refresh()
}, 20_000)
onUnmounted(() => {
  revision++
  clearInterval(refreshTimer)
  clearTimeout(searchTimer)
  document.removeEventListener('visibilitychange', activate)
})

function chooseView() {
  editingView.value = false
  const view = views.value.find((view) => view.id === activeView.value)
  filters.value = view
    ? {
        tags: [],
        ...JSON.parse(JSON.stringify(view.filters)),
        ...(view.team_id ? { team_id: view.team_id } : {}),
      }
    : { tags: [], queue: 'mine' }
}
function changeTeam() {
  activeView.value = ''
  editingView.value = false
}

function pageBy(delta: number) {
  offset.value = Math.max(0, offset.value + delta)
  selected.value = []
  results.value = {}
  void refresh()
}

function editView() {
  const view = currentView.value
  if (!view?.capabilities?.edit) return
  editingView.value = true
  viewName.value = view.name
  viewTeam.value = view.team_id ?? ''
}
async function updateView() {
  if (
    !currentView.value?.capabilities?.edit ||
    !viewName.value.trim() ||
    saving.value
  )
    return
  saving.value = true
  try {
    await inboxService.updateView(
      activeView.value,
      viewName.value.trim(),
      selectedFilters(),
      viewTeam.value || undefined,
    )
    editingView.value = false
    await refreshViews()
  } catch {
    toast.error(t('inboxQueue.saveError'))
  } finally {
    saving.value = false
  }
}
async function saveView() {
  if (!viewName.value.trim() || saving.value) return
  saving.value = true
  try {
    const { data } = await inboxService.saveView(
      viewName.value.trim(),
      selectedFilters(),
      viewTeam.value || undefined,
    )
    viewName.value = ''
    viewTeam.value = ''
    activeView.value = data.data.view.id
    await refreshViews()
    chooseView()
  } catch {
    toast.error(t('inboxQueue.saveError'))
  } finally {
    saving.value = false
  }
}

async function removeView() {
  try {
    await inboxService.deleteView(activeView.value)
    await refreshViews()
    activeView.value = ''
  } catch {
    toast.error(t('inboxQueue.saveError'))
  }
}

async function applyBulk() {
  if (!actionReady.value || saving.value) return
  saving.value = true
  try {
    const snapshot = [...selectedCases.value]
    resultRows.value = Object.fromEntries(snapshot.map((row) => [row.id, row]))
    const selection = snapshot.map((row) => ({
      id: row.id,
      version: row.version,
    }))
    const { data } = await inboxService.bulk(
      action.value,
      selection,
      actionValue.value,
      action.value === 'assign'
        ? overrideReason.value.trim() || undefined
        : undefined,
    )
    results.value = Object.fromEntries(
      data.data.results.map((item) => [item.id, item.result]),
    )
    resultContacts.value = Object.fromEntries(
      snapshot.map((row) => [
        row.id,
        row.contact_name || t('inboxQueue.unnamed'),
      ]),
    )
    reviewing.value = false
    overrideReason.value = ''
    selected.value = []
    emit('changed')
    await refresh()
  } catch {
    toast.error(t('inboxQueue.bulkError'))
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <section
    v-show="active"
    class="inbox-ui inbox-queue"
    :aria-label="t('inboxQueue.title')"
  >
    <div class="inbox-queue-tools">
      <label class="inbox-label mb-3"
        >{{ t('inboxFollowup.search') }}
        <input
          v-model="filters.search"
          type="search"
          maxlength="120"
          :placeholder="t('inboxFollowup.searchHelp')"
        />
      </label>
      <nav class="inbox-queue-shortcuts" :aria-label="t('inboxQueue.title')">
        <button
          v-for="queue in queues"
          :key="queue"
          type="button"
          :aria-pressed="(filters.queue || '') === queue"
          :aria-label="t('inboxQueue.queue.' + (queue || 'all'))"
          @click="filters.queue = queue"
        >
          {{ t('inboxQueue.queue.' + (queue || 'all')) }}
          <span
            v-if="counts[queue || 'all'] !== undefined"
            class="tabular-nums"
            >{{ counts[queue || 'all'] }}</span
          >
        </button>
      </nav>
      <div class="mt-3 flex items-center gap-1">
        <Popover
          ><PopoverTrigger as-child
            ><Button variant="outline" size="sm"
              ><Filter class="mr-1.5 h-3.5 w-3.5" aria-hidden="true" />{{
                t('inboxQueue.filters') +
                (filterCount ? ' (' + filterCount + ')' : '')
              }}</Button
            ></PopoverTrigger
          ><PopoverContent
            class="inbox-ui max-h-[75dvh] w-80 overflow-auto"
            align="start"
            ><h2 class="inbox-section-heading mb-4">
              {{ t('inboxUX.queueFilters') }}
            </h2>
            <div class="grid gap-3">
              <label class="inbox-label"
                >{{ t('inboxQueue.account')
                }}<select v-model="filters.account_id">
                  <option :value="undefined">
                    {{ t('inboxQueue.allAccounts') }}
                  </option>
                  <option
                    v-for="item in accounts"
                    :key="item.id"
                    :value="item.id"
                  >
                    {{ item.name }}
                  </option>
                </select></label
              >
              <label class="inbox-label"
                >{{ t('inboxQueue.team')
                }}<select v-model="filters.team_id" @change="changeTeam">
                  <option :value="undefined">
                    {{ t('inboxQueue.allTeams') }}
                  </option>
                  <option v-for="item in teams" :key="item.id" :value="item.id">
                    {{ item.name }}
                  </option>
                </select></label
              >
              <InboxAgentSelect
                v-model="filters.assignee_id"
                :users="agents"
                :label="t('inboxQueue.assignee')"
                :empty-label="t('inboxQueue.allAssignees')"
              />
              <div class="inbox-grid">
                <label class="inbox-label"
                  >{{ t('inboxWorkflow.caseStatus')
                  }}<select v-model="filters.status">
                    <option :value="undefined">
                      {{ t('inboxQueue.allStatuses') }}
                    </option>
                    <option
                      v-for="status in statuses"
                      :key="status"
                      :value="status"
                    >
                      {{ t('inboxWorkflow.status.' + status) }}
                    </option>
                  </select></label
                ><label class="inbox-label"
                  >{{ t('inboxWorkflow.casePriority')
                  }}<select v-model="filters.priority">
                    <option :value="undefined">
                      {{ t('inboxQueue.allPriorities') }}
                    </option>
                    <option
                      v-for="priority in priorities"
                      :key="priority"
                      :value="priority"
                    >
                      {{ t('inboxWorkflow.priority.' + priority) }}
                    </option>
                  </select></label
                >
              </div>
              <fieldset v-if="tags.length">
                <legend class="mb-2 text-xs font-medium">
                  {{ t('contacts.tags') }}
                </legend>
                <div class="flex flex-wrap gap-3">
                  <label v-for="tag in tags" :key="tag.name" class="inbox-check"
                    ><input
                      v-model="filters.tags"
                      type="checkbox"
                      :value="tag.name"
                    />{{ tag.name }}</label
                  >
                </div>
              </fieldset>
              <details
                :open="!!(filters.fields?.length || filters.predicates?.length)"
              >
                <summary class="min-h-10 cursor-pointer py-2">
                  {{ t('inboxReview.advancedConditions') }}
                </summary>
                <div class="grid gap-3 py-3">
                  <InboxFieldMatchers
                    :predicate-count="filters.predicates?.length ?? 0"
                    :model-value="filters.fields ?? []"
                    @update:model-value="filters.fields = $event"
                  /><InboxConditionBuilder v-model="filters" />
                </div>
              </details>
              <Button variant="outline" size="sm" @click="clearFilters">{{
                t('inboxUX.resetFilters')
              }}</Button>
            </div></PopoverContent
          ></Popover
        >
        <Popover
          ><PopoverTrigger as-child
            ><Button variant="ghost" size="sm"
              ><Bookmark class="mr-1.5 h-3.5 w-3.5" aria-hidden="true" />{{
                t('inboxQueue.savedViews')
              }}</Button
            ></PopoverTrigger
          ><PopoverContent
            class="inbox-ui w-80 max-h-[75dvh] overflow-auto"
            align="start"
            ><h2 class="inbox-section-heading mb-3">
              {{ t('inboxQueue.savedViews') }}
            </h2>
            <label class="inbox-label"
              >{{ t('inboxQueue.chooseView')
              }}<select v-model="activeView" @change="chooseView">
                <option value="">{{ t('inboxQueue.chooseView') }}</option>
                <optgroup
                  v-for="group in ['personal', 'team']"
                  :key="group"
                  :label="t('inboxQueue.' + group)"
                >
                  <option
                    v-for="view in views.filter((view) =>
                      group === 'team' ? !!view.team_id : !view.team_id,
                    )"
                    :key="view.id"
                    :value="view.id"
                  >
                    {{ view.name }}
                  </option>
                </optgroup>
              </select></label
            ><Button
              v-if="currentView?.capabilities?.remove"
              class="mt-2 text-destructive"
              variant="ghost"
              size="sm"
              @click="removeView"
              >{{ t('inboxQueue.deleteView') }}</Button
            >
            <Button
              v-if="currentView?.capabilities?.edit"
              variant="outline"
              size="sm"
              class="mt-2"
              @click="editView"
              >{{ t('common.edit') }}</Button
            >
            <form
              class="mt-4 grid gap-3 border-t border-border pt-4"
              @submit.prevent="editingView ? updateView() : saveView()"
            >
              <p class="inbox-caption">{{ t('inboxUX.viewSaveHelp') }}</p>
              <label class="inbox-label"
                >{{ t('inboxQueue.viewName')
                }}<input v-model="viewName" maxlength="100" required /></label
              ><label v-if="publishTeams.length" class="inbox-label"
                >{{ t('inboxQueue.visibility')
                }}<select v-model="viewTeam">
                  <option value="">{{ t('inboxQueue.personal') }}</option>
                  <option
                    v-for="entry in publishTeams"
                    :key="entry.id"
                    :value="entry.id"
                  >
                    {{ entry.name }}
                  </option>
                </select></label
              ><Button
                type="submit"
                size="sm"
                :disabled="saving || !viewName.trim()"
                >{{
                  t(editingView ? 'common.save' : 'inboxQueue.saveView')
                }}</Button
              >
            </form></PopoverContent
          ></Popover
        >
        <Button
          variant="ghost"
          size="icon"
          class="ml-auto h-8 w-8"
          :aria-label="t('inboxQuality.refresh')"
          :disabled="loading"
          @click="refresh"
          ><RotateCw class="h-3.5 w-3.5" aria-hidden="true"
        /></Button>
      </div>
      <div v-if="filterCount" class="mt-2 grid gap-1">
        <InboxFilterSummary
          :filters="filters"
          :accounts="accounts"
          :teams="teams"
          :users="agents"
        /><Button
          type="button"
          variant="ghost"
          size="sm"
          class="justify-self-start"
          @click="clearFilters"
          >{{ t('inboxUX.resetFilters') }}</Button
        >
      </div>
      <label v-if="canEdit && selectable.length" class="inbox-check mt-2"
        ><input
          type="checkbox"
          :checked="pageSelected"
          :disabled="saving || loading"
          @change="selectPage"
        />{{ t('inboxReview.selectPage') }}</label
      >
      <p v-if="updatedAt" class="inbox-caption mt-2" role="status">
        {{ t('inboxQuality.lastUpdated', { time: updatedAt }) }}
      </p>
      <p v-if="activeView" class="inbox-caption mt-2">
        {{ views.find((v) => v.id === activeView)?.name }}
      </p>
      <p v-if="lookupFailed" role="alert" class="mt-2 text-xs text-destructive">
        {{ t('inboxQueue.loadFiltersError') }}
        <button type="button" class="underline" @click="loadLookups">
          {{ t('common.retry') }}
        </button>
      </p>
    </div>
    <div v-if="canEdit && selectedCases.length" class="inbox-queue-bulk">
      <p class="mb-2 text-sm font-medium">
        {{
          t(
            'inboxQueue.confirmCount',
            { count: selectedCases.length },
            selectedCases.length,
          )
        }}
      </p>
      <div class="flex gap-2">
        <select
          v-model="action"
          class="inbox-control"
          :aria-label="t('inboxQueue.bulkAction')"
          @change="resetBulkReview"
        >
          <option value="resolve">{{ t('inboxQueue.resolve') }}</option>
          <option v-if="canAssignOrTag" value="assign">
            {{ t('inboxQueue.assign') }}
          </option>
          <option v-if="canAssignOrTag" value="tag">
            {{ t('inboxQueue.addTag') }}
          </option></select
        ><Button
          v-if="!reviewing"
          variant="outline"
          size="sm"
          :disabled="!actionReady"
          @click="reviewing = true"
          >{{ t('inboxQueue.review') }}</Button
        >
      </div>
      <InboxAgentSelect
        v-if="action === 'assign'"
        :model-value="actionValue"
        @update:model-value="actionValue = $event ?? ''"
        :users="agents"
        :label="t('inboxQueue.chooseAgent')"
        :empty-label="t('inboxQueue.chooseAgent')"
        :disabled="saving"
      /><select
        v-if="action === 'tag'"
        v-model="actionValue"
        class="inbox-control mt-2"
        :aria-label="t('inboxQueue.chooseTag')"
      >
        <option value="">{{ t('inboxQueue.chooseTag') }}</option>
        <option v-for="entry in tags" :key="entry.name" :value="entry.name">
          {{ entry.name }}
        </option>
      </select>
      <template v-if="reviewing"
        ><ul class="inbox-caption my-2 max-h-28 overflow-auto">
          <li v-for="row in selectedCases" :key="row.id">
            {{ row.contact_name || t('inboxQueue.unnamed') }} ({{
              row.account_name
            }})
          </li>
        </ul>
        <label
          v-if="action === 'assign' && auth.hasPermission('teams', 'write')"
          class="inbox-label my-2"
          >{{ t('inboxCapacity.override')
          }}<input v-model="overrideReason" maxlength="500"
        /></label>
        <div class="flex gap-2">
          <Button
            size="sm"
            :disabled="saving || !actionReady"
            @click="applyBulk"
            >{{ t('inboxQueue.apply') }}</Button
          ><Button size="sm" variant="outline" @click="reviewing = false">{{
            t('common.cancel')
          }}</Button>
        </div></template
      >
    </div>
    <div class="inbox-queue-feed">
      <details v-if="Object.keys(results).length" class="p-3">
        <summary role="status">
          {{
            t('inboxQueue.bulkSummary', {
              updated: Object.values(results).filter((v) => v === 'updated')
                .length,
              total: Object.keys(results).length,
            })
          }}
        </summary>
        <ul class="mt-2 text-xs">
          <li v-for="(result, id) in results" :key="id">
            <button
              v-if="resultRows[id]"
              type="button"
              class="min-h-10 text-left underline"
              @click="emit('open', resultRows[id]!)"
            >
              {{ resultContacts[id] }} · {{ resultRows[id]!.account_name }} ·
              {{ id.slice(0, 8) }}</button
            ><span v-else>{{ resultContacts[id] }}</span
            >: {{ t('inboxQueue.result.' + result) }}
          </li>
        </ul>
      </details>
      <p v-if="failed" role="alert" class="p-3 text-sm text-destructive">
        {{ t('inboxQueue.loadError') }}
        <button type="button" class="underline" @click="refresh">
          {{ t('inboxQueue.retry') }}
        </button>
      </p>
      <div v-if="loading && !rows.length" role="status" class="p-4">
        <p class="inbox-caption">{{ t('common.loading') }}</p>
        <div
          v-for="n in 3"
          :key="n"
          class="mt-4 h-16 animate-pulse rounded bg-muted"
          aria-hidden="true"
        />
      </div>
      <div v-else-if="!rows.length && !failed" class="inbox-empty px-5">
        <Inbox class="h-7 w-7" aria-hidden="true" />
        <h3>{{ t('inboxQueue.empty') }}</h3>
        <p>{{ t('inboxUX.queueEmptyHelp') }}</p>
      </div>
      <ul v-else class="inbox-list">
        <li
          v-for="row in rows"
          :key="row.id"
          class="inbox-queue-row"
          :class="{
            'inbox-queue-row-selected': isCurrent(row),
          }"
        >
          <input
            v-if="canEdit && row.status !== 'resolved'"
            v-model="selected"
            type="checkbox"
            :value="row.id"
            :aria-label="
              t('inboxQueue.selectCase', {
                name: row.contact_name || t('inboxQueue.unnamed'),
              })
            "
          /><button
            type="button"
            class="inbox-queue-open"
            :aria-current="isCurrent(row) ? 'page' : undefined"
            @click="emit('open', row)"
          >
            <strong class="block truncate">{{
              row.contact_name || t('inboxQueue.unnamed')
            }}</strong
            ><span class="inbox-caption mt-1 block truncate">{{
              row.account_name
            }}</span
            ><span
              v-if="row.message_preview"
              class="mt-1 block truncate text-sm"
              >{{ row.message_preview }}</span
            ><span class="inbox-caption mt-1 block truncate"
              >{{ row.team_name || t('inboxQuality.noTeam') }} ·
              {{ row.assignee_name || t('inboxQueue.queue.unassigned') }}</span
            ><span v-if="waitingAge(row)" class="inbox-caption mt-1 block">{{
              waitingAge(row)
            }}</span
            ><span v-if="row.last_inbound_at" class="inbox-caption mt-1 block"
              >{{ t('inboxQuality.waitingSince') }}
              {{ new Date(row.last_inbound_at).toLocaleString() }}</span
            ><span class="mt-2 flex flex-wrap gap-1.5"
              ><span class="inbox-badge">{{
                t('inboxWorkflow.status.' + row.status)
              }}</span
              ><span
                class="inbox-badge"
                :class="{ 'text-destructive': row.priority === 'urgent' }"
                >{{ t('inboxWorkflow.priority.' + row.priority) }}</span
              ></span
            ><small
              v-if="!row.assignee_id && row.routing_reason"
              class="inbox-caption mt-2 block"
              >{{ t('inboxCapacity.reason.' + row.routing_reason) }}</small
            ><small v-if="results[row.id]" class="inbox-caption">{{
              t('inboxQueue.result.' + results[row.id])
            }}</small>
          </button>
          <InboxSLAIndicators
            v-if="row.sla"
            class="inbox-queue-deadlines mt-2"
            compact
            :sla="row.sla"
          />
        </li>
      </ul>
    </div>
    <div v-if="offset || hasMore" class="flex gap-2 border-t border-border p-3">
      <Button
        variant="outline"
        size="sm"
        :disabled="!offset || loading"
        @click="pageBy(-50)"
        >{{ t('inboxQueue.previous') }}</Button
      ><Button
        variant="outline"
        size="sm"
        :disabled="!hasMore || loading"
        @click="pageBy(50)"
        >{{ t('inboxQueue.next') }}</Button
      >
    </div>
  </section>
</template>
