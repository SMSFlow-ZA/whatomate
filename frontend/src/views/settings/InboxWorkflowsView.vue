<script setup lang="ts">
import InboxFieldMatchers from '@/components/chat/InboxFieldMatchers.vue'
import Modal from '@/components/oat/Modal.vue'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useTeamsStore } from '@/stores/teams'
import { toast } from '@/lib/toast'
import { PageHeader } from '@/components/shared'
import InboxTabs from '@/components/shared/InboxTabs.vue'
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
} from '@/lib/icons'
import InboxActionEditor from '@/components/settings/InboxActionEditor.vue'
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
import InboxWorkflowPreviewPanel from '@/components/chat/InboxWorkflowPreview.vue'

const { t } = useI18n()
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
const dryRunId = ref('')
const preview = ref<InboxWorkflowPreview | null>(null)
function closePreview() {
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
const statuses = ['open', 'pending', 'waiting_customer', 'resolved']
const priorities = ['low', 'normal', 'high', 'urgent']
const triggers = ['case_opened', 'incoming_message', 'status_changed']
const fresh = (): InboxWorkflowInput => ({
  kind: mode.value,
  name: '',
  trigger: mode.value === 'rule' ? 'case_opened' : '',
  position: 0,
  actions: [{ type: 'note', value: '' }],
  conditions: { tags: [] },
})
const form = ref(fresh())
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
        action.value.trim() &&
        (action.type !== 'send_template' || action.consent_confirmed),
    ) &&
    form.value.actions.filter(
      (action) =>
        action.type === 'send_text' || action.type === 'send_template',
    ).length <= 1,
)
async function load() {
  busy.value = true
  try {
    enabled.value = (await inboxService.settings()).data.data.enabled
    if (enabled.value) {
      const [w, r, state] = await Promise.all([
        inboxService.workflowDefinitions(mode.value),
        inboxService.workflowRuns(),
        inboxService.automationState(),
      ])
      list.value = w.data.data.workflows
      runs.value = r.data.data.runs
      switchState.value = state.data.data
    }
    ready.value = true
    failed.value = false
  } catch {
    failed.value = true
  } finally {
    busy.value = false
  }
}
function select(w?: InboxWorkflow, open = true) {
  editing.value = open
  id.value = w?.id ?? ''
  deleting.value = false
  if (w) {
    form.value = {
      kind: w.kind,
      name: w.name,
      trigger: w.trigger ?? '',
      position: w.position,
      actions: w.actions.map((action) => ({ ...action })),
      conditions: {
        ...w.conditions,
        tags: [...(w.conditions.tags ?? [])],
      },
      enabled: !!w.enabled_at,
    }
  } else form.value = fresh()
}
function changeTab(value: string) {
  if (value === 'history') {
    showHistory.value = true
    dryRunId.value = ''
    void load()
  } else void changeMode(value as 'macro' | 'rule')
}
async function changeMode(next: typeof mode.value) {
  mode.value = next
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
    select(undefined, false)
    await load()
    toast.success(t('settings.saved'))
  } catch {
    toast.error(t('inboxRules.saveError'))
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
  try {
    cases.value = (await inboxService.queue({})).data.data.cases
  } catch {
    toast.error(t('inboxRules.previewError'))
  }
}
async function runPreview() {
  preview.value = null
  try {
    preview.value = (
      await inboxService.previewWorkflow(dryRunId.value, caseId.value)
    ).data.data
  } catch {
    toast.error(t('inboxRules.previewError'))
  }
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
  await load()
  if (!enabled.value) return
  loadLookups()
})
</script>

<template>
  <main class="inbox-ui inbox-page">
    <PageHeader
      :title="t('inboxRules.title')"
      :description="t('inboxUX.workflowsDescription')"
    >
      <template #actions
        ><button
          v-if="editing"
          form="inbox-workflow-editor"
          type="submit"
          :disabled="busy || !readyToSave"
          class=""
        >
          {{ t('common.save') }}</button
        ><button
          v-if="ready && enabled && !editing && !showHistory"
          :disabled="busy"
          @click="select()"
          class=""
          type="button"
        >
          <Plus class="mr-2 h-4 w-4" aria-hidden="true" />{{
            t(mode === 'rule' ? 'inboxUX.newRule' : 'inboxUX.newMacro')
          }}
        </button></template
      >
    </PageHeader>
    <div class="inbox-page-scroll">
      <div class="inbox-container">
        <div v-if="failed" role="alert" class="inbox-notice">
          {{ t('inboxRules.loadError')
          }}<button @click="load" class="outline small" type="button">
            {{ t('common.retry') }}
          </button>
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
            }}<button @click="loadLookups" class="outline small" type="button">
              {{ t('common.retry') }}
            </button>
          </div>
          <template v-if="!editing">
            <InboxTabs
              :model-value="showHistory ? 'history' : mode"
              :label="t('inboxRules.title')"
              :items="[
                { value: 'rule', label: t('inboxRules.rules') },
                { value: 'macro', label: t('inboxRules.macros') },
                { value: 'history', label: t('inboxUX.historyTab') },
              ]"
              @update:model-value="changeTab"
            />

            <template v-if="!showHistory">
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
                <ul v-if="filtered.length" class="inbox-list">
                  <li v-for="w in filtered" :key="w.id" class="inbox-list-row">
                    <div class="inbox-list-copy">
                      <p class="inbox-list-title">{{ w.name }}</p>
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
                      <button
                        @click="select(w)"
                        class="outline small"
                        type="button"
                      >
                        <Pencil
                          class="inbox-icon-gap inbox-icon-height-small inbox-icon-width-small"
                          aria-hidden="true"
                        />{{ t('common.edit') }}</button
                      ><button
                        @click="prepare(w)"
                        class="ghost small"
                        type="button"
                      >
                        {{ t('inboxRules.dryRun') }}
                      </button>
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
                  <button
                    v-if="!query"
                    @click="select()"
                    class="outline"
                    type="button"
                  >
                    {{
                      t(
                        mode === 'rule'
                          ? 'inboxUX.newRule'
                          : 'inboxUX.newMacro',
                      )
                    }}
                  </button>
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
                  <button
                    @click="closePreview"
                    class="ghost small"
                    type="button"
                  >
                    {{ t('common.close') }}
                  </button>
                </div>
                <p class="inbox-caption mb-4">
                  {{ t('inboxRules.dryRunHelp') }}
                </p>
                <label class="inbox-label"
                  >{{ t('inboxRules.chooseCase')
                  }}<select v-model="caseId" @change="preview = null">
                    <option value="">{{ t('inboxRules.chooseCase') }}</option>
                    <option v-for="c in cases" :key="c.id" :value="c.id">
                      {{ c.contact_name || t('inboxQueue.unnamed') }} ({{
                        c.account_name
                      }})
                    </option>
                  </select></label
                >
                <p v-if="!cases.length" class="inbox-caption mt-2">
                  {{ t('inboxUX.noCasesHelp') }}
                </p>
                <button
                  class="outline my-3"
                  :disabled="!caseId"
                  @click="runPreview"
                  type="button"
                >
                  {{ t('inboxRules.review') }}</button
                ><InboxWorkflowPreviewPanel
                  v-if="preview"
                  :preview="preview"
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
                  <button
                    :disabled="busy || !switchState.dispatch_available"
                    @click="stopOrStart"
                    class="outline"
                    type="button"
                  >
                    {{
                      t(
                        switchState.enabled
                          ? 'inboxRules.stopSends'
                          : 'inboxRules.allowSends',
                      )
                    }}
                  </button>
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
            </div>
          </template>
          <template v-else>
            <button
              class="ghost small mb-4 -ml-2"
              @click="select(undefined, false)"
              type="button"
            >
              <ArrowLeft class="mr-2 h-4 w-4" aria-hidden="true" />{{
                t('inboxUX.back')
              }}
            </button>
            <form
              id="inbox-workflow-editor"
              class="inbox-form"
              @submit.prevent="save()"
            >
              <section class="inbox-form-section">
                <h2>{{ t(id ? 'inboxRules.edit' : 'inboxRules.add') }}</h2>
                <p>
                  {{
                    t(
                      mode === 'rule'
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
                <div v-if="mode === 'rule'" class="inbox-grid mt-4">
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
                <label v-if="mode === 'rule' && id" class="inbox-check mt-4"
                  ><input v-model="form.enabled" type="checkbox" />{{
                    t('inboxRules.enableRule')
                  }}</label
                >
                <p v-if="mode === 'rule' && !id" class="inbox-caption mt-4">
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
                  <label class="inbox-label"
                    >{{ t('inboxQueue.assignee')
                    }}<select v-model="form.conditions.assignee_id">
                      <option :value="undefined">
                        {{ t('inboxSla.any') }}
                      </option>
                      <option v-for="a in users" :key="a.id" :value="a.id">
                        {{ a.full_name }}
                      </option>
                    </select></label
                  >
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
                <div class="mt-4"></div>
              <InboxFieldMatchers :model-value="form.conditions.fields ?? []" @update:model-value="form.conditions.fields = $event" /></section>
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
                    :model-value="action"
                    @update:model-value="(value) => (form.actions[i] = value)"
                    :users="users"
                    :tags="tags"
                    :templates="templates"
                  />
                  <div class="inbox-action-controls">
                    <button
                      type="button"
                      :aria-label="t('inboxRules.moveUp')"
                      :disabled="!i"
                      @click="move(i, -1)"
                      class="ghost icon"
                    >
                      <ArrowUp class="h-4 w-4" aria-hidden="true" /></button
                    ><button
                      type="button"
                      :aria-label="t('inboxRules.moveDown')"
                      :disabled="i === form.actions.length - 1"
                      @click="move(i, 1)"
                      class="ghost icon"
                    >
                      <ArrowDown class="h-4 w-4" aria-hidden="true" /></button
                    ><button
                      type="button"
                      :aria-label="t('common.remove')"
                      :disabled="form.actions.length <= 1"
                      @click="form.actions.splice(i, 1)"
                      class="ghost icon"
                    >
                      <Trash2 class="h-4 w-4" aria-hidden="true" />
                    </button>
                  </div>
                </div>
                <button
                  class="outline mt-3"
                  type="button"
                  :disabled="form.actions.length >= 10"
                  @click="form.actions.push({ type: 'note', value: '' })"
                >
                  <Plus class="mr-2 h-4 w-4" aria-hidden="true" />{{
                    t('inboxRules.addAction')
                  }}
                </button>
              </section>
              <footer class="inbox-form-footer">
                <button type="submit" :disabled="busy || !readyToSave" class="">
                  {{ t('common.save') }}</button
                ><button
                  type="button"
                  :disabled="busy"
                  @click="select(undefined, false)"
                  class="outline"
                >
                  {{ t('common.cancel') }}</button
                ><button
                  v-if="id"
                  class="ghost ml-auto text-destructive"
                  type="button"
                  :disabled="busy"
                  @click="deleting = true"
                >
                  {{ t('common.delete') }}
                </button>
              </footer>
            </form>
          </template>
        </template>
        <p v-else-if="!failed" role="status" class="inbox-caption">
          {{ t('common.loading') }}
        </p>
      </div>
    </div>
    <Modal
      v-model:open="deleting"
      :title="[String(t('common.delete')), String(form.name), '?'].join(' ')"
      alert
      ><p>{{ t('inboxUX.deleteHelp') }}</p>
      <template #footer
        ><button type="button" class="outline" @click="deleting = false">
          {{ t('common.cancel') }}</button
        ><button type="button" :disabled="busy" @click="save(true)">
          {{ t('common.delete') }}
        </button></template
      ></Modal
    >
    <Modal
      v-model:open="confirmSends"
      :title="[String(t('inboxRules.allowSends'))].join(' ')"
      alert
      ><p>{{ t('inboxUX.allowRepliesConfirm') }}</p>
      <template #footer
        ><button type="button" class="outline" @click="confirmSends = false">
          {{ t('common.cancel') }}</button
        ><button type="button" :disabled="busy" @click="stopOrStart">
          {{ t('inboxRules.allowSends') }}
        </button></template
      ></Modal
    >
  </main>
</template>
