<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useTeamsStore } from '@/stores/teams'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { PageHeader } from '@/components/shared'
import InboxFilterSummary from '@/components/chat/InboxFilterSummary.vue'
import InboxAgentSelect from '@/components/shared/InboxAgentSelect.vue'
import InboxTabs from '@/components/shared/InboxTabs.vue'
import InboxSLASettings from '@/components/settings/InboxSLASettings.vue'
import { useRoute } from 'vue-router'
import { useInboxDraftGuard } from '@/composables/useInboxDraftGuard'
import UnsavedChangesDialog from '@/components/shared/UnsavedChangesDialog.vue'
import { useAuthStore } from '@/stores/auth'
import {
  ArrowLeft,
  ArrowUp,
  ArrowDown,
  Plus,
  Search,
  Trash2,
  Workflow,
  ShieldCheck,
  History,
  Pencil,
  RotateCw,
} from 'lucide-vue-next'
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogCancel,
  AlertDialogAction,
} from '@/components/ui/alert-dialog'
import InboxActionEditor from '@/components/settings/InboxActionEditor.vue'
import '@/assets/inbox.css'
import {
  accountsService,
  tagsService,
  templatesService,
  usersService,
} from '@/services/api'
import {
  inboxService,
  type InboxWorkflow,
  type InboxWorkflowInput,
  type InboxWorkflowPreview,
  type InboxWorkflowRun,
  type QueueCase,
} from '@/services/inbox'
import InboxFieldMatchers from '@/components/chat/InboxFieldMatchers.vue'
import InboxConditionBuilder from '@/components/chat/InboxConditionBuilder.vue'
import InboxWorkflowPreviewPanel from '@/components/chat/InboxWorkflowPreview.vue'

const { t } = useI18n()
const route = useRoute()
const auth = useAuthStore()
const showTargets = ref(false)
const targetEditor = ref<InstanceType<typeof InboxSLASettings>>()
const teamsStore = useTeamsStore()
const mode = ref<'macro' | 'rule'>('rule')
const list = ref<InboxWorkflow[]>([])
const runs = ref<InboxWorkflowRun[]>([])
const id = ref('')
const enabled = ref(false)
const switchState = ref({ enabled: false, dispatch_available: false })
const busy = ref(false)
const ready = ref(false)
const failed = ref(false)
const lookupFailed = ref(false)
const caseId = ref('')
const cases = ref<QueueCase[]>([])
const caseSearch = ref(''),
  caseOffset = ref(0),
  casesMore = ref(false),
  casesBusy = ref(false),
  casesFailed = ref(false)
let caseSequence = 0
async function loadCases(offset = 0) {
  const generation = ++caseSequence,
    workflow = dryRunId.value
  casesBusy.value = true
  casesFailed.value = false
  try {
    const { data } = await inboxService.queue(
      { search: caseSearch.value },
      offset,
    )
    if (generation !== caseSequence || workflow !== dryRunId.value) return
    const selected = cases.value.find((row) => row.id === caseId.value)
    cases.value =
      selected && !data.data.cases.some((row) => row.id === selected.id)
        ? [selected, ...data.data.cases]
        : data.data.cases
    caseOffset.value = offset
    casesMore.value = data.data.has_more
  } catch {
    if (generation === caseSequence) casesFailed.value = true
  } finally {
    if (generation === caseSequence) casesBusy.value = false
  }
}
const dryRunId = ref('')
const preview = ref<InboxWorkflowPreview | null>(null)
const previewBusy = ref(false)
let loadSequence = 0
let previewSequence = 0
watch(
  [caseId, dryRunId],
  () => {
    previewSequence++
    preview.value = null
    previewBusy.value = false
  },
  { flush: 'sync' },
)
onUnmounted(() => {
  caseSequence++
  loadSequence++
  previewSequence++
})
function closePreview() {
  caseSequence++
  casesBusy.value = false
  dryRunId.value = ''
  preview.value = null
}
const accounts = ref<{ id: string; name: string }[]>([])
const teams = ref<{ id: string; name: string }[]>([])
const users = ref<{ id: string; full_name: string }[]>([])
const tags = ref<{ name: string }[]>([])
const templates = ref<
  { id: string; name: string; language: string; whats_app_account?: string }[]
>([])
const statuses = ['open', 'pending', 'waiting_customer', 'snoozed', 'resolved']
const priorities = ['low', 'normal', 'high', 'urgent']
const triggers = ['case_opened', 'incoming_message', 'status_changed']
const fresh = (): InboxWorkflowInput => ({
  kind: mode.value,
  name: '',
  description: '',
  visibility: 'organization',
  trigger: mode.value === 'rule' ? 'case_opened' : '',
  position: 0,
  actions: [{ type: 'note', value: '' }],
  conditions: { tags: [] },
})
const form = ref(fresh())
const baseline = ref('')
const saveError = ref('')
const syntheticText = ref('')
const historyOffset = ref(0),
  historyMore = ref(false)
const { discardOpen, confirmDiscard, cancelDiscard, requestDiscard } =
  useInboxDraftGuard(
    () => editing.value && JSON.stringify(form.value) !== baseline.value,
  )
const deleting = ref(false)
const editing = ref(false)
const showHistory = ref(false)
const confirmSends = ref(false)
const query = ref('')
const filtered = computed(() =>
  list.value.filter((w) =>
    w.name.toLocaleLowerCase().includes(query.value.toLocaleLowerCase()),
  ),
)
const hasReply = (w: InboxWorkflow) =>
  w.actions.some((a) => ['send_text', 'send_template'].includes(a.type))
const inboxName = (w: InboxWorkflow) =>
  accounts.value.find((a) => a.id === w.conditions.account_id)?.name ??
  t('inboxSla.any')
const readyToSave = computed(
  () =>
    !!form.value.name.trim() &&
    form.value.actions.every(
      (action) =>
        (action.type === 'snooze'
          ? (action.duration_minutes ?? 0) > 0
          : action.value.trim()) &&
        (action.type !== 'send_template' || action.consent_confirmed),
    ) &&
    form.value.actions.filter(
      (action) =>
        action.type === 'send_text' || action.type === 'send_template',
    ).length <= 1,
)
async function load() {
  const request = ++loadSequence
  const requestedMode = mode.value
  busy.value = true
  try {
    const settings = await inboxService.settings()
    if (request !== loadSequence || requestedMode !== mode.value) return
    enabled.value = settings.data.data.enabled
    if (enabled.value) {
      const [w, r, state] = await Promise.all([
        inboxService.workflowDefinitions(requestedMode),
        inboxService.workflowRuns(undefined, historyOffset.value),
        inboxService.automationState(),
      ])
      if (request !== loadSequence || requestedMode !== mode.value) return
      list.value = w.data.data.workflows.filter(
        (item) => item.kind === requestedMode,
      )
      runs.value = r.data.data.runs ?? []
      historyMore.value = r.data.data.has_more ?? false
      switchState.value = state.data.data
    }
    ready.value = true
    failed.value = false
  } catch {
    if (request === loadSequence) failed.value = true
  } finally {
    if (request === loadSequence) busy.value = false
  }
}
function select(w?: InboxWorkflow, open = true, force = false) {
  if (!force) {
    requestDiscard(() => select(w, open, true))
    return
  }
  saveError.value = ''
  editing.value = open
  id.value = w?.id ?? ''
  deleting.value = false
  if (w) {
    mode.value = w.kind
    form.value = {
      kind: w.kind,
      name: w.name,
      description: w.description ?? '',
      visibility: w.visibility ?? 'organization',
      team_id: w.team_id,
      trigger: w.trigger ?? '',
      position: w.position,
      actions: w.actions.map((action) => ({ ...action })),
      conditions: {
        ...w.conditions,
        tags: [...(w.conditions.tags ?? [])],
        fields: w.conditions.fields?.map((field) => ({ ...field })) ?? [],
        predicates:
          w.conditions.predicates?.map((predicate) => ({ ...predicate })) ?? [],
      },
      enabled: !!w.enabled_at,
    }
  } else form.value = fresh()
  baseline.value = JSON.stringify(form.value)
}
function changeTab(value: string, force = false) {
  if (showTargets.value && !force && targetEditor.value) {
    targetEditor.value.confirmLeave(() => changeTab(value, true))
    return
  }
  busy.value = false
  historyOffset.value = 0
  loadSequence++
  closePreview()
  showTargets.value = value === 'sla'
  if (showTargets.value) {
    showHistory.value = false
    return
  }
  if (value === 'history') {
    showHistory.value = true
    dryRunId.value = ''
    void load()
  } else void changeMode(value as 'macro' | 'rule')
}
async function changeMode(next: typeof mode.value) {
  mode.value = next
  list.value = []
  select(undefined, false)
  showHistory.value = false
  query.value = ''
  dryRunId.value = ''
  preview.value = null
  await load()
}
async function save(remove = false) {
  const target = id.value
  busy.value = true
  try {
    if (remove) await inboxService.deleteWorkflow(target)
    else if (target) await inboxService.updateWorkflow(target, form.value)
    else await inboxService.createWorkflow(form.value)
    select(undefined, false, true)
    await load()
    toast.success(t('settings.saved'))
  } catch {
    saveError.value = t('inboxRules.saveError')
    toast.error(saveError.value)
  } finally {
    busy.value = false
  }
}
async function stopOrStart() {
  if (!switchState.value.enabled && !confirmSends.value) {
    confirmSends.value = true
    return
  }
  busy.value = true
  try {
    const { data } = await inboxService.setAutomation(
      !switchState.value.enabled,
    )
    confirmSends.value = false
    switchState.value = data.data
    await load()
  } catch {
    toast.error(t('inboxRules.saveError'))
  } finally {
    busy.value = false
  }
}
async function prepare(w: InboxWorkflow) {
  dryRunId.value = w.id
  preview.value = null
  caseId.value = ''
  caseSearch.value = ''
  cases.value = []
  caseOffset.value = 0
  await loadCases()
}

async function runPreview() {
  const workflow = dryRunId.value
  const selectedCase = caseId.value
  if (!workflow || !selectedCase) return
  const request = ++previewSequence
  previewBusy.value = true
  preview.value = null
  try {
    const reply = await inboxService.previewWorkflow(
      workflow,
      selectedCase,
      syntheticText.value,
    )
    if (
      request !== previewSequence ||
      workflow !== dryRunId.value ||
      selectedCase !== caseId.value
    )
      return
    preview.value = reply.data.data
  } catch {
    if (request === previewSequence) toast.error(t('inboxRules.previewError'))
  } finally {
    if (request === previewSequence) previewBusy.value = false
  }
}
watch(syntheticText, () => {
  previewSequence++
  preview.value = null
  previewBusy.value = false
})
function clone(w: InboxWorkflow) {
  requestDiscard(() => {
    select(w, true, true)
    id.value = ''
    form.value.name = t('inboxQuality.copyName', { name: w.name }).slice(0, 100)
    form.value.enabled = false
  })
}
function move(index: number, delta: number) {
  const items = form.value.actions
  const other = index + delta
  if (other < 0 || other >= items.length) return
  const [item] = items.splice(index, 1)
  items.splice(other, 0, item)
}
function loadLookups() {
  lookupFailed.value = false
  void accountsService
    .list()
    .then((r) => {
      accounts.value = r.data.data?.accounts ?? []
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
  void usersService
    .list()
    .then((r) => {
      users.value = (r.data as any).data?.users ?? (r.data as any).users ?? []
    })
    .catch(() => {
      lookupFailed.value = true
    })
  void tagsService
    .list()
    .then((r) => {
      tags.value = (r.data as any).data?.tags ?? (r.data as any).tags ?? []
    })
    .catch(() => {
      lookupFailed.value = true
    })
  void templatesService
    .list({ status: 'APPROVED', category: 'UTILITY', limit: 100 })
    .then((r) => {
      templates.value =
        (r.data as any).data?.templates ?? (r.data as any).templates ?? []
    })
    .catch(() => {
      lookupFailed.value = true
    })
}
onMounted(async () => {
  if (route.query.tab === 'sla' && auth.hasPermission('accounts', 'write'))
    showTargets.value = true
  await load()
  if (!enabled.value) return
  loadLookups()
})
function pageHistory(delta: number) {
  historyOffset.value = Math.max(0, historyOffset.value + delta)
  void load()
}
</script>

<template>
  <main class="inbox-ui inbox-page">
    <UnsavedChangesDialog
      :open="discardOpen"
      @leave="confirmDiscard"
      @stay="cancelDiscard"
    />
    <PageHeader
      :title="t('inboxRules.title')"
      :description="t('inboxUX.workflowsDescription')"
    >
      <template #actions
        ><Button
          v-if="ready && enabled && !editing && !showTargets"
          variant="outline"
          size="sm"
          :disabled="busy"
          @click="load"
          ><RotateCw class="mr-2 h-4 w-4" aria-hidden="true" />{{
            t('inboxQuality.refresh')
          }}</Button
        ><Button
          v-if="editing"
          form="inbox-workflow-editor"
          type="submit"
          :disabled="busy || !readyToSave"
          >{{ t('common.save') }}</Button
        ><Button
          v-if="ready && enabled && !editing && !showHistory && !showTargets"
          :disabled="busy"
          @click="select()"
          ><Plus class="mr-2 h-4 w-4" aria-hidden="true" />{{
            t(mode === 'rule' ? 'inboxUX.newRule' : 'inboxUX.newMacro')
          }}</Button
        ></template
      >
    </PageHeader>
    <div class="inbox-page-scroll">
      <div class="inbox-container">
        <div v-if="failed" role="alert" class="inbox-notice">
          {{ t('inboxRules.loadError')
          }}<Button variant="outline" size="sm" @click="load">{{
            t('common.retry')
          }}</Button>
        </div>
        <p v-else-if="ready && !enabled">
          <RouterLink to="/settings" class="underline">{{
            t('inboxRules.enableFirst')
          }}</RouterLink>
        </p>
        <template v-else-if="ready && enabled">
          <div v-if="!editing" class="inbox-notice" role="status">
            <ShieldCheck class="h-5 w-5" aria-hidden="true" />
            <div>
              <strong>{{
                t(
                  switchState.enabled
                    ? 'inboxRules.sendsEnabled'
                    : 'inboxRules.sendsHeld',
                )
              }}</strong>
              <p class="inbox-caption">{{ t('inboxUX.replySafety') }}</p>
            </div>
          </div>
          <div v-if="lookupFailed" role="alert" class="inbox-notice">
            {{ t('inboxQueue.loadFiltersError')
            }}<Button variant="outline" size="sm" @click="loadLookups">{{
              t('common.retry')
            }}</Button>
          </div>
          <template v-if="!editing">
            <InboxTabs
              :model-value="
                showTargets ? 'sla' : showHistory ? 'history' : mode
              "
              :label="t('inboxRules.title')"
              :items="[
                { value: 'rule', label: t('inboxRules.rules') },
                { value: 'macro', label: t('inboxRules.macros') },
                { value: 'history', label: t('inboxUX.historyTab') },
                ...(auth.hasPermission('accounts', 'write')
                  ? [{ value: 'sla', label: t('inboxUX.responseTargets') }]
                  : []),
              ]"
              @update:model-value="changeTab"
            />
            <InboxSLASettings v-if="showTargets" ref="targetEditor" />
            <template v-else-if="!showHistory">
              <div class="inbox-toolbar">
                <p class="inbox-caption">
                  {{
                    t(
                      mode === 'rule'
                        ? 'inboxUX.rulesHelp'
                        : 'inboxUX.macrosHelp',
                    )
                  }}
                </p>
                <label class="inbox-search"
                  ><Search class="h-4 w-4 shrink-0" aria-hidden="true" /><input
                    v-model="query"
                    :aria-label="t('inboxUX.searchWorkflows')"
                    :placeholder="t('inboxUX.searchWorkflows')"
                /></label>
              </div>
              <div class="inbox-panel">
                <p v-if="busy" class="inbox-empty" role="status">
                  {{ t('common.loading') }}
                </p>
                <ul v-else-if="filtered.length" class="inbox-list">
                  <li v-for="w in filtered" :key="w.id" class="inbox-list-row">
                    <div class="inbox-list-copy">
                      <p class="inbox-list-title">{{ w.name }}</p>
                      <p v-if="w.description" class="inbox-caption mt-1">
                        {{ w.description }}
                      </p>
                      <p class="inbox-caption mt-1">
                        {{
                          w.kind === 'rule'
                            ? t('inboxQuality.ruleOrder', { order: w.position })
                            : t(
                                'inboxQuality.visibility.' +
                                  (w.visibility || 'organization'),
                              )
                        }}
                      </p>
                      <p class="inbox-caption mt-1">
                        {{
                          w.kind === 'rule'
                            ? t('inboxRules.event.' + w.trigger)
                            : t('inboxUX.manualOnly')
                        }}<span class="mx-2" aria-hidden="true">/</span
                        >{{ inboxName(w) }}
                      </p>
                      <p class="inbox-caption">
                        {{
                          w.actions
                            .map((a) => t('inboxRules.action.' + a.type))
                            .join(', ')
                        }}
                      </p>
                    </div>
                    <span
                      v-if="w.kind === 'rule'"
                      class="inbox-badge"
                      :data-state="w.enabled_at ? 'active' : 'disabled'"
                      >{{
                        t(
                          w.enabled_at
                            ? 'inboxRules.enabled'
                            : 'inboxRules.disabled',
                        )
                      }}</span
                    ><span v-else class="inbox-badge">{{
                      t(
                        hasReply(w)
                          ? 'inboxUX.manualReply'
                          : 'inboxUX.internalActions',
                      )
                    }}</span>
                    <div class="flex gap-1">
                      <Button variant="outline" size="sm" @click="select(w)"
                        ><Pencil
                          class="mr-1.5 h-3.5 w-3.5"
                          aria-hidden="true"
                        />{{ t('common.edit') }}</Button
                      ><Button variant="ghost" size="sm" @click="prepare(w)">{{
                        t('inboxRules.dryRun')
                      }}</Button
                      ><Button variant="ghost" size="sm" @click="clone(w)">{{
                        t('inboxQuality.clone')
                      }}</Button>
                    </div>
                  </li>
                </ul>
                <div v-else class="inbox-empty">
                  <Workflow class="h-8 w-8" aria-hidden="true" />
                  <h2>
                    {{
                      t(
                        query
                          ? 'inboxUX.noMatches'
                          : mode === 'rule'
                            ? 'inboxUX.noRules'
                            : 'inboxUX.noMacros',
                      )
                    }}
                  </h2>
                  <p>
                    {{
                      t(
                        mode === 'rule'
                          ? 'inboxUX.rulesEmptyHelp'
                          : 'inboxUX.macrosEmptyHelp',
                      )
                    }}
                  </p>
                  <Button v-if="!query" variant="outline" @click="select()">{{
                    t(mode === 'rule' ? 'inboxUX.newRule' : 'inboxUX.newMacro')
                  }}</Button>
                </div>
              </div>
              <section
                v-if="dryRunId"
                class="inbox-panel mt-5 p-5"
                :aria-label="t('inboxRules.dryRun')"
              >
                <div class="inbox-toolbar">
                  <h2 class="inbox-section-heading">
                    {{ t('inboxRules.dryRun') }}
                  </h2>
                  <Button size="sm" variant="ghost" @click="closePreview">{{
                    t('common.close')
                  }}</Button>
                </div>
                <p class="inbox-caption mb-4">
                  {{ t('inboxRules.dryRunHelp') }}
                </p>
                <form
                  class="mb-3 flex flex-wrap items-end gap-2"
                  @submit.prevent="loadCases()"
                >
                  <label class="inbox-label min-w-0 flex-1"
                    >{{ t('inboxFollowup.search')
                    }}<input
                      v-model="caseSearch"
                      type="search"
                      maxlength="120"
                      :placeholder="t('inboxFollowup.searchHelp')"
                  /></label>
                  <Button
                    type="submit"
                    variant="outline"
                    size="sm"
                    :disabled="casesBusy"
                    >{{ t('common.search') }}</Button
                  >
                </form>
                <p v-if="casesBusy" role="status" class="inbox-caption">
                  {{ t('common.loading') }}
                </p>
                <p v-if="casesFailed" role="alert" class="inbox-notice">
                  {{ t('inboxFollowup.casesError') }}
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    @click="loadCases(caseOffset)"
                    >{{ t('common.retry') }}</Button
                  >
                </p>
                <label class="inbox-label"
                  >{{ t('inboxRules.chooseCase')
                  }}<select
                    v-model="caseId"
                    :disabled="casesBusy"
                    @change="preview = null"
                  >
                    <option value="">{{ t('inboxRules.chooseCase') }}</option>
                    <option v-for="c in cases" :key="c.id" :value="c.id">
                      {{ c.contact_name || t('inboxQueue.unnamed') }} ({{
                        c.account_name
                      }}) · {{ c.id.slice(0, 8) }}
                    </option>
                  </select></label
                >
                <div v-if="caseOffset || casesMore" class="my-3 flex gap-2">
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    :disabled="!caseOffset || casesBusy"
                    @click="loadCases(Math.max(0, caseOffset - 50))"
                    >{{ t('inboxQueue.previous') }}</Button
                  ><Button
                    type="button"
                    size="sm"
                    variant="outline"
                    :disabled="!casesMore || casesBusy"
                    @click="loadCases(caseOffset + 50)"
                    >{{ t('inboxQueue.next') }}</Button
                  >
                </div>
                <p
                  v-if="!cases.length && !casesBusy && !casesFailed"
                  class="inbox-caption mt-2"
                >
                  {{ t('inboxUX.noCasesHelp') }}
                </p>
                <label
                  v-if="
                    list.find((w) => w.id === dryRunId)?.conditions
                      .message_contains
                  "
                  class="inbox-label mt-3"
                  >{{ t('inboxQuality.syntheticText')
                  }}<textarea
                    v-model="syntheticText"
                    maxlength="4096"
                    rows="2"
                  />
                </label>
                <Button
                  class="my-3"
                  variant="outline"
                  :disabled="!caseId || previewBusy || casesBusy"
                  @click="runPreview"
                  >{{ t('inboxRules.review') }}</Button
                ><InboxWorkflowPreviewPanel
                  v-if="preview"
                  :preview="preview"
                  :teams="teams"
                  :users="users"
                />
              </section>
              <details class="mt-6">
                <summary class="cursor-pointer text-sm font-medium">
                  {{ t('inboxUX.replyControls') }}
                </summary>
                <div class="inbox-notice mt-3">
                  <div class="flex-1">
                    <p>
                      {{
                        t(
                          !switchState.dispatch_available
                            ? 'inboxRules.dispatchHeld'
                            : switchState.enabled
                              ? 'inboxRules.sendsEnabled'
                              : 'inboxRules.sendsHeld',
                        )
                      }}
                    </p>
                    <p class="inbox-caption">
                      {{ t('inboxUX.replyControlHelp') }}
                    </p>
                  </div>
                  <Button
                    variant="outline"
                    :disabled="busy || !switchState.dispatch_available"
                    @click="stopOrStart"
                    >{{
                      t(
                        switchState.enabled
                          ? 'inboxRules.stopSends'
                          : 'inboxRules.allowSends',
                      )
                    }}</Button
                  >
                </div>
              </details>
            </template>
            <div v-else class="inbox-panel">
              <div v-if="!runs.length" class="inbox-empty">
                <History class="h-8 w-8" aria-hidden="true" />
                <h2>{{ t('inboxRules.noRuns') }}</h2>
                <p>{{ t('inboxUX.historyHelp') }}</p>
              </div>
              <ul v-else class="inbox-list">
                <li v-for="run in runs" :key="run.id" class="inbox-list-row">
                  <div class="inbox-list-copy">
                    <p class="inbox-list-title">{{ run.workflow_name }}</p>
                    <p class="inbox-caption mt-1">
                      {{ run.actor_name || t('inboxQuality.formerUser') }} ·
                      {{ run.account_name }}
                    </p>
                    <RouterLink
                      v-if="run.contact_id && run.account_id"
                      class="text-sm underline"
                      :to="{
                        name: 'chat-conversation',
                        params: { contactId: run.contact_id },
                        query: {
                          inbox_account: run.account_name,
                          inbox_account_id: run.account_id,
                          case_id: run.case_id,
                        },
                      }"
                      >{{ run.contact_name || t('inboxQuality.caseLabel') }} ·
                      {{ run.case_id?.slice(0, 8) }}</RouterLink
                    >
                    <details class="mt-2">
                      <summary
                        class="inbox-caption min-h-8 cursor-pointer py-1"
                      >
                        {{ t('inboxQuality.runDetails') }}
                      </summary>
                      <p class="inbox-caption">
                        {{
                          t(
                            run.conditions_matched === undefined
                              ? 'inboxQuality.legacy'
                              : run.conditions_matched
                                ? 'inboxQuality.conditionMet'
                                : 'inboxQuality.conditionNotMet',
                          )
                        }}
                      </p>
                      <ul class="inbox-caption grid gap-1">
                        <li
                          v-for="(condition, index) in run.condition_outcomes"
                          :key="index"
                        >
                          {{
                            condition.label ||
                            t('inboxQuality.conditionName.' + condition.field)
                          }}
                          ·
                          {{
                            t(
                              condition.matched
                                ? 'inboxQuality.conditionMet'
                                : 'inboxQuality.conditionNotMet',
                            )
                          }}
                          ({{
                            t(
                              condition.group === 'any'
                                ? 'inboxQuality.matchAny'
                                : 'inboxQuality.matchAll',
                            )
                          }})
                        </li>
                      </ul>
                      <ul class="inbox-caption grid gap-1">
                        <li
                          v-for="(outcome, index) in run.action_outcomes"
                          :key="index"
                        >
                          {{ t('inboxRules.action.' + outcome.type) }} ·
                          {{ t('inboxQuality.actionState.' + outcome.status) }}
                          · {{ t('inboxQuality.preflight.' + outcome.reason) }}
                        </li>
                      </ul>
                      <p v-if="run.message_status" class="inbox-caption">
                        {{ t('inboxQuality.messageStatus') }}:
                        {{ run.message_status }}
                      </p>
                      <p v-if="run.state === 'unknown'" class="inbox-notice">
                        {{ t('inboxQuality.reconciliationHelp') }}
                      </p>
                    </details>
                    <p class="inbox-caption">
                      {{ new Date(run.created_at).toLocaleString()
                      }}<span v-if="run.outcome">
                        / {{ t('inboxRules.outcome.' + run.outcome) }}</span
                      >
                    </p>
                  </div>
                  <span class="inbox-badge">{{
                    t('inboxRules.runState.' + run.state)
                  }}</span>
                </li>
              </ul>
              <div
                v-if="historyOffset || historyMore"
                class="inbox-subsection flex gap-2"
              >
                <Button
                  variant="outline"
                  size="sm"
                  :disabled="!historyOffset || busy"
                  @click="pageHistory(-50)"
                  >{{ t('inboxQueue.previous') }}</Button
                ><Button
                  variant="outline"
                  size="sm"
                  :disabled="!historyMore || busy"
                  @click="pageHistory(50)"
                  >{{ t('inboxQueue.next') }}</Button
                >
              </div>
            </div>
          </template>
          <template v-else>
            <Button
              variant="ghost"
              size="sm"
              class="mb-4 -ml-2"
              @click="select(undefined, false)"
              ><ArrowLeft class="mr-2 h-4 w-4" aria-hidden="true" />{{
                t('inboxUX.back')
              }}</Button
            >
            <form
              id="inbox-workflow-editor"
              class="inbox-form"
              @submit.prevent="save()"
            >
              <p
                v-if="saveError"
                role="alert"
                class="inbox-notice text-destructive"
              >
                {{ saveError }}
              </p>
              <section class="inbox-form-section">
                <h2>{{ t(id ? 'inboxRules.edit' : 'inboxRules.add') }}</h2>
                <p>
                  {{
                    t(
                      form.kind === 'rule'
                        ? 'inboxUX.ruleEditorHelp'
                        : 'inboxUX.macroEditorHelp',
                    )
                  }}
                </p>
                <label class="inbox-label"
                  >{{ t('inboxRules.name')
                  }}<input
                    v-model.trim="form.name"
                    maxlength="100"
                    required
                    :placeholder="t('inboxUX.nameExample')"
                /></label>
                <label class="inbox-label mt-4"
                  >{{ t('inboxQuality.description')
                  }}<textarea
                    v-model="form.description"
                    rows="2"
                    maxlength="1000"
                  />
                </label>
                <div v-if="form.kind === 'macro'" class="inbox-grid mt-4">
                  <label class="inbox-label"
                    >{{ t('inboxQueue.visibility')
                    }}<select
                      v-model="form.visibility"
                      @change="form.team_id = undefined"
                    >
                      <option
                        v-for="scope in ['personal', 'team', 'organization']"
                        :key="scope"
                        :value="scope"
                      >
                        {{ t('inboxQuality.visibility.' + scope) }}
                      </option>
                    </select></label
                  ><label v-if="form.visibility === 'team'" class="inbox-label"
                    >{{ t('inboxQueue.team')
                    }}<select v-model="form.team_id" required>
                      <option
                        v-for="team in teams"
                        :key="team.id"
                        :value="team.id"
                      >
                        {{ team.name }}
                      </option>
                    </select></label
                  >
                </div>
                <div v-if="form.kind === 'rule'" class="inbox-grid mt-4">
                  <label class="inbox-label"
                    >{{ t('inboxRules.trigger')
                    }}<select v-model="form.trigger">
                      <option
                        v-for="trigger in triggers"
                        :key="trigger"
                        :value="trigger"
                      >
                        {{ t('inboxRules.event.' + trigger) }}
                      </option>
                    </select></label
                  ><label class="inbox-label"
                    >{{ t('inboxRules.position')
                    }}<input
                      v-model.number="form.position"
                      type="number"
                      min="-10000"
                      max="10000"
                      required
                    /><span class="inbox-caption">{{
                      t('inboxUX.orderHelp')
                    }}</span></label
                  >
                </div>
                <label
                  v-if="form.kind === 'rule' && id"
                  class="inbox-check mt-4"
                  ><input v-model="form.enabled" type="checkbox" />{{
                    t('inboxRules.enableRule')
                  }}</label
                >
                <p
                  v-if="form.kind === 'rule' && !id"
                  class="inbox-caption mt-4"
                >
                  {{ t('inboxUX.newRulesDisabled') }}
                </p>
              </section>
              <section class="inbox-form-section">
                <h2>{{ t('inboxRules.conditions') }}</h2>
                <p>{{ t('inboxUX.conditionsHelp') }}</p>
                <div class="inbox-grid">
                  <label class="inbox-label"
                    >{{ t('inboxQueue.account')
                    }}<select v-model="form.conditions.account_id">
                      <option :value="undefined">
                        {{ t('inboxSla.any') }}
                      </option>
                      <option v-for="a in accounts" :key="a.id" :value="a.id">
                        {{ a.name }}
                      </option>
                    </select></label
                  >
                  <label class="inbox-label"
                    >{{ t('inboxQueue.team')
                    }}<select v-model="form.conditions.team_id">
                      <option :value="undefined">
                        {{ t('inboxSla.any') }}
                      </option>
                      <option v-for="a in teams" :key="a.id" :value="a.id">
                        {{ a.name }}
                      </option>
                    </select></label
                  >
                  <label class="inbox-label"
                    >{{ t('inboxWorkflow.caseStatus')
                    }}<select v-model="form.conditions.status">
                      <option :value="undefined">
                        {{ t('inboxSla.any') }}
                      </option>
                      <option v-for="s in statuses" :key="s" :value="s">
                        {{ t('inboxWorkflow.status.' + s) }}
                      </option>
                    </select></label
                  >
                  <label class="inbox-label"
                    >{{ t('inboxWorkflow.casePriority')
                    }}<select v-model="form.conditions.priority">
                      <option :value="undefined">
                        {{ t('inboxSla.any') }}
                      </option>
                      <option v-for="p in priorities" :key="p" :value="p">
                        {{ t('inboxWorkflow.priority.' + p) }}
                      </option>
                    </select></label
                  >
                  <InboxAgentSelect
                    v-model="form.conditions.assignee_id"
                    :users="users"
                    :label="t('inboxQueue.assignee')"
                    :empty-label="t('inboxSla.any')"
                  />
                  <fieldset>
                    <legend class="mb-2 text-xs font-medium">
                      {{ t('contacts.tags') }}
                    </legend>
                    <p v-if="!tags.length" class="inbox-caption">
                      {{ t('inboxUX.noTags') }}
                    </p>
                    <div class="flex flex-wrap gap-3">
                      <label
                        v-for="tag in tags"
                        :key="tag.name"
                        class="inbox-check"
                        ><input
                          v-model="form.conditions.tags"
                          type="checkbox"
                          :value="tag.name"
                        />{{ tag.name }}</label
                      >
                    </div>
                  </fieldset>
                </div>
                <div class="mt-4">
                  <details
                    :open="
                      !!(
                        form.conditions.fields?.length ||
                        form.conditions.predicates?.length
                      )
                    "
                  >
                    <summary class="min-h-10 cursor-pointer py-2">
                      {{ t('inboxReview.advancedConditions') }}
                    </summary>
                    <div class="grid gap-3 py-3">
                      <InboxFieldMatchers
                        :predicate-count="
                          form.conditions.predicates?.length ?? 0
                        "
                        :model-value="form.conditions.fields ?? []"
                        @update:model-value="form.conditions.fields = $event"
                      /><InboxConditionBuilder v-model="form.conditions" />
                    </div>
                  </details>
                  <aside class="inbox-notice mt-4 min-w-0 flex-col break-words">
                    <h3 class="font-medium">
                      {{ t('inboxReview.workflowSummary') }}
                    </h3>
                    <InboxFilterSummary
                      :filters="form.conditions"
                      :accounts="accounts"
                      :teams="teams"
                      :users="users"
                    />
                    <ol
                      class="mt-2 max-h-64 w-full list-inside list-decimal overflow-auto whitespace-pre-wrap break-words"
                      tabindex="0"
                      :aria-label="t('inboxRules.actions')"
                    >
                      <li v-for="(action, index) in form.actions" :key="index">
                        {{ t('inboxRules.action.' + action.type) }}:
                        {{
                          action.type === 'assign'
                            ? users.find((user) => user.id === action.value)
                                ?.full_name || t('inboxUX.agentUnavailable')
                            : action.type === 'assign_team'
                              ? teams.find((team) => team.id === action.value)
                                  ?.name || t('inboxQuality.teamUnavailable')
                              : action.type === 'snooze'
                                ? t('inboxQuality.snoozeDuration', {
                                    minutes: action.duration_minutes,
                                  })
                                : action.type === 'status' ||
                                    action.type === 'priority'
                                  ? t(
                                      'inboxWorkflow.' +
                                        action.type +
                                        '.' +
                                        action.value,
                                    )
                                  : action.type === 'send_template'
                                    ? templates.find(
                                        (template) =>
                                          template.id === action.value,
                                      )?.name || t('inboxUX.chooseTemplate')
                                    : action.value
                        }}
                      </li>
                    </ol>
                    <p class="inbox-caption mt-2">
                      {{ t('inboxQuality.preflightHelp') }}
                    </p>
                  </aside>
                  <label
                    v-if="
                      form.kind === 'rule' &&
                      form.trigger === 'incoming_message'
                    "
                    class="inbox-label mt-4"
                    >{{ t('inboxQuality.messageContains')
                    }}<input
                      v-model="form.conditions.message_contains"
                      maxlength="500"
                    /><span class="inbox-caption">{{
                      t('inboxQuality.messageContainsHelp')
                    }}</span></label
                  >
                </div>
              </section>
              <section class="inbox-form-section">
                <h2>{{ t('inboxRules.actions') }}</h2>
                <p>{{ t('inboxUX.actionsHelp') }}</p>
                <div
                  v-for="(action, i) in form.actions"
                  :key="i"
                  class="inbox-action-row"
                >
                  <span class="inbox-action-number" aria-hidden="true">{{
                    i + 1
                  }}</span
                  ><InboxActionEditor
                    :teams="teams"
                    :model-value="action"
                    @update:model-value="(value) => (form.actions[i] = value)"
                    :users="users"
                    :tags="tags"
                    :templates="templates"
                  />
                  <div class="inbox-action-controls">
                    <Button
                      size="icon"
                      variant="ghost"
                      type="button"
                      :aria-label="t('inboxRules.moveUp')"
                      :disabled="!i"
                      @click="move(i, -1)"
                      ><ArrowUp class="h-4 w-4" aria-hidden="true" /></Button
                    ><Button
                      size="icon"
                      variant="ghost"
                      type="button"
                      :aria-label="t('inboxRules.moveDown')"
                      :disabled="i === form.actions.length - 1"
                      @click="move(i, 1)"
                      ><ArrowDown class="h-4 w-4" aria-hidden="true" /></Button
                    ><Button
                      size="icon"
                      variant="ghost"
                      type="button"
                      :aria-label="t('common.remove')"
                      :disabled="form.actions.length <= 1"
                      @click="form.actions.splice(i, 1)"
                      ><Trash2 class="h-4 w-4" aria-hidden="true"
                    /></Button>
                  </div>
                </div>
                <Button
                  class="mt-3"
                  type="button"
                  variant="outline"
                  :disabled="form.actions.length >= 10"
                  @click="form.actions.push({ type: 'note', value: '' })"
                  ><Plus class="mr-2 h-4 w-4" aria-hidden="true" />{{
                    t('inboxRules.addAction')
                  }}</Button
                >
              </section>
              <footer class="inbox-form-footer">
                <Button type="submit" :disabled="busy || !readyToSave">{{
                  t('common.save')
                }}</Button
                ><Button
                  type="button"
                  variant="outline"
                  :disabled="busy"
                  @click="select(undefined, false)"
                  >{{ t('common.cancel') }}</Button
                ><Button
                  v-if="id"
                  class="ml-auto text-destructive"
                  type="button"
                  variant="ghost"
                  :disabled="busy"
                  @click="deleting = true"
                  >{{ t('common.delete') }}</Button
                >
              </footer>
            </form>
          </template>
        </template>
        <p v-else-if="!failed" role="status" class="inbox-caption">
          {{ t('common.loading') }}
        </p>
      </div>
    </div>
    <AlertDialog v-model:open="deleting"
      ><AlertDialogContent
        ><AlertDialogHeader
          ><AlertDialogTitle
            >{{ t('common.delete') }} {{ form.name }}?</AlertDialogTitle
          ><AlertDialogDescription>{{
            t('inboxUX.deleteHelp')
          }}</AlertDialogDescription></AlertDialogHeader
        ><AlertDialogFooter
          ><AlertDialogCancel>{{ t('common.cancel') }}</AlertDialogCancel
          ><AlertDialogAction :disabled="busy" @click="save(true)">{{
            t('common.delete')
          }}</AlertDialogAction></AlertDialogFooter
        ></AlertDialogContent
      ></AlertDialog
    >
    <AlertDialog v-model:open="confirmSends"
      ><AlertDialogContent
        ><AlertDialogHeader
          ><AlertDialogTitle>{{ t('inboxRules.allowSends') }}</AlertDialogTitle
          ><AlertDialogDescription>{{
            t('inboxUX.allowRepliesConfirm')
          }}</AlertDialogDescription></AlertDialogHeader
        ><AlertDialogFooter
          ><AlertDialogCancel>{{ t('common.cancel') }}</AlertDialogCancel
          ><AlertDialogAction :disabled="busy" @click="stopOrStart">{{
            t('inboxRules.allowSends')
          }}</AlertDialogAction></AlertDialogFooter
        ></AlertDialogContent
      ></AlertDialog
    >
  </main>
</template>
