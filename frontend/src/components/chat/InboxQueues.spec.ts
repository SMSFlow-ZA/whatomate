// @vitest-environment happy-dom
import { createApp, h, nextTick, ref } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
import InboxQueues from './InboxQueues.vue'
vi.mock('./InboxFieldMatchers.vue', () => ({ default: { render: () => null } }))

const mocks = vi.hoisted(() => ({
  queue: vi.fn(),
  teams: vi.fn(),
  views: vi.fn(),
}))
vi.mock('@/services/inbox', () => ({
  inboxService: { queue: mocks.queue, views: mocks.views },
}))
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
vi.mock('@/lib/toast', () => ({ toast: { error: vi.fn() } }))

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
