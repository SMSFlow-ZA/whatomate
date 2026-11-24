<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import {
  ArrowUp,
  ArrowDown,
  ArrowLeft,
  Plus,
  Copy,
  Pencil,
  Archive,
  RotateCw,
} from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogCancel,
} from '@/components/ui/alert-dialog'
import UnsavedChangesDialog from '@/components/shared/UnsavedChangesDialog.vue'
import InboxConditionBuilder from '@/components/chat/InboxConditionBuilder.vue'
import InboxResponseTargetsForm from './InboxResponseTargetsForm.vue'
import InboxResponsePolicyPreview from './InboxResponsePolicyPreview.vue'
import { useAuthStore } from '@/stores/auth'
import { useInboxViewerContext } from '@/composables/useInboxViewerContext'
import { useInboxDraftGuard } from '@/composables/useInboxDraftGuard'
import { type InboxSLAPolicy } from '@/services/inbox'
import { teamsService } from '@/services/api'
import {
  responsePolicyService,
  type ResponsePolicy,
  type ResponsePolicyInput,
} from '@/services/inboxResponsePolicies'
import {
  defaultInboxTargets,
  copyInboxTargets,
  inboxCalendarIssues,
} from '@/lib/inboxCalendar'

const props = defineProps<{
  account: string
  defaultTargets?: InboxSLAPolicy
}>()
const emit = defineEmits<{ busy: [value: boolean] }>()
const { t } = useI18n(),
  auth = useAuthStore(),
  viewer = useInboxViewerContext()
const context = computed(() => JSON.stringify([viewer.value, props.account]))
const canRead = computed(() =>
  ['settings.general', 'chat', 'contacts'].every((resource) =>
    auth.hasPermission(resource, 'read'),
  ),
)
const canWrite = computed(
  () => canRead.value && auth.hasPermission('settings.general', 'write'),
)
const rows = ref<ResponsePolicy[]>([]),
  maximum = ref(20),
  loading = ref(false),
  failed = ref(false),
  ready = ref(false)
const busy = ref(false),
  unconfirmed = ref(false),
  conflict = ref(false),
  blocked = ref(false),
  message = ref('')
const editing = ref(''),
  baseline = ref(''),
  archiveRow = ref<ResponsePolicy | null>(null),
  heading = ref<HTMLElement>(),
  listRoot = ref<HTMLElement>()
const freshDraft = (): ResponsePolicyInput => ({
  name: '',
  description: '',
  enabled: false,
  conditions: { match: 'all', predicates: [] },
  targets: copyInboxTargets(
    props.defaultTargets ?? defaultInboxTargets(props.account),
  ),
})
const draft = ref(freshDraft())
const conditionsReady = ref(true)
const locked = computed(
  () =>
    busy.value ||
    loading.value ||
    unconfirmed.value ||
    conflict.value ||
    blocked.value,
)
const { discardOpen, confirmDiscard, cancelDiscard, requestDiscard } =
  useInboxDraftGuard(
    () => !!editing.value && JSON.stringify(draft.value) !== baseline.value,
  )
let listGeneration = 0,
  mutationGeneration = 0,
  teamsGeneration = 0
const previewOpen = ref(false)
const teams = ref<{ id: string; name: string; is_active?: boolean }[]>([]),
  teamSearch = ref(''),
  teamPage = ref(1),
  teamsMore = ref(false),
  teamsBusy = ref(false),
  teamsFailed = ref(false)
const validDraft = computed(
  () =>
    conditionsReady.value &&
    !!draft.value.name.trim() &&
    Array.from(draft.value.name).length <= 100 &&
    !/\p{Cc}/u.test(draft.value.name) &&
    Array.from(draft.value.description).length <= 1000 &&
    !inboxCalendarIssues(draft.value.targets).length,
)
const selectedTeamKnown = computed(
  () =>
    !draft.value.conditions.team_id ||
    teams.value.some((team) => team.id === draft.value.conditions.team_id),
)
function invalidatePreview() {
  previewOpen.value = false
}
function closeEditor() {
  editing.value = ''
  draft.value = freshDraft()
  baseline.value = ''
  invalidatePreview()
}
function confirmLeave(action: () => void) {
  if (!busy.value)
    requestDiscard(() => {
      baseline.value = JSON.stringify(draft.value)
      action()
    })
}
defineExpose({ confirmLeave })
function focusHeading() {
  void nextTick(() => heading.value?.focus())
}
function focusRow(id: string) {
  void nextTick(() => {
    const row = Array.from(
      listRoot.value?.querySelectorAll<HTMLElement>('[data-policy-id]') ?? [],
    ).find((item) => item.dataset.policyId === id)
    if (row) row.querySelector<HTMLButtonElement>('button')?.focus()
    else heading.value?.focus()
  })
}
function edit(row?: ResponsePolicy) {
  if (locked.value || !ready.value) return
  requestDiscard(() => {
    invalidatePreview()
    previewOpen.value = false
    editing.value = row?.id ?? 'new'
    draft.value = row
      ? {
          revision: row.revision,
          name: row.definition.name,
          description: row.definition.description,
          enabled: row.enabled,
          conditions: JSON.parse(JSON.stringify(row.definition.conditions)),
          targets: copyInboxTargets(row.targets),
        }
      : freshDraft()
    draft.value.conditions.match ||= 'all'
    draft.value.conditions.predicates ??= []
    baseline.value = JSON.stringify(draft.value)
    void loadTeams()
    focusHeading()
  })
}
async function load() {
  if (!canRead.value || blocked.value) return false
  const key = context.value,
    generation = ++listGeneration
  loading.value = true
  failed.value = false
  ready.value = false
  try {
    const { data } = await responsePolicyService.list(props.account)
    if (key !== context.value || generation !== listGeneration) return false
    const response = data.data
    if (
      response.policies.some(
        (row) =>
          row.account_id !== props.account ||
          row.definition.policy_id !== row.id,
      )
    )
      throw new Error('Mismatched response policy context')
    rows.value = response.policies
    maximum.value = response.maximum
    ready.value = true
    return true
  } catch (cause) {
    if (key === context.value && generation === listGeneration) {
      rows.value = []
      failed.value = true
      if (
        [401, 403].includes(
          (cause as { response?: { status?: number } })?.response?.status ?? 0,
        )
      ) {
        blocked.value = true
        closeEditor()
      }
    }
    return false
  } finally {
    if (key === context.value && generation === listGeneration)
      loading.value = false
  }
}
function refresh() {
  confirmLeave(() => {
    closeEditor()
    unconfirmed.value = false
    conflict.value = false
    message.value = ''
    invalidatePreview()
    void load()
    focusHeading()
  })
}
type PolicyAck = {
  data: {
    data: {
      policy?: { id: string; account_id: string }
      reordered?: boolean
      archived?: boolean
    }
  }
}
async function mutate(
  action: () => Promise<PolicyAck>,
  kind: 'policy' | 'reordered' | 'archived',
  expectedId?: string,
) {
  if (!canWrite.value || locked.value || !ready.value) return
  const key = context.value,
    account = props.account,
    generation = ++mutationGeneration
  const current = () =>
    key === context.value && generation === mutationGeneration
  busy.value = true
  emit('busy', true)
  message.value = ''
  invalidatePreview()
  let acknowledged = false
  try {
    const response = await action()
    if (!current()) return
    const ack = response.data.data
    if (
      kind === 'policy'
        ? !ack.policy?.id ||
          ack.policy.account_id !== account ||
          (!!expectedId && ack.policy.id !== expectedId)
        : ack[kind] !== true
    )
      throw new Error('Unconfirmed response policy mutation')
    acknowledged = true
    closeEditor()
    archiveRow.value = null
    const loaded = await load()
    if (!current()) return
    if (!loaded) message.value = t('inboxPolicy.savedRefreshFailed')
    else toast.success(t('settings.saved'))
    if (loaded && kind === 'policy' && ack.policy) focusRow(ack.policy.id)
    else if (loaded && kind === 'reordered' && expectedId) focusRow(expectedId)
    else focusHeading()
  } catch (cause) {
    if (!current()) return
    const status = (cause as { response?: { status?: number } })?.response
      ?.status
    if (status === 403 || status === 401) {
      blocked.value = true
      rows.value = []
      closeEditor()
      teams.value = []
      archiveRow.value = null
      listGeneration++
      teamsGeneration++
      message.value = t('inboxPolicy.accessChanged')
    } else if (acknowledged) message.value = t('inboxPolicy.savedRefreshFailed')
    else {
      conflict.value = status === 409
      unconfirmed.value = !status || status >= 500
      message.value = t(
        conflict.value
          ? 'inboxPolicy.conflict'
          : unconfirmed.value
            ? 'inboxPolicy.unconfirmed'
            : 'inboxPolicy.invalid',
      )
    }
  } finally {
    if (current()) {
      busy.value = false
      emit('busy', false)
    }
  }
}
function save() {
  if (!validDraft.value) return
  const input = JSON.parse(JSON.stringify(draft.value)) as ResponsePolicyInput,
    id = editing.value,
    account = props.account
  if (id === 'new')
    void mutate(() => responsePolicyService.create(account, input), 'policy')
  else if (id)
    void mutate(
      () => responsePolicyService.update(account, id, input),
      'policy',
      id,
    )
}
function move(row: ResponsePolicy, direction: number) {
  const index = rows.value.findIndex((item) => item.id === row.id),
    target = index + direction
  if (index < 0 || target < 0 || target >= rows.value.length) return
  const order = [...rows.value]
  ;[order[index], order[target]] = [order[target]!, order[index]!]
  const account = props.account
  void mutate(
    () => responsePolicyService.reorder(account, order),
    'reordered',
    row.id,
  )
}
function archive() {
  const row = archiveRow.value,
    account = props.account
  if (row)
    void mutate(() => responsePolicyService.archive(account, row), 'archived')
}
async function loadTeams(page = 1) {
  const key = context.value,
    search = teamSearch.value,
    generation = ++teamsGeneration
  teamsBusy.value = true
  teamsFailed.value = false
  try {
    const { data } = await teamsService.list({ search, page, limit: 100 })
    if (
      key !== context.value ||
      generation !== teamsGeneration ||
      search !== teamSearch.value
    )
      return
    const payload = (
      data as unknown as { data: { teams: typeof teams.value; total?: number } }
    ).data
    teams.value =
      page === 1
        ? payload.teams
        : [
            ...teams.value,
            ...payload.teams.filter(
              (row) => !teams.value.some((old) => old.id === row.id),
            ),
          ]
    teamPage.value = page
    teamsMore.value =
      (payload.total ?? teams.value.length) > teams.value.length &&
      payload.teams.length > 0
  } catch {
    if (key === context.value && generation === teamsGeneration)
      teamsFailed.value = true
  } finally {
    if (key === context.value && generation === teamsGeneration)
      teamsBusy.value = false
  }
}
watch(
  teamSearch,
  () => {
    teamsGeneration++
    teams.value = []
    teamsMore.value = false
    teamsBusy.value = false
  },
  { flush: 'sync' },
)
watch(
  [context, canRead],
  () => {
    listGeneration++
    mutationGeneration++
    teamsGeneration++
    invalidatePreview()
    cancelDiscard()
    rows.value = []
    ready.value = false
    blocked.value = false
    unconfirmed.value = false
    conflict.value = false
    message.value = ''
    busy.value = false
    emit('busy', false)
    closeEditor()
    archiveRow.value = null
    previewOpen.value = false
    teams.value = []
    teamSearch.value = ''
    if (canRead.value) void load()
  },
  { immediate: true, flush: 'sync' },
)
onUnmounted(() => {
  listGeneration++
  mutationGeneration++
  teamsGeneration++
  emit('busy', false)
})
</script>

<template>
  <section class="mt-5 min-w-0" :aria-busy="loading || busy">
    <UnsavedChangesDialog
      :open="discardOpen"
      @leave="confirmDiscard"
      @stay="cancelDiscard"
    />
    <p v-if="!canRead || blocked" class="inbox-notice" role="alert">
      {{ t('inboxPolicy.accessNeeded') }}
    </p>
    <template v-else>
      <div class="flex flex-wrap items-center gap-2">
        <h3
          ref="heading"
          tabindex="-1"
          class="inbox-section-heading mr-auto focus-visible:outline-2 focus-visible:outline-ring"
        >
          {{
            editing
              ? t(
                  editing === 'new'
                    ? 'inboxPolicy.new'
                    : canWrite
                      ? 'inboxPolicy.edit'
                      : 'inboxPolicy.view',
                )
              : t('inboxPolicy.title')
          }}
        </h3>
        <Button
          v-if="editing"
          type="button"
          variant="outline"
          :disabled="busy"
          @click="
            confirmLeave(() => {
              closeEditor()
              focusHeading()
            })
          "
          ><ArrowLeft class="mr-2 h-4 w-4" aria-hidden="true" />{{
            t('inboxPolicy.back')
          }}</Button
        >
        <template v-else
          ><Button
            type="button"
            variant="outline"
            :disabled="busy || loading"
            @click="refresh"
            ><RotateCw class="mr-2 h-4 w-4" aria-hidden="true" />{{
              t('common.refresh')
            }}</Button
          ><Button
            v-if="canWrite"
            type="button"
            :disabled="locked || !ready || rows.length >= maximum"
            @click="edit()"
            ><Plus class="mr-2 h-4 w-4" aria-hidden="true" />{{
              t('inboxPolicy.new')
            }}</Button
          ></template
        >
      </div>
      <p class="inbox-caption mt-2">{{ t('inboxPolicy.orderHelp') }}</p>
      <p v-if="message" class="inbox-notice mt-3" role="alert">
        {{ message
        }}<Button
          v-if="conflict || unconfirmed"
          type="button"
          variant="outline"
          @click="refresh"
          >{{ t('inboxPolicy.refreshSaved') }}</Button
        >
      </p>
      <p v-if="loading" class="py-4" role="status">{{ t('common.loading') }}</p>
      <div v-else-if="failed" class="inbox-notice mt-3" role="alert">
        {{ t('inboxPolicy.loadError')
        }}<Button variant="outline" @click="refresh">{{
          t('common.retry')
        }}</Button>
      </div>
      <form v-else-if="editing" class="inbox-form" @submit.prevent="save">
        <fieldset class="min-w-0 border-0 p-0" :disabled="locked || !canWrite">
          <section class="inbox-form-section">
            <label class="inbox-label"
              >{{ t('inboxPolicy.name')
              }}<input v-model="draft.name" maxlength="100" required
            /></label>
            <label class="inbox-label mt-3"
              >{{ t('inboxPolicy.description')
              }}<textarea
                v-model="draft.description"
                maxlength="1000"
                rows="2"
              />
            </label>
            <p v-if="editing === 'new'" class="inbox-caption mt-3">
              {{ t('inboxPolicy.startsDisabled') }}
            </p>
            <label v-else class="inbox-check mt-3"
              ><input v-model="draft.enabled" type="checkbox" />{{
                t('inboxPolicy.enabledForNew')
              }}</label
            >
          </section>
          <section class="inbox-form-section">
            <h4 class="inbox-section-heading">
              {{ t('inboxPolicy.matchTitle') }}
            </h4>
            <div class="mt-3 flex flex-wrap items-end gap-2">
              <label class="inbox-label min-w-0 flex-1"
                >{{ t('inboxPolicy.findTeam')
                }}<input
                  v-model="teamSearch"
                  type="search"
                  @keydown.enter.prevent="loadTeams()" /></label
              ><Button
                type="button"
                variant="outline"
                :disabled="teamsBusy"
                @click="loadTeams()"
                >{{ t('common.search') }}</Button
              >
            </div>
            <p v-if="teamsFailed" class="inbox-notice mt-2" role="alert">
              {{ t('inboxPolicy.teamError')
              }}<Button type="button" variant="outline" @click="loadTeams()">{{
                t('common.retry')
              }}</Button>
            </p>
            <label class="inbox-label mt-3"
              >{{ t('inboxPolicy.team')
              }}<select
                v-model="draft.conditions.team_id"
                :disabled="teamsBusy"
              >
                <option :value="undefined">
                  {{ t('inboxPolicy.anyTeam') }}
                </option>
                <option
                  v-if="!selectedTeamKnown"
                  :value="draft.conditions.team_id"
                >
                  {{
                    t('inboxPolicy.recordedTeam', {
                      id: draft.conditions.team_id,
                    })
                  }}
                </option>
                <option
                  v-for="team in teams.filter(
                    (item) => item.is_active !== false,
                  )"
                  :key="team.id"
                  :value="team.id"
                >
                  {{ team.name }}
                </option>
              </select></label
            >
            <p class="inbox-caption mt-2">{{ t('inboxPolicy.teamHelp') }}</p>
            <Button
              v-if="teamsMore"
              type="button"
              class="mt-2"
              variant="outline"
              :disabled="teamsBusy"
              @click="loadTeams(teamPage + 1)"
              >{{ t('inboxPolicy.moreTeams') }}</Button
            >
            <InboxConditionBuilder
              v-model="draft.conditions"
              class="mt-4"
              :allowed-fields="['priority', 'custom_field']"
              :help="t('inboxPolicy.conditionsHelp')"
              @validity="conditionsReady = $event"
            />
            <p
              v-if="
                !draft.conditions.team_id &&
                !draft.conditions.predicates?.length
              "
              class="inbox-notice mt-3"
            >
              {{ t('inboxPolicy.allCasesWarning') }}
            </p>
          </section>
          <InboxResponseTargetsForm
            v-model="draft.targets"
            :disabled="locked || !canWrite"
          />
        </fieldset>
        <footer class="inbox-form-footer">
          <Button
            type="button"
            variant="outline"
            :disabled="busy"
            @click="
              confirmLeave(() => {
                closeEditor()
                focusHeading()
              })
            "
            >{{ t('common.cancel') }}</Button
          ><Button
            v-if="canWrite"
            type="submit"
            :disabled="locked || !ready || !validDraft"
            >{{ busy ? t('inboxPolicy.saving') : t('common.save') }}</Button
          >
        </footer>
      </form>
      <template v-else-if="ready">
        <p v-if="!rows.length" class="py-6 text-sm">
          {{ t('inboxPolicy.empty') }}
        </p>
        <ol
          v-else
          ref="listRoot"
          class="mt-4 divide-y divide-border border-y border-border"
        >
          <li
            v-for="(row, index) in rows"
            :key="row.id"
            :data-policy-id="row.id"
            class="flex min-w-0 flex-wrap items-center gap-3 py-4"
          >
            <span
              class="w-6 shrink-0 text-center text-sm font-medium tabular-nums"
              >{{ index + 1 }}</span
            >
            <div class="min-w-0 flex-1">
              <h4 class="break-words font-medium">{{ row.definition.name }}</h4>
              <p class="inbox-caption mt-1">
                {{
                  row.enabled
                    ? t('inboxPolicy.enabled')
                    : t('inboxPolicy.disabled')
                }}
                ·
                {{
                  t('inboxPolicy.matchCount', {
                    count: row.definition.conditions.predicates?.length ?? 0,
                  })
                }}
              </p>
              <p
                v-if="row.definition.conditions.team_id"
                class="inbox-caption mt-1"
              >
                {{ t('inboxPolicy.teamScope') }}
              </p>
              <p
                v-if="row.definition.description"
                class="inbox-caption mt-1 break-words"
              >
                {{ row.definition.description }}
              </p>
            </div>
            <div class="flex w-full flex-wrap justify-end gap-1 sm:w-auto">
              <Button
                type="button"
                variant="outline"
                :disabled="locked"
                :aria-label="
                  t(
                    canWrite
                      ? 'inboxPolicy.editNamed'
                      : 'inboxPolicy.viewNamed',
                    { name: row.definition.name },
                  )
                "
                @click="edit(row)"
                ><Pencil class="h-4 w-4" aria-hidden="true" /><span
                  class="sr-only"
                  >{{
                    t(canWrite ? 'inboxPolicy.edit' : 'inboxPolicy.view')
                  }}</span
                ></Button
              >
              <template v-if="canWrite">
                <Button
                  type="button"
                  variant="outline"
                  :disabled="locked || index === 0"
                  :aria-label="
                    t('inboxPolicy.moveUp', { name: row.definition.name })
                  "
                  @click="move(row, -1)"
                  ><ArrowUp class="h-4 w-4" aria-hidden="true"
                /></Button>
                <Button
                  type="button"
                  variant="outline"
                  :disabled="locked || index === rows.length - 1"
                  :aria-label="
                    t('inboxPolicy.moveDown', { name: row.definition.name })
                  "
                  @click="move(row, 1)"
                  ><ArrowDown class="h-4 w-4" aria-hidden="true"
                /></Button>
                <Button
                  type="button"
                  variant="outline"
                  :disabled="locked || rows.length >= maximum"
                  :aria-label="
                    t('inboxPolicy.cloneNamed', { name: row.definition.name })
                  "
                  @click="
                    mutate(
                      () => responsePolicyService.clone(account, row),
                      'policy',
                    )
                  "
                  ><Copy class="h-4 w-4" aria-hidden="true"
                /></Button>
                <Button
                  type="button"
                  variant="outline"
                  :disabled="locked"
                  :aria-label="
                    t('inboxPolicy.archiveNamed', { name: row.definition.name })
                  "
                  @click="archiveRow = row"
                  ><Archive class="h-4 w-4" aria-hidden="true"
                /></Button>
              </template>
            </div>
          </li>
        </ol>
        <p class="inbox-caption mt-3">
          {{ t('inboxPolicy.limit', { count: rows.length, maximum }) }}
        </p>
        <Button
          class="mt-4"
          type="button"
          variant="outline"
          :disabled="locked"
          @click="previewOpen = !previewOpen"
          >{{ t('inboxPolicy.preview') }}</Button
        >
        <InboxResponsePolicyPreview v-if="previewOpen" :account="account" />
      </template>
    </template>
    <AlertDialog
      :open="!!archiveRow"
      @update:open="
        (value) => {
          if (!value) archiveRow = null
        }
      "
      ><AlertDialogContent
        ><AlertDialogHeader
          ><AlertDialogTitle>{{
            t('inboxPolicy.archiveTitle')
          }}</AlertDialogTitle
          ><AlertDialogDescription>{{
            t('inboxPolicy.archiveHelp', { name: archiveRow?.definition.name })
          }}</AlertDialogDescription>
          <p v-if="message" class="text-sm text-destructive" role="alert">
            {{ message }}
          </p></AlertDialogHeader
        ><AlertDialogFooter
          ><AlertDialogCancel>{{ t('common.cancel') }}</AlertDialogCancel
          ><Button type="button" :disabled="locked" @click="archive">{{
            t('inboxPolicy.archive')
          }}</Button></AlertDialogFooter
        ></AlertDialogContent
      ></AlertDialog
    >
  </section>
</template>
