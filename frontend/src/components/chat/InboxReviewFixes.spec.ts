// @vitest-environment happy-dom
import { createApp, h, nextTick, ref } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
import InboxWorkload from './InboxWorkload.vue'
import InboxCaseActions from './InboxCaseActions.vue'
import InboxFieldsView from '@/views/settings/InboxFieldsView.vue'
import InboxSLAReport from './InboxSLAReport.vue'
import InboxFieldMatchers from './InboxFieldMatchers.vue'
import InboxCapacityPresets from '@/components/settings/InboxCapacityPresets.vue'
import InboxSLASettings from '@/components/settings/InboxSLASettings.vue'
import InboxTeamCapacity from '@/components/settings/InboxTeamCapacity.vue'
import InboxSLAHistory from './InboxSLAHistory.vue'

const mocks = vi.hoisted(() => ({
  report: vi.fn(),
  workload: vi.fn(),
  conversation: vi.fn(),
  createField: vi.fn(),
  accounts: vi.fn(),
  teams: vi.fn(),
  policies: vi.fn(),
  savePolicy: vi.fn(),
  fields: vi.fn(),
  presets: vi.fn(),
  remove: vi.fn(),
  capacity: vi.fn(),
  setCapacity: vi.fn(),
  history: vi.fn(),
}))
vi.mock('@/services/inbox', () => ({
  inboxService: {
    settings: async () => ({ data: { data: { enabled: true } } }),
    slaReport: mocks.report,
    workload: mocks.workload,
    conversation: mocks.conversation,
    createFieldDefinition: mocks.createField,
    fieldDefinitions: mocks.fields,
    capacityPresets: mocks.presets,
    deleteCapacityPreset: mocks.remove,
    slaPolicies: mocks.policies,
    setSlaPolicy: mocks.savePolicy,
    teamCapacity: mocks.capacity,
    setTeamCapacity: mocks.setCapacity,
    slaHistory: mocks.history,
  },
}))
vi.mock('@/components/settings/InboxRoutingPolicies.vue', () => ({
  default: { template: '<div />' },
}))
vi.mock('@/services/api', () => ({ accountsService: { list: mocks.accounts } }))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ hasPermission: () => true }),
}))
vi.mock('@/stores/organizations', () => ({
  useOrganizationsStore: () => ({ selectedOrgId: 'test-org' }),
}))
vi.mock('@/stores/teams', () => ({
  useTeamsStore: () => ({ fetchTeams: mocks.teams }),
}))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key, locale: { value: 'en' } }),
}))
vi.mock('vue-router', async (original) => ({
  ...(await original<object>()),
  useRoute: () => ({ query: {} }),
  onBeforeRouteLeave: vi.fn(),
  onBeforeRouteUpdate: vi.fn(),
}))
const settle = async () => {
  for (let i = 0; i < 15; i++) {
    await Promise.resolve()
    await nextTick()
  }
}
let app: ReturnType<typeof createApp> | undefined, root: HTMLElement
let mountedVm: any
function mount(component: Parameters<typeof createApp>[0], props = {}) {
  root = document.createElement('div')
  document.body.append(root)
  app = createApp(component, props)
  app.component('RouterLink', { template: '<a><slot /></a>' })
  app.config.globalProperties.$t = (key: string) => key
  const vm = app.mount(root)
  mountedVm = vm
  return (vm.$ as unknown as { setupState: Record<string, any> }).setupState
}
afterEach(() => {
  app?.unmount()
  root?.remove()
  vi.clearAllMocks()
  vi.useRealTimers()
})
const emptyReport = {
  data: {
    data: {
      cases: [],
      has_more: false,
      summary: { total: 1, breached: 0, at_risk: 0 },
    },
  },
}

it('clears capacity drafts on confirmed denial and ignores a denied save from an older account', async () => {
  const account = ref('a')
  const capacity = (name: string) => ({
    data: {
      data: {
        agents: [
          {
            agent_id: 'private-agent',
            name,
            active: 1,
            max_active: 4,
            available: true,
          },
        ],
      },
    },
  })
  mocks.presets.mockResolvedValue({ data: { data: { presets: [] } } })
  mocks.capacity.mockImplementation((_team, selected) =>
    Promise.resolve(capacity(selected)),
  )
  mount({
    render: () =>
      h(InboxTeamCapacity, {
        team: 'team',
        account: account.value,
        canWrite: true,
      }),
  })
  await settle()
  const state = mountedVm.$.subTree.component.setupState
  let deny!: (error: unknown) => void
  mocks.setCapacity.mockReturnValueOnce(
    new Promise((_resolve, reject) => {
      deny = reject
    }),
  )
  const saving = state.save(state.rows[0])
  account.value = 'b'
  await settle()
  deny({ response: { status: 403 } })
  await saving
  expect(state.enabled).toBe(true)
  expect(state.rows[0].name).toBe('b')
  state.rows[0].max_active = 9
  mocks.capacity.mockRejectedValueOnce({ response: { status: 403 } })
  await state.load()
  await settle()
  expect(state.rows).toEqual([])
  expect(state.enabled).toBe(false)
  expect(state.unsavedLimits).toBe(false)
})

it('clears historical result rows, summaries and filter identities on confirmed denial', async () => {
  mocks.history.mockResolvedValue({
    data: {
      data: {
        results: [],
        summary: { total: 7, met: 6, breached: 1, unanswered: 0, legacy: 0 },
        has_more: true,
        filters: {
          agents: [{ id: 'private', name: 'Private agent' }],
          policy_versions: [],
        },
      },
    },
  })
  const state = mount(InboxSLAHistory)
  await settle()
  expect(state.summary.total).toBe(7)
  mocks.history.mockRejectedValueOnce({ response: { status: 403 } })
  await state.load()
  expect(state.summary.total).toBe(0)
  expect(state.agents).toEqual([])
  expect(state.rows).toEqual([])
  expect(state.more).toBe(false)
})
it('issues one first-page report request when filters change from a later page', async () => {
  mocks.accounts.mockResolvedValue({ data: { data: { accounts: [] } } })
  mocks.teams.mockResolvedValue({ teams: [] })
  mocks.report.mockResolvedValue(emptyReport)
  const state = mount(InboxSLAReport)
  await settle()
  state.offset = 100
  await settle()
  mocks.report.mockClear()
  state.account = 'another-account'
  await settle()
  expect(mocks.report).toHaveBeenCalledTimes(1)
  expect(mocks.report).toHaveBeenLastCalledWith('another-account', '', 0, false)
})

it('issues one workload request on a context reset and clears denied private rows', async () => {
  const account = ref('a')
  mocks.workload.mockResolvedValue({
    data: {
      data: {
        agents: [{ agent_id: 'private', name: 'Private agent' }],
        unique_agents: 1,
        has_more: true,
        updated_at: new Date().toISOString(),
      },
    },
  })
  mount({ render: () => h(InboxWorkload, { account: account.value }) })
  await settle()
  const state = mountedVm.$.subTree.component.setupState
  state.offset = 100
  await settle()
  mocks.workload.mockClear()
  account.value = 'b'
  await settle()
  expect(mocks.workload).toHaveBeenCalledTimes(1)
  expect(mocks.workload).toHaveBeenLastCalledWith(undefined, 'b', 0)
  mocks.workload.mockRejectedValueOnce({ response: { status: 403 } })
  await state.load()
  await settle()
  expect(state.rows).toHaveLength(0)
  expect(state.uniqueAgents).toBe(0)
  expect(state.more).toBe(false)
  expect(root.textContent).not.toContain('Private agent')
})

it('hides old report results during a scope change and recovers failed lookups separately', async () => {
  mocks.accounts
    .mockRejectedValueOnce(new Error('synthetic outage'))
    .mockResolvedValue({ data: { data: { accounts: [] } } })
  mocks.teams.mockResolvedValue({ teams: [] })
  mocks.report.mockResolvedValueOnce(emptyReport)
  const state = mount(InboxSLAReport)
  await settle()
  expect(state.lookupFailed).toBe(true)
  await state.loadLookups()
  expect(state.lookupFailed).toBe(false)
  let finish!: (value: unknown) => void
  mocks.report.mockImplementationOnce(
    () =>
      new Promise((resolve) => {
        finish = resolve
      }),
  )
  state.team = 'new-team'
  await settle()
  expect(state.loading).toBe(true)
  expect(state.rows).toHaveLength(0)
  expect(root.textContent).toContain('inboxReview.updatingTargets')
  expect(root.textContent).not.toContain('inboxSla.empty')
  finish(emptyReport)
  await settle()
  expect(state.loading).toBe(false)
  state.section = 'history'
  await settle()
  const calls = mocks.report.mock.calls.length
  await state.load()
  expect(mocks.report).toHaveBeenCalledTimes(calls)
})

it('enforces the combined predicate budget in the legacy field editor', async () => {
  mocks.fields.mockResolvedValue({
    data: {
      data: {
        definitions: [
          { id: 'field', label: 'Reference', kind: 'text', scope: 'case' },
        ],
      },
    },
  })
  const state = mount(InboxFieldMatchers, {
    modelValue: [],
    predicateCount: 20,
  })
  await settle()
  state.chosen = 'field'
  state.value = 'one'
  await nextTick()
  state.add()
  expect(state.matches).toEqual([])
})

it('keeps a preset draft when switching is cancelled and supports confirmed discard', async () => {
  mocks.presets.mockResolvedValue({
    data: {
      data: {
        presets: [
          { id: 'a', name: 'Original', max_active: 3 },
          { id: 'b', name: 'Other', max_active: 4 },
        ],
      },
    },
  })
  const state = mount(InboxCapacityPresets, {
    team: 'team',
    account: 'account',
    agents: ['agent'],
  })
  await settle()
  state.selected = 'a'
  state.edit()
  state.name = 'Unsaved'
  await nextTick()
  const control = root.querySelector('select')!
  control.value = 'b'
  control.dispatchEvent(new Event('change'))
  await settle()
  expect(state.discardOpen).toBe(true)
  expect(state.selected).toBe('a')
  state.cancelDiscard()
  expect(state.name).toBe('Unsaved')
  control.value = 'b'
  control.dispatchEvent(new Event('change'))
  await settle()
  state.confirmDiscard()
  await settle()
  expect(state.selected).toBe('b')
  expect(state.editing).toBe(false)
  state.removing = true
  mocks.remove
    .mockRejectedValueOnce(new Error('synthetic deletion outage'))
    .mockResolvedValue({})
  await state.remove()
  await settle()
  expect(state.removing).toBe(true)
  expect(
    document.querySelector('[role="alertdialog"] [role="alert"]')?.textContent,
  ).toContain('inboxReview.presetDeleteError')
  await state.remove()
  expect(mocks.remove).toHaveBeenCalledWith('b')
  expect(state.selected).toBe('')
})

it('does not restore discarded policy edits after a save and account switch', async () => {
  mocks.accounts.mockResolvedValue({
    data: {
      data: {
        accounts: [
          { id: 'a', name: 'A' },
          { id: 'b', name: 'B' },
        ],
      },
    },
  })
  mocks.policies.mockImplementation(async () => ({
    data: {
      data: {
        policies: [
          {
            account_id: 'a',
            first_reply_minutes: 30,
            next_reply_minutes: 60,
            resolution_minutes: 480,
            timezone: 'UTC',
            business_hours: false,
            shifts: [{ Day: 1, StartMinute: 480, EndMinute: 1020 }],
          },
        ],
      },
    },
  }))
  mocks.savePolicy.mockImplementation(async (_account, policy) => ({
    data: { data: { policy: JSON.parse(JSON.stringify(policy)) } },
  }))
  const state = mount(InboxSLASettings)
  await settle()
  state.selectAccount({ target: { value: 'a' } })
  await nextTick()
  await state.save()
  state.policy.first_reply_minutes = 999
  state.policy.shifts[0].StartMinute = 900
  state.selectAccount({ target: { value: 'b' } })
  expect(state.discardOpen).toBe(true)
  state.confirmDiscard()
  await nextTick()
  state.selectAccount({ target: { value: 'a' } })
  await nextTick()
  expect(state.policy.first_reply_minutes).toBe(30)
  expect(state.policy.shifts[0].StartMinute).toBe(480)
})

it('refreshes response-target state for a quiet open case without a version change', async () => {
  vi.useFakeTimers()
  const c = {
    id: 'case',
    version: 1,
    status: 'open',
    priority: 'normal',
    sla: { resolution: { status: 'on_track', remaining_seconds: 600 } },
  }
  mocks.conversation
    .mockResolvedValueOnce({ data: { data: { case: c, activity: [] } } })
    .mockResolvedValue({
      data: {
        data: {
          case: {
            ...c,
            sla: { resolution: { status: 'at_risk', remaining_seconds: 280 } },
          },
          activity: [],
        },
      },
    })
  const state = mount(InboxCaseActions, {
    accountId: 'account',
    contactId: 'contact',
    canWrite: false,
  })
  await settle()
  expect(state.current.sla.resolution.status).toBe('on_track')
  await vi.advanceTimersByTimeAsync(30000)
  await settle()
  expect(state.current.version).toBe(1)
  expect(state.current.sla.resolution.status).toBe('at_risk')
  expect(state.current.sla.resolution.remaining_seconds).toBe(280)
})
it('rejects invalid choices with a specific correction before issuing a create request', async () => {
  mocks.fields.mockResolvedValue({ data: { data: { definitions: [] } } })
  const state = mount(InboxFieldsView)
  await settle()
  state.begin()
  state.form.label = 'Order state'
  state.form.kind = 'select'
  state.choices = 'New\nNew'
  await nextTick()
  await state.save()
  expect(state.saveError).toBe('inboxReview.choiceDuplicate')
  expect(mocks.createField).not.toHaveBeenCalled()
  state.choices = 'a'.repeat(101)
  await nextTick()
  await state.save()
  expect(state.saveError).toBe('inboxReview.choiceLength')
  state.choices = 'New\nComplete'
  await nextTick()
  expect(state.choiceError).toBe('')
})

it('clears workload rows and totals when scope changes and never labels pending or failed data as empty', async () => {
  const account = ref('a')
  const reply = (name: string) => ({
    data: {
      data: {
        agents: [
          {
            agent_id: name,
            name,
            team_id: 'team',
            team_name: 'Team',
            account_id: name,
            account_name: name,
            active: 1,
            max_active: 4,
            available: true,
          },
        ],
        unique_agents: 1,
        has_more: false,
        updated_at: new Date().toISOString(),
      },
    },
  })
  mocks.workload.mockResolvedValueOnce(reply('Original'))
  mount({ render: () => h(InboxWorkload, { account: account.value }) })
  await settle()
  const state = mountedVm.$.subTree.component.setupState
  expect(root.textContent).toContain('Original')
  let finish!: (value: unknown) => void
  mocks.workload.mockImplementationOnce(
    () =>
      new Promise((resolve) => {
        finish = resolve
      }),
  )
  account.value = 'b'
  await settle()
  expect(state.loading).toBe(true)
  expect(state.rows).toHaveLength(0)
  expect(root.textContent).not.toContain('Original')
  expect(root.textContent).not.toContain('inboxReview.workloadSummary')
  finish(reply('Current'))
  await settle()
  expect(root.textContent).toContain('Current')
  mocks.workload.mockRejectedValueOnce(new Error('synthetic report outage'))
  account.value = 'c'
  await settle()
  expect(state.failed).toBe(true)
  expect(root.textContent).not.toContain('inboxReview.workloadSummary')
  expect(root.textContent).not.toContain('inboxUX.capacityEmptyHelp')
})
