// @vitest-environment happy-dom
import { createApp, h, nextTick, reactive, ref } from 'vue'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import InboxIntegrationReview from './InboxIntegrationReview.vue'
import type { IntegrationControlReview } from '@/services/inboxIntegrations'

const mock = vi.hoisted(() => ({
  auth: undefined as any,
  org: undefined as any,
  leave: [] as (() => unknown)[],
  update: [] as (() => unknown)[],
  retryPreview: vi.fn(),
  cancelPreview: vi.fn(),
  sequencePreview: vi.fn(),
  resume: vi.fn(),
  retry: vi.fn(),
  cancel: vi.fn(),
  recover: vi.fn(),
  recoveries: vi.fn(),
  runRecoveries: vi.fn(),
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => mock.auth }))
vi.mock('@/stores/organizations', () => ({
  useOrganizationsStore: () => mock.org,
}))
vi.mock('@/services/inboxIntegrations', () => ({ inboxIntegrations: mock }))
vi.mock('vue-router', () => ({
  onBeforeRouteLeave: (fn: () => unknown) => mock.leave.push(fn),
  onBeforeRouteUpdate: (fn: () => unknown) => mock.update.push(fn),
}))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key, te: () => true }),
}))
const runId = '11111111-1111-4111-8111-111111111111'
const operationId = '22222222-2222-4222-8222-222222222222'
const otherId = '33333333-3333-4333-8333-333333333333'
const envelope = (data: unknown) => ({ data: { data } })
const proposal = (id = operationId): IntegrationControlReview => ({
  kind: 'retry',
  run: {
    id: runId,
    state: 'sequence_unknown',
    workflow_name: 'Synthetic workflow',
    created_at: '2026-01-01T00:00:00Z',
    account_name: 'Synthetic inbox',
    contact_name: 'Synthetic customer',
  },
  execution: {
    id,
    run_id: runId,
    action_index: 0,
    integration_id: 'definition-a',
    account_id: 'account-a',
    case_id: 'case-a',
    actor_id: 'reviewer',
    state: 'unknown',
    attempt_count: 1,
    created_at: '2026-01-01T00:00:00Z',
  },
})
const retryPreview = (name = 'Synthetic service', changes = {}) =>
  envelope({
    eligible: true,
    confirmation: 'a'.repeat(64),
    same_operation: true,
    retry_until: '2026-01-01T00:10:00Z',
    intent: {
      id: 'definition-a',
      name,
      method: 'POST',
      destination: 'https://service.example.invalid',
      available: true,
      inputs: { count: 0, active: false },
      outputs: [],
    },
    ...changes,
  })
const resumeProposal = (): IntegrationControlReview => ({
  kind: 'resume',
  run: {
    ...proposal().run,
    actor_id: 'reviewer',
    case_id: 'case-a',
    state: 'sequence_review',
    sequenced: true,
    next_action: 2,
  },
})
const sequencePreview = (changes = {}) =>
  envelope({
    run_id: runId,
    case_id: 'case-a',
    next_action: 2,
    matches: true,
    actions_eligible: true,
    confirmation: 'a'.repeat(64),
    send_will_be_skipped: false,
    send_unavailable: false,
    recipient: { name: 'Current synthetic recipient', phone: '+12025550123' },
    account: 'Current synthetic account',
    actions: [{ type: 'send_text', value: 'Authored synthetic reply' }],
    reply: { text: 'Exact reviewed synthetic reply' },
    ...changes,
  })
function deferred() {
  let resolve!: (value: any) => void
  const promise = new Promise<any>((done) => {
    resolve = done
  })
  return { promise, resolve }
}
const mounted: { app: ReturnType<typeof createApp>; root: HTMLElement }[] = []
const settle = async () => {
  for (let i = 0; i < 25; i++) {
    await Promise.resolve()
    await nextTick()
  }
}
function dialog() {
  const found = document.querySelector<HTMLElement>('[role=dialog]')
  expect(found).toBeTruthy()
  return found!
}
function button(label: string, root: ParentNode = dialog()) {
  const element = Array.from(root.querySelectorAll('button')).find(
    (item) => item.textContent?.trim() === label,
  )
  expect(element, label).toBeTruthy()
  return element!
}
async function reason(value = 'Synthetic investigation completed') {
  const field = dialog().querySelector('textarea')!
  field.value = value
  field.dispatchEvent(new Event('input', { bubbles: true }))
  await settle()
}
async function mount(initial: IntegrationControlReview = proposal()) {
  const request = ref<IntegrationControlReview | undefined>(initial),
    changed = vi.fn(),
    closed = vi.fn()
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp({
    render: () =>
      h(InboxIntegrationReview, {
        request: request.value,
        onClose: () => {
          closed()
          request.value = undefined
        },
        onChanged: changed,
      }),
  })
  app.config.globalProperties.$t = (key: string) => key
  app.mount(root)
  mounted.push({ app, root })
  await settle()
  return { request, changed, closed }
}
beforeEach(() => {
  vi.resetAllMocks()
  mock.leave = []
  mock.update = []
  mock.auth = reactive({
    user: { id: 'reviewer', role_id: 'admin', role: { permissions: [] } },
    organizationId: 'org-a',
    allowed: true,
    hasPermission: () => mock.auth.allowed,
  })
  mock.org = reactive({ selectedOrgId: 'org-a' })
  mock.retryPreview.mockResolvedValue(retryPreview())
  mock.sequencePreview.mockResolvedValue(sequencePreview())
  mock.cancelPreview.mockResolvedValue(
    envelope({
      eligible: true,
      confirmation: 'a'.repeat(64),
      provider_outcome_unresolved: true,
      submission_in_progress: true,
    }),
  )
})
afterEach(() => {
  for (const { app, root } of mounted.splice(0)) {
    app.unmount()
    root.remove()
  }
  vi.restoreAllMocks()
})

it('lets an owning agent review and authorise the exact remainder without operator permissions', async () => {
  mock.auth.hasPermission = (resource: string) => resource === 'chat'
  mock.resume.mockImplementation((target, _confirmation, _reason, operation) =>
    Promise.resolve(
      envelope({
        recovery: { id: operation, kind: 'resume_sequence', run_id: target },
      }),
    ),
  )
  const { changed } = await mount(resumeProposal())
  expect(mock.sequencePreview).toHaveBeenCalledWith(runId)
  expect(dialog().textContent).toContain('Current synthetic recipient')
  expect(dialog().textContent).toContain('Current synthetic account')
  expect(dialog().textContent).toContain('Exact reviewed synthetic reply')
  expect(dialog().textContent).not.toContain('inboxRules.matches')
  expect(
    dialog().querySelector('.inbox-action-number')?.textContent?.trim(),
  ).toBe('3')
  expect(button('inboxRecovery.confirmResume').disabled).toBe(true)
  expect(dialog().querySelector('textarea')!.required).toBe(true)
  expect(
    dialog().querySelector('textarea')!.getAttribute('aria-required'),
  ).toBe('true')
  expect(dialog().textContent).toContain('common.required')
  expect(mock.resume).not.toHaveBeenCalled()
  await reason('Reviewed known remaining work')
  button('inboxRecovery.confirmResume').click()
  await settle()
  expect(mock.resume).toHaveBeenCalledWith(
    runId,
    'a'.repeat(64),
    'Reviewed known remaining work',
    expect.stringMatching(/^[a-f0-9-]{36}$/),
  )
  expect(changed).toHaveBeenCalledWith(runId)
  expect(mock.retry).not.toHaveBeenCalled()
  expect(mock.cancel).not.toHaveBeenCalled()
})

it('preserves a skipped reply and deferred input explanation without requiring a new send reservation', async () => {
  mock.sequencePreview.mockResolvedValue(
    sequencePreview({
      send_will_be_skipped: true,
      send_unavailable: true,
      send_unavailable_reason: 'outside_window',
      actions: [{ type: 'integration', value: 'definition-a' }],
      integrations: {
        0: {
          id: 'definition-a',
          name: 'Current service',
          method: 'POST',
          destination: 'https://service.example.invalid',
          available: true,
          inputs: { count: 0, active: false },
          deferred_inputs: { reference: ['case.reference'] },
          outputs: [],
        },
      },
      reply: {
        text: 'Authored reply for context',
        deferred_variables: ['case.reference'],
      },
    }),
  )
  await mount(resumeProposal())
  expect(dialog().textContent).toContain('Authored reply for context')
  expect(dialog().textContent).toContain('inboxRecovery.sendSkipped')
  expect(dialog().textContent).toContain('inboxIntegrations.deferredReply')
  expect(dialog().textContent).toContain('case.reference')
  expect(dialog().textContent).toContain('false')
  expect(dialog().textContent).not.toContain(
    'inboxQuality.sendReason.outside_window',
  )
  await reason()
  expect(button('inboxRecovery.confirmResume').disabled).toBe(false)
  expect(mock.resume).not.toHaveBeenCalled()
})

it('keeps exact authored intent visible when the reserved reply or another remaining action is blocked', async () => {
  mock.sequencePreview.mockResolvedValueOnce(
    sequencePreview({
      send_unavailable: true,
      send_unavailable_reason: 'outside_window',
    }),
  )
  const { request } = await mount(resumeProposal())
  expect(dialog().textContent).toContain('Exact reviewed synthetic reply')
  expect(dialog().textContent).toContain(
    'inboxQuality.sendReason.outside_window',
  )
  await reason()
  expect(button('inboxRecovery.confirmResume').disabled).toBe(true)
  await reason('')
  mock.sequencePreview.mockResolvedValueOnce(
    sequencePreview({
      actions_eligible: false,
      send_will_be_skipped: true,
    }),
  )
  request.value = resumeProposal()
  await settle()
  await reason()
  expect(dialog().textContent).toContain(
    'inboxRecovery.reason.remaining_actions_blocked',
  )
  expect(button('inboxRecovery.confirmResume').disabled).toBe(true)
  expect(mock.resume).not.toHaveBeenCalled()
})

it('rejects mismatched preview identities and permits an explicit fresh lookup without exposing the bad preview', async () => {
  mock.sequencePreview.mockResolvedValueOnce(
    sequencePreview({
      run_id: otherId,
      account: 'WRONG_CONTEXT',
    }),
  )
  await mount(resumeProposal())
  expect(dialog().textContent).toContain('inboxRecovery.previewError')
  expect(dialog().textContent).not.toContain('WRONG_CONTEXT')
  expect(button('inboxRecovery.confirmResume').disabled).toBe(true)
  button('common.retry').click()
  await settle()
  expect(dialog().textContent).toContain('Current synthetic account')
  await reason()
  expect(button('inboxRecovery.confirmResume').disabled).toBe(false)
})

it('rejects a mismatched case or incomplete eligibility flags instead of treating absence as approval', async () => {
  mock.sequencePreview
    .mockResolvedValueOnce(sequencePreview({ case_id: 'different-case' }))
    .mockResolvedValueOnce(sequencePreview({ send_unavailable: undefined }))
    .mockResolvedValueOnce(sequencePreview({ recipient: undefined }))
  await mount(resumeProposal())
  for (let attempt = 0; attempt < 3; attempt++) {
    expect(dialog().textContent).toContain('inboxRecovery.previewError')
    expect(button('inboxRecovery.confirmResume').disabled).toBe(true)
    if (attempt < 2) {
      button('common.retry').click()
      await settle()
    }
  }
  expect(mock.resume).not.toHaveBeenCalled()
})

it('ignores a late sequence preview after a rapid run switch or closing the review', async () => {
  const old = deferred(),
    closed = deferred()
  mock.sequencePreview.mockReturnValueOnce(old.promise)
  const { request } = await mount(resumeProposal())
  const next = resumeProposal()
  next.run.id = otherId
  mock.sequencePreview.mockResolvedValueOnce(
    sequencePreview({ run_id: otherId, account: 'Current second account' }),
  )
  request.value = next
  await settle()
  old.resolve(sequencePreview({ account: 'STALE_SEQUENCE' }))
  await settle()
  expect(dialog().textContent).toContain('Current second account')
  expect(dialog().textContent).not.toContain('STALE_SEQUENCE')
  mock.sequencePreview.mockReturnValueOnce(closed.promise)
  request.value = resumeProposal()
  await settle()
  button('common.cancel').click()
  await settle()
  closed.resolve(sequencePreview({ account: 'CLOSED_SEQUENCE' }))
  await settle()
  expect(document.querySelector('[role=dialog]')).toBeNull()
  expect(mock.resume).not.toHaveBeenCalled()
})

it('does not offer a resume preview to another agent or a read-only owner', async () => {
  mock.auth.hasPermission = (resource: string) => resource === 'chat'
  const selection = resumeProposal()
  selection.run.actor_id = 'another-agent'
  const { request } = await mount(selection)
  expect(mock.sequencePreview).not.toHaveBeenCalled()
  expect(button('inboxRecovery.confirmResume').disabled).toBe(true)
  mock.auth.hasPermission = () => false
  request.value = resumeProposal()
  await settle()
  expect(mock.sequencePreview).not.toHaveBeenCalled()
  expect(mock.resume).not.toHaveBeenCalled()
})

it('keeps a lost resume acknowledgement until its exact run decision is found', async () => {
  mock.resume.mockRejectedValue(new Error('unknown acknowledgement'))
  mock.runRecoveries.mockImplementation(() =>
    Promise.resolve(
      envelope({
        recoveries: [
          {
            id: mock.resume.mock.calls[0]![3],
            kind: 'resume_sequence',
            run_id: runId,
          },
        ],
        has_more: false,
        next_cursor: '',
      }),
    ),
  )
  const { changed } = await mount(resumeProposal())
  await reason()
  button('inboxRecovery.confirmResume').click()
  await settle()
  expect(dialog().textContent).toContain('inboxRecovery.unconfirmedTitle')
  expect(document.activeElement?.textContent?.trim()).toBe(
    'inboxRecovery.unconfirmedTitle',
  )
  expect(mock.leave[0]!()).toBe(false)
  expect(changed).not.toHaveBeenCalled()
  button('inboxRecovery.checkRecord').click()
  await settle()
  expect(changed).toHaveBeenCalledWith(runId)
  expect(mock.resume).toHaveBeenCalledTimes(1)
  expect(mock.runRecoveries).toHaveBeenCalledWith(runId, undefined)
})

it('previews exact intent without dispatch, then records an explicitly confirmed original-operation decision', async () => {
  mock.retry.mockImplementation((target, input) =>
    Promise.resolve(
      envelope({
        recovery: {
          id: input.operation_id,
          kind: 'retry',
          execution_id: target,
        },
      }),
    ),
  )
  const { changed, closed } = await mount()
  expect(dialog().textContent).toContain('Synthetic service')
  expect(dialog().textContent).toContain('false')
  expect(dialog().textContent).toContain('0')
  expect(mock.retry).not.toHaveBeenCalled()
  expect(button('inboxRecovery.confirmRetry').disabled).toBe(true)
  await reason()
  button('inboxRecovery.confirmRetry').click()
  await settle()
  expect(mock.retry).toHaveBeenCalledTimes(1)
  expect(mock.retry.mock.calls[0]![0]).toBe(operationId)
  expect(mock.retry.mock.calls[0]![1]).toMatchObject({
    confirmation: 'a'.repeat(64),
    reason: 'Synthetic investigation completed',
  })
  expect(changed).toHaveBeenCalledWith(runId)
  expect(closed).toHaveBeenCalled()
  expect(document.querySelector('[role=dialog]')).toBeNull()
})

it('shows blocked intent safely and rejects previews lacking the original-operation guarantee', async () => {
  mock.retryPreview.mockResolvedValueOnce(
    retryPreview('<img src=x onerror=alert(1)>', {
      eligible: false,
      reason: 'dispatch_disabled',
    }),
  )
  const { request } = await mount()
  expect(dialog().textContent).toContain('<img src=x onerror=alert(1)>')
  expect(dialog().querySelector('img')).toBeNull()
  expect(dialog().textContent).toContain(
    'inboxRecovery.reason.dispatch_disabled',
  )
  await reason()
  expect(button('inboxRecovery.confirmRetry').disabled).toBe(true)
  mock.retryPreview.mockResolvedValue(
    retryPreview('Current service', { same_operation: false }),
  )
  // Clear a normal unsaved draft before selecting another record.
  const textarea = dialog().querySelector('textarea')!
  textarea.value = ''
  textarea.dispatchEvent(new Event('input', { bubbles: true }))
  await settle()
  request.value = proposal(otherId)
  await settle()
  await reason()
  expect(dialog().textContent).toContain(
    'inboxRecovery.originalGuaranteeRequired',
  )
  expect(button('inboxRecovery.confirmRetry').disabled).toBe(true)
  expect(mock.retry).not.toHaveBeenCalled()
})

it('preserves an unconfirmed decision, blocks leaving and ignores a new selected operation until explicit replay', async () => {
  mock.retry
    .mockRejectedValueOnce(new Error('lost acknowledgement'))
    .mockImplementationOnce((target, input) =>
      Promise.resolve(
        envelope({
          recovery: {
            id: input.operation_id,
            kind: 'retry',
            execution_id: target,
          },
        }),
      ),
    )
  const { request, changed } = await mount()
  await reason()
  button('inboxRecovery.confirmRetry').click()
  await settle()
  expect(dialog().textContent).toContain('inboxRecovery.unconfirmedTitle')
  expect(dialog().querySelector('textarea')?.disabled).toBe(true)
  expect(mock.leave[0]!()).toBe(false)
  expect(mock.update[0]!()).toBe(false)
  const unload = new Event('beforeunload', { cancelable: true })
  window.dispatchEvent(unload)
  expect(unload.defaultPrevented).toBe(true)
  button('Close').click()
  await settle()
  expect(dialog().textContent).toContain('inboxRecovery.closeBlocked')
  request.value = proposal(otherId)
  await settle()
  expect(mock.retryPreview).toHaveBeenCalledTimes(1)
  button('inboxRecovery.repeatDecision').click()
  await settle()
  expect(mock.retry.mock.calls[1]).toEqual(mock.retry.mock.calls[0])
  expect(changed).toHaveBeenCalledWith(runId)
})

it('checks the exact recorded decision after a lost acknowledgement without another submission', async () => {
  mock.retry.mockRejectedValue(new Error('unknown'))
  mock.recoveries.mockImplementation(() =>
    Promise.resolve(
      envelope({
        recoveries: [
          {
            id: mock.retry.mock.calls[0]![1].operation_id,
            kind: 'retry',
            execution_id: operationId,
          },
        ],
        has_more: false,
        next_cursor: '',
      }),
    ),
  )
  const { changed } = await mount()
  await reason()
  button('inboxRecovery.confirmRetry').click()
  await settle()
  button('inboxRecovery.checkRecord').click()
  await settle()
  expect(mock.retry).toHaveBeenCalledTimes(1)
  expect(mock.recoveries).toHaveBeenCalledWith(operationId, undefined)
  expect(changed).toHaveBeenCalledWith(runId)
})

it('keeps cancellation warnings and confirmation separate from the unresolved provider outcome', async () => {
  mock.cancel.mockImplementation((target, input) =>
    Promise.resolve(
      envelope({
        recovery: {
          id: input.operation_id,
          kind: 'cancel_remaining',
          run_id: target,
        },
      }),
    ),
  )
  const initial = proposal()
  const { changed } = await mount({ kind: 'cancel', run: initial.run })
  expect(dialog().textContent).toContain('inboxRecovery.unresolvedCancel')
  expect(dialog().textContent).toContain('inboxRecovery.inFlightCancel')
  expect(mock.cancel).not.toHaveBeenCalled()
  await reason()
  button('inboxRecovery.confirmCancel').click()
  await settle()
  expect(mock.cancel.mock.calls[0]![0]).toBe(runId)
  expect(changed).toHaveBeenCalledWith(runId)
  expect(mock.retry).not.toHaveBeenCalled()
})

it('drops reversed previews and cancels old unsaved callbacks when the viewer changes', async () => {
  const old = deferred()
  mock.retryPreview
    .mockReturnValueOnce(old.promise)
    .mockResolvedValue(retryPreview('CURRENT_PREVIEW'))
  const { request } = await mount()
  request.value = proposal(otherId)
  await settle()
  old.resolve(retryPreview('STALE_PRIVATE_PREVIEW'))
  await settle()
  expect(dialog().textContent).toContain('CURRENT_PREVIEW')
  expect(dialog().textContent).not.toContain('STALE_PRIVATE_PREVIEW')
  await reason('PRIVATE_REASON')
  button('common.cancel').click()
  await settle()
  expect(document.querySelector('[role=alertdialog]')).not.toBeNull()
  mock.org.selectedOrgId = 'org-b'
  await settle()
  expect(document.body.textContent).not.toContain('PRIVATE_REASON')
  expect(document.body.textContent).not.toContain('CURRENT_PREVIEW')
  expect(document.querySelector('[role=alertdialog]')).toBeNull()
})

it('requires an explicit reviewed discard before a new preview and never automatically creates a second decision', async () => {
  mock.retry.mockRejectedValue({ response: { status: 409 } })
  await mount()
  await reason()
  button('inboxRecovery.confirmRetry').click()
  await settle()
  expect(dialog().textContent).toContain('inboxRecovery.conflict')
  expect(button('inboxRecovery.freshReview').disabled).toBe(true)
  const checkbox = dialog().querySelector<HTMLInputElement>(
    'input[type=checkbox]',
  )!
  checkbox.checked = true
  checkbox.dispatchEvent(new Event('change', { bubbles: true }))
  await settle()
  button('inboxRecovery.freshReview').click()
  await settle()
  expect(mock.retryPreview).toHaveBeenCalledTimes(2)
  expect(mock.retry).toHaveBeenCalledTimes(1)
  expect(dialog().textContent).not.toContain('inboxRecovery.unconfirmedTitle')
})

it('offers retry after a failed preview and performs no request with missing operator access', async () => {
  mock.retryPreview
    .mockRejectedValueOnce(new Error('private provider details'))
    .mockResolvedValue(retryPreview())
  const { request } = await mount()
  expect(dialog().textContent).toContain('inboxRecovery.previewError')
  expect(dialog().textContent).not.toContain('private provider details')
  button('common.retry').click()
  await settle()
  expect(dialog().textContent).toContain('Synthetic service')
  mock.auth.allowed = false
  mock.auth.user.role.permissions = ['revoked']
  await settle()
  request.value = proposal(otherId)
  await settle()
  expect(mock.retryPreview).toHaveBeenCalledTimes(2)
  expect(dialog().textContent).toContain('inboxRecovery.previewError')
  expect(mock.retry).not.toHaveBeenCalled()
})

it('explains an invalid audit reason beside its field and restores confirmation after correction', async () => {
  await mount()
  await reason('First line\nSecond line')
  const field = dialog().querySelector('textarea')!
  expect(field.getAttribute('aria-invalid')).toBe('true')
  expect(dialog().textContent).toContain('inboxRecovery.reasonError')
  expect(field.getAttribute('aria-describedby')).toContain('error')
  expect(button('inboxRecovery.confirmRetry').disabled).toBe(true)
  await reason('One safe investigation summary')
  expect(field.getAttribute('aria-invalid')).toBe('false')
  expect(dialog().textContent).not.toContain('inboxRecovery.reasonError')
  expect(button('inboxRecovery.confirmRetry').disabled).toBe(false)
  expect(mock.retry).not.toHaveBeenCalled()
})

it('does not display or approve an empty or mismatched retry intent', async () => {
  mock.retryPreview.mockResolvedValue(
    retryPreview('Ignored', {
      intent: {
        id: '00000000-0000-0000-0000-000000000000',
        name: '',
        method: '',
        destination: '',
        available: false,
        inputs: {},
        outputs: [],
      },
    }),
  )
  await mount()
  await reason()
  expect(dialog().textContent).toContain(
    'inboxRecovery.originalGuaranteeRequired',
  )
  expect(dialog().textContent).not.toContain('inboxIntegrations.unavailable')
  expect(button('inboxRecovery.confirmRetry').disabled).toBe(true)
  expect(mock.retry).not.toHaveBeenCalled()
})
