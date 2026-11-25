// @vitest-environment happy-dom
import { createApp, h, nextTick, reactive, ref } from 'vue'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import InboxRoutingPolicies from './InboxRoutingPolicies.vue'
import InboxAssignmentRate from '@/components/chat/InboxAssignmentRate.vue'

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  configuration: vi.fn(),
  create: vi.fn(),
  update: vi.fn(),
  archive: vi.fn(),
  preview: vi.fn(),
  apply: vi.fn(),
  success: vi.fn(),
}))
const viewer = ref('viewer-a'),
  permission = ref(true)
vi.mock('@/services/inboxRouting', () => ({ inboxRouting: mocks }))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ hasPermission: () => permission.value }),
}))
vi.mock('@/composables/useInboxViewerContext', () => ({
  useInboxViewerContext: () => viewer,
}))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, values?: unknown) =>
      key + (values ? JSON.stringify(values) : ''),
  }),
}))
vi.mock('vue-router', () => ({
  onBeforeRouteLeave: vi.fn(),
  onBeforeRouteUpdate: vi.fn(),
}))
vi.mock('vue-sonner', () => ({ toast: { success: mocks.success } }))
const envelope = (data: unknown) => ({ data: { data } })
const policy = {
  id: 'policy',
  revision: 2,
  name: 'Synthetic routing',
  description: 'Synthetic template',
  strategy: 'round_robin',
  max_active: null,
  max_assignments: 10,
  window_seconds: 3600,
}
const config = () => ({
  configuration: null,
  account_name: 'Synthetic inbox',
  team_name: 'Synthetic team',
  team_strategy: 'round_robin',
  effective_strategy: 'round_robin',
  can_write: true,
})
const review = () => ({
  account_id: 'account-a',
  account_name: 'Synthetic inbox',
  team_id: 'team-a',
  team_name: 'Synthetic team',
  policy,
  configuration: null,
  changes: [
    {
      agent_id: 'agent-a',
      name: 'Synthetic agent',
      before_active: 0,
      after_active: 0,
      before_assignments: 0,
      after_assignments: 10,
      before_window_seconds: 0,
      after_window_seconds: 3600,
    },
  ],
  expires_at: new Date(Date.now() + 240000).toISOString(),
  confirmation: 'a'.repeat(64),
  limits_shared_across_teams: true,
  before_strategy: 'manual',
  effective_strategy: 'round_robin',
})
function props() {
  return reactive({
    team: 'team-a',
    account: 'account-a',
    agents: [
      {
        agent_id: 'agent-a',
        name: 'Synthetic agent',
        max_active: 0,
        active: 0,
        available: true,
      },
    ],
    canWrite: true,
    blocked: false,
  })
}
const deferred = () => {
  let resolve!: (value: unknown) => void
  const promise = new Promise((r) => (resolve = r))
  return { resolve, promise }
}
const settle = async () => {
  for (let i = 0; i < 20; i++) {
    await Promise.resolve()
    await nextTick()
  }
}
let app: ReturnType<typeof createApp> | undefined, root: HTMLElement
function mount(component: any, input: Record<string, unknown>) {
  root = document.createElement('div')
  document.body.append(root)
  app = createApp({ render: () => h(component, input) })
  app.config.globalProperties.$t = (key: string) => key
  const vm = app.mount(root)
  return (
    vm.$.subTree.component as unknown as { setupState: Record<string, any> }
  ).setupState
}
beforeEach(() => {
  vi.clearAllMocks()
  viewer.value = 'viewer-a'
  permission.value = true
  mocks.list.mockResolvedValue(
    envelope({ policies: [policy], has_more: false, can_write: true }),
  )
  mocks.configuration.mockResolvedValue(envelope(config()))
  mocks.create.mockResolvedValue(envelope({ policy }))
  mocks.update.mockResolvedValue(envelope({ policy }))
  mocks.archive.mockResolvedValue(envelope({}))
  mocks.preview.mockImplementation(async () => envelope(review()))
  mocks.apply.mockResolvedValue(
    envelope({ applied: true, already_applied: false, configuration: {} }),
  )
})
afterEach(() => {
  app?.unmount()
  app = undefined
  root?.remove()
})

it('saves an unapplied policy with explicit optional capacity and a minute-based rolling window', async () => {
  const state = mount(InboxRoutingPolicies, props())
  await settle()
  state.begin()
  await settle()
  state.form.name = 'New synthetic policy'
  state.rateEnabled = true
  state.rateMaximum = 3
  state.windowMinutes = 15
  state.activeMaximum = 27
  state.changeCapacity = false
  await state.save()
  await settle()
  expect(mocks.create).toHaveBeenCalledWith(
    expect.objectContaining({
      name: 'New synthetic policy',
      max_active: null,
      max_assignments: 3,
      window_seconds: 900,
    }),
  )
  expect(mocks.apply).not.toHaveBeenCalled()
  expect(state.pending).toBe('')
  expect(state.editing).toBe(false)
  expect(document.activeElement?.id).toBe('routing-policy-new')
})

it('preserves a stale policy draft and prevents repeated writes before a deliberate reload', async () => {
  const state = mount(InboxRoutingPolicies, props())
  await settle()
  state.begin(policy)
  await settle()
  state.form.name = 'Preserved draft'
  mocks.update.mockRejectedValue({
    isAxiosError: true,
    response: { status: 409 },
  })
  await state.save()
  await settle()
  expect(state.form.name).toBe('Preserved draft')
  expect(state.conflict).toBe(true)
  await state.save()
  expect(mocks.update).toHaveBeenCalledTimes(1)
})

it('ignores reversed loads after changing the inbox without showing the earlier configuration', async () => {
  const oldList = deferred(),
    oldConfig = deferred()
  mocks.list.mockReturnValueOnce(oldList.promise).mockResolvedValue(
    envelope({
      policies: [{ ...policy, name: 'Current policy' }],
      has_more: false,
      can_write: true,
    }),
  )
  mocks.configuration
    .mockReturnValueOnce(oldConfig.promise)
    .mockResolvedValue(envelope({ ...config(), account_name: 'Current inbox' }))
  const input = props(),
    state = mount(InboxRoutingPolicies, input)
  await settle()
  input.account = 'account-b'
  await settle()
  oldList.resolve(
    envelope({
      policies: [{ ...policy, name: 'Earlier private policy' }],
      has_more: false,
      can_write: true,
    }),
  )
  oldConfig.resolve(
    envelope({ ...config(), account_name: 'Earlier private inbox' }),
  )
  await settle()
  expect(state.accountName).toBe('Current inbox')
  expect(state.policies[0].name).toBe('Current policy')
  expect(root.textContent).not.toContain('Earlier private')
})

it('restores dialog focus to the current review control or heading after access changes', async () => {
  const state = mount(InboxRoutingPolicies, props())
  await settle()
  state.selected = policy.id
  await settle()
  await state.preview()
  await settle()
  state.reviewing = false
  state.restoreDialogFocus(new Event('closeAutoFocus'))
  await settle()
  expect(document.activeElement?.id).toBe('routing-policy-review')
  permission.value = false
  await settle()
  state.restoreDialogFocus(new Event('closeAutoFocus'))
  await settle()
  expect(document.activeElement?.id).toBe('routing-policy-heading')
})

it('keeps preview read-only and invalidates it when the team roster changes', async () => {
  const input = props(),
    state = mount(InboxRoutingPolicies, input)
  await settle()
  state.selected = policy.id
  await settle()
  await state.preview()
  await settle()
  expect(mocks.preview).toHaveBeenCalledWith(
    'policy',
    expect.objectContaining({
      account_id: 'account-a',
      team_id: 'team-a',
      agent_ids: ['agent-a'],
      revision: 2,
    }),
  )
  expect(state.reviewing).toBe(true)
  expect(document.body.textContent?.replace(/\s+/g, ' ')).toContain(
    'inboxRouting.strategy.manual → inboxRouting.strategy.round_robin',
  )
  expect(mocks.apply).not.toHaveBeenCalled()
  input.agents = []
  await settle()
  expect(state.reviewing).toBe(false)
  expect(state.reviewInput).toBeUndefined()
  await state.apply()
  expect(mocks.apply).not.toHaveBeenCalled()
})

it('discards a cancelled or late preview after the account context changes', async () => {
  const pending = deferred()
  mocks.preview.mockReturnValue(pending.promise)
  const input = props(),
    state = mount(InboxRoutingPolicies, input)
  await settle()
  state.selected = policy.id
  await settle()
  const request = state.preview()
  input.account = 'account-b'
  await settle()
  pending.resolve(
    envelope({ ...review(), account_name: 'Earlier private preview' }),
  )
  await request
  await settle()
  expect(state.reviewing).toBe(false)
  expect(state.review).toBeUndefined()
  expect(document.body.textContent).not.toContain('Earlier private preview')
})

it('retries an uncertain Apply with the identical receipt even after local expiry', async () => {
  const state = mount(InboxRoutingPolicies, props())
  await settle()
  state.selected = policy.id
  await settle()
  await state.preview()
  await settle()
  mocks.apply
    .mockRejectedValueOnce(new Error('synthetic network loss'))
    .mockResolvedValue(
      envelope({
        applied: true,
        already_applied: true,
        refresh_current_limits: true,
        configuration: {},
      }),
    )
  await state.apply()
  await settle()
  expect(state.applyUnknown).toBe(true)
  expect(state.reviewInput).toBeDefined()
  state.now = Date.parse(state.review.expires_at) + 1000
  expect(state.expired).toBe(true)
  await state.apply()
  await settle()
  expect(mocks.apply).toHaveBeenCalledTimes(2)
  expect(mocks.apply.mock.calls[1]).toEqual(mocks.apply.mock.calls[0])
  expect(mocks.preview).toHaveBeenCalledTimes(1)
  expect(state.reviewing).toBe(false)
  expect(state.pending).toBe('')
})

it('rejects mismatched configuration and preview identities', async () => {
  mocks.configuration.mockResolvedValue(
    envelope({
      ...config(),
      configuration: {
        account_id: 'foreign',
        team_id: 'team-a',
        name: 'Never exposed',
      },
    }),
  )
  const state = mount(InboxRoutingPolicies, props())
  await settle()
  expect(state.failed).toBe(true)
  expect(state.configuration).toBeNull()
  expect(root.textContent).not.toContain('Never exposed')
  mocks.configuration.mockResolvedValue(envelope(config()))
  await state.load()
  await settle()
  state.selected = policy.id
  await settle()
  mocks.preview.mockResolvedValue(
    envelope({ ...review(), account_id: 'foreign' }),
  )
  await state.preview()
  await settle()
  expect(state.reviewing).toBe(false)
  expect(state.review).toBeUndefined()
  expect(mocks.apply).not.toHaveBeenCalled()
})

it('does not resurrect an authoring draft or success response after write access is revoked', async () => {
  const pending = deferred()
  mocks.create.mockReturnValue(pending.promise)
  const state = mount(InboxRoutingPolicies, props())
  await settle()
  state.begin()
  await settle()
  state.form.name = 'Private pending draft'
  const request = state.save()
  permission.value = false
  await settle()
  expect(state.editing).toBe(false)
  expect(state.form.name).toBe('')
  pending.resolve(
    envelope({ policy: { ...policy, name: 'Late saved private name' } }),
  )
  await request
  await settle()
  expect(state.selected).toBe('')
  expect(mocks.success).not.toHaveBeenCalled()
  expect(state.pending).toBe('')
  expect(root.textContent).not.toContain('Late saved private name')
})

it('blocks review while the parent has unsaved active limits and validates bounded integer rates', async () => {
  const input = props(),
    state = mount(InboxRoutingPolicies, input)
  await settle()
  state.selected = policy.id
  await settle()
  input.blocked = true
  await settle()
  await state.preview()
  expect(mocks.preview).not.toHaveBeenCalled()
  state.begin(policy)
  await settle()
  state.rateMaximum = 0
  expect(state.valid).toBe(false)
  state.rateMaximum = 1
  state.windowMinutes = 1.5
  expect(state.valid).toBe(false)
  state.windowMinutes = 1441
  expect(state.valid).toBe(false)
  state.windowMinutes = 60
  expect(state.valid).toBe(true)
})

it('does not turn hidden assignment activity into a visible zero or expose its recovery time', async () => {
  mount(InboxAssignmentRate, {
    row: {
      agent_id: 'agent',
      name: 'Synthetic agent',
      max_active: 0,
      active: 0,
      available: true,
      max_assignments: 10,
      window_seconds: 3600,
      assignments: 0,
      rate_details_hidden: true,
      rate_resumes_at: '2026-11-25T10:00:00Z',
      assignment_reason: 'rate_limited',
    },
  })
  await settle()
  expect(root.textContent).toContain('inboxRouting.hiddenActivity')
  expect(root.textContent).not.toContain('inboxRouting.rateUsage')
  expect(root.textContent).not.toContain('inboxRouting.rateSlot')
})
