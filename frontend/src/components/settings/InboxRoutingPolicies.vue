<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, watch } from 'vue'
import { isAxiosError } from 'axios'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Plus, Pencil, Archive } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog'
import ConfirmDialog from '@/components/shared/ConfirmDialog.vue'
import UnsavedChangesDialog from '@/components/shared/UnsavedChangesDialog.vue'
import { useInboxDraftGuard } from '@/composables/useInboxDraftGuard'
import { useInboxViewerContext } from '@/composables/useInboxViewerContext'
import { useAuthStore } from '@/stores/auth'
import type { InboxCapacity } from '@/services/inbox'
import {
  inboxRouting,
  type RoutingPolicy,
  type RoutingPolicyInput,
  type RoutingConfiguration,
  type RoutingStrategy,
  type RoutingApplication,
  type RoutingReview,
} from '@/services/inboxRouting'

const props = defineProps<{
  team: string
  account: string
  agents: InboxCapacity[]
  canWrite?: boolean
  blocked?: boolean
}>()
const emit = defineEmits<{ applied: [] }>()
const { t } = useI18n(),
  auth = useAuthStore(),
  viewer = useInboxViewerContext()
const scope = computed(() =>
  JSON.stringify([viewer.value, props.team, props.account]),
)
const members = computed(() =>
  props.agents
    .map((a) => a.agent_id)
    .sort()
    .join(','),
)
const policies = ref<RoutingPolicy[]>([]),
  configuration = ref<RoutingConfiguration | null>(null)
const accountName = ref(''),
  teamName = ref(''),
  effective = ref<RoutingStrategy>('inherit')
const loading = ref(false),
  failed = ref(false),
  incomplete = ref(false),
  serverWrite = ref(false),
  pending = ref('')
const editing = ref(false),
  conflict = ref(false),
  selected = ref(''),
  error = ref(''),
  baseline = ref('')
const heading = ref<HTMLElement>(),
  editorHeading = ref<HTMLElement>(),
  selectedAgents = ref<string[]>([])
const form = ref<RoutingPolicyInput>(defaults())
const changeCapacity = ref(false),
  activeMaximum = ref(0),
  rateEnabled = ref(false),
  rateMaximum = ref(10),
  windowMinutes = ref(60)
const archiveOpen = ref(false),
  archiveTarget = ref<RoutingPolicy>()
const reviewing = ref(false),
  review = ref<RoutingReview>(),
  reviewInput = ref<RoutingApplication>(),
  reviewError = ref(''),
  applyUnknown = ref(false),
  now = ref(Date.now())
let generation = 0,
  mutation = 0,
  previewGeneration = 0,
  selectionInitialised = false,
  origin = 'routing-policy-new'
let timer: ReturnType<typeof setInterval> | undefined
const canEdit = computed(
  () =>
    !!props.canWrite &&
    serverWrite.value &&
    auth.hasPermission('teams', 'write'),
)
const busy = computed(() => loading.value || !!pending.value)
const chosen = computed(() =>
  policies.value.find((p) => p.id === selected.value),
)
const draft = () =>
  JSON.stringify([
    form.value,
    changeCapacity.value,
    activeMaximum.value,
    rateEnabled.value,
    rateMaximum.value,
    windowMinutes.value,
  ])
const { discardOpen, confirmDiscard, cancelDiscard, requestDiscard } =
  useInboxDraftGuard(() => editing.value && draft() !== baseline.value)
function confirmLeave(action: () => void) {
  if (!pending.value) requestDiscard(action)
}
defineExpose({ confirmLeave })
function defaults(): RoutingPolicyInput {
  return {
    revision: 0,
    name: '',
    description: '',
    strategy: 'inherit',
    max_active: null,
    max_assignments: 0,
    window_seconds: 0,
  }
}
const valid = computed(
  () =>
    !!form.value.name.trim() &&
    form.value.name.length <= 100 &&
    form.value.description.length <= 1000 &&
    (!changeCapacity.value ||
      (Number.isInteger(activeMaximum.value) &&
        activeMaximum.value >= 0 &&
        activeMaximum.value <= 10000)) &&
    (!rateEnabled.value ||
      (Number.isInteger(rateMaximum.value) &&
        rateMaximum.value >= 1 &&
        rateMaximum.value <= 10000 &&
        Number.isInteger(windowMinutes.value) &&
        windowMinutes.value >= 1 &&
        windowMinutes.value <= 1440)),
)
const canReview = computed(
  () =>
    canEdit.value &&
    !busy.value &&
    !failed.value &&
    !incomplete.value &&
    !props.blocked &&
    !!chosen.value &&
    selectedAgents.value.length > 0 &&
    selectedAgents.value.length <= 100 &&
    selectedAgents.value.every((id) =>
      props.agents.some((a) => a.agent_id === id),
    ),
)
const expired = computed(
  () =>
    !review.value ||
    !Number.isFinite(Date.parse(review.value.expires_at)) ||
    now.value >= Date.parse(review.value.expires_at),
)
function resetReview() {
  previewGeneration++
  reviewing.value = false
  review.value = undefined
  reviewInput.value = undefined
  reviewError.value = ''
  applyUnknown.value = false
  if (pending.value === 'preview') pending.value = ''
}
async function load() {
  const request = ++generation,
    context = scope.value
  loading.value = true
  failed.value = false
  try {
    const [list, current] = await Promise.all([
      inboxRouting.list(),
      inboxRouting.configuration(props.team, props.account),
    ])
    if (request !== generation || context !== scope.value) return
    const value = current.data.data
    if (
      value.configuration &&
      (value.configuration.account_id !== props.account ||
        value.configuration.team_id !== props.team)
    )
      throw new Error('routing_configuration_context_changed')
    policies.value = list.data.data.policies
    incomplete.value = list.data.data.has_more
    configuration.value = value.configuration
    accountName.value = value.account_name
    teamName.value = value.team_name
    effective.value = value.effective_strategy
    serverWrite.value = list.data.data.can_write && value.can_write
  } catch {
    if (request === generation && context === scope.value) {
      failed.value = true
      policies.value = []
      configuration.value = null
      accountName.value = ''
      teamName.value = ''
      effective.value = 'inherit'
      serverWrite.value = false
    }
  } finally {
    if (request === generation && context === scope.value) loading.value = false
  }
}
function begin(item?: RoutingPolicy) {
  if (!canEdit.value || busy.value) return
  origin = item ? 'routing-policy-edit' : 'routing-policy-new'
  confirmLeave(async () => {
    form.value = item
      ? {
          revision: item.revision,
          name: item.name,
          description: item.description,
          strategy: item.strategy,
          max_active: item.max_active,
          max_assignments: item.max_assignments,
          window_seconds: item.window_seconds,
        }
      : defaults()
    changeCapacity.value = form.value.max_active !== null
    activeMaximum.value = form.value.max_active ?? 0
    rateEnabled.value = form.value.max_assignments > 0
    rateMaximum.value = form.value.max_assignments || 10
    windowMinutes.value = form.value.window_seconds
      ? form.value.window_seconds / 60
      : 60
    selected.value = item?.id ?? ''
    editing.value = true
    conflict.value = false
    error.value = ''
    baseline.value = draft()
    await nextTick()
    editorHeading.value?.focus()
  })
}
function focusActions() {
  const target = document.getElementById(origin),
    fallback = document.getElementById('routing-policy-new')
  if (target instanceof HTMLButtonElement && !target.disabled) target.focus()
  else if (fallback instanceof HTMLButtonElement && !fallback.disabled)
    fallback.focus()
  else heading.value?.focus()
}
function closeEditor() {
  confirmLeave(async () => {
    editing.value = false
    await nextTick()
    focusActions()
  })
}
function permissionFailure(problem: unknown) {
  if (
    isAxiosError(problem) &&
    [401, 403].includes(problem.response?.status ?? 0)
  ) {
    serverWrite.value = false
    editing.value = false
    form.value = defaults()
    baseline.value = ''
    cancelDiscard()
    resetReview()
    return true
  }
  return false
}
async function save() {
  if (!canEdit.value || busy.value || !valid.value || conflict.value) return
  const request = ++mutation,
    context = scope.value,
    id = selected.value
  pending.value = 'save'
  error.value = ''
  let restore = false
  const input: RoutingPolicyInput = {
    ...form.value,
    name: form.value.name.trim(),
    max_active: changeCapacity.value ? activeMaximum.value : null,
    max_assignments: rateEnabled.value ? rateMaximum.value : 0,
    window_seconds: rateEnabled.value ? windowMinutes.value * 60 : 0,
  }
  try {
    const response = id
      ? await inboxRouting.update(id, input)
      : await inboxRouting.create(input)
    if (request !== mutation || context !== scope.value || !canEdit.value)
      return
    selected.value = response.data.data.policy.id
    editing.value = false
    baseline.value = ''
    await load()
    if (request !== mutation || context !== scope.value) return
    toast.success(t('inboxRouting.saved'))
    restore = true
  } catch (problem) {
    if (request === mutation && context === scope.value) {
      conflict.value = isAxiosError(problem) && problem.response?.status === 409
      error.value = t(
        permissionFailure(problem)
          ? 'inboxRouting.permissionChanged'
          : conflict.value
            ? 'inboxRouting.policyChanged'
            : 'inboxRouting.saveFailed',
      )
    }
  } finally {
    if (request === mutation && context === scope.value) {
      pending.value = ''
      if (restore) {
        await nextTick()
        focusActions()
      }
    }
  }
}
function reloadDraft() {
  confirmLeave(async () => {
    const id = selected.value
    await load()
    if (failed.value) return
    baseline.value = draft()
    const item = policies.value.find((p) => p.id === id)
    if (item) begin(item)
    else editing.value = false
  })
}
async function preview() {
  if (!canReview.value || !chosen.value) return
  const request = ++previewGeneration,
    context = scope.value,
    roster = members.value,
    policy = chosen.value
  const input: RoutingApplication = {
    account_id: props.account,
    team_id: props.team,
    agent_ids: [...selectedAgents.value],
    revision: policy.revision,
  }
  pending.value = 'preview'
  error.value = ''
  reviewError.value = ''
  applyUnknown.value = false
  try {
    const response = await inboxRouting.preview(policy.id, input)
    if (
      request !== previewGeneration ||
      context !== scope.value ||
      roster !== members.value
    )
      return
    const value = response.data.data
    if (
      value.account_id !== input.account_id ||
      value.team_id !== input.team_id ||
      value.policy.id !== policy.id ||
      !value.confirmation ||
      !Number.isFinite(Date.parse(value.expires_at))
    )
      throw new Error('routing_review_context_changed')
    review.value = value
    reviewInput.value = {
      ...input,
      confirmation: value.confirmation,
      expires_at: value.expires_at,
    }
    now.value = Date.now()
    reviewing.value = true
  } catch (problem) {
    if (request === previewGeneration && context === scope.value) {
      error.value = t(
        permissionFailure(problem)
          ? 'inboxRouting.permissionChanged'
          : 'inboxRouting.previewFailed',
      )
    }
  } finally {
    if (request === previewGeneration && context === scope.value)
      pending.value = ''
  }
}
async function apply() {
  if (
    !canEdit.value ||
    busy.value ||
    !review.value ||
    !reviewInput.value ||
    (expired.value && !applyUnknown.value)
  )
    return
  const request = ++mutation,
    context = scope.value,
    id = review.value.policy.id,
    input = JSON.parse(JSON.stringify(reviewInput.value)) as RoutingApplication
  pending.value = 'apply'
  reviewError.value = ''
  try {
    const response = await inboxRouting.apply(id, input)
    if (request !== mutation || context !== scope.value || !canEdit.value)
      return
    if (!response.data.data.applied)
      throw new Error('routing_application_unconfirmed')
    reviewing.value = false
    review.value = undefined
    reviewInput.value = undefined
    applyUnknown.value = false
    emit('applied')
    await load()
    if (request !== mutation || context !== scope.value) return
    toast.success(
      t(
        response.data.data.already_applied
          ? 'inboxRouting.alreadyApplied'
          : 'inboxRouting.applied',
      ),
    )
  } catch (problem) {
    if (request === mutation && context === scope.value) {
      if (permissionFailure(problem))
        error.value = t('inboxRouting.permissionChanged')
      else {
        const stale =
          isAxiosError(problem) &&
          [404, 409, 422].includes(problem.response?.status ?? 0)
        applyUnknown.value = !stale
        reviewError.value = t(
          stale ? 'inboxRouting.reviewChanged' : 'inboxRouting.applyUncertain',
        )
      }
    }
  } finally {
    if (request === mutation && context === scope.value) pending.value = ''
  }
}
function askArchive() {
  if (canEdit.value && !busy.value && chosen.value) {
    archiveTarget.value = chosen.value
    archiveOpen.value = true
  }
}
function reviewAgain() {
  reviewing.value = false
  void preview()
}
async function archive() {
  if (!canEdit.value || busy.value || !archiveTarget.value) return
  const request = ++mutation,
    context = scope.value,
    target = archiveTarget.value
  pending.value = 'archive'
  error.value = ''
  try {
    await inboxRouting.archive(target.id, target.revision)
    if (request !== mutation || context !== scope.value || !canEdit.value)
      return
    archiveOpen.value = false
    archiveTarget.value = undefined
    selected.value = ''
    await load()
    if (request === mutation && context === scope.value)
      toast.success(t('inboxRouting.archived'))
  } catch (problem) {
    if (request === mutation && context === scope.value) {
      archiveOpen.value = false
      error.value = t(
        permissionFailure(problem)
          ? 'inboxRouting.permissionChanged'
          : 'inboxRouting.archiveFailed',
      )
    }
  } finally {
    if (request === mutation && context === scope.value) pending.value = ''
  }
}
function duration(seconds: number) {
  return seconds % 60 === 0
    ? t('inboxRouting.minutes', { count: seconds / 60 })
    : t('inboxRouting.seconds', { count: seconds })
}
function capacity(value: number) {
  return value === 0 ? t('inboxCapacity.unlimited') : String(value)
}
function rate(maximum: number, seconds: number) {
  return maximum
    ? t('inboxRouting.rateSummary', {
        count: maximum,
        window: duration(seconds),
      })
    : t('inboxRouting.rateOff')
}
watch(
  scope,
  () => {
    generation++
    mutation++
    resetReview()
    cancelDiscard()
    editing.value = false
    pending.value = ''
    form.value = defaults()
    baseline.value = ''
    error.value = ''
    selected.value = ''
    policies.value = []
    configuration.value = null
    accountName.value = ''
    teamName.value = ''
    effective.value = 'inherit'
    serverWrite.value = false
    archiveOpen.value = false
    archiveTarget.value = undefined
    selectionInitialised = props.agents.length > 0
    selectedAgents.value =
      props.agents.length <= 100 ? props.agents.map((a) => a.agent_id) : []
    if (props.team && props.account) void load()
  },
  { immediate: true },
)
watch(members, () => {
  resetReview()
  const validIDs = new Set(props.agents.map((a) => a.agent_id))
  if (!selectionInitialised && validIDs.size) {
    selectedAgents.value = validIDs.size <= 100 ? [...validIDs] : []
    selectionInitialised = true
  } else
    selectedAgents.value = selectedAgents.value.filter((id) => validIDs.has(id))
})
watch(selected, () => {
  resetReview()
  error.value = ''
})
watch(canEdit, (allowed) => {
  if (!allowed) {
    editing.value = false
    form.value = defaults()
    baseline.value = ''
    cancelDiscard()
    resetReview()
  }
})
watch(reviewing, (open) => {
  clearInterval(timer)
  if (open) timer = setInterval(() => (now.value = Date.now()), 1000)
})
onUnmounted(() => {
  generation++
  mutation++
  previewGeneration++
  clearInterval(timer)
})
function restoreDialogFocus(event: Event) {
  // Preview disables its opener while awaiting the server, which can move
  // focus to body. Reacquire the current scoped control after close instead
  // of restoring that transient element or a replaced/removed button.
  event.preventDefault()
  void nextTick(() => {
    const control = document.getElementById('routing-policy-review')
    if (control instanceof HTMLElement && !control.matches(':disabled'))
      control.focus()
    else heading.value?.focus()
  })
}
</script>

<template>
  <section class="inbox-subsection" aria-labelledby="routing-policy-heading">
    <UnsavedChangesDialog
      :open="discardOpen"
      @leave="confirmDiscard"
      @stay="cancelDiscard"
    />
    <div class="flex flex-wrap items-center justify-between gap-2">
      <h3
        id="routing-policy-heading"
        ref="heading"
        tabindex="-1"
        class="inbox-section-heading"
      >
        {{ t('inboxRouting.title') }}
      </h3>
      <Button variant="outline" size="sm" :disabled="busy" @click="load">{{
        t('common.refresh')
      }}</Button>
    </div>
    <p class="inbox-caption mt-2">{{ t('inboxRouting.help') }}</p>
    <p
      v-if="!loading && !failed && accountName && teamName"
      class="inbox-caption mt-1"
    >
      {{ teamName }} · {{ accountName }}
    </p>
    <p v-if="loading" role="status" class="inbox-caption mt-3">
      {{ t('common.loading') }}
    </p>
    <div v-else-if="failed" role="alert" class="inbox-notice mt-3">
      {{ t('inboxRouting.loadFailed') }}
      <Button variant="outline" size="sm" @click="load">{{
        t('common.retry')
      }}</Button>
    </div>
    <template v-else>
      <p class="mt-3 text-sm">
        <span class="font-medium">{{ t('inboxRouting.current') }}:</span>
        {{
          configuration
            ? t('inboxRouting.appliedVersion', {
                name: configuration.name,
                revision: configuration.policy_revision,
              })
            : t('inboxRouting.useTeam')
        }}
        · {{ t('inboxRouting.strategy.' + effective) }}
      </p>
      <div v-if="error" role="alert" class="inbox-notice mt-3">
        {{ error }}
        <Button
          v-if="!editing"
          variant="outline"
          size="sm"
          :disabled="busy"
          @click="load"
          >{{ t('common.refresh') }}</Button
        >
      </div>
      <p v-if="incomplete" role="alert" class="inbox-caption mt-3">
        {{ t('inboxRouting.incomplete') }}
      </p>
      <template v-if="canEdit && !editing">
        <div
          class="mt-4 grid min-w-0 grid-cols-1 gap-2 sm:flex sm:flex-wrap sm:items-end"
        >
          <label class="inbox-label min-w-0 w-full sm:w-auto sm:flex-1"
            >{{ t('inboxRouting.choose')
            }}<select v-model="selected" :disabled="busy || incomplete">
              <option value="">
                {{ t('inboxRouting.choosePlaceholder') }}
              </option>
              <option v-if="selected && !chosen" :value="selected" disabled>
                {{ t('inboxRouting.unavailable') }}
              </option>
              <option
                v-for="policy in policies"
                :key="policy.id"
                :value="policy.id"
              >
                {{ policy.name }}
              </option>
            </select></label
          >
          <div class="flex flex-wrap items-center gap-2 sm:contents">
            <Button
              id="routing-policy-new"
              variant="outline"
              :disabled="busy || incomplete"
              @click="begin()"
              ><Plus class="mr-1.5 h-4 w-4" aria-hidden="true" />{{
                t('inboxRouting.new')
              }}</Button
            ><Button
              id="routing-policy-edit"
              variant="outline"
              :disabled="busy || !chosen"
              @click="chosen && begin(chosen)"
              ><Pencil class="mr-1.5 h-4 w-4" aria-hidden="true" />{{
                t('common.edit')
              }}</Button
            ><Button
              variant="ghost"
              size="icon"
              :aria-label="t('inboxRouting.archive')"
              :disabled="busy || !chosen"
              @click="askArchive"
              ><Archive class="h-4 w-4" aria-hidden="true"
            /></Button>
          </div>
        </div>
        <p
          v-if="
            chosen &&
            configuration?.policy_id === chosen.id &&
            configuration.policy_revision !== chosen.revision
          "
          class="inbox-caption mt-2"
        >
          {{ t('inboxRouting.unapplied') }}
        </p>
        <fieldset v-if="chosen" class="mt-4 min-w-0" :disabled="busy">
          <legend class="text-sm font-medium">
            {{ t('inboxRouting.affectedAgents') }}
          </legend>
          <p id="routing-agent-help" class="inbox-caption mt-1">
            {{ t('inboxRouting.agentHelp') }}
          </p>
          <div class="mt-2 flex flex-wrap gap-2">
            <Button
              type="button"
              size="sm"
              variant="outline"
              :disabled="agents.length > 100"
              @click="selectedAgents = agents.map((a) => a.agent_id)"
              >{{ t('inboxRouting.selectAll') }}</Button
            ><Button
              type="button"
              size="sm"
              variant="ghost"
              @click="selectedAgents = []"
              >{{ t('inboxRouting.clearSelection') }}</Button
            >
          </div>
          <ul class="mt-2 grid max-h-48 gap-1 overflow-auto sm:grid-cols-2">
            <li v-for="agent in agents" :key="agent.agent_id">
              <label class="inbox-check min-h-11"
                ><input
                  v-model="selectedAgents"
                  type="checkbox"
                  :value="agent.agent_id"
                  aria-describedby="routing-agent-help"
                />{{ agent.name }}</label
              >
            </li>
          </ul>
          <p v-if="!agents.length" class="inbox-caption">
            {{ t('inboxRouting.noAgents') }}
          </p>
          <p
            v-if="selectedAgents.length > 100"
            role="alert"
            class="text-sm text-destructive"
          >
            {{ t('inboxRouting.tooManyAgents') }}
          </p>
        </fieldset>
        <p v-if="blocked" role="status" class="inbox-caption mt-3">
          {{ t('inboxRouting.unsavedCapacity') }}
        </p>
        <Button
          id="routing-policy-review"
          class="mt-4"
          :disabled="!canReview"
          @click="preview"
          >{{
            t(
              pending === 'preview'
                ? 'inboxRouting.preparing'
                : 'inboxRouting.review',
            )
          }}</Button
        >
      </template>
      <form
        v-else-if="canEdit && editing"
        class="inbox-form mt-4"
        :aria-busy="!!pending"
        @submit.prevent="save"
      >
        <h4 ref="editorHeading" tabindex="-1" class="font-medium">
          {{ t(form.revision ? 'inboxRouting.edit' : 'inboxRouting.new') }}
        </h4>
        <p class="inbox-caption">{{ t('inboxRouting.templateHelp') }}</p>
        <p v-if="conflict" role="alert" class="inbox-notice">
          {{ t('inboxRouting.policyChanged') }}
          <Button
            type="button"
            variant="outline"
            :disabled="busy"
            @click="reloadDraft"
            >{{ t('inboxRouting.reload') }}</Button
          >
        </p>
        <fieldset class="grid min-w-0 gap-4" :disabled="busy">
          <label class="inbox-label"
            >{{ t('common.name')
            }}<input v-model="form.name" required maxlength="100"
          /></label>
          <label class="inbox-label"
            >{{ t('common.description')
            }}<textarea v-model="form.description" rows="2" maxlength="1000" />
          </label>
          <label class="inbox-label"
            >{{ t('inboxRouting.strategyLabel')
            }}<select v-model="form.strategy">
              <option
                v-for="value in [
                  'inherit',
                  'manual',
                  'round_robin',
                  'load_balanced',
                ] as const"
                :key="value"
                :value="value"
              >
                {{ t('inboxRouting.strategy.' + value) }}
              </option>
            </select></label
          >
          <label class="inbox-check"
            ><input v-model="changeCapacity" type="checkbox" />{{
              t('inboxRouting.replaceCapacity')
            }}</label
          >
          <label v-if="changeCapacity" class="inbox-label"
            >{{ t('inboxRouting.activeMaximum')
            }}<input
              v-model.number="activeMaximum"
              type="number"
              min="0"
              max="10000"
              step="1"
              required
              aria-describedby="routing-active-help"
          /></label>
          <p id="routing-active-help" class="inbox-caption">
            {{ t('inboxRouting.activeHelp') }}
          </p>
          <label class="inbox-check"
            ><input v-model="rateEnabled" type="checkbox" />{{
              t('inboxRouting.limitRate')
            }}</label
          >
          <div v-if="rateEnabled" class="inbox-grid">
            <label class="inbox-label"
              >{{ t('inboxRouting.assignmentMaximum')
              }}<input
                v-model.number="rateMaximum"
                type="number"
                min="1"
                max="10000"
                step="1"
                required /></label
            ><label class="inbox-label"
              >{{ t('inboxRouting.windowMinutes')
              }}<input
                v-model.number="windowMinutes"
                type="number"
                min="1"
                max="1440"
                step="1"
                required
            /></label>
          </div>
          <p class="inbox-caption">{{ t('inboxRouting.rateHelp') }}</p>
          <div class="flex flex-wrap gap-2">
            <Button type="submit" :disabled="!valid || conflict">{{
              t(pending === 'save' ? 'common.saving' : 'common.save')
            }}</Button
            ><Button type="button" variant="outline" @click="closeEditor">{{
              t('common.cancel')
            }}</Button>
          </div>
        </fieldset>
      </form>
    </template>
    <ConfirmDialog
      v-model:open="archiveOpen"
      :title="t('inboxRouting.archive')"
      :description="
        t('inboxRouting.archiveHelp', { name: archiveTarget?.name ?? '' })
      "
      :confirm-label="t('inboxRouting.archive')"
      :is-submitting="pending === 'archive'"
      @confirm="archive"
    />
    <Dialog
      :open="reviewing"
      @update:open="
        (value) => {
          if (pending !== 'apply') reviewing = value
        }
      "
      ><DialogContent
        class="inbox-ui max-h-[85dvh] overflow-auto sm:max-w-2xl"
        @close-auto-focus="restoreDialogFocus"
        @interact-outside="pending === 'apply' && $event.preventDefault()"
        ><DialogHeader
          ><DialogTitle>{{ t('inboxRouting.reviewTitle') }}</DialogTitle
          ><DialogDescription>{{
            t('inboxRouting.reviewHelp')
          }}</DialogDescription></DialogHeader
        >
        <template v-if="review"
          ><p class="text-sm font-medium">
            {{ review.team_name }} · {{ review.account_name }}
          </p>
          <p class="inbox-caption">
            {{
              t('inboxRouting.appliedVersion', {
                name: review.policy.name,
                revision: review.policy.revision,
              })
            }}
          </p>
          <dl class="grid gap-x-4 gap-y-1 text-sm sm:grid-cols-2">
            <dt class="text-muted-foreground">
              {{ t('inboxRouting.strategyLabel') }}
            </dt>
            <dd>
              {{
                review.before_strategy
                  ? t('inboxRouting.strategy.' + review.before_strategy)
                  : t('inboxRouting.strategyUnknown')
              }}
              → {{ t('inboxRouting.strategy.' + review.effective_strategy) }}
            </dd>
          </dl>
          <p class="inbox-notice">{{ t('inboxRouting.sharedLimits') }}</p>
          <p v-if="reviewError" role="alert" class="text-sm text-destructive">
            {{ reviewError }}
          </p>
          <p v-else-if="expired" role="status" class="text-sm text-destructive">
            {{ t('inboxRouting.reviewExpired') }}
          </p>
          <ul class="divide-y divide-border">
            <li
              v-for="change in review.changes"
              :key="change.agent_id"
              class="py-3"
            >
              <p class="font-medium">{{ change.name }}</p>
              <dl class="mt-2 grid gap-x-4 gap-y-1 text-sm sm:grid-cols-2">
                <dt class="text-muted-foreground">
                  {{ t('inboxRouting.activeMaximum') }}
                </dt>
                <dd>
                  {{ capacity(change.before_active) }} →
                  {{ capacity(change.after_active) }}
                </dd>
                <dt class="text-muted-foreground">
                  {{ t('inboxRouting.assignmentRate') }}
                </dt>
                <dd>
                  {{
                    rate(
                      change.before_assignments,
                      change.before_window_seconds,
                    )
                  }}
                  →
                  {{
                    rate(change.after_assignments, change.after_window_seconds)
                  }}
                </dd>
              </dl>
            </li>
          </ul>
          <p class="inbox-caption">
            {{ t('inboxRouting.reviewRechecks') }}
          </p></template
        >
        <DialogFooter
          ><Button
            variant="outline"
            :disabled="pending === 'apply'"
            @click="reviewing = false"
            >{{ t('common.cancel') }}</Button
          ><Button
            v-if="expired && !applyUnknown"
            variant="outline"
            :disabled="busy"
            @click="reviewAgain"
            >{{ t('inboxRouting.reviewAgain') }}</Button
          ><Button
            :disabled="busy || !canEdit || (expired && !applyUnknown)"
            @click="apply"
            >{{
              t(
                pending === 'apply'
                  ? 'inboxRouting.applying'
                  : applyUnknown
                    ? 'inboxRouting.checkApply'
                    : 'inboxRouting.apply',
              )
            }}</Button
          ></DialogFooter
        >
      </DialogContent></Dialog
    >
  </section>
</template>
