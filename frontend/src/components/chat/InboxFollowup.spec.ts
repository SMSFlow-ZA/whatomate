// @vitest-environment happy-dom
import { createApp, nextTick, reactive } from 'vue'
import { afterEach, it, expect, vi } from 'vitest'
import InboxCaseActions from './InboxCaseActions.vue'
import InboxFieldPanel from './InboxFieldPanel.vue'
import InboxNextTarget from './InboxNextTarget.vue'
const mock = vi.hoisted(() => ({
  conversation: vi.fn(),
  update: vi.fn(),
  fields: vi.fn(),
  values: vi.fn(),
}))
const route = reactive<{ query: Record<string, string> }>({ query: {} })
vi.mock('@/composables/useInboxViewerContext', () => ({
  useInboxViewerContext: () => ({ value: 'followup-fixture-viewer' }),
}))
vi.mock('@/services/inboxIntake', () => ({
  inboxIntake: {
    summary: async (_account: string, _contact: string, caseId: string) => ({
      data: { data: { case_id: caseId, summaries: [], has_more: false } },
    }),
  },
}))
vi.mock('vue-router', () => ({
  useRoute: () => route,
  onBeforeRouteLeave: vi.fn(),
  onBeforeRouteUpdate: vi.fn(),
}))
vi.mock('@/services/inbox', () => ({
  inboxService: {
    conversation: mock.conversation,
    update: mock.update,
    settings: async () => ({ data: { data: { enabled: true } } }),
    fieldDefinitions: mock.fields,
    fieldValues: mock.values,
  },
}))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key, locale: { value: 'en' } }),
}))
vi.mock('vue-sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))
const settle = async () => {
  for (let i = 0; i < 25; i++) {
    await Promise.resolve()
    await nextTick()
  }
}
let app: ReturnType<typeof createApp>, root: HTMLElement
async function mount(
  component: Parameters<typeof createApp>[0],
  props: Record<string, unknown>,
) {
  root = document.createElement('div')
  document.body.append(root)
  app = createApp(component, props)
  app.component('RouterLink', { template: '<a><slot/></a>' })
  const vm = app.mount(root)
  await settle()
  return (vm.$ as unknown as { setupState: Record<string, any> }).setupState
}
afterEach(() => {
  app?.unmount()
  root?.remove()
  route.query = {}
  vi.clearAllMocks()
})
const activity = (id: string) => ({
  id,
  case_id: 'past',
  kind: 'workflow_note',
  created_at: '2026-11-20T10:00:00Z',
  detail: id,
})
const reply = (extra = {}) => ({
  data: {
    data: {
      case: {
        id: 'past',
        account_id: 'a',
        contact_id: 'c',
        status: 'resolved',
        priority: 'normal',
        version: 2,
      },
      read_only: true,
      current_case_id: 'current',
      activity: [activity('z')],
      activity_next_cursor: 'cursor-1',
      activity_has_more: true,
      ...extra,
    },
  },
})
it('shows exact historical identity and refuses operational actions', async () => {
  route.query = { case_id: 'past' }
  mock.conversation.mockResolvedValue(reply())
  const context = vi.fn()
  const state = await mount(InboxCaseActions, {
    accountId: 'a',
    contactId: 'c',
    canWrite: true,
    onContext: context,
  })
  expect(mock.conversation).toHaveBeenCalledWith('a', 'c', 'past')
  expect(state.readOnly).toBe(true)
  expect(state.current.id).toBe('past')
  expect(root.textContent).toContain('inboxFollowup.historical')
  expect(root.textContent).toContain('inboxFollowup.returnCurrent')
  expect(root.querySelector('select')).toBeNull()
  await state.update({ status: 'pending' })
  expect(mock.update).not.toHaveBeenCalled()
  expect(context).toHaveBeenCalledWith(
    expect.objectContaining({ readOnly: true, currentCaseId: 'current' }),
  )
})
it('loads older activity without duplicates or losing older pages on refresh', async () => {
  route.query = { case_id: 'past' }
  mock.fields.mockResolvedValue({ data: { data: { definitions: [] } } })
  mock.values.mockResolvedValue({
    data: { data: { subject_id: 'past', values: [] } },
  })
  mock.conversation
    .mockResolvedValueOnce(reply())
    .mockResolvedValueOnce(
      reply({
        activity: [activity('z'), activity('y')],
        activity_next_cursor: '',
        activity_has_more: false,
      }),
    )
    .mockResolvedValue(reply({ activity: [activity('zz'), activity('z')] }))
  const state = await mount(InboxCaseActions, {
    accountId: 'a',
    contactId: 'c',
    canWrite: true,
    history: true,
  })
  await state.loadOlder()
  await settle()
  expect(mock.conversation).toHaveBeenNthCalledWith(
    2,
    'a',
    'c',
    'past',
    'cursor-1',
  )
  expect(state.activity.map((x: any) => x.id)).toEqual(['z', 'y'])
  await state.load()
  expect(state.activity.map((x: any) => x.id)).toEqual(['zz', 'z', 'y'])
})
it('reads archived values from the exact historical case and keeps them disabled', async () => {
  mock.fields.mockResolvedValue({
    data: {
      data: {
        definitions: [
          {
            id: 'f',
            label: 'Archived reference',
            scope: 'case',
            kind: 'text',
            archived_at: '2026-11-01',
          },
        ],
      },
    },
  })
  mock.values.mockResolvedValue({
    data: {
      data: {
        subject_id: 'past',
        values: [{ definition_id: 'f', value: 'Recorded reference' }],
      },
    },
  })
  await mount(InboxFieldPanel, {
    account: 'a',
    contact: 'c',
    caseId: 'past',
    canWrite: false,
  })
  expect(mock.values).toHaveBeenCalledWith('c', 'a', 'past')
  expect(root.textContent).toContain('Archived reference')
  expect(root.querySelector('input')?.value).toBe('Recorded reference')
  expect(root.querySelector('input')?.disabled).toBe(true)
})
it('shows a recoverable error if field values do not match the selected case', async () => {
  mock.fields.mockResolvedValue({ data: { data: { definitions: [] } } })
  mock.values.mockResolvedValue({
    data: { data: { subject_id: 'current', values: [] } },
  })
  const state = await mount(InboxFieldPanel, {
    account: 'a',
    contact: 'c',
    caseId: 'past',
  })
  expect(state.failed).toBe(true)
  expect(root.querySelector('[role=alert]')).toBeTruthy()
})
it('keeps a newly authored field draft when a background response arrives', async () => {
  mock.fields.mockResolvedValue({
    data: {
      data: {
        definitions: [
          {
            id: 'f',
            scope: 'case',
            key: 'reference',
            label: 'Reference',
            kind: 'text',
          },
        ],
      },
    },
  })
  mock.values.mockResolvedValue({
    data: {
      data: {
        subject_id: 'past',
        values: [{ definition_id: 'f', value: 'Stored value' }],
      },
    },
  })
  const state = await mount(InboxFieldPanel, {
    account: 'a',
    contact: 'c',
    caseId: 'past',
    canWrite: true,
  })
  let finish!: (value: unknown) => void
  mock.values.mockReturnValueOnce(
    new Promise((resolve) => {
      finish = resolve
    }),
  )
  const pending = state.load(true)
  await settle()
  state.values.f = 'New authored draft'
  finish({
    data: {
      data: {
        subject_id: 'past',
        values: [{ definition_id: 'f', value: 'Remote update' }],
      },
    },
  })
  expect(await pending).toBe(false)
  expect(state.values.f).toBe('New authored draft')
  expect(state.stored.f).toBe('Stored value')
  expect(state.ready).toBe(true)
})

it('keeps a focused field draft mounted through background failure, retry and recovery', async () => {
  mock.fields.mockResolvedValue({
    data: {
      data: {
        definitions: [
          {
            id: 'f',
            scope: 'case',
            key: 'reference',
            label: 'Reference',
            kind: 'text',
          },
        ],
      },
    },
  })
  mock.values.mockResolvedValue({
    data: {
      data: {
        subject_id: 'past',
        values: [{ definition_id: 'f', value: 'Stored value' }],
      },
    },
  })
  const state = await mount(InboxFieldPanel, {
    account: 'a',
    contact: 'c',
    caseId: 'past',
    canWrite: true,
  })
  const input = root.querySelector('input')!
  input.focus()
  let fail!: (error: Error) => void
  mock.values.mockReturnValueOnce(
    new Promise((_resolve, reject) => {
      fail = reject
    }),
  )
  const pending = state.load(true)
  await settle()
  state.values.f = 'Authored during request'
  fail(new Error('Synthetic temporary outage'))
  expect(await pending).toBe(false)
  await settle()
  expect(state.failed).toBe(true)
  expect(root.querySelector('input')).toBe(input)
  expect(document.activeElement).toBe(input)
  expect(input.value).toBe('Authored during request')
  expect(root.textContent).toContain('inboxLive.fieldRefreshError')
  const calls = mock.values.mock.calls.length
  const retry = [...root.querySelectorAll('button')].find((button) =>
    button.textContent?.includes('common.retry'),
  )!
  retry.click()
  await settle()
  expect(mock.values).toHaveBeenCalledTimes(calls)
  expect(state.live.pending.value).toBe(true)
  expect(input.value).toBe('Authored during request')
  mock.values.mockResolvedValue({
    data: {
      data: {
        subject_id: 'past',
        values: [{ definition_id: 'f', value: 'Recovered server value' }],
      },
    },
  })
  state.cancelField('f')
  await settle()
  expect(root.querySelector('input')).toBe(input)
  expect(input.value).toBe('Recovered server value')
  expect(state.failed).toBe(false)
  expect(state.live.pending.value).toBe(false)
})

it('defers a case refresh if a resolution review opens during its lookup', async () => {
  mock.conversation.mockResolvedValue(reply())
  const state = await mount(InboxCaseActions, {
    accountId: 'a',
    contactId: 'c',
    canWrite: true,
  })
  let finish!: (value: unknown) => void
  mock.conversation.mockReturnValueOnce(
    new Promise((resolve) => {
      finish = resolve
    }),
  )
  const pending = state.load(false, true)
  state.resolving = true
  finish(reply({ case: { ...state.current, id: 'replacement', version: 3 } }))
  expect(await pending).toBe(false)
  expect(state.current.id).toBe('past')
  expect(state.resolving).toBe(true)
})
it('shows only the earliest actionable target with the server timezone', async () => {
  await mount(InboxNextTarget, {
    sla: {
      first_reply: {
        status: 'met',
        deadline: '2026-11-20T09:00:00Z',
        remaining_seconds: 0,
      },
      next_reply: {
        status: 'on_track',
        deadline: '2026-11-20T12:00:00Z',
        remaining_seconds: 120,
        timezone: 'Africa/Johannesburg',
      },
      resolution: {
        status: 'paused',
        deadline: '2026-11-20T10:00:00Z',
        remaining_seconds: 400,
      },
    },
  })
  expect(root.textContent).toContain('next_reply')
  expect(root.textContent).not.toContain('first_reply')
  expect(root.textContent).not.toContain('resolution')
  expect(root.textContent).toContain('Africa/Johannesburg')
  expect(root.querySelector('time')?.dateTime).toBe('2026-11-20T12:00:00Z')
})
