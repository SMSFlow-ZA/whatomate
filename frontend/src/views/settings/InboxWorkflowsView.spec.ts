// @vitest-environment happy-dom
import { createApp, nextTick } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
import InboxWorkflowsView from './InboxWorkflowsView.vue'

const mock = vi.hoisted(() => ({
  definitions: vi.fn(),
  preview: vi.fn(),
  queue: vi.fn(),
}))
vi.mock('@/services/inbox', () => ({
  inboxService: {
    settings: vi.fn().mockResolvedValue({ data: { data: { enabled: true } } }),
    workflowDefinitions: mock.definitions,
    previewWorkflow: mock.preview,
    queue: mock.queue,
    workflowRuns: vi.fn().mockResolvedValue({ data: { data: { runs: [] } } }),
    automationState: vi.fn().mockResolvedValue({
      data: { data: { enabled: false, dispatch_available: false } },
    }),
  },
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
  templatesService: {
    list: vi.fn().mockResolvedValue({ data: { data: { templates: [] } } }),
  },
}))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ hasPermission: () => true }),
}))
vi.mock('@/stores/teams', () => ({
  useTeamsStore: () => ({
    fetchTeams: vi.fn().mockResolvedValue({ teams: [] }),
  }),
}))
vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} }),
  onBeforeRouteLeave: vi.fn(),
  onBeforeRouteUpdate: vi.fn(),
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }))
vi.mock('@/components/settings/InboxSLASettings.vue', () => ({
  default: { render: () => null },
}))
vi.mock('@/components/chat/InboxFieldMatchers.vue', () => ({
  default: { render: () => null },
}))

const reply = (workflows: unknown[]) => ({ data: { data: { workflows } } })
const workflow = (kind: string, name: string) => ({
  id: name,
  kind,
  name,
  position: 0,
  conditions: {},
  trigger: kind === 'rule' ? 'case_opened' : '',
  actions: [{ type: 'note', value: 'Synthetic internal note' }],
})
const deferred = () => {
  let resolve!: (value: any) => void
  const promise = new Promise<any>((done) => {
    resolve = done
  })
  return { promise, resolve }
}
const settle = async () => {
  for (let i = 0; i < 20; i++) {
    await Promise.resolve()
    await nextTick()
  }
}
const mounts: { app: ReturnType<typeof createApp>; root: HTMLElement }[] = []
afterEach(() => {
  for (const { app, root } of mounts.splice(0)) {
    app.unmount()
    root.remove()
  }
  vi.clearAllMocks()
})
async function mount() {
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp(InboxWorkflowsView)
  app.component('RouterLink', { template: '<a><slot/></a>' })
  app.config.globalProperties.$t = (key: string) => key
  app.mount(root)
  mounts.push({ app, root })
  await settle()
  return root
}
function click(root: HTMLElement, text: string) {
  const button = Array.from(root.querySelectorAll('button')).find(
    (button) => button.textContent?.trim() === text,
  )
  expect(button, `Missing ${text}`).toBeTruthy()
  button!.click()
}

it('never renders a late macro list under the Rules tab', async () => {
  const macro = deferred(),
    rule = deferred()
  mock.definitions
    .mockResolvedValueOnce(reply([workflow('rule', 'Initial rule')]))
    .mockImplementation((kind: string) =>
      kind === 'macro' ? macro.promise : rule.promise,
    )
  const root = await mount()
  click(root, 'inboxRules.macros')
  await settle()
  click(root, 'inboxRules.rules')
  await settle()
  rule.resolve(reply([workflow('rule', 'Current rule')]))
  await settle()
  macro.resolve(reply([workflow('macro', 'Stale macro')]))
  await settle()
  expect(root.textContent).toContain('Current rule')
  expect(root.textContent).not.toContain('Stale macro')
})

it('discards a dry run when the agent has selected another case', async () => {
  mock.definitions.mockResolvedValue(reply([workflow('rule', 'Example rule')]))
  mock.queue.mockResolvedValue({
    data: {
      data: {
        cases: [
          {
            id: 'case-a',
            contact_name: 'First case',
            account_name: 'Example inbox',
          },
          {
            id: 'case-b',
            contact_name: 'Second case',
            account_name: 'Example inbox',
          },
        ],
        has_more: false,
      },
    },
  })
  const preview = deferred()
  mock.preview.mockReturnValue(preview.promise)
  const root = await mount()
  click(root, 'inboxRules.dryRun')
  await settle()
  const selector = root.querySelector<HTMLSelectElement>('select')!
  expect(selector).toBeTruthy()
  selector.value = 'case-a'
  selector.dispatchEvent(new Event('change'))
  await settle()
  click(root, 'inboxRules.review')
  await settle()
  selector.value = 'case-b'
  selector.dispatchEvent(new Event('change'))
  await settle()
  preview.resolve({
    data: {
      data: {
        case_id: 'case-a',
        matches: true,
        actions: [],
        confirmation: 'synthetic',
        recipient: { name: 'First case', phone: '+12025550190' },
        account: 'Example inbox',
        reply: { text: 'STALE_REPLY_MUST_NOT_APPEAR' },
        send_unavailable: false,
      },
    },
  })
  await settle()
  expect(selector.value).toBe('case-b')
  expect(root.textContent).not.toContain('STALE_REPLY_MUST_NOT_APPEAR')
})
it('loads and selects cases beyond page one without losing account and case identity', async () => {
  mock.definitions.mockResolvedValue(reply([workflow('rule', 'rule-a')]))
  mock.queue
    .mockResolvedValueOnce({
      data: {
        data: {
          cases: Array.from({ length: 50 }, (_, i) => ({
            id: 'case-' + i,
            contact_name: 'Example ' + i,
            account_name: 'Inbox A',
          })),
          has_more: true,
        },
      },
    })
    .mockResolvedValueOnce({
      data: {
        data: {
          cases: [
            {
              id: 'case-51',
              contact_name: 'Example 51',
              account_name: 'Inbox B',
            },
          ],
          has_more: false,
        },
      },
    })
  const root = await mount()
  click(root, 'inboxRules.dryRun')
  await settle()
  expect(mock.queue).toHaveBeenNthCalledWith(1, { search: '' }, 0)
  click(root, 'inboxQueue.next')
  await settle()
  expect(mock.queue).toHaveBeenNthCalledWith(2, { search: '' }, 50)
  const option = Array.from(root.querySelectorAll('option')).find(
    (option) => option.value === 'case-51',
  )
  expect(option?.textContent).toContain('Inbox B')
  expect(option?.textContent).toContain('case-51')
})
