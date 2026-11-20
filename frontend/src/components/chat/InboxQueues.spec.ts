// @vitest-environment happy-dom
import { createApp, h, nextTick, ref } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
import InboxQueues from './InboxQueues.vue'

const mocks = vi.hoisted(() => ({
  queue: vi.fn(),
  teams: vi.fn(),
  views: vi.fn(),
  fields: vi.fn(),
}))
vi.mock('@/services/inbox', () => ({
  inboxService: {
    queue: mocks.queue,
    views: mocks.views,
    fieldDefinitions: mocks.fields,
  },
}))

it('isolates saved-view drafts and detaches its scope when the team changes', async () => {
  const view = {
    id: 'shared-view',
    name: 'Team view',
    team_id: 'team-a',
    filters: {
      predicates: [{ field: 'priority', operator: 'eq', value: 'high' }],
    },
  }
  mocks.queue.mockResolvedValue({
    data: { data: { cases: [], has_more: false } },
  })
  mocks.views.mockResolvedValue({ data: { data: { views: [view] } } })
  mocks.fields.mockResolvedValue({ data: { data: { definitions: [] } } })
  mocks.teams.mockResolvedValue({ teams: [{ id: 'team-a', name: 'Team A' }] })
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp({ render: () => h(InboxQueues, { active: true }) })
  const vm = app.mount(root)
  const state = (
    vm.$.subTree.component as unknown as {
      setupState: {
        activeView: string
        filters: { team_id?: string; predicates?: unknown[] }
        chooseView: () => void
        changeTeam: () => void
      }
    }
  ).setupState
  const settle = async () => {
    for (let i = 0; i < 15; i++) {
      await Promise.resolve()
      await nextTick()
    }
  }
  try {
    await settle()
    state.activeView = view.id
    state.chooseView()
    expect(state.filters.team_id).toBe('team-a')
    state.filters.predicates!.splice(0, 1)
    expect(view.filters.predicates).toHaveLength(1)
    state.chooseView()
    expect(state.filters.predicates).toHaveLength(1)
    state.filters.team_id = undefined
    state.changeTeam()
    await settle()
    expect(state.activeView).toBe('')
    expect(mocks.queue.mock.lastCall![3]).toBeUndefined()
  } finally {
    app.unmount()
    root.remove()
  }
})
vi.mock('@/services/api', () => ({
  accountsService: {
    list: vi.fn().mockResolvedValue({ data: { data: { accounts: [] } } }),
  },
  usersService: {
    list: vi.fn().mockResolvedValue({ data: { data: { users: [] } } }),
  },
  tagsService: {
    list: vi.fn().mockResolvedValue({ data: { data: { tags: [] } } }),
  },
}))
vi.mock('@/stores/teams', () => ({
  useTeamsStore: () => ({ fetchTeams: mocks.teams }),
}))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ hasPermission: () => true }),
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-sonner', () => ({ toast: { error: vi.fn() } }))
vi.mock('./InboxFieldMatchers.vue', () => ({ default: { render: () => null } }))
vi.mock('./InboxSLAIndicators.vue', () => ({ default: { render: () => null } }))

afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
  vi.clearAllMocks()
})

it('polls only the active queue and surfaces recoverable lookup failures', async () => {
  vi.useFakeTimers()
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  mocks.queue.mockResolvedValue({
    data: { data: { cases: [], has_more: false } },
  })
  mocks.views.mockResolvedValue({ data: { data: { views: [] } } })
  mocks.teams
    .mockRejectedValueOnce(new Error('synthetic lookup outage'))
    .mockResolvedValue({ teams: [] })
  const active = ref(false)
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp({
    render: () => h(InboxQueues, { active: active.value }),
  })
  app.mount(root)
  const settle = async () => {
    for (let i = 0; i < 8; i++) await nextTick()
  }
  try {
    await settle()
    await vi.advanceTimersByTimeAsync(40_000)
    expect(mocks.queue).not.toHaveBeenCalled()
    expect(mocks.teams).not.toHaveBeenCalled()
    active.value = true
    await settle()
    expect(mocks.queue).toHaveBeenCalledTimes(1)
    expect(root.querySelector('[role=alert]')).not.toBeNull()
    root.querySelector<HTMLButtonElement>('[role=alert] button')!.click()
    await settle()
    expect(root.querySelector('[role=alert]')).toBeNull()
    await vi.advanceTimersByTimeAsync(20_000)
    expect(mocks.queue).toHaveBeenCalledTimes(2)
    active.value = false
    await settle()
    await vi.advanceTimersByTimeAsync(40_000)
    expect(mocks.queue).toHaveBeenCalledTimes(2)
    active.value = true
    await settle()
    expect(mocks.queue).toHaveBeenCalledTimes(3)
  } finally {
    app.unmount()
    root.remove()
  }
  await vi.advanceTimersByTimeAsync(40_000)
  expect(mocks.queue).toHaveBeenCalledTimes(3)
})

it('marks only the selected account thread as current for a shared contact', async () => {
  vi.useFakeTimers()
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  mocks.teams.mockResolvedValue({ teams: [] })
  mocks.views.mockResolvedValue({ data: { data: { views: [] } } })
  mocks.queue.mockResolvedValue({
    data: {
      data: {
        cases: [
          {
            id: 'case-support',
            contact_id: 'same-contact',
            contact_name: 'Example customer',
            account_id: 'support',
            account_name: 'Support inbox',
            status: 'open',
            priority: 'normal',
            version: 1,
          },
          {
            id: 'case-sales',
            contact_id: 'same-contact',
            contact_name: 'Example customer',
            account_id: 'sales',
            account_name: 'Sales inbox',
            status: 'open',
            priority: 'normal',
            version: 1,
          },
        ],
        has_more: false,
      },
    },
  })
  const account = ref('support')
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp({
    render: () =>
      h(InboxQueues, {
        active: true,
        selectedContact: 'same-contact',
        selectedAccount: account.value,
      }),
  })
  app.mount(root)
  try {
    for (let i = 0; i < 8; i++) await nextTick()
    expect(root.querySelectorAll('[aria-current="page"]')).toHaveLength(1)
    expect(root.querySelector('[aria-current="page"]')?.textContent).toContain(
      'Support inbox',
    )
    account.value = 'sales'
    await nextTick()
    expect(root.querySelectorAll('[aria-current="page"]')).toHaveLength(1)
    expect(root.querySelector('[aria-current="page"]')?.textContent).toContain(
      'Sales inbox',
    )
  } finally {
    app.unmount()
    root.remove()
  }
})

it('restores saved queue filters only within the same user and organization context', async () => {
  const key = 'inbox-navigation:quality-user:org-a'
  localStorage.setItem(
    key,
    JSON.stringify({
      filters: { queue: 'follow_ups', priority: 'high' },
      view: '',
    }),
  )
  mocks.queue.mockResolvedValue({
    data: { data: { cases: [], has_more: false } },
  })
  mocks.views.mockResolvedValue({ data: { data: { views: [] } } })
  mocks.teams.mockResolvedValue({ teams: [] })
  const context = ref('quality-user:org-a'),
    root = document.createElement('div')
  document.body.append(root)
  const app = createApp({
    render: () => h(InboxQueues, { active: true, contextKey: context.value }),
  })
  app.mount(root)
  try {
    for (let i = 0; i < 15; i++) {
      await Promise.resolve()
      await nextTick()
    }
    expect(mocks.queue.mock.calls[0]![0]).toMatchObject({
      queue: 'follow_ups',
      priority: 'high',
    })
    context.value = 'quality-user:org-b'
    for (let i = 0; i < 15; i++) {
      await Promise.resolve()
      await nextTick()
    }
    expect(mocks.queue.mock.lastCall![0]).toMatchObject({ queue: 'mine' })
    expect(mocks.queue.mock.lastCall![0].priority).toBeUndefined()
  } finally {
    app.unmount()
    root.remove()
    localStorage.removeItem(key)
    localStorage.removeItem('inbox-navigation:quality-user:org-b')
  }
})
