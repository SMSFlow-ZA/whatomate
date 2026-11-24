// @vitest-environment happy-dom
import { createApp, reactive } from 'vue'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import {
  useInboxRecoveryDecision,
  type InboxRecoveryCommand,
} from './useInboxRecoveryDecision'

const api = vi.hoisted(() => ({
  retry: vi.fn(),
  cancel: vi.fn(),
  recover: vi.fn(),
  resume: vi.fn(),
  recoveries: vi.fn(),
  runRecoveries: vi.fn(),
}))
vi.mock('@/services/inboxIntegrations', () => ({ inboxIntegrations: api }))
const target = '11111111-1111-4111-8111-111111111111'
const operation = '22222222-2222-4222-8222-222222222222'
const nextOperation = '33333333-3333-4333-8333-333333333333'
const confirmation = 'a'.repeat(64)
const envelope = (data: unknown) => ({ data: { data } })
const record = (kind = 'retry', id = operation) => ({
  id,
  kind,
  execution_id: target,
  run_id: target,
})
const command = (): InboxRecoveryCommand => ({
  kind: 'retry',
  target,
  input: { confirmation, reason: 'Synthetic investigation completed' },
})
function deferred() {
  let resolve!: (value: any) => void
  const promise = new Promise<any>((done) => {
    resolve = done
  })
  return { promise, resolve }
}
const mounted: ReturnType<typeof createApp>[] = []
function mount() {
  const viewer = reactive({ context: 'viewer-a/org-a/permissions-a' })
  let controller!: ReturnType<typeof useInboxRecoveryDecision>
  const app = createApp({
    setup() {
      controller = useInboxRecoveryDecision(() => viewer.context)
      return () => null
    },
  })
  app.mount(document.createElement('div'))
  mounted.push(app)
  return { viewer, controller, app }
}
beforeEach(() => {
  vi.resetAllMocks()
  vi.spyOn(crypto, 'randomUUID').mockReturnValue(operation)
})
afterEach(() => {
  for (const app of mounted.splice(0)) app.unmount()
  vi.restoreAllMocks()
})

it('retains the exact decision and UUID after a lost acknowledgement and replays only explicitly', async () => {
  api.retry
    .mockRejectedValueOnce(new Error('network interrupted'))
    .mockResolvedValueOnce(envelope({ recovery: record() }))
  const { controller } = mount()
  await controller.begin(command())
  expect(controller.phase.value).toBe('unconfirmed')
  expect(controller.pending.value?.operation_id).toBe(operation)
  expect(api.retry).toHaveBeenCalledTimes(1)
  expect(controller.reset()).toBe(false)
  await controller.begin({ ...command(), target: nextOperation })
  expect(api.retry).toHaveBeenCalledTimes(1)
  await controller.submitSame()
  expect(api.retry.mock.calls[1]).toEqual(api.retry.mock.calls[0])
  expect(crypto.randomUUID).toHaveBeenCalledTimes(1)
  expect(controller.phase.value).toBe('confirmed')
  expect(controller.confirmedId.value).toBe(operation)
  expect(controller.pending.value).toBeNull()
})

it('copies and freezes investigated values, preserving false and zero despite later form edits', async () => {
  const first = deferred()
  api.recover
    .mockReturnValueOnce(first.promise)
    .mockResolvedValueOnce(envelope({ recovery: record('confirm_succeeded') }))
  const input: InboxRecoveryCommand = {
    kind: 'recover',
    target,
    input: {
      confirmation,
      reason: 'Synthetic investigated receipt',
      action: 'confirm_succeeded',
      evidence_reference: 'SYNTHETIC-CASE-1',
      result: { score: 0, active: false },
    },
  }
  const { controller } = mount()
  const writing = controller.begin(input)
  expect(controller.phase.value).toBe('submitting')
  await controller.submitSame()
  expect(api.recover).toHaveBeenCalledTimes(1)
  if (input.kind === 'recover') {
    input.input.result!.score = 99
    input.input.evidence_reference = 'CHANGED'
  }
  expect(Object.isFrozen(controller.pending.value!.input)).toBe(true)
  expect(Object.isFrozen((controller.pending.value!.input as any).result)).toBe(
    true,
  )
  first.resolve(
    envelope({
      recovery: { ...record('confirm_succeeded'), id: 'wrong-acknowledgement' },
    }),
  )
  await writing
  expect(controller.phase.value).toBe('unconfirmed')
  await controller.submitSame()
  expect(api.recover.mock.calls[1]).toEqual(api.recover.mock.calls[0])
  expect(api.recover.mock.calls[1]![1].result).toEqual({
    score: 0,
    active: false,
  })
  expect(api.recover.mock.calls[1]![1].evidence_reference).toBe(
    'SYNTHETIC-CASE-1',
  )
})

it('accepts only a matching UUID, decision kind and target from scoped history', async () => {
  api.retry.mockResolvedValue(
    envelope({ recovery: { ...record(), execution_id: nextOperation } }),
  )
  api.recoveries
    .mockResolvedValueOnce(
      envelope({
        recoveries: [
          record('confirm_succeeded'),
          { ...record(), execution_id: nextOperation },
        ],
        has_more: false,
        next_cursor: '',
      }),
    )
    .mockResolvedValueOnce(
      envelope({ recoveries: [record()], has_more: false, next_cursor: '' }),
    )
  const { controller } = mount()
  await controller.begin(command())
  expect(controller.phase.value).toBe('unconfirmed')
  await controller.checkRecorded()
  expect(controller.pending.value).not.toBeNull()
  expect(api.retry).toHaveBeenCalledTimes(1)
  await controller.checkRecorded()
  expect(controller.phase.value).toBe('confirmed')
  expect(controller.confirmedId.value).toBe(operation)
})

it('does not infer absence from a cursor page or lookup error and never dispatches while checking', async () => {
  const reading = deferred()
  api.retry.mockRejectedValue(new Error('unknown acknowledgement'))
  api.recoveries
    .mockResolvedValueOnce(
      envelope({ recoveries: [], has_more: true, next_cursor: 'older' }),
    )
    .mockRejectedValueOnce(new Error('offline'))
    .mockReturnValueOnce(reading.promise)
  const { controller } = mount()
  await controller.begin(command())
  await controller.checkRecorded()
  expect(controller.nextCursor.value).toBe('older')
  expect(controller.pending.value?.operation_id).toBe(operation)
  await controller.checkRecorded('older')
  expect(controller.checkFailed.value).toBe(true)
  expect(controller.pending.value).not.toBeNull()
  const lookup = controller.checkRecorded('older')
  await controller.submitSame()
  expect(api.retry).toHaveBeenCalledTimes(1)
  reading.resolve(
    envelope({ recoveries: [record()], has_more: false, next_cursor: '' }),
  )
  await lookup
  expect(api.recoveries).toHaveBeenLastCalledWith(target, 'older')
  expect(controller.phase.value).toBe('confirmed')
})

it('erases private decisions on a viewer change and ignores an old acknowledgement during a new submission', async () => {
  const old = deferred(),
    current = deferred()
  api.retry
    .mockReturnValueOnce(old.promise)
    .mockReturnValueOnce(current.promise)
  vi.mocked(crypto.randomUUID)
    .mockReturnValueOnce(operation)
    .mockReturnValueOnce(nextOperation)
  const { controller, viewer } = mount()
  const first = controller.begin(command())
  viewer.context = 'viewer-b/org-b/permissions-b'
  expect(controller.pending.value).toBeNull()
  expect(controller.phase.value).toBe('idle')
  const second = controller.begin(command())
  old.resolve(envelope({ recovery: record() }))
  await first
  expect(controller.phase.value).toBe('submitting')
  expect(controller.pending.value?.operation_id).toBe(nextOperation)
  current.resolve(envelope({ recovery: record('retry', nextOperation) }))
  await second
  expect(controller.confirmedId.value).toBe(nextOperation)
})

it('drops late private lookup results after permission changes or unmount', async () => {
  const old = deferred()
  api.retry.mockRejectedValue(new Error('unknown'))
  api.recoveries.mockReturnValueOnce(old.promise)
  const { controller, viewer, app } = mount()
  await controller.begin(command())
  const checking = controller.checkRecorded()
  viewer.context = 'viewer-a/org-a/revoked-permissions'
  app.unmount()
  mounted.splice(mounted.indexOf(app), 1)
  old.resolve(
    envelope({ recoveries: [record()], has_more: false, next_cursor: '' }),
  )
  await checking
  expect(controller.pending.value).toBeNull()
  expect(controller.confirmedId.value).toBe('')
  expect(controller.phase.value).toBe('idle')
})

it('uses run-scoped cancellation history and retains access-error acknowledgements as unconfirmed', async () => {
  api.cancel.mockRejectedValue({
    response: { status: 404, data: 'DO_NOT_RETAIN_ERROR_BODY' },
  })
  api.runRecoveries.mockResolvedValue(
    envelope({
      recoveries: [
        {
          ...record('cancel_remaining'),
          execution_id: '00000000-0000-0000-0000-000000000000',
        },
      ],
      has_more: false,
      next_cursor: '',
    }),
  )
  const { controller } = mount()
  await controller.begin({ ...command(), kind: 'cancel' })
  expect(controller.problem.value).toBe('access')
  expect(controller.pending.value).not.toBeNull()
  await controller.checkRecorded()
  expect(api.runRecoveries).toHaveBeenCalledWith(target, undefined)
  expect(api.recoveries).not.toHaveBeenCalled()
  expect(controller.phase.value).toBe('confirmed')
})

it('does not infer resume acceptance from a completed run and explicitly replays the original frozen decision', async () => {
  api.resume
    .mockResolvedValueOnce(
      envelope({ run: { id: target, state: 'completed' } }),
    )
    .mockResolvedValueOnce(
      envelope({ recovery: record('resume_sequence'), run: { id: target } }),
    )
  const { controller } = mount()
  const input: InboxRecoveryCommand = { ...command(), kind: 'resume' }
  await controller.begin(input)
  expect(controller.phase.value).toBe('unconfirmed')
  expect(controller.pending.value?.operation_id).toBe(operation)
  expect(controller.reset()).toBe(false)
  input.input.reason = 'Later form edit is not approved'
  await controller.begin({ ...input, target: nextOperation })
  expect(api.resume).toHaveBeenCalledTimes(1)
  await controller.submitSame()
  expect(api.resume.mock.calls[1]).toEqual([
    target,
    confirmation,
    'Synthetic investigation completed',
    operation,
  ])
  expect(api.resume.mock.calls[1]).toEqual(api.resume.mock.calls[0])
  expect(crypto.randomUUID).toHaveBeenCalledTimes(1)
  expect(api.retry).not.toHaveBeenCalled()
  expect(api.recover).not.toHaveBeenCalled()
  expect(controller.phase.value).toBe('confirmed')
})

it('finds a resume receipt only by matching UUID, resume kind and run, even when mutations become unavailable', async () => {
  api.resume.mockRejectedValue({ response: { status: 403 } })
  api.runRecoveries
    .mockResolvedValueOnce(
      envelope({
        recoveries: [
          record('cancel_remaining'),
          { ...record('resume_sequence'), run_id: nextOperation },
          record('resume_sequence', nextOperation),
        ],
        has_more: true,
        next_cursor: 'older-resume-decisions',
      }),
    )
    .mockResolvedValueOnce(
      envelope({
        recoveries: [
          {
            ...record('resume_sequence'),
            execution_id: '00000000-0000-0000-0000-000000000000',
          },
        ],
        has_more: false,
        next_cursor: '',
      }),
    )
  const { controller } = mount()
  await controller.begin({ ...command(), kind: 'resume' })
  expect(controller.problem.value).toBe('access')
  await controller.checkRecorded()
  expect(controller.phase.value).toBe('unconfirmed')
  expect(controller.nextCursor.value).toBe('older-resume-decisions')
  await controller.checkRecorded(controller.nextCursor.value)
  expect(api.runRecoveries).toHaveBeenLastCalledWith(
    target,
    'older-resume-decisions',
  )
  expect(api.recoveries).not.toHaveBeenCalled()
  expect(api.resume).toHaveBeenCalledTimes(1)
  expect(controller.confirmedId.value).toBe(operation)
  expect(controller.problem.value).toBe('')
})

it('clears a pending resume on a viewer change and drops a late run-history receipt', async () => {
  const old = deferred()
  api.resume.mockRejectedValue(new Error('acknowledgement lost'))
  api.runRecoveries.mockReturnValueOnce(old.promise)
  const { controller, viewer } = mount()
  await controller.begin({ ...command(), kind: 'resume' })
  const checking = controller.checkRecorded()
  viewer.context = 'viewer-a/org-a/revoked-permissions'
  old.resolve(
    envelope({
      recoveries: [record('resume_sequence')],
      has_more: false,
      next_cursor: '',
    }),
  )
  await checking
  expect(controller.pending.value).toBeNull()
  expect(controller.confirmedId.value).toBe('')
  expect(controller.phase.value).toBe('idle')
  expect(api.resume).toHaveBeenCalledTimes(1)
})

it('never replaces a conflicted decision automatically and requires its exact ID for an explicit reviewed discard', async () => {
  api.retry.mockRejectedValue({ response: { status: 409 } })
  const { controller } = mount()
  await controller.begin(command())
  expect(controller.problem.value).toBe('conflict')
  expect(controller.reset()).toBe(false)
  expect(controller.discardAfterReview(nextOperation)).toBe(false)
  await controller.begin(command())
  expect(api.retry).toHaveBeenCalledTimes(1)
  expect(controller.discardAfterReview(operation)).toBe(true)
  expect(controller.phase.value).toBe('idle')
  expect(api.retry).toHaveBeenCalledTimes(1)
  expect(crypto.randomUUID).toHaveBeenCalledTimes(1)
})

it('rejects malformed targets, confirmations and unsafe reasons before creating any operation', async () => {
  const { controller } = mount()
  for (const input of [
    { ...command(), target: 'https://example.invalid' },
    { ...command(), target: '00000000-0000-0000-0000-000000000000' },
    { ...command(), input: { confirmation: 'bad', reason: 'test' } },
    { ...command(), input: { confirmation, reason: '  ' } },
    { ...command(), input: { confirmation, reason: 'line\nbreak' } },
    { ...command(), input: { confirmation, reason: 'x'.repeat(1001) } },
  ]) {
    await controller.begin(input as InboxRecoveryCommand)
    expect(controller.phase.value).toBe('invalid')
    expect(controller.pending.value).toBeNull()
  }
  expect(crypto.randomUUID).not.toHaveBeenCalled()
  expect(api.retry).not.toHaveBeenCalled()
})
