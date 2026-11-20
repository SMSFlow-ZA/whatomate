// @vitest-environment happy-dom
import { createApp, nextTick } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
import InboxCaseActions from './InboxCaseActions.vue'
import InboxFieldsView from '@/views/settings/InboxFieldsView.vue'
import InboxSLAReport from './InboxSLAReport.vue'
import InboxFieldMatchers from './InboxFieldMatchers.vue'
import InboxCapacityPresets from '@/components/settings/InboxCapacityPresets.vue'
import InboxSLASettings from '@/components/settings/InboxSLASettings.vue'

const mocks = vi.hoisted(() => ({
  report: vi.fn(),
  conversation: vi.fn(),
  createField: vi.fn(),
  accounts: vi.fn(),
  teams: vi.fn(),
  policies: vi.fn(),
  savePolicy: vi.fn(),
  fields: vi.fn(),
  presets: vi.fn(),
  remove: vi.fn(),
}))
vi.mock('@/services/inbox', () => ({
  inboxService: {
    settings: async () => ({ data: { data: { enabled: true } } }),
    slaReport: mocks.report,
    conversation: mocks.conversation,
    createFieldDefinition: mocks.createField,
    fieldDefinitions: mocks.fields,
    capacityPresets: mocks.presets,
    deleteCapacityPreset: mocks.remove,
    slaPolicies: mocks.policies,
    setSlaPolicy: mocks.savePolicy,
  },
}))
vi.mock('@/services/api', () => ({ accountsService: { list: mocks.accounts } }))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ hasPermission: () => true }),
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
function mount(component: Parameters<typeof createApp>[0], props = {}) {
  root = document.createElement('div')
  document.body.append(root)
  app = createApp(component, props)
  app.component('RouterLink', { template: '<a><slot /></a>' })
  app.config.globalProperties.$t = (key: string) => key
  const vm = app.mount(root)
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
            shifts: [{ Day: 1, StartMinute: 480, EndMinute: 1020 }],
          },
        ],
      },
    },
  }))
  mocks.savePolicy.mockResolvedValue({})
  const state = mount(InboxSLASettings)
  await settle()
  state.selected = 'a'
  await nextTick()
  await state.save()
  state.policy.first_reply_minutes = 999
  state.policy.shifts[0].StartMinute = 900
  state.selected = 'b'
  await nextTick()
  state.selected = 'a'
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
