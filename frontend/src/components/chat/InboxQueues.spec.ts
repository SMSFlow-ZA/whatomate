// @vitest-environment happy-dom
import { createApp, h, nextTick, ref } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
import InboxQueues from './InboxQueues.vue'
import type {
  InboxFilters,
  InboxQueueDiscovery,
  QueueCase,
  SavedInboxView,
} from '@/services/inbox'
import type { useInboxQueuePage } from '@/composables/useInboxQueuePage'

const mocks = vi.hoisted(() => ({
  queue: vi.fn(),
  teams: vi.fn(),
  views: vi.fn(),
  fields: vi.fn(),
  accounts: vi.fn().mockResolvedValue({ data: { data: { accounts: [] } } }),
  saveView: vi.fn(),
  permission: vi.fn(() => true),
  users: vi.fn().mockResolvedValue({ data: { data: { users: [] } } }),
  tags: vi.fn().mockResolvedValue({ data: { data: { tags: [] } } }),
}))
vi.mock('@/services/inbox', () => ({
  inboxService: {
    queue: mocks.queue,
    views: mocks.views,
    fieldDefinitions: mocks.fields,
    saveView: mocks.saveView,
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
    list: mocks.accounts,
  },
  usersService: {
    list: mocks.users,
  },
  tagsService: {
    list: mocks.tags,
  },
}))
vi.mock('@/stores/teams', () => ({
  useTeamsStore: () => ({ fetchTeams: mocks.teams }),
}))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ hasPermission: mocks.permission }),
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-sonner', () => ({ toast: { error: vi.fn() } }))
vi.mock('./InboxFieldMatchers.vue', () => ({ default: { render: () => null } }))
vi.mock('./InboxSLAIndicators.vue', () => ({ default: { render: () => null } }))

afterEach(() => {
  for (const cleanup of discoveryCleanups.splice(0)) cleanup()
  vi.useRealTimers()
  vi.restoreAllMocks()
  vi.clearAllMocks()
  mocks.permission.mockImplementation(() => true)
})

it('does not request unavailable optional filter directories for restricted agents', async () => {
  discoveryDefaults()
  mocks.permission.mockImplementation(
    (...args: unknown[]) => !['users', 'tags'].includes(String(args[0])),
  )
  const { root, close } = await discoveryHarness()
  try {
    expect(mocks.users).not.toHaveBeenCalled()
    expect(mocks.tags).not.toHaveBeenCalled()
    expect(root.textContent).not.toContain('inboxQuality.lookupError')
    expect(root.querySelector('[role="alert"]')).toBeNull()
  } finally {
    close()
  }
})

const discoveryCleanups: (() => void)[] = []
type DiscoveryQueueState = {
  filters: InboxFilters
  discovery: InboxQueueDiscovery
  activeView: string
  navigationReset: boolean
  views: SavedInboxView[]
  accounts: { id: string; name: string }[]
  rows: QueueCase[]
  viewName: string
  selected: string[]
  saving: boolean
  chooseView: () => void
  refresh: () => Promise<void>
  refreshFromStart: () => Promise<void>
  saveView: () => Promise<void>
  pager: ReturnType<typeof useInboxQueuePage>
}
async function settleDiscovery() {
  for (let i = 0; i < 16; i++) {
    await Promise.resolve()
    await nextTick()
  }
}
async function discoveryHarness(context = ref('discovery-user:org-a')) {
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp({
    render: () => h(InboxQueues, { active: true, contextKey: context.value }),
  })
  const vm = app.mount(root)
  const state = (
    vm.$.subTree.component as unknown as { setupState: DiscoveryQueueState }
  ).setupState
  let closed = false
  const close = () => {
    if (closed) return
    closed = true
    app.unmount()
    root.remove()
    localStorage.removeItem('inbox-navigation:discovery-user:org-a')
    localStorage.removeItem('inbox-navigation:discovery-user:org-b')
  }
  discoveryCleanups.push(close)
  await settleDiscovery()
  return { root, state, context, close }
}
function discoveryDefaults() {
  mocks.queue
    .mockReset()
    .mockResolvedValue({ data: { data: { cases: [], has_more: false } } })
  mocks.views.mockReset().mockResolvedValue({ data: { data: { views: [] } } })
  mocks.accounts.mockReset().mockResolvedValue({
    data: {
      data: { accounts: [{ id: 'account-a', name: 'Synthetic inbox A' }] },
    },
  })
  mocks.fields.mockResolvedValue({ data: { data: { definitions: [] } } })
  mocks.teams.mockResolvedValue({ teams: [] })
}
it('persists sort and exact dates within their viewer context and ignores malformed saved discovery', async () => {
  discoveryDefaults()
  const key = 'inbox-navigation:discovery-user:org-a'
  localStorage.setItem(
    key,
    JSON.stringify({
      filters: { queue: 'mine', account_id: 'account-a' },
      discovery: {
        order: 'oldest',
        timezone: 'UTC',
        opened_from: '2026-11-01T00:00:00.000001Z',
      },
      view: '',
    }),
  )
  const f = await discoveryHarness()
  expect(mocks.queue.mock.lastCall![4]).toMatchObject({
    order: 'oldest',
    opened_from: '2026-11-01T00:00:00.000001Z',
  })
  f.state.discovery.order = 'newest'
  await settleDiscovery()
  expect(JSON.parse(localStorage.getItem(key)!).discovery.order).toBe('newest')
  localStorage.setItem(
    'inbox-navigation:discovery-user:org-b',
    JSON.stringify({
      discovery: { order: 'unsupported' },
      view: 'invalid-view',
    }),
  )
  f.context.value = 'discovery-user:org-b'
  await settleDiscovery()
  expect(f.state.discovery).toEqual({ order: 'priority_waiting' })
  expect(f.state.activeView).toBe('')
  expect(f.state.navigationReset).toBe(true)
  expect(mocks.queue.mock.lastCall![0]).toMatchObject({ queue: 'mine' })
})
it('deep-copies saved discovery and restores the legacy default when switching views', async () => {
  discoveryDefaults()
  const saved = {
    id: 'saved',
    name: 'Saved order',
    filters: { queue: 'mine' },
    discovery: {
      order: 'oldest',
      timezone: 'UTC',
      opened_from: '2026-11-01T00:00:00Z',
    },
  }
  mocks.views.mockResolvedValue({
    data: {
      data: {
        views: [
          saved,
          { id: 'legacy', name: 'Legacy view', filters: { queue: 'mine' } },
        ],
      },
    },
  })
  const f = await discoveryHarness()
  f.state.activeView = 'saved'
  f.state.chooseView()
  await settleDiscovery()
  f.state.discovery.order = 'newest'
  expect(saved.discovery.order).toBe('oldest')
  f.state.activeView = 'legacy'
  f.state.chooseView()
  await settleDiscovery()
  expect(f.state.discovery).toEqual({ order: 'priority_waiting' })
})
it('holds background movement while selecting and offers explicit first-page refresh for changed results', async () => {
  vi.useFakeTimers()
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  discoveryDefaults()
  const row = {
    id: 'one',
    contact_id: 'contact-one',
    contact_name: 'Visible synthetic enquiry',
    account_id: 'account-a',
    account_name: 'Synthetic inbox A',
    status: 'open',
    priority: 'normal',
    version: 1,
  }
  mocks.queue
    .mockResolvedValueOnce({
      data: { data: { cases: [row], counts: { all: 1 }, has_more: false } },
    })
    .mockResolvedValue({
      data: {
        data: {
          cases: [{ ...row, id: 'two', contact_name: 'New synthetic enquiry' }],
          counts: { all: 2 },
          has_more: false,
        },
      },
    })
  const f = await discoveryHarness()
  f.state.selected = ['one']
  await settleDiscovery()
  await vi.advanceTimersByTimeAsync(20000)
  expect(mocks.queue).toHaveBeenCalledTimes(1)
  f.state.selected = []
  await vi.advanceTimersByTimeAsync(20000)
  await settleDiscovery()
  expect(f.state.rows[0].id).toBe('one')
  expect(f.state.pager.changed.value).toBe(true)
  await f.state.refreshFromStart()
  await settleDiscovery()
  expect(f.state.rows[0].id).toBe('two')
  expect(f.state.pager.changed.value).toBe(false)
  expect(document.activeElement).toBe(
    f.root.querySelector('[data-queue-refresh]'),
  )
})
it('discards old lookup and saved-view responses after a context switch', async () => {
  discoveryDefaults()
  let oldViews!: (value: unknown) => void,
    oldAccounts!: (value: unknown) => void
  mocks.views
    .mockReturnValueOnce(
      new Promise((resolve) => {
        oldViews = resolve
      }),
    )
    .mockResolvedValue({
      data: {
        data: {
          views: [
            { id: 'new-view', name: 'Current permitted view', filters: {} },
          ],
        },
      },
    })
  mocks.accounts
    .mockReturnValueOnce(
      new Promise((resolve) => {
        oldAccounts = resolve
      }),
    )
    .mockResolvedValue({
      data: {
        data: {
          accounts: [{ id: 'new-account', name: 'Current permitted inbox' }],
        },
      },
    })
  const f = await discoveryHarness()
  f.context.value = 'discovery-user:org-b'
  await settleDiscovery()
  oldViews({
    data: {
      data: {
        views: [{ id: 'old-view', name: 'Old private view', filters: {} }],
      },
    },
  })
  oldAccounts({
    data: {
      data: { accounts: [{ id: 'old-account', name: 'Old private inbox' }] },
    },
  })
  await settleDiscovery()
  expect(f.state.views.map((v) => v.name)).toEqual(['Current permitted view'])
  expect(f.state.accounts.map((v) => v.name)).toEqual([
    'Current permitted inbox',
  ])
  expect(f.root.textContent).not.toContain('Old private')
})
it('does not select a newly saved view in a different context after a late acknowledgement', async () => {
  discoveryDefaults()
  let acknowledge!: (value: unknown) => void
  mocks.saveView.mockReturnValueOnce(
    new Promise((resolve) => {
      acknowledge = resolve
    }),
  )
  const f = await discoveryHarness()
  f.state.viewName = 'Owned old view'
  const saving = f.state.saveView()
  f.context.value = 'discovery-user:org-b'
  await settleDiscovery()
  acknowledge({
    data: {
      data: {
        view: {
          id: 'old-view',
          filters: { account_id: 'old-account' },
          discovery: { order: 'oldest' },
        },
      },
    },
  })
  await saving
  await settleDiscovery()
  expect(f.state.activeView).toBe('')
  expect(f.state.filters).toMatchObject({ queue: 'mine' })
  expect(f.state.saving).toBe(false)
})
it('clears old queue and cached lookup material after confirmed queue access loss', async () => {
  discoveryDefaults()
  const row = {
    id: 'one',
    contact_id: 'contact-one',
    contact_name: 'Previously permitted enquiry',
    account_id: 'account-a',
    account_name: 'Synthetic inbox A',
    status: 'open',
    priority: 'normal',
    version: 1,
  }
  mocks.queue
    .mockResolvedValueOnce({
      data: { data: { cases: [row], has_more: false } },
    })
    .mockRejectedValue({ response: { status: 403 } })
  const f = await discoveryHarness()
  await f.state.refresh()
  await settleDiscovery()
  expect(f.state.rows).toEqual([])
  expect(f.state.accounts).toEqual([])
  expect(f.state.views).toEqual([])
  expect(f.root.textContent).not.toContain('Previously permitted enquiry')
  expect(f.state.pager.denied.value).toBe(true)
})

it('ignores a late saved-view acknowledgement after the queue is unmounted', async () => {
  discoveryDefaults()
  let acknowledge!: (value: unknown) => void
  mocks.saveView.mockReturnValueOnce(
    new Promise((resolve) => {
      acknowledge = resolve
    }),
  )
  const f = await discoveryHarness()
  f.state.viewName = 'Unmounted draft'
  const saving = f.state.saveView()
  const calls = mocks.views.mock.calls.length
  f.close()
  acknowledge({
    data: {
      data: {
        view: { id: 'late-view', filters: {}, discovery: { order: 'oldest' } },
      },
    },
  })
  await saving
  await settleDiscovery()
  expect(mocks.views).toHaveBeenCalledTimes(calls)
  expect(f.state.activeView).toBe('')
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
