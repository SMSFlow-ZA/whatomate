<script setup lang="ts">
import InboxAgentSelect from '@/components/shared/InboxAgentSelect.vue'
import InboxLiveStatus from '@/components/shared/InboxLiveStatus.vue'
import { useInboxLive } from '@/composables/useInboxLive'
import InboxFilterSummary from './InboxFilterSummary.vue'
import { Button } from '@/components/ui/button'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import { Bell, Filter, Bookmark, Inbox, RotateCw, Info } from 'lucide-vue-next'
import '@/assets/inbox.css'
import InboxFieldMatchers from './InboxFieldMatchers.vue'
import InboxConditionBuilder from './InboxConditionBuilder.vue'
import InboxSLAIndicators from './InboxSLAIndicators.vue'
import InboxQueueDates from './InboxQueueDates.vue'
import {
  inboxDateKeys,
  inboxQueueOrders,
  readInboxDiscovery,
} from '@/lib/inboxDiscovery'
import { useInboxQueuePage } from '@/composables/useInboxQueuePage'
import {
  computed,
  nextTick,
  onMounted,
  onUnmounted,
  ref,
  useId,
  watch,
} from 'vue'
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
  type InboxQueueDiscovery,
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
const root = ref<HTMLElement>(),
  queueID = useId()
const filters = ref<InboxFilters>({ tags: [], queue: 'mine' })
const discovery = ref<InboxQueueDiscovery>({ order: 'priority_waiting' })
const navigationReset = ref(false)
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
      state?.filters &&
      typeof state.filters === 'object' &&
      !Array.isArray(state.filters)
        ? state.filters
        : { tags: [], queue: 'mine' }
    activeView.value = typeof state?.view === 'string' ? state.view : ''
    const loaded = readInboxDiscovery(state?.discovery)
    discovery.value = loaded ?? { order: 'priority_waiting' }
    navigationReset.value = !loaded
    if (!loaded) activeView.value = ''
  } catch {
    filters.value = { tags: [], queue: 'mine' }
    discovery.value = { order: 'priority_waiting' }
    activeView.value = ''
    navigationReset.value = true
  }
}
const saving = ref(false)
const viewName = ref('')
const viewTeam = ref('')
const activeView = ref('')
restoreNavigation()
const queueContext = () =>
  JSON.stringify([
    props.contextKey,
    props.active,
    selectedFilters(),
    discovery.value,
    activeView.value,
  ])
const pager = useInboxQueuePage(queueContext, async (cursor, revision) => {
  const response = await inboxService.queue(
    selectedFilters(),
    0,
    true,
    activeView.value || undefined,
    { ...discovery.value },
    cursor,
    revision,
  )
  return response.data.data
})
const { rows, counts, loading, failed, hasMore } = pager
const offset = computed(() => pager.position.value * 50)
const updatedAt = computed(() =>
  pager.updatedAt.value
    ? new Date(pager.updatedAt.value).toLocaleTimeString()
    : '',
)
const dateCount = computed(
  () => inboxDateKeys.filter((key) => discovery.value[key]).length,
)
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
    (filters.value.predicates?.length ?? 0) +
    dateCount.value,
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
  discovery.value = { order: discovery.value.order }
}
function resetDiscovery() {
  discovery.value = { order: 'priority_waiting' }
  activeView.value = ''
  navigationReset.value = false
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
    !pager.changed.value &&
    !pager.denied.value &&
    (action.value === 'resolve' || actionValue.value !== ''),
)
let viewGeneration = 0
let lookupGeneration = 0
let mutationGeneration = 0
let searchTimer: ReturnType<typeof setTimeout> | undefined
let lastFilterShape = JSON.stringify([
  { ...filters.value, search: undefined },
  discovery.value,
  activeView.value,
])

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
  selected.value = []
  reviewing.value = false
  await pager.refresh()
}
async function refreshFromStart() {
  await refresh()
  if (!failed.value && !pager.changed.value && !pager.denied.value)
    await nextTick(() =>
      root.value
        ?.querySelector<HTMLButtonElement>('[data-queue-refresh]')
        ?.focus(),
    )
}
function poll() {
  if (
    !props.active ||
    document.visibilityState !== 'visible' ||
    loading.value ||
    reviewing.value ||
    saving.value ||
    selected.value.length
  )
    return
  return pager.poll()
}

const live = useInboxLive(
  () => ({
    kind: 'queue',
    account_id: filters.value.account_id || undefined,
    team_id: filters.value.team_id || undefined,
  }),
  {
    context: () => props.contextKey ?? '',
    active: () => props.active,
    blocked: () =>
      loading.value ||
      reviewing.value ||
      saving.value ||
      selected.value.length > 0,
    refresh: async (hint) => {
      await poll()
      if (hint.reset) await refreshViews()
    },
    denied: () => {
      pager.reset()
      pager.denied.value = true
    },
    clear: () => {
      pager.reset()
      selected.value = []
      results.value = {}
      resultContacts.value = {}
      resultRows.value = {}
      reviewing.value = false
    },
  },
)

async function refreshViews() {
  const request = ++viewGeneration,
    context = props.contextKey
  try {
    const response = (await inboxService.views()).data.data
    if (request !== viewGeneration || context !== props.contextKey) return
    views.value = response.views
    publishTeams.value = response.publish_teams ?? []
    if (
      activeView.value &&
      !views.value.some((view) => view.id === activeView.value)
    ) {
      activeView.value = ''
      filters.value = { tags: [], queue: 'mine' }
      discovery.value = { order: 'priority_waiting' }
      navigationReset.value = true
    }
  } catch {
    if (request !== viewGeneration || context !== props.contextKey) return
    toast.error(t('inboxQueue.loadViewsError'))
  }
}

watch(
  [filters, discovery, activeView],
  () => {
    if (storageKey.value)
      try {
        localStorage.setItem(
          storageKey.value,
          JSON.stringify({
            filters: filters.value,
            view: activeView.value,
            discovery: discovery.value,
          }),
        )
      } catch {
        /* Storage can be disabled by the browser. */
      }
    selected.value = []
    results.value = {}
    resultContacts.value = {}
    resultRows.value = {}
    mutationGeneration++
    saving.value = false
    reviewing.value = false
    pager.reset()
    clearTimeout(searchTimer)
    const shape = JSON.stringify([
      { ...filters.value, search: undefined },
      discovery.value,
      activeView.value,
    ])
    const searching = shape === lastFilterShape && !!filters.value.search
    lastFilterShape = shape
    if (searching) {
      loading.value = true
      searchTimer = setTimeout(() => void refresh(), 250)
    } else void refresh()
  },
  { deep: true },
)

async function loadLookups() {
  const request = ++lookupGeneration,
    context = props.contextKey
  lookupFailed.value = false
  const results = await Promise.allSettled([
    accountsService.list(),
    teamsStore.fetchTeams({ limit: 100 }),
    auth.hasPermission('users', 'read')
      ? usersService.list({ limit: 100 })
      : Promise.resolve({ data: { data: { users: [] } } }),
    auth.hasPermission('tags', 'read')
      ? tagsService.list({ limit: 100 })
      : Promise.resolve({ data: { data: { tags: [] } } }),
  ])
  if (request !== lookupGeneration || context !== props.contextKey) return
  if (results[0].status === 'fulfilled')
    accounts.value = results[0].value.data.data?.accounts ?? []
  if (results[1].status === 'fulfilled') teams.value = results[1].value.teams
  if (results[2].status === 'fulfilled')
    agents.value = results[2].value.data.data?.users ?? []
  if (results[3].status === 'fulfilled')
    tags.value =
      (results[3].value.data as { data?: { tags?: { name: string }[] } }).data
        ?.tags ?? []
  for (const [index, list, field] of [
    [0, accounts.value, 'account_id'],
    [1, teams.value, 'team_id'],
  ] as const) {
    if (
      results[index].status === 'fulfilled' &&
      filters.value[field] &&
      !list.some((item) => item.id === filters.value[field])
    ) {
      filters.value[field] = undefined
      activeView.value = ''
      navigationReset.value = true
    }
  }
  lookupsLoaded = results.every((result) => result.status === 'fulfilled')
  lookupFailed.value = !lookupsLoaded
}
function activate() {
  if (!props.active || document.visibilityState !== 'visible') return
  if (pager.updatedAt.value) void poll()
  else void refresh()
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
      pager.invalidate()
    }
  },
)
watch(
  () => props.contextKey,
  () => {
    pager.reset()
    viewGeneration++
    lookupGeneration++
    views.value = []
    publishTeams.value = []
    accounts.value = []
    teams.value = []
    agents.value = []
    tags.value = []
    selected.value = []
    lookupsLoaded = false
    restoreNavigation()
    if (props.active && document.visibilityState === 'visible') {
      void refreshViews()
      void loadLookups()
    }
  },
)
watch(selected, () => {
  reviewing.value = false
})
watch(pager.denied, (denied) => {
  if (!denied) return
  selected.value = []
  results.value = {}
  resultContacts.value = {}
  resultRows.value = {}
  reviewing.value = false
  views.value = []
  publishTeams.value = []
  accounts.value = []
  teams.value = []
  agents.value = []
  tags.value = []
  lookupsLoaded = false
  viewGeneration++
  lookupGeneration++
})
const refreshTimer = setInterval(() => {
  // Keep independent reconciliation if the live path silently loses hints,
  // without repeating the old 20-second poll while fresh reads are arriving.
  if (
    live.state.value === 'live' &&
    Date.now() - new Date(pager.updatedAt.value || 0).getTime() < 60_000
  )
    return
  void poll()
}, 20_000)
onUnmounted(() => {
  pager.invalidate()
  mutationGeneration++
  viewGeneration++
  lookupGeneration++
  clearInterval(refreshTimer)
  clearTimeout(searchTimer)
  document.removeEventListener('visibilitychange', activate)
})

function chooseView() {
  editingView.value = false
  const view = views.value.find((view) => view.id === activeView.value)
  const loaded = readInboxDiscovery(view?.discovery)
  discovery.value = loaded ?? { order: 'priority_waiting' }
  navigationReset.value = !loaded
  if (!loaded) activeView.value = ''
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
  selected.value = []
  results.value = {}
  if (delta > 0) void pager.older()
  else void pager.newer()
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
  const request = ++mutationGeneration,
    context = queueContext()
  try {
    await inboxService.updateView(
      activeView.value,
      viewName.value.trim(),
      selectedFilters(),
      viewTeam.value || undefined,
      { ...discovery.value },
    )
    if (request !== mutationGeneration || context !== queueContext()) return
    editingView.value = false
    await refreshViews()
  } catch {
    if (request === mutationGeneration && context === queueContext())
      toast.error(t('inboxQueue.saveError'))
  } finally {
    if (request === mutationGeneration) saving.value = false
  }
}
async function saveView() {
  if (!viewName.value.trim() || saving.value) return
  saving.value = true
  const request = ++mutationGeneration,
    context = queueContext()
  try {
    const { data } = await inboxService.saveView(
      viewName.value.trim(),
      selectedFilters(),
      viewTeam.value || undefined,
      { ...discovery.value },
    )
    if (request !== mutationGeneration || context !== queueContext()) return
    viewName.value = ''
    viewTeam.value = ''
    activeView.value = data.data.view.id
    const saved = data.data.view
    filters.value = {
      tags: [],
      ...saved.filters,
      ...(saved.team_id ? { team_id: saved.team_id } : {}),
    }
    discovery.value = readInboxDiscovery(saved.discovery) ?? {
      order: 'priority_waiting',
    }
    await refreshViews()
  } catch {
    if (request === mutationGeneration && context === queueContext())
      toast.error(t('inboxQueue.saveError'))
  } finally {
    if (request === mutationGeneration) saving.value = false
  }
}

async function removeView() {
  if (saving.value || !currentView.value?.capabilities?.remove) return
  saving.value = true
  const request = ++mutationGeneration,
    context = queueContext()
  try {
    await inboxService.deleteView(activeView.value)
    if (request !== mutationGeneration || context !== queueContext()) return
    activeView.value = ''
    await refreshViews()
  } catch {
    if (request === mutationGeneration && context === queueContext())
      toast.error(t('inboxQueue.saveError'))
  } finally {
    if (request === mutationGeneration) saving.value = false
  }
}

async function applyBulk() {
  if (!actionReady.value || saving.value) return
  saving.value = true
  const request = ++mutationGeneration,
    context = queueContext()
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
    if (request !== mutationGeneration || context !== queueContext()) return
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
    if (request === mutationGeneration && context === queueContext())
      toast.error(t('inboxQueue.bulkError'))
  } finally {
    if (request === mutationGeneration) saving.value = false
  }
}
</script>

<template>
  <section
    ref="root"
    v-show="active"
    class="inbox-ui inbox-queue"
    :aria-label="t('inboxQueue.title')"
  >
    <div class="inbox-queue-tools">
      <label class="inbox-label inbox-queue-search"
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
          {{
            t(
              queue === ''
                ? 'inboxVisual.all'
                : queue === 'awaiting_reply'
                  ? 'inboxVisual.firstReply'
                  : 'inboxQueue.queue.' + queue,
            )
          }}
          <span
            v-if="counts[queue || 'all'] !== undefined"
            class="inbox-queue-count tabular-nums"
            >{{ counts[queue || 'all'] }}</span
          >
        </button>
      </nav>
      <div class="inbox-queue-actions">
        <Popover
          ><PopoverTrigger as-child
            ><Button variant="outline" size="sm"
              ><Filter class="mr-1.5 h-3.5 w-3.5" aria-hidden="true" />{{
                t('inboxQueue.filters') +
                (filterCount ? ' (' + filterCount + ')' : '')
              }}</Button
            ></PopoverTrigger
          ><PopoverContent
            class="inbox-ui w-80 overflow-auto"
            style="
              max-height: min(
                75dvh,
                var(--reka-popper-available-height, 75dvh)
              );
            "
            :collision-padding="12"
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
              <InboxQueueDates v-model="discovery" :disabled="saving" />
              <Button variant="outline" size="sm" @click="clearFilters">{{
                t('inboxUX.resetFilters')
              }}</Button>
            </div></PopoverContent
          ></Popover
        >
        <Popover
          ><PopoverTrigger as-child
            ><Button
              variant="ghost"
              size="sm"
              :aria-label="t('inboxQueue.savedViews')"
              :title="t('inboxQueue.savedViews')"
              ><Bookmark class="mr-1.5 h-3.5 w-3.5" aria-hidden="true" />{{
                t('inboxVisual.views')
              }}</Button
            ></PopoverTrigger
          ><PopoverContent
            class="inbox-ui w-80 overflow-auto"
            style="
              max-height: min(
                75dvh,
                var(--reka-popper-available-height, 75dvh)
              );
            "
            :collision-padding="12"
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
        <RouterLink
          :to="{ name: 'inbox-notices' }"
          class="inbox-queue-alert-link inline-flex items-center justify-center rounded-lg text-muted-foreground hover:bg-muted hover:text-foreground"
          :aria-label="t('inboxNotice.title')"
          :title="t('inboxNotice.title')"
          ><Bell class="h-4 w-4" aria-hidden="true" /><span class="sr-only">{{
            t('inboxNotice.title')
          }}</span></RouterLink
        >
        <Button
          variant="ghost"
          size="icon"
          class="ml-auto h-8 w-8"
          :aria-label="t('inboxQuality.refresh')"
          :disabled="loading"
          data-queue-refresh
          @click="refresh"
          ><RotateCw class="h-3.5 w-3.5" aria-hidden="true"
        /></Button>
      </div>
      <div class="inbox-queue-order">
        <label class="inbox-label min-w-0 flex-1" :for="queueID + '-sort'"
          ><span class="sr-only">{{ t('inboxDiscovery.sort') }}</span>
          <select
            :id="queueID + '-sort'"
            v-model="discovery.order"
            class="min-w-0 w-full"
            :disabled="saving"
          >
            <option
              v-for="order in inboxQueueOrders"
              :key="order"
              :value="order"
            >
              {{ t('inboxDiscovery.order.' + order) }}
            </option>
          </select>
        </label>
        <Popover
          ><PopoverTrigger as-child
            ><Button
              variant="ghost"
              size="icon"
              class="h-8 w-8"
              :aria-label="t('inboxDiscovery.orderDetails')"
              ><Info
                class="h-4 w-4"
                aria-hidden="true" /></Button></PopoverTrigger
          ><PopoverContent
            class="inbox-ui max-w-[calc(100vw-2rem)] w-72"
            align="end"
            ><p class="text-sm">
              {{ t('inboxDiscovery.orderHelp.' + discovery.order) }}
            </p></PopoverContent
          ></Popover
        >
      </div>
      <p v-if="dateCount" class="inbox-caption mt-2">
        {{ t('inboxDiscovery.appliedDates', { timezone: discovery.timezone }) }}
      </p>
      <p v-if="navigationReset" role="status" class="inbox-caption mt-2">
        {{ t('inboxDiscovery.navigationReset') }}
        <button
          type="button"
          class="min-h-[44px] underline"
          @click="navigationReset = false"
        >
          {{ t('inboxDiscovery.dismiss') }}
        </button>
      </p>
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
      <div class="inbox-queue-meta">
        <label v-if="canEdit && selectable.length" class="inbox-check"
          ><input
            type="checkbox"
            :aria-label="t('inboxReview.selectPage')"
            :checked="pageSelected"
            :disabled="saving || loading"
            @change="selectPage"
          /><span :title="t('inboxReview.selectPage')">{{
            t('inboxVisual.selectPage')
          }}</span></label
        >
        <InboxLiveStatus
          :state="live.state.value"
          :pending="live.pending.value"
          :title="
            updatedAt
              ? t('inboxQuality.lastUpdated', { time: updatedAt })
              : undefined
          "
        />
        <span v-if="updatedAt" class="sr-only" role="status">{{
          t('inboxQuality.lastUpdated', { time: updatedAt })
        }}</span>
      </div>
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
    <div
      v-if="pager.changed.value"
      role="status"
      data-queue-refresh-notice
      class="border-y border-border p-3"
    >
      <p class="text-sm">
        {{
          t(
            pager.invalid.value
              ? 'inboxDiscovery.invalidPage'
              : 'inboxDiscovery.changed',
          )
        }}
      </p>
      <p v-if="selected.length" class="inbox-caption mt-1">
        {{ t('inboxDiscovery.selectionRefresh') }}
      </p>
      <Button
        variant="outline"
        size="sm"
        class="mt-2"
        :disabled="loading"
        @click="refreshFromStart"
        >{{ t('inboxDiscovery.refreshStart') }}</Button
      >
      <Button
        v-if="pager.invalid.value"
        variant="ghost"
        size="sm"
        :disabled="loading"
        @click="resetDiscovery"
        >{{ t('inboxDiscovery.resetDiscovery') }}</Button
      >
    </div>
    <div
      v-if="pager.denied.value"
      role="alert"
      class="border-y border-border p-3"
    >
      <p class="text-sm">{{ t('inboxDiscovery.denied') }}</p>
      <Button
        variant="outline"
        size="sm"
        class="mt-2"
        :disabled="loading"
        @click="refresh"
        >{{ t('common.retry') }}</Button
      >
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
      <div
        v-else-if="!rows.length && !failed && !pager.denied.value"
        class="inbox-empty px-5"
      >
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
            <strong class="inbox-queue-name block truncate">{{
              row.contact_name || t('inboxQueue.unnamed')
            }}</strong
            ><span class="inbox-caption mt-1 block truncate">{{
              row.account_name
            }}</span
            ><span
              v-if="row.message_preview"
              class="inbox-queue-preview mt-1 block truncate text-sm"
              >{{ row.message_preview }}</span
            ><span class="inbox-caption mt-1 block truncate"
              >{{ row.team_name || t('inboxQuality.noTeam') }} ·
              {{ row.assignee_name || t('inboxQueue.queue.unassigned') }}</span
            ><span
              v-if="row.last_inbound_at"
              class="inbox-caption mt-1 block"
              :title="
                t('inboxQuality.waitingSince') +
                ' ' +
                new Date(row.last_inbound_at).toLocaleString()
              "
              ><span v-if="waitingAge(row)">{{ waitingAge(row) }}</span>
              <time v-else :datetime="row.last_inbound_at">{{
                new Date(row.last_inbound_at).toLocaleDateString()
              }}</time>
              <span class="sr-only"
                >{{ t('inboxQuality.waitingSince') }}
                {{ new Date(row.last_inbound_at).toLocaleString() }}</span
              ></span
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
    <div
      v-if="offset || hasMore"
      class="flex flex-wrap items-center gap-2 border-t border-border p-3"
    >
      <Button
        variant="outline"
        size="sm"
        :disabled="!pager.hasPrevious.value || loading || pager.changed.value"
        @click="pageBy(-50)"
        >{{ t('inboxQueue.previous') }}</Button
      ><Button
        variant="outline"
        size="sm"
        :disabled="!hasMore || loading || pager.changed.value"
        @click="pageBy(50)"
        >{{ t('inboxQueue.next') }}</Button
      >
      <span class="inbox-caption">{{
        t('inboxDiscovery.page', { number: pager.position.value + 1 })
      }}</span>
      <p v-if="pager.historyTrimmed.value" class="inbox-caption w-full">
        {{ t('inboxDiscovery.historyTrimmed') }}
        <button
          type="button"
          class="min-h-[44px] underline"
          @click="refreshFromStart"
        >
          {{ t('inboxDiscovery.refreshStart') }}
        </button>
      </p>
    </div>
  </section>
</template>
