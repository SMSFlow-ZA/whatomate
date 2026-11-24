// @vitest-environment happy-dom
import { createApp, h, nextTick, reactive } from 'vue'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import InboxIntegrationRun from './InboxIntegrationRun.vue'
import InboxIntegrationRecords from './InboxIntegrationRecords.vue'

const mock = vi.hoisted(() => ({
  auth: undefined as any,
  org: undefined as any,
  executions: vi.fn(),
  recoveries: vi.fn(),
  runRecoveries: vi.fn(),
  recoveryEvidence: vi.fn(),
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => mock.auth }))
vi.mock('@/stores/organizations', () => ({
  useOrganizationsStore: () => mock.org,
}))
vi.mock('@/services/inboxIntegrations', () => ({ inboxIntegrations: mock }))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) =>
      key + (params ? ':' + Object.values(params).join(',') : ''),
    te: () => true,
  }),
}))

const envelope = (data: unknown) => ({ data: { data } })
const run = (id = 'run-a') => ({
  id,
  workflow_name: 'Synthetic service',
  state: 'sequence_cancelled',
  created_at: '2026-01-01T00:00:00Z',
  sequenced: true,
})
const operation = (id = 'execution-a') => ({
  id,
  run_id: 'run-a',
  action_index: 0,
  integration_id: 'definition-a',
  case_id: 'case-a',
  account_id: 'account-a',
  actor_id: 'viewer-a',
  state: 'succeeded',
  attempt_count: 1,
  result_origin: 'provider',
  provider_status: 200,
  duration_ms: 0,
  created_at: '2026-01-01T00:00:00Z',
})
const decision = (id = 'decision-a') => ({
  id,
  execution_id: 'execution-a',
  actor_id: 'viewer-a',
  kind: 'confirm_succeeded',
  from_state: 'unknown',
  attempt_count: 1,
  reason: 'Synthetic investigation completed',
  has_evidence: true,
  created_at: '2026-01-01T00:00:00Z',
})
const empty = () =>
  envelope({ recoveries: [], has_more: false, next_cursor: '' })
const page = (name = 'execution-a', next = '') =>
  envelope({
    executions: [operation(name)],
    has_more: !!next,
    next_cursor: next,
  })
const recordPage = (name = 'decision-a', next = '') =>
  envelope({
    recoveries: [decision(name)],
    has_more: !!next,
    next_cursor: next,
  })
function deferred() {
  let resolve!: (value: any) => void
  const promise = new Promise<any>((done) => {
    resolve = done
  })
  return { promise, resolve }
}
const mounts: { app: ReturnType<typeof createApp>; root: HTMLElement }[] = []
const settle = async () => {
  for (let i = 0; i < 25; i++) {
    await Promise.resolve()
    await nextTick()
  }
}
async function mount(
  component: any = InboxIntegrationRun,
  props = reactive<any>({
    run: run(),
    users: [{ id: 'viewer-a', full_name: 'Synthetic reviewer' }],
  }),
) {
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp({ render: () => h(component, props) })
  app.mount(root)
  mounts.push({ app, root })
  await settle()
  return { root, props }
}
function button(root: HTMLElement, text: string) {
  const element = Array.from(root.querySelectorAll('button')).find(
    (item) => item.textContent?.trim() === text,
  )
  expect(element, text).toBeTruthy()
  return element!
}
beforeEach(() => {
  vi.resetAllMocks()
  mock.auth = reactive({
    user: { id: 'viewer-a', role_id: 'admin', role: { permissions: [] } },
    organizationId: 'org-a',
    allowOperator: true,
    hasPermission: (resource: string, action: string) =>
      action === 'read' || resource === 'chat' || mock.auth.allowOperator,
  })
  mock.org = reactive({ selectedOrgId: 'org-a' })
  mock.executions.mockResolvedValue(page())
  mock.recoveries.mockResolvedValue(empty())
  mock.runRecoveries.mockResolvedValue(empty())
})
afterEach(() => {
  for (const { app, root } of mounts.splice(0)) {
    app.unmount()
    root.remove()
  }
})

it('shows provider truth after parent cancellation without requesting private values or evidence', async () => {
  mock.executions.mockResolvedValue(
    envelope({
      executions: [
        {
          ...operation(),
          encrypted_request: 'PRIVATE_REQUEST',
          encrypted_result: 'PRIVATE_RESULT',
          inputs: { customer: 'PRIVATE_INPUT' },
          response_body: 'PRIVATE_BODY',
        },
      ],
      has_more: false,
      next_cursor: '',
    }),
  )
  const { root } = await mount()
  expect(root.textContent).toContain('inboxIntegrationHistory.cancelledHelp')
  expect(root.textContent).toContain('inboxIntegrationHistory.state.succeeded')
  expect(root.textContent).toContain('inboxIntegrationHistory.origin.provider')
  expect(root.textContent).toContain('inboxIntegrationHistory.outputsPending')
  expect(root.textContent).toContain('HTTP 200')
  expect(root.textContent).toContain('0 ms')
  expect(root.textContent).not.toContain('PRIVATE_')
  expect(mock.executions).toHaveBeenCalledWith({
    run_id: 'run-a',
    cursor: undefined,
  })
  expect(mock.runRecoveries).not.toHaveBeenCalled()
  expect(mock.recoveryEvidence).not.toHaveBeenCalled()
  button(root, 'inboxIntegrationHistory.decisions').click()
  await settle()
  expect(mock.runRecoveries).toHaveBeenCalledWith('run-a', undefined)
})

it('rejects reversed run and organisation results and removes the old viewer metadata immediately', async () => {
  const old = deferred()
  mock.executions
    .mockReturnValueOnce(old.promise)
    .mockResolvedValue(page('current-operation'))
  const { root, props } = await mount()
  props.run = run('run-b')
  await settle()
  expect(root.textContent).toContain('current-operation')
  old.resolve(page('STALE_OPERATION'))
  await settle()
  expect(root.textContent).not.toContain('STALE_OPERATION')
  const next = deferred()
  mock.executions.mockReturnValueOnce(next.promise)
  mock.org.selectedOrgId = 'org-b'
  await nextTick()
  expect(root.textContent).not.toContain('current-operation')
  next.resolve(page('new-organisation-operation'))
  await settle()
  expect(root.textContent).toContain('new-organisation-operation')
})

it('retains the page on a failed move, navigates with exact cursors and rejects repeating cursors', async () => {
  mock.executions
    .mockResolvedValueOnce(page('first', 'cursor-two'))
    .mockRejectedValueOnce(new Error('offline'))
    .mockResolvedValueOnce(page('second', 'cursor-two'))
    .mockResolvedValueOnce(page('first', 'cursor-two'))
  const { root } = await mount()
  button(root, 'inboxIntegrationHistory.older').click()
  await settle()
  expect(root.textContent).toContain('first')
  expect(root.textContent).toContain('inboxIntegrationHistory.page:1')
  expect(root.textContent).toContain('inboxIntegrationHistory.loadError')
  button(root, 'inboxIntegrationHistory.older').click()
  await settle()
  expect(mock.executions).toHaveBeenLastCalledWith({
    run_id: 'run-a',
    cursor: 'cursor-two',
  })
  expect(root.textContent).toContain('second')
  expect(root.textContent).toContain('inboxIntegrationHistory.page:2')
  expect(document.activeElement).toBe(root.querySelector('h3'))
  expect(button(root, 'inboxIntegrationHistory.older').disabled).toBe(true)
  button(root, 'inboxIntegrationHistory.newer').click()
  await settle()
  expect(mock.executions).toHaveBeenLastCalledWith({
    run_id: 'run-a',
    cursor: undefined,
  })
  expect(root.textContent).toContain('inboxIntegrationHistory.page:1')
})

it('separates manual attestations and applied/discarded outputs without inferring a provider receipt', async () => {
  mock.executions.mockResolvedValue(
    envelope({
      executions: [
        {
          ...operation(),
          result_origin: 'manual',
          outputs_discarded_at: '2026-01-01T01:00:00Z',
          outcome: 'manually_confirmed_success',
        },
        {
          ...operation('applied'),
          result_origin: '',
          outputs_applied_at: '2026-01-01T01:00:00Z',
        },
      ],
      has_more: false,
      next_cursor: '',
    }),
  )
  const { root } = await mount()
  expect(root.textContent).toContain('inboxIntegrationHistory.origin.manual')
  expect(root.textContent).toContain('inboxIntegrationHistory.manualHelp')
  expect(root.textContent).toContain('inboxIntegrationHistory.outputsDiscarded')
  expect(root.textContent).toContain('inboxIntegrationHistory.outputsApplied')
  expect(root.textContent).toContain('inboxIntegrationHistory.origin.none')
  expect(root.textContent).not.toContain(
    'inboxIntegrationHistory.origin.provider',
  )
})

it('keeps ordinary agents on execution-scoped recovery metadata without evidence controls', async () => {
  mock.auth.allowOperator = false
  mock.recoveries.mockResolvedValue(recordPage())
  const { root } = await mount()
  button(root, 'inboxIntegrationHistory.decisions').click()
  await settle()
  expect(mock.recoveries).toHaveBeenCalledWith('execution-a', undefined)
  expect(mock.runRecoveries).not.toHaveBeenCalled()
  expect(root.textContent).toContain(
    'inboxIntegrationHistory.evidenceRestricted',
  )
  expect(root.textContent).not.toContain(
    'inboxIntegrationHistory.inspectEvidence',
  )
  expect(mock.recoveryEvidence).not.toHaveBeenCalled()
})

it('loads evidence only explicitly, escapes references and clears it on page refresh', async () => {
  mock.runRecoveries.mockResolvedValue(recordPage())
  mock.recoveryEvidence.mockResolvedValue(
    envelope({
      evidence_reference: '<img src=x onerror=alert(1)>',
      source: 'manual_attestation',
      recorded_by: 'viewer-a',
    }),
  )
  const { root } = await mount(
    InboxIntegrationRecords,
    reactive({ runId: 'run-a' }),
  )
  expect(mock.recoveryEvidence).not.toHaveBeenCalled()
  button(root, 'inboxIntegrationHistory.inspectEvidence').click()
  await settle()
  expect(mock.recoveryEvidence).toHaveBeenCalledWith(
    'execution-a',
    'decision-a',
  )
  expect(root.textContent).toContain('<img src=x onerror=alert(1)>')
  expect(root.querySelector('img')).toBeNull()
  button(root, 'common.refresh').click()
  await settle()
  expect(root.textContent).not.toContain('<img src=x onerror=alert(1)>')
})

it('ignores evidence returned after context or permissions change', async () => {
  mock.runRecoveries.mockResolvedValue(recordPage())
  mock.recoveries.mockResolvedValue(recordPage())
  const old = deferred()
  mock.recoveryEvidence.mockReturnValueOnce(old.promise)
  const { root } = await mount(
    InboxIntegrationRecords,
    reactive({ executionId: 'execution-a' }),
  )
  button(root, 'inboxIntegrationHistory.inspectEvidence').click()
  await settle()
  mock.auth.allowOperator = false
  mock.auth.user.role.permissions = ['changed']
  await settle()
  old.resolve(envelope({ evidence_reference: 'OLD_PRIVATE_REFERENCE' }))
  await settle()
  expect(root.textContent).not.toContain('OLD_PRIVATE_REFERENCE')
  expect(root.textContent).toContain(
    'inboxIntegrationHistory.evidenceRestricted',
  )
})

it('retains independent recovery pages on failures and hides a late old page', async () => {
  const late = deferred()
  mock.runRecoveries
    .mockResolvedValueOnce(recordPage('first-decision', 'older'))
    .mockReturnValueOnce(late.promise)
    .mockResolvedValue(recordPage('new-context-decision'))
  const { root, props } = await mount(
    InboxIntegrationRecords,
    reactive({ runId: 'run-a' }),
  )
  button(root, 'inboxIntegrationHistory.older').click()
  await settle()
  props.runId = 'run-b'
  await settle()
  late.resolve(recordPage('OLD_DECISION'))
  await settle()
  expect(root.textContent).not.toContain('OLD_DECISION')
  expect(root.textContent).toContain('new-context-decision')
  mock.runRecoveries.mockRejectedValueOnce(new Error('offline'))
  button(root, 'common.refresh').click()
  await settle()
  expect(root.textContent).toContain('inboxIntegrationHistory.recordsError')
  expect(root.textContent).toContain('new-context-decision')
})

it('retains prior submission provenance separately from the later investigation decision', async () => {
  mock.runRecoveries.mockResolvedValue(
    envelope({
      recoveries: [
        {
          ...decision(),
          from_result_origin: 'provider',
          provider_status: 503,
          duration_ms: 0,
          prior_started_at: '2026-01-01T00:00:00Z',
          prior_completed_at: '2026-01-01T00:00:01Z',
        },
      ],
      has_more: false,
      next_cursor: '',
    }),
  )
  const { root } = await mount(
    InboxIntegrationRecords,
    reactive({ runId: 'run-a' }),
  )
  expect(root.textContent).toContain(
    'inboxIntegrationHistory.decision.confirm_succeeded',
  )
  expect(root.textContent).toContain('inboxIntegrationHistory.manualHelp')
  expect(root.textContent).toContain('inboxIntegrationHistory.state.unknown')
  expect(root.textContent).toContain('inboxIntegrationHistory.origin.provider')
  expect(root.textContent).toContain('HTTP 503')
  expect(root.textContent).toContain('0 ms')
  expect(root.querySelectorAll('time')).toHaveLength(3)
  expect(mock.recoveryEvidence).not.toHaveBeenCalled()
})

it('associates different run-wide decisions with their exact operations and distinguishes workflow scope', async () => {
  mock.runRecoveries.mockResolvedValue(
    envelope({
      recoveries: [
        decision('first'),
        {
          ...decision('second'),
          execution_id: 'execution-b',
          kind: 'discard_outputs',
        },
        {
          ...decision('workflow'),
          execution_id: '00000000-0000-0000-0000-000000000000',
          run_id: 'run-a',
          kind: 'cancel_remaining',
          has_evidence: false,
        },
        {
          ...decision('resumed'),
          execution_id: '00000000-0000-0000-0000-000000000000',
          run_id: 'run-a',
          kind: 'resume_sequence',
          has_evidence: false,
        },
      ],
      has_more: false,
      next_cursor: '',
    }),
  )
  const { root } = await mount(
    InboxIntegrationRecords,
    reactive({
      runId: 'run-a',
      operations: [
        { id: 'execution-a', action_index: 0 },
        { id: 'execution-b', action_index: 1 },
      ],
    }),
  )
  const records = root.querySelectorAll('ol > li')
  expect(records[0]!.textContent).toContain(
    'inboxIntegrationHistory.actionNumber:1',
  )
  expect(records[0]!.textContent).toContain('execution-a')
  expect(records[1]!.textContent).toContain(
    'inboxIntegrationHistory.actionNumber:2',
  )
  expect(records[1]!.textContent).toContain('execution-b')
  expect(records[2]!.textContent).toContain(
    'inboxIntegrationHistory.workflowDecision',
  )
  expect(records[2]!.textContent).toContain('run-a')
  expect(records[2]!.textContent).not.toContain(
    '00000000-0000-0000-0000-000000000000',
  )
  expect(records[3]!.textContent).toContain(
    'inboxIntegrationHistory.workflowDecision',
  )
  expect(records[3]!.textContent).toContain(
    'inboxIntegrationHistory.decision.resume_sequence',
  )
  expect(records[3]!.textContent).toContain('run-a')
})

it('retains the operation identifier when its action is not on the loaded execution page', async () => {
  mock.runRecoveries.mockResolvedValue(recordPage())
  const { root } = await mount(
    InboxIntegrationRecords,
    reactive({ runId: 'run-a', operations: [] }),
  )
  expect(root.textContent).toContain(
    'inboxIntegrationHistory.externalOperation',
  )
  expect(root.textContent).toContain('execution-a')
  expect(root.textContent).not.toContain(
    'inboxIntegrationHistory.workflowDecision',
  )
})
