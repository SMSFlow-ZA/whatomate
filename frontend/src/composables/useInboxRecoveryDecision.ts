import { computed, onUnmounted, ref, shallowRef, watch } from 'vue'
import {
  inboxIntegrations,
  type IntegrationRecovery,
  type IntegrationRecoveryInput,
} from '@/services/inboxIntegrations'

type Confirmation = { confirmation: string; reason: string }
export type InboxRecoveryCommand =
  | { kind: 'retry'; target: string; input: Confirmation }
  | { kind: 'cancel'; target: string; input: Confirmation }
  | { kind: 'resume'; target: string; input: Confirmation }
  | {
      kind: 'recover'
      target: string
      input: Confirmation & IntegrationRecoveryInput
    }
export type InboxPendingRecovery = InboxRecoveryCommand & {
  operation_id: string
}

const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const nilUUID = '00000000-0000-0000-0000-000000000000'
function freeze<T>(value: T): T {
  if (value && typeof value === 'object') {
    Object.values(value).forEach(freeze)
    Object.freeze(value)
  }
  return value
}
function matches(record: IntegrationRecovery, decision: InboxPendingRecovery) {
  const kind =
    decision.kind === 'cancel'
      ? 'cancel_remaining'
      : decision.kind === 'retry'
        ? 'retry'
        : decision.kind === 'resume'
          ? 'resume_sequence'
          : decision.input.action
  return (
    record.id === decision.operation_id &&
    record.kind === kind &&
    (decision.kind === 'cancel' || decision.kind === 'resume'
      ? record.run_id === decision.target
      : record.execution_id === decision.target)
  )
}

// Owned by the page-level recovery review, not by a collapsible history row.
// The caller protects closing/navigation while pending and requires a fresh
// signed preview after explicitly discarding a rejected/reconciled review.
// Nothing is persisted in storage, URLs or logs; viewer changes erase drafts.
export function useInboxRecoveryDecision(context: () => string) {
  const pending = shallowRef<InboxPendingRecovery | null>(null)
  const phase = ref<
    'idle' | 'submitting' | 'unconfirmed' | 'confirmed' | 'invalid'
  >('idle')
  const problem = ref<'' | 'conflict' | 'access' | 'unconfirmed'>('')
  const confirmedId = ref('')
  const checking = ref(false),
    checkFailed = ref(false),
    nextCursor = ref('')
  let generation = 0
  function clear() {
    generation++
    pending.value = null
    phase.value = 'idle'
    problem.value = ''
    confirmedId.value = ''
    checking.value = false
    checkFailed.value = false
    nextCursor.value = ''
  }
  watch(context, clear, { flush: 'sync' })
  onUnmounted(clear)

  async function submitSame() {
    const decision = pending.value
    if (!decision || phase.value === 'submitting' || checking.value) return
    const request = ++generation,
      scope = context()
    phase.value = 'submitting'
    problem.value = ''
    try {
      const input = { ...decision.input, operation_id: decision.operation_id }
      const response =
        decision.kind === 'retry'
          ? await inboxIntegrations.retry(decision.target, input)
          : decision.kind === 'cancel'
            ? await inboxIntegrations.cancel(decision.target, input)
            : decision.kind === 'resume'
              ? await inboxIntegrations.resume(
                  decision.target,
                  decision.input.confirmation,
                  decision.input.reason,
                  decision.operation_id,
                )
              : await inboxIntegrations.recover(decision.target, {
                  ...decision.input,
                  operation_id: decision.operation_id,
                })
      if (request !== generation || scope !== context()) return
      const record = response.data?.data?.recovery as
        | IntegrationRecovery
        | undefined
      if (!record || !matches(record, decision))
        throw new Error('Unconfirmed acknowledgement')
      confirmedId.value = record.id
      pending.value = null
      phase.value = 'confirmed'
    } catch (error: unknown) {
      if (request !== generation || scope !== context()) return
      const status = (error as { response?: { status?: number } })?.response
        ?.status
      problem.value =
        status === 409
          ? 'conflict'
          : status === 403 || status === 404
            ? 'access'
            : 'unconfirmed'
      phase.value = 'unconfirmed'
      // The exact decision/UUID survives every unconfirmed acknowledgement.
      // Even an access failure does not prove an earlier attempt was absent.
    }
  }
  async function begin(command: InboxRecoveryCommand) {
    if (pending.value || phase.value === 'submitting' || checking.value) return
    if (
      !context() ||
      !uuid.test(command.target) ||
      command.target === nilUUID ||
      !/^[a-f0-9]{64}$/.test(command.input.confirmation) ||
      !command.input.reason.trim() ||
      Array.from(command.input.reason).length > 1000 ||
      /[\r\n\0]/.test(command.input.reason)
    ) {
      phase.value = 'invalid'
      return
    }
    try {
      const copy = JSON.parse(JSON.stringify(command)) as InboxRecoveryCommand
      const operation = crypto.randomUUID()
      pending.value = freeze({ ...copy, operation_id: operation })
    } catch {
      phase.value = 'invalid'
      return
    }
    confirmedId.value = ''
    checkFailed.value = false
    nextCursor.value = ''
    await submitSame()
  }
  async function checkRecorded(cursor?: string) {
    const decision = pending.value
    if (!decision || phase.value === 'submitting' || checking.value) return
    const request = ++generation,
      scope = context()
    checking.value = true
    checkFailed.value = false
    try {
      const { data } =
        decision.kind === 'cancel' || decision.kind === 'resume'
          ? await inboxIntegrations.runRecoveries(decision.target, cursor)
          : await inboxIntegrations.recoveries(decision.target, cursor)
      if (request !== generation || scope !== context()) return
      const record = data.data.recoveries.find((record) =>
        matches(record, decision),
      )
      if (record) {
        confirmedId.value = record.id
        pending.value = null
        phase.value = 'confirmed'
        problem.value = ''
        nextCursor.value = ''
      } else {
        // Absence on a page is not proof that the write did not commit.
        nextCursor.value = data.data.has_more ? data.data.next_cursor : ''
      }
    } catch {
      if (request === generation && scope === context())
        checkFailed.value = true
    } finally {
      if (request === generation) checking.value = false
    }
  }
  function discardAfterReview(operation: string) {
    if (
      !pending.value ||
      operation !== pending.value.operation_id ||
      phase.value === 'submitting' ||
      checking.value
    )
      return false
    clear()
    return true
  }
  function reset() {
    if (pending.value || phase.value === 'submitting' || checking.value)
      return false
    clear()
    return true
  }
  return {
    pending: computed(() => pending.value),
    phase: computed(() => phase.value),
    problem: computed(() => problem.value),
    confirmedId: computed(() => confirmedId.value),
    checking: computed(() => checking.value),
    checkFailed: computed(() => checkFailed.value),
    nextCursor: computed(() => nextCursor.value),
    begin,
    submitSame,
    checkRecorded,
    discardAfterReview,
    reset,
  }
}
