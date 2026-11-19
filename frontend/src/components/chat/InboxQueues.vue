<script setup lang="ts">
import {
  Filter,
  Save as Bookmark,
  MessageSquare as Inbox,
  RotateCw,
} from '@/lib/icons'
import { computed, onMounted, onUnmounted, ref, watch, useId } from 'vue'
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
import { toast } from '@/lib/toast'

const props = defineProps<{
  active: boolean
  selectedContact?: string
  selectedAccount?: string
}>()
const filterId = useId()
const viewId = useId()
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
const isUrgent = (row: QueueCase) => row.priority === 'urgent'
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
const selectedCases = computed(() =>
  rows.value.filter((row) => selected.value.includes(row.id)),
)
const actionReady = computed(
  () =>
    selectedCases.value.length > 0 &&
    (action.value === 'resolve' || actionValue.value !== ''),
)
let revision = 0

function isCurrent(row: QueueCase) {
  return (
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
    const { data } = await inboxService.queue(selectedFilters(), offset.value)
    if (id !== revision) return
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
    views.value = (await inboxService.views()).data.data.views
  } catch {
    toast.error(t('inboxQueue.loadViewsError'))
  }
}

watch(
  filters,
  () => {
    offset.value = 0
    selected.value = []
    results.value = {}
    reviewing.value = false
    overrideReason.value = ''
    void refresh()
  },
  { deep: true },
)

async function loadLookups() {
  lookupFailed.value = false
  const results = await Promise.allSettled([
    accountsService.list().then((res) => {
      accounts.value = res.data.data?.accounts ?? []
    }),
    teamsStore.fetchTeams({ limit: 100 }).then((result) => {
      teams.value = result.teams
    }),
    usersService.list({ limit: 100 }).then((res) => {
      agents.value = res.data.data?.users ?? []
    }),
    tagsService.list({ limit: 100 }).then((res) => {
      tags.value = (res.data as any).data?.tags ?? []
    }),
  ])
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
watch(selected, () => {
  reviewing.value = false
})
const refreshTimer = setInterval(() => {
  if (!loading.value && !reviewing.value && !saving.value) void refresh()
}, 20_000)
onUnmounted(() => {
  revision++
  clearInterval(refreshTimer)
  document.removeEventListener('visibilitychange', activate)
})

function chooseView() {
  const view = views.value.find((view) => view.id === activeView.value)
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
    const selection = selectedCases.value.map((row) => ({
      id: row.id,
      version: row.version,
    }))
    const { data } = await inboxService.bulk(
      action.value,
      selection,
      actionValue.value,
      action.value === 'assign' ? overrideReason.value.trim() || undefined : undefined,
    )
    results.value = Object.fromEntries(
      data.data.results.map((item) => [item.id, item.result]),
    )
    resultContacts.value = Object.fromEntries(
      selectedCases.value.map((row) => [
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
      <nav class="inbox-queue-shortcuts" :aria-label="t('inboxQueue.title')">
        <button
          v-for="queue in queues"
          :key="queue"
          type="button"
          :aria-pressed="(filters.queue || '') === queue"
          @click="filters.queue = queue"
        >
          {{ t('inboxQueue.queue.' + (queue || 'all')) }}
        </button>
      </nav>
      <div class="mt-3 flex items-center gap-1">
        <ot-dropdown
          ><button
            :popovertarget="filterId"
            class="outline small"
            type="button"
          >
            <Filter
              class="inbox-icon-gap inbox-icon-height-small inbox-icon-width-small"
              aria-hidden="true"
            />{{ t('inboxQueue.filters') }}
          </button>
          <div :id="filterId" popover class="inbox-ui inbox-filter-popover">
            <h2 class="inbox-section-heading mb-4">
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
                }}<select v-model="filters.team_id">
                  <option :value="undefined">
                    {{ t('inboxQueue.allTeams') }}
                  </option>
                  <option v-for="item in teams" :key="item.id" :value="item.id">
                    {{ item.name }}
                  </option>
                </select></label
              >
              <label class="inbox-label"
                >{{ t('inboxQueue.assignee')
                }}<select v-model="filters.assignee_id">
                  <option :value="undefined">
                    {{ t('inboxQueue.allAssignees') }}
                  </option>
                  <option
                    v-for="item in agents"
                    :key="item.id"
                    :value="item.id"
                  >
                    {{ item.full_name }}
                  </option>
                </select></label
              >
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
              <button
                @click="filters = { tags: [], queue: filters.queue }"
                class="outline small"
                type="button"
              >
                {{ t('inboxUX.resetFilters') }}
              </button>
            </div>
          </div></ot-dropdown
        >
        <ot-dropdown
          ><button :popovertarget="viewId" class="ghost small" type="button">
            <Bookmark
              class="inbox-icon-gap inbox-icon-height-small inbox-icon-width-small"
              aria-hidden="true"
            />{{ t('inboxQueue.savedViews') }}
          </button>
          <div :id="viewId" popover class="inbox-ui inbox-filter-popover">
            <h2 class="inbox-section-heading mb-3">
              {{ t('inboxQueue.savedViews') }}
            </h2>
            <label class="inbox-label"
              >{{ t('inboxQueue.chooseView')
              }}<select v-model="activeView" @change="chooseView">
                <option value="">{{ t('inboxQueue.chooseView') }}</option>
                <option v-for="view in views" :key="view.id" :value="view.id">
                  {{ view.name }}
                </option>
              </select></label
            ><button
              v-if="activeView"
              class="ghost small mt-2 text-destructive"
              @click="removeView"
              type="button"
            >
              {{ t('inboxQueue.deleteView') }}
            </button>
            <form
              class="mt-4 grid gap-3 border-t border-border pt-4"
              @submit.prevent="saveView"
            >
              <p class="inbox-caption">{{ t('inboxUX.viewSaveHelp') }}</p>
              <label class="inbox-label"
                >{{ t('inboxQueue.viewName')
                }}<input v-model="viewName" maxlength="100" required /></label
              ><label v-if="teams.length" class="inbox-label"
                >{{ t('inboxQueue.visibility')
                }}<select v-model="viewTeam">
                  <option value="">{{ t('inboxQueue.personal') }}</option>
                  <option
                    v-for="entry in teams"
                    :key="entry.id"
                    :value="entry.id"
                  >
                    {{ entry.name }}
                  </option>
                </select></label
              ><button
                type="submit"
                :disabled="saving || !viewName.trim()"
                class="small"
              >
                {{ t('inboxQueue.saveView') }}
              </button>
            </form>
          </div></ot-dropdown
        >
        <button
          class="ghost icon ml-auto h-8 w-8"
          :aria-label="t('inboxQueue.retry')"
          :disabled="loading"
          @click="refresh"
          type="button"
        >
          <RotateCw
            class="inbox-icon-height-small inbox-icon-width-small"
            aria-hidden="true"
          />
        </button>
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
    <div v-if="canEdit && selectedCases.length" class="inbox-queue-bulk">
      <p class="mb-2 text-sm font-medium">
        {{ t('inboxQueue.confirmCount', { count: selectedCases.length }) }}
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
        ><button
          v-if="!reviewing"
          :disabled="!actionReady"
          @click="reviewing = true"
          class="outline small"
          type="button"
        >
          {{ t('inboxQueue.review') }}
        </button>
      </div>
      <select
        v-if="action === 'assign'"
        v-model="actionValue"
        class="inbox-control mt-2"
        :aria-label="t('inboxQueue.chooseAgent')"
      >
        <option value="">{{ t('inboxQueue.chooseAgent') }}</option>
        <option v-for="entry in agents" :key="entry.id" :value="entry.id">
          {{ entry.full_name }}
        </option></select
      ><select
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

        <label v-if="action === 'assign' && auth.hasPermission('teams','write')" class="inbox-label my-2">{{ t('inboxCapacity.override') }}<input v-model="overrideReason" maxlength="500" /></label><div class="flex gap-2">
          <button
            :disabled="saving || !actionReady"
            @click="applyBulk"
            class="small"
            type="button"
          >
            {{ t('inboxQueue.apply') }}</button
          ><button
            @click="reviewing = false"
            class="outline small"
            type="button"
          >
            {{ t('common.cancel') }}
          </button>
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
            {{ resultContacts[id] }}: {{ t('inboxQueue.result.' + result) }}
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
            ><span class="mt-2 flex flex-wrap inbox-gap-small"
              ><span class="inbox-badge">{{
                t('inboxWorkflow.status.' + row.status)
              }}</span
              ><span
                class="inbox-badge"
                :class="{ 'text-destructive': isUrgent(row) }"
                >{{ t('inboxWorkflow.priority.' + row.priority) }}</span
              ></span
            ><small v-if="results[row.id]" class="inbox-caption">{{
              t('inboxQueue.result.' + results[row.id])
            }}</small>
            <span v-if="row.routing_reason" class="inbox-caption">{{ t('inboxCapacity.reason.'+row.routing_reason) }}</span>
          </button>
        </li>
      </ul>
    </div>
    <div v-if="offset || hasMore" class="flex gap-2 border-t border-border p-3">
      <button
        :disabled="!offset || loading"
        @click="pageBy(-50)"
        class="outline small"
        type="button"
      >
        {{ t('inboxQueue.previous') }}</button
      ><button
        :disabled="!hasMore || loading"
        @click="pageBy(50)"
        class="outline small"
        type="button"
      >
        {{ t('inboxQueue.next') }}
      </button>
    </div>
  </section>
</template>
