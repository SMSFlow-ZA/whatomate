<script setup lang="ts">
import {
  computed,
  getCurrentInstance,
  nextTick,
  onUnmounted,
  ref,
  shallowRef,
  watch,
} from 'vue'
import { onBeforeRouteLeave, onBeforeRouteUpdate } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from '@/components/ui/dialog'
import UnsavedChangesDialog from '@/components/shared/UnsavedChangesDialog.vue'
import InboxIntegrationIntent from '@/components/chat/InboxIntegrationIntent.vue'
import InboxWorkflowPreview from '@/components/chat/InboxWorkflowPreview.vue'
import { useAuthStore } from '@/stores/auth'
import { useInboxViewerContext } from '@/composables/useInboxViewerContext'
import { useInboxDialogFocus } from '@/composables/useInboxDialogFocus'
import { useInboxDraftGuard } from '@/composables/useInboxDraftGuard'
import { useInboxRecoveryDecision } from '@/composables/useInboxRecoveryDecision'
import {
  inboxIntegrations,
  type IntegrationControlReview,
} from '@/services/inboxIntegrations'

const props = defineProps<{ request?: IntegrationControlReview }>()
const emit = defineEmits<{ close: []; changed: [runId: string] }>()
const { t, te } = useI18n()
const auth = useAuthStore(),
  viewer = useInboxViewerContext()
const decision = useInboxRecoveryDecision(() => viewer.value)
const selected = shallowRef<IntegrationControlReview>()
const open = ref(false),
  loading = ref(false),
  failed = ref(false),
  reason = ref('')
const closeBlocked = ref(false),
  investigated = ref(false)
const pendingHeading = ref<HTMLElement>()
const reasonHelpId = 'inbox-recovery-reason-help-' + getCurrentInstance()!.uid
const reasonErrorId = reasonHelpId + '-error'
const reasonError = computed(
  () => /[\r\n\0]/.test(reason.value) || Array.from(reason.value).length > 1000,
)
const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone
type RetryPreview = Awaited<
  ReturnType<typeof inboxIntegrations.retryPreview>
>['data']['data']
type CancelPreview = Awaited<
  ReturnType<typeof inboxIntegrations.cancelPreview>
>['data']['data']
type SequencePreview = Awaited<
  ReturnType<typeof inboxIntegrations.sequencePreview>
>['data']['data']
const retry = shallowRef<RetryPreview>(),
  cancellation = shallowRef<CancelPreview>(),
  sequence = shallowRef<SequencePreview>()
let generation = 0
const operator = computed(() =>
  ['settings.general', 'chat', 'custom_actions'].every((resource) =>
    auth.hasPermission(resource, 'write'),
  ),
)
const allowed = computed(() =>
  selected.value?.kind === 'resume'
    ? auth.hasPermission('chat', 'write') &&
      (selected.value.run.actor_id === auth.user?.id ||
        auth.hasPermission('settings.general', 'write'))
    : operator.value,
)
const locked = computed(() => !!decision.pending.value)
const target = computed(() =>
  selected.value?.kind === 'retry'
    ? selected.value.execution.id
    : selected.value?.run.id,
)
const result = computed(() => {
  if (selected.value?.kind === 'retry') return retry.value
  if (selected.value?.kind !== 'resume') return cancellation.value
  const preview = sequence.value
  if (!preview) return undefined
  return {
    eligible:
      preview.actions_eligible === true &&
      (preview.send_will_be_skipped || !preview.send_unavailable),
    confirmation: preview.confirmation,
    reason:
      preview.actions_eligible !== true
        ? 'remaining_actions_blocked'
        : preview.send_unavailable_reason,
  }
})
const reviewKind = computed(() => selected.value?.kind || 'cancel')
const titleKey = computed(() => 'inboxRecovery.' + reviewKind.value + 'Title')
const helpKey = computed(() => 'inboxRecovery.' + reviewKind.value + 'Help')
const confirmKey = computed(
  () =>
    'inboxRecovery.confirm' +
    reviewKind.value[0]!.toUpperCase() +
    reviewKind.value.slice(1),
)
const retryIntent = computed(() => {
  const intent = retry.value?.intent
  return selected.value?.kind === 'retry' &&
    intent?.id === selected.value.execution.integration_id &&
    intent.id !== '00000000-0000-0000-0000-000000000000' &&
    intent.name &&
    intent.method &&
    intent.destination
    ? intent
    : undefined
})
const retryGuarantee = computed(
  () =>
    retry.value?.same_operation === true &&
    retryIntent.value?.available === true,
)
const eligible = computed(
  () =>
    allowed.value &&
    !loading.value &&
    !failed.value &&
    !!result.value?.eligible &&
    !!result.value.confirmation &&
    (selected.value?.kind !== 'retry' || retryGuarantee.value) &&
    !!reason.value.trim() &&
    !/[\r\n\0]/.test(reason.value) &&
    Array.from(reason.value).length <= 1000 &&
    !locked.value,
)
const restoreFocus = useInboxDialogFocus(open)
function preventPendingLeave() {
  if (!locked.value) return
  closeBlocked.value = true
  return false
}
onBeforeRouteLeave(preventPendingLeave)
onBeforeRouteUpdate(preventPendingLeave)
const { discardOpen, requestDiscard, confirmDiscard, cancelDiscard } =
  useInboxDraftGuard(() => open.value && !locked.value && !!reason.value.trim())
function beforeUnload(event: BeforeUnloadEvent) {
  if (locked.value) {
    event.preventDefault()
    event.returnValue = ''
  }
}
window.addEventListener('beforeunload', beforeUnload)
onUnmounted(() => {
  generation++
  window.removeEventListener('beforeunload', beforeUnload)
})
function clear() {
  cancelDiscard()
  generation++
  selected.value = undefined
  retry.value = undefined
  cancellation.value = undefined
  sequence.value = undefined
  reason.value = ''
  loading.value = false
  failed.value = false
  investigated.value = false
  closeBlocked.value = false
  open.value = false
}
function close() {
  if (locked.value) {
    closeBlocked.value = true
    return
  }
  requestDiscard(() => {
    clear()
    decision.reset()
    emit('close')
  })
}
watch(
  viewer,
  () => {
    clear()
    emit('close')
  },
  { flush: 'sync' },
)
watch(
  () => props.request,
  (request) => {
    if (!request) {
      if (!locked.value) clear()
      return
    }
    if (locked.value) {
      closeBlocked.value = true
      return
    }
    const apply = () => {
      decision.reset()
      selected.value = JSON.parse(
        JSON.stringify(request),
      ) as IntegrationControlReview
      reason.value = ''
      investigated.value = false
      closeBlocked.value = false
      open.value = true
      void loadPreview()
    }
    requestDiscard(apply)
  },
  { immediate: true },
)
async function loadPreview() {
  const selection = selected.value
  if (!selection || locked.value) return
  const request = ++generation,
    context = viewer.value
  retry.value = undefined
  cancellation.value = undefined
  sequence.value = undefined
  loading.value = true
  failed.value = false
  try {
    if (!allowed.value) throw new Error('Current review access required')
    if (selection.kind === 'resume') {
      const { data } = await inboxIntegrations.sequencePreview(selection.run.id)
      if (request !== generation || context !== viewer.value) return
      const preview = data.data
      if (
        preview.run_id !== selection.run.id ||
        !preview.case_id ||
        (selection.run.case_id && preview.case_id !== selection.run.case_id) ||
        !Number.isInteger(preview.next_action) ||
        preview.next_action < 0 ||
        !Array.isArray(preview.actions) ||
        !preview.actions.length ||
        typeof preview.send_will_be_skipped !== 'boolean' ||
        typeof preview.send_unavailable !== 'boolean' ||
        typeof preview.actions_eligible !== 'boolean' ||
        typeof preview.recipient?.name !== 'string' ||
        typeof preview.recipient?.phone !== 'string' ||
        typeof preview.account !== 'string' ||
        !preview.account ||
        !/^[a-f0-9]{64}$/.test(preview.confirmation)
      )
        throw new Error('Inconsistent remaining-workflow review')
      sequence.value = preview
    } else if (selection.kind === 'retry') {
      const { data } = await inboxIntegrations.retryPreview(
        selection.execution.id,
      )
      if (request === generation && context === viewer.value)
        retry.value = data.data
    } else {
      const { data } = await inboxIntegrations.cancelPreview(selection.run.id)
      if (request === generation && context === viewer.value)
        cancellation.value = data.data
    }
  } catch {
    if (request === generation && context === viewer.value) failed.value = true
  } finally {
    if (request === generation) loading.value = false
  }
}
function unavailableReason() {
  const key = 'inboxRecovery.reason.' + result.value?.reason
  if (te?.(key)) return t(key)
  const shared = 'inboxIntegrations.reason.' + result.value?.reason
  return te?.(shared) ? t(shared) : t('inboxRecovery.unavailable')
}
async function confirm() {
  const selection = selected.value,
    confirmation = result.value?.confirmation
  if (!selection || !target.value || !confirmation || !eligible.value) return
  await decision.begin({
    kind: selection.kind,
    target: target.value,
    input: { confirmation, reason: reason.value.trim() },
  })
}
watch(decision.confirmedId, (id) => {
  if (!id || !selected.value) return
  const run = selected.value.run.id
  clear()
  decision.reset()
  emit('close')
  emit('changed', run)
})
watch(decision.phase, (phase) => {
  if (phase !== 'unconfirmed') return
  const context = viewer.value
  void nextTick(() => {
    if (context !== viewer.value || decision.phase.value !== 'unconfirmed')
      return
    pendingHeading.value?.focus({ preventScroll: true })
    pendingHeading.value?.scrollIntoView?.({ block: 'start', behavior: 'auto' })
  })
})
async function freshReview() {
  if (!investigated.value || !decision.pending.value) return
  if (!decision.discardAfterReview(decision.pending.value.operation_id)) return
  investigated.value = false
  closeBlocked.value = false
  await loadPreview()
}
function focusTitle(event: Event) {
  event.preventDefault()
  void nextTick(() =>
    document.querySelector<HTMLElement>('[data-inbox-recovery-title]')?.focus(),
  )
}
defineExpose({
  pending: locked,
  confirmLeave: (action: () => void) => {
    if (!locked.value) requestDiscard(action)
    else closeBlocked.value = true
  },
})
</script>
<template>
  <Dialog
    :open="open"
    @update:open="
      (value) => {
        if (!value) close()
      }
    "
  >
    <DialogContent
      class="inbox-ui max-h-[90dvh] max-w-2xl overflow-y-auto [&>button:last-child]:min-h-11 [&>button:last-child]:min-w-11 [&>button:last-child]:right-2 [&>button:last-child]:top-2 [&>button:last-child]:flex [&>button:last-child]:items-center [&>button:last-child]:justify-center"
      @open-auto-focus="focusTitle"
      @close-auto-focus="restoreFocus"
      @escape-key-down="locked && $event.preventDefault()"
      @interact-outside="locked && $event.preventDefault()"
    >
      <DialogHeader>
        <DialogTitle tabindex="-1" data-inbox-recovery-title class="pr-10">{{
          t(titleKey)
        }}</DialogTitle>
        <DialogDescription>{{ t(helpKey) }}</DialogDescription>
      </DialogHeader>
      <template v-if="selected">
        <div class="grid gap-1 text-sm">
          <p class="font-semibold break-words">
            {{ selected.run.workflow_name }}
          </p>
          <p class="inbox-caption break-words">
            {{ selected.run.contact_name || t('inboxQuality.caseLabel') }} ·
            {{ selected.run.account_name }}
          </p>
          <p class="inbox-caption break-all">
            {{
              t(
                selected.kind === 'retry'
                  ? 'inboxIntegrationHistory.operationId'
                  : 'inboxIntegrationHistory.runId',
              )
            }}: {{ target }}
          </p>
        </div>
        <p v-if="loading" role="status">{{ t('common.loading') }}</p>
        <div v-if="failed" role="alert" class="grid gap-2">
          <p>{{ t('inboxRecovery.previewError') }}</p>
          <Button
            type="button"
            variant="outline"
            class="justify-self-start"
            :disabled="loading || locked"
            @click="loadPreview"
            >{{ t('common.retry') }}</Button
          >
        </div>
        <template v-if="result">
          <p v-if="!result.eligible" role="status" class="inbox-notice">
            {{ unavailableReason() }}
          </p>
          <p
            v-if="retry?.eligible && !retryGuarantee"
            role="alert"
            class="inbox-notice"
          >
            {{ t('inboxRecovery.originalGuaranteeRequired') }}
          </p>
          <InboxIntegrationIntent v-if="retryIntent" :intent="retryIntent" />
          <div v-if="sequence" class="grid gap-3">
            <p
              v-if="sequence.send_will_be_skipped"
              class="inbox-notice"
              role="status"
            >
              {{ t('inboxRecovery.sendSkipped') }}
            </p>
            <p class="inbox-caption">
              {{ t('inboxRecovery.remainingReviewHelp') }}
            </p>
            <InboxWorkflowPreview
              purpose="sequence"
              :preview="sequence"
              :start-index="sequence.next_action"
              :send-will-be-skipped="sequence.send_will_be_skipped"
            />
          </div>
          <p v-if="retry?.retry_until" class="inbox-caption">
            {{ t('inboxIntegrationHistory.retryUntil') }}:
            <time :datetime="retry.retry_until">{{
              new Date(retry.retry_until).toLocaleString()
            }}</time>
            · {{ timezone }}
          </p>
          <p
            v-if="cancellation?.provider_outcome_unresolved"
            class="inbox-notice"
          >
            {{ t('inboxRecovery.unresolvedCancel') }}
          </p>
          <p v-if="cancellation?.submission_in_progress" class="inbox-notice">
            {{ t('inboxRecovery.inFlightCancel') }}
          </p>
          <label class="inbox-label"
            ><span class="flex flex-wrap items-baseline gap-1"
              >{{ t('inboxRecovery.reasonLabel') }}
              <span aria-hidden="true" class="inbox-caption"
                >({{ t('common.required') }})</span
              > </span
            ><textarea
              v-model="reason"
              required
              aria-required="true"
              maxlength="1000"
              rows="3"
              :disabled="locked"
              :aria-invalid="reasonError"
              :aria-describedby="
                reasonError ? reasonHelpId + ' ' + reasonErrorId : reasonHelpId
              "
            />
          </label>
          <p :id="reasonHelpId" class="inbox-caption">
            {{ t('inboxRecovery.reasonHelp') }}
          </p>
          <p v-if="reasonError" :id="reasonErrorId" role="alert">
            {{ t('inboxRecovery.reasonError') }}
          </p>
        </template>
        <p v-if="decision.phase.value === 'invalid'" role="alert">
          {{ t('inboxRecovery.preparationError') }}
        </p>
        <section
          v-if="locked"
          class="grid gap-3 rounded-lg border border-border p-3"
          aria-live="polite"
        >
          <h3 ref="pendingHeading" tabindex="-1" class="font-semibold">
            {{
              t(
                decision.phase.value === 'submitting'
                  ? 'inboxRecovery.recording'
                  : 'inboxRecovery.unconfirmedTitle',
              )
            }}
          </h3>
          <p>{{ t('inboxRecovery.unconfirmedHelp') }}</p>
          <p class="inbox-caption break-all">
            {{ t('inboxIntegrationHistory.recordId') }}:
            {{ decision.pending.value?.operation_id }}
          </p>
          <p v-if="decision.problem.value === 'conflict'">
            {{ t('inboxRecovery.conflict') }}
          </p>
          <p v-if="decision.problem.value === 'access'">
            {{ t('inboxRecovery.accessChanged') }}
          </p>
          <p v-if="closeBlocked" role="alert">
            {{ t('inboxRecovery.closeBlocked') }}
          </p>
          <div class="flex flex-wrap gap-2">
            <Button
              type="button"
              variant="outline"
              :disabled="
                decision.phase.value === 'submitting' || decision.checking.value
              "
              @click="decision.checkRecorded()"
              >{{ t('inboxRecovery.checkRecord') }}</Button
            >
            <Button
              v-if="decision.nextCursor.value"
              type="button"
              variant="outline"
              :disabled="
                decision.phase.value === 'submitting' || decision.checking.value
              "
              @click="decision.checkRecorded(decision.nextCursor.value)"
              >{{ t('inboxRecovery.checkOlder') }}</Button
            >
            <Button
              type="button"
              variant="outline"
              :disabled="
                decision.phase.value === 'submitting' || decision.checking.value
              "
              @click="decision.submitSame()"
              >{{ t('inboxRecovery.repeatDecision') }}</Button
            >
          </div>
          <p v-if="decision.checkFailed.value" role="alert">
            {{ t('inboxRecovery.recordLookupError') }}
          </p>
          <details>
            <summary class="min-h-11 cursor-pointer py-2 text-sm">
              {{ t('inboxRecovery.freshReviewTitle') }}
            </summary>
            <div class="mt-2 grid gap-3">
              <p>{{ t('inboxRecovery.freshReviewHelp') }}</p>
              <label class="inbox-check"
                ><input
                  v-model="investigated"
                  type="checkbox"
                  :disabled="
                    decision.phase.value === 'submitting' ||
                    decision.checking.value
                  "
                />{{ t('inboxRecovery.investigated') }}</label
              >
              <Button
                type="button"
                variant="outline"
                :disabled="
                  !investigated ||
                  decision.phase.value === 'submitting' ||
                  decision.checking.value
                "
                @click="freshReview"
                >{{ t('inboxRecovery.freshReview') }}</Button
              >
            </div>
          </details>
        </section>
        <div
          class="flex flex-wrap justify-end gap-2 border-t border-border pt-4"
        >
          <Button
            type="button"
            variant="outline"
            :disabled="locked"
            @click="close"
            >{{ t('common.cancel') }}</Button
          >
          <Button type="button" :disabled="!eligible" @click="confirm">{{
            t(confirmKey)
          }}</Button>
        </div>
      </template>
    </DialogContent>
  </Dialog>
  <UnsavedChangesDialog
    :open="discardOpen"
    @stay="cancelDiscard"
    @leave="confirmDiscard"
  />
</template>
