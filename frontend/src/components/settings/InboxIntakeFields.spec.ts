// @vitest-environment happy-dom
import { createApp, nextTick, ref } from 'vue'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import InboxFieldsView from '@/views/settings/InboxFieldsView.vue'

const mocks = vi.hoisted(() => ({
  fields: vi.fn(),
  create: vi.fn(),
  update: vi.fn(),
  accounts: vi.fn(),
  teams: vi.fn(),
}))
const context = ref('viewer-a'),
  canWrite = ref(true)
vi.mock('@/composables/useInboxViewerContext', () => ({
  useInboxViewerContext: () => context,
}))
vi.mock('@/services/inbox', () => ({
  inboxService: {
    settings: async () => ({ data: { data: { enabled: true } } }),
    fieldDefinitions: mocks.fields,
    createFieldDefinition: mocks.create,
    updateFieldDefinition: mocks.update,
  },
}))
vi.mock('@/services/api', () => ({ accountsService: { list: mocks.accounts } }))
vi.mock('@/stores/teams', () => ({
  useTeamsStore: () => ({ fetchTeams: mocks.teams }),
}))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ hasPermission: () => canWrite.value }),
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-router', () => ({
  onBeforeRouteLeave: vi.fn(),
  onBeforeRouteUpdate: vi.fn(),
}))

const envelope = (definitions: unknown[]) => ({
  data: { data: { definitions } },
})
const field = {
  id: 'reference',
  revision: 4,
  key: 'reference',
  label: 'Reference',
  scope: 'case',
  kind: 'text',
  required_on_resolve: false,
}
const deferred = () => {
  let resolve!: (value: unknown) => void
  const promise = new Promise((r) => {
    resolve = r
  })
  return { promise, resolve }
}
const settle = async () => {
  for (let i = 0; i < 18; i++) {
    await Promise.resolve()
    await nextTick()
  }
}
let app: ReturnType<typeof createApp> | undefined, root: HTMLElement
function mount() {
  root = document.createElement('div')
  document.body.append(root)
  app = createApp(InboxFieldsView)
  app.component('RouterLink', { template: '<a><slot /></a>' })
  app.config.globalProperties.$t = (key: string) => key
  const vm = app.mount(root)
  return (vm.$ as unknown as { setupState: Record<string, any> }).setupState
}
beforeEach(() => {
  vi.clearAllMocks()
  context.value = 'viewer-a'
  canWrite.value = true
  mocks.fields.mockResolvedValue(envelope([field]))
  mocks.accounts.mockResolvedValue({
    data: { data: { accounts: [{ id: 'account', name: 'Synthetic inbox' }] } },
  })
  mocks.teams.mockResolvedValue({
    teams: [{ id: 'team', name: 'Synthetic team', is_active: true }],
    total: 1,
  })
  mocks.create.mockResolvedValue({})
  mocks.update.mockResolvedValue({})
})
afterEach(() => {
  app?.unmount()
  app = undefined
  root?.remove()
})

it('preserves legacy optional/always semantics and authors typed conditions through the shared builder', async () => {
  const state = mount()
  await settle()
  state.begin(field)
  expect(state.requirementMode).toBe('optional')
  state.requirementMode = 'always'
  expect(state.form.required_on_resolve).toBe(true)
  expect(state.form.required_when).toBeNull()
  state.requirementMode = 'conditional'
  await settle()
  expect(state.requirementValid).toBe(false)
  const add = [...root.querySelectorAll('button')].find((b) =>
    b.textContent?.includes('inboxQuality.addCondition'),
  )!
  add.click()
  await settle()
  expect(state.form.required_when.predicates).toEqual([
    { field: 'priority', operator: 'eq', value: 'normal' },
  ])
  expect(state.requirementValid).toBe(true)
  const match = [...root.querySelectorAll('select')].find((s) =>
    s.querySelector('option[value="any"]'),
  )!
  match.value = 'any'
  match.dispatchEvent(new Event('change', { bubbles: true }))
  await settle()
  expect(state.form.required_when.match).toBe('any')
  await state.save()
  expect(mocks.update).toHaveBeenCalledWith(
    'reference',
    expect.objectContaining({
      revision: 4,
      required_on_resolve: false,
      required_when: {
        match: 'any',
        predicates: [{ field: 'priority', operator: 'eq', value: 'normal' }],
      },
    }),
  )
})

it('keeps a rejected definition draft and requires an explicit reload after a stale-edit conflict', async () => {
  const state = mount()
  await settle()
  state.begin(field)
  state.form.label = 'Unsent draft'
  mocks.update.mockRejectedValue({
    isAxiosError: true,
    response: { status: 409 },
  })
  await state.save()
  await settle()
  expect(state.conflict).toBe(true)
  expect(state.editing).toBe(true)
  expect(state.form.label).toBe('Unsent draft')
  expect(root.textContent).toContain('inboxIntake.reload')
  await state.save()
  expect(mocks.update).toHaveBeenCalledTimes(1)
})

it('drops late list and mutation results when viewer scope changes', async () => {
  const state = mount()
  await settle()
  const old = deferred(),
    current = deferred()
  mocks.fields
    .mockReturnValueOnce(old.promise)
    .mockReturnValueOnce(current.promise)
  const first = state.load()
  await settle()
  const second = state.load()
  await settle()
  current.resolve(envelope([{ ...field, label: 'Current' }]))
  await second
  old.resolve(envelope([{ ...field, label: 'Stale' }]))
  await first
  expect(state.definitions[0].label).toBe('Current')
  state.begin(field)
  const mutation = deferred()
  mocks.update.mockReturnValue(mutation.promise)
  const saving = state.save()
  context.value = 'viewer-b'
  await settle()
  mutation.resolve({})
  await saving
  expect(state.editing).toBe(false)
  expect(state.form.label).toBe('')
  expect(state.busy).toBe(false)
})

it('does not allow a conditional save with failed or unavailable reference choices', async () => {
  mocks.teams.mockRejectedValue(new Error('offline'))
  const state = mount()
  await settle()
  state.begin({
    ...field,
    conditional_required: true,
    required_when: {
      account_id: 'account',
      team_id: 'team',
      match: 'all',
      predicates: [],
    },
  })
  await settle()
  expect(state.requirementValid).toBe(false)
  expect(root.textContent).toContain('inboxIntake.choicesFailed')
  await state.save()
  expect(mocks.update).not.toHaveBeenCalled()
  mocks.teams.mockResolvedValue({
    teams: [{ id: 'team', name: 'Synthetic team', is_active: true }],
    total: 1,
  })
  const retry = [...root.querySelectorAll('button')].find((b) =>
    b.textContent?.includes('common.retry'),
  )!
  retry.click()
  await settle()
  expect(state.requirementValid).toBe(true)
  expect(state.form.required_when.team_id).toBe('team')
})

it('blocks archived condition definitions without changing their recorded values', async () => {
  const archived = { ...field, id: 'old', archived_at: '2026-11-25T00:00:00Z' }
  mocks.fields.mockResolvedValue(envelope([field, archived]))
  const state = mount()
  await settle()
  state.begin({
    ...field,
    required_when: {
      match: 'all',
      predicates: [
        {
          field: 'custom_field',
          definition_id: 'old',
          operator: 'eq',
          value: 'retained',
        },
      ],
    },
  })
  await settle()
  expect(state.requirementValid).toBe(false)
  expect(state.form.required_when.predicates[0].value).toBe('retained')
  await state.save()
  expect(mocks.update).not.toHaveBeenCalled()
})

it('adds explicit email/link kinds without converting existing text definitions', async () => {
  const state = mount()
  await settle()
  state.begin()
  state.form.label = 'Contact email'
  state.form.kind = 'email'
  await nextTick()
  expect(root.textContent).toContain('inboxIntake.emailHelp')
  await state.save()
  expect(mocks.create).toHaveBeenCalledWith(
    expect.objectContaining({ kind: 'email', required_when: null }),
  )
  state.begin(field)
  expect(state.form.kind).toBe('text')
})
