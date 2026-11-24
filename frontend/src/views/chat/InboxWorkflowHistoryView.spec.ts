// @vitest-environment happy-dom
import { createApp, h, nextTick, reactive, type SetupContext } from 'vue'
import { beforeEach, afterEach, expect, it, vi } from 'vitest'
import InboxWorkflowHistoryView from './InboxWorkflowHistoryView.vue'

const mock = vi.hoisted(() => ({
  route: undefined as any,
  auth: undefined as any,
  org: undefined as any,
  workflowRuns: vi.fn(),
  confirmLeave: vi.fn(),
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => mock.auth }))
vi.mock('@/stores/organizations', () => ({
  useOrganizationsStore: () => mock.org,
}))
vi.mock('@/services/inbox', () => ({
  inboxService: { workflowRuns: mock.workflowRuns },
}))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key, te: () => true }),
}))
vi.mock('vue-router', () => ({
  useRoute: () => mock.route,
  RouterLink: {
    props: ['to'],
    setup(_props: unknown, { slots }: SetupContext) {
      return () => slots.default?.()
    },
  },
}))
vi.mock('@/components/settings/InboxIntegrationRun.vue', () => ({
  default: {
    props: ['run'],
    render() {
      return h('div', 'Synthetic operation metadata')
    },
  },
}))
vi.mock('@/components/settings/InboxIntegrationReview.vue', () => ({
  default: {
    setup(_props: unknown, { expose }: any) {
      expose({
        confirmLeave: (action: () => void) => mock.confirmLeave(action),
      })
      return () => null
    },
  },
}))
const first = '11111111-1111-4111-8111-111111111111',
  second = '22222222-2222-4222-8222-222222222222'
const page = (label = 'Current workflow', caseId = first, more = false) => ({
  data: {
    data: {
      runs: [
        {
          id: 'run-a',
          case_id: caseId,
          workflow_name: label,
          state: 'sequence_review',
          created_at: '2026-01-01T00:00:00Z',
        },
      ],
      has_more: more,
    },
  },
})
function deferred() {
  let resolve!: (value: any) => void
  const promise = new Promise<any>((done) => {
    resolve = done
  })
  return { promise, resolve }
}
const mounts: { app: ReturnType<typeof createApp>; root: HTMLElement }[] = []
async function settle() {
  for (let i = 0; i < 25; i++) {
    await Promise.resolve()
    await nextTick()
  }
}
async function mount() {
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp({ render: () => h(InboxWorkflowHistoryView) })
  app.mount(root)
  mounts.push({ app, root })
  await settle()
  return root
}
function button(root: HTMLElement, text: string) {
  const found = [...root.querySelectorAll('button')].find(
    (item) => item.textContent?.trim() === text,
  )
  expect(found, text).toBeTruthy()
  return found!
}
beforeEach(() => {
  vi.resetAllMocks()
  mock.route = reactive({ params: { caseId: first } })
  mock.auth = reactive({
    user: { id: 'agent-a', role_id: 'agent', role: { permissions: [] } },
    organizationId: 'org-a',
  })
  mock.org = reactive({ selectedOrgId: 'org-a' })
  mock.confirmLeave.mockImplementation((action: () => void) => action())
  mock.workflowRuns.mockResolvedValue(page())
})
afterEach(() => {
  for (const { app, root } of mounts.splice(0)) {
    app.unmount()
    root.remove()
  }
})

it('reads only the requested case and does not require administrative lookups', async () => {
  const root = await mount()
  expect(mock.workflowRuns).toHaveBeenCalledWith(first, 0)
  expect(root.textContent).toContain('Current workflow')
  expect(root.textContent).not.toContain('inboxRules.newRule')
})
it('rejects a malformed case link without sending a lookup', async () => {
  mock.route.params.caseId = 'not-a-case'
  const root = await mount()
  expect(root.textContent).toContain('inboxRecovery.invalidCase')
  expect(mock.workflowRuns).not.toHaveBeenCalled()
})
it('drops reversed case responses and erases the previous viewer metadata immediately', async () => {
  const old = deferred()
  mock.workflowRuns
    .mockReturnValueOnce(old.promise)
    .mockResolvedValueOnce(page('Second case', second))
  const root = await mount()
  mock.route.params.caseId = second
  await settle()
  old.resolve(page('STALE_CASE'))
  await settle()
  expect(root.textContent).toContain('Second case')
  expect(root.textContent).not.toContain('STALE_CASE')
  const next = deferred()
  mock.workflowRuns.mockReturnValueOnce(next.promise)
  mock.auth.user.role.permissions = ['revoked']
  await nextTick()
  expect(root.textContent).not.toContain('Second case')
  next.resolve(page('Current access', second))
  await settle()
  expect(root.textContent).toContain('Current access')
})
it('rejects mixed-case results without displaying data from another case', async () => {
  mock.workflowRuns.mockResolvedValue(page('OTHER_CASE', second))
  const root = await mount()
  expect(root.textContent).toContain('inboxRecovery.caseHistoryError')
  expect(root.textContent).not.toContain('OTHER_CASE')
})
it('keeps a failed page and its offset, retries safely, and protects paging during an unconfirmed review', async () => {
  mock.workflowRuns
    .mockResolvedValueOnce(page('First page', first, true))
    .mockRejectedValueOnce(new Error('offline'))
    .mockResolvedValueOnce(page('Second page'))
  const root = await mount()
  mock.confirmLeave.mockImplementationOnce(() => undefined)
  button(root, 'inboxIntegrationHistory.older').click()
  await settle()
  expect(mock.workflowRuns).toHaveBeenCalledTimes(1)
  button(root, 'inboxIntegrationHistory.older').click()
  await settle()
  expect(root.textContent).toContain('First page')
  expect(root.textContent).toContain('inboxRecovery.caseHistoryError')
  expect(mock.workflowRuns).toHaveBeenLastCalledWith(first, 50)
  button(root, 'inboxIntegrationHistory.older').click()
  await settle()
  expect(root.textContent).toContain('Second page')
  expect(mock.workflowRuns).toHaveBeenLastCalledWith(first, 50)
})
it('offers retry and a clear empty state after an initial lookup failure', async () => {
  mock.workflowRuns
    .mockRejectedValueOnce(new Error('PRIVATE_BACKEND_DETAIL'))
    .mockResolvedValueOnce({ data: { data: { runs: [], has_more: false } } })
  const root = await mount()
  expect(root.textContent).not.toContain('PRIVATE_BACKEND_DETAIL')
  button(root, 'common.retry').click()
  await settle()
  expect(root.textContent).toContain('inboxRecovery.caseHistoryEmpty')
})
