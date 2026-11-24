// @vitest-environment happy-dom
import { createApp, h, nextTick, reactive } from 'vue'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'
import InboxNoticesView from './InboxNoticesView.vue'
const mock = vi.hoisted(() => ({
  auth: undefined as any,
  org: undefined as any,
  list: vi.fn(),
  read: vi.fn(),
  availability: vi.fn(),
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => mock.auth }))
vi.mock('@/stores/organizations', () => ({
  useOrganizationsStore: () => mock.org,
}))
vi.mock('@/services/inboxNotices', () => ({
  inboxNoticeService: mock,
  inboxNoticesChanged: 'whatomate:inbox-notices-changed',
}))
vi.mock('vue-router', () => ({
  RouterLink: {
    props: ['to'],
    setup:
      (props: any, { slots }: any) =>
      () =>
        h(
          'a',
          { 'data-destination': JSON.stringify(props.to) },
          slots.default?.(),
        ),
  },
}))
const envelope = (data: unknown) => ({ data: { data } })
const notice = (id = 'notice-a', overrides: Record<string, unknown> = {}) => ({
  id,
  event_id: 'event-' + id,
  case_id: 'case-a',
  account_id: 'account-a',
  contact_id: 'contact-a',
  account_name: 'Example inbox',
  contact_name: 'Example enquiry',
  policy_name: 'Priority policy',
  policy_version_id: 'immutable-a',
  threshold: 'at_risk',
  kind: 'first_reply',
  due_at: '2026-11-24T12:00:00Z',
  observed_at: '2026-11-24T11:50:00Z',
  target_state: 'active',
  ...overrides,
})
const list = (rows = [notice()], cursor = '') =>
  envelope({
    notices: rows,
    has_more: !!cursor,
    next_cursor: cursor,
    delivery: 'in_app',
  })
const mounts: { app: ReturnType<typeof createApp>; root: HTMLElement }[] = []
async function settle() {
  for (let i = 0; i < 20; i++) {
    await Promise.resolve()
    await nextTick()
  }
}
async function mount() {
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp(InboxNoticesView)
  app.use(createI18n({ legacy: false, locale: 'en', messages: { en } }))
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
function all(root: HTMLElement) {
  const checkbox = root.querySelector<HTMLInputElement>('input[type=checkbox]')!
  checkbox.checked = false
  checkbox.dispatchEvent(new Event('change', { bubbles: true }))
}
function deferred() {
  let resolve!: (value: any) => void
  const promise = new Promise<any>((done) => {
    resolve = done
  })
  return { promise, resolve }
}
beforeEach(() => {
  vi.resetAllMocks()
  mock.auth = reactive({
    user: { id: 'viewer-a', role_id: 'agent', role: { permissions: [] } },
    organizationId: 'org-a',
    read: true,
    hasPermission() {
      return this.read
    },
  })
  mock.org = reactive({ selectedOrgId: '' })
  mock.list.mockResolvedValue(list())
  mock.availability.mockResolvedValue(envelope({ enabled: true }))
  mock.read.mockImplementation((id: string) =>
    Promise.resolve(envelope({ id, read: true })),
  )
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
})
afterEach(() => {
  for (const { app, root } of mounts.splice(0)) {
    app.unmount()
    root.remove()
  }
  vi.useRealTimers()
  vi.restoreAllMocks()
})

it('uses a scoped case link and acknowledges exactly one notice with focus restored', async () => {
  const root = await mount()
  expect(mock.list).toHaveBeenCalledExactlyOnceWith(undefined, true, undefined)
  const destination = JSON.parse(
    [...root.querySelectorAll('a')].find(
      (item) => item.textContent === 'Example enquiry',
    )!.dataset.destination!,
  )
  expect(destination).toEqual({
    name: 'chat-conversation',
    params: { contactId: 'contact-a' },
    query: {
      inbox_account: 'Example inbox',
      inbox_account_id: 'account-a',
      case_id: 'case-a',
    },
  })
  button(root, 'Acknowledge').click()
  await settle()
  expect(mock.read).toHaveBeenCalledExactlyOnceWith('notice-a')
  expect(root.textContent).not.toContain('Example enquiry')
  expect(root.textContent).toContain('Notice acknowledged')
  expect(document.activeElement?.textContent).toContain('Notice acknowledged')
})

it('shows acknowledged state without inventing a stored read timestamp', async () => {
  const retained = notice()
  mock.list.mockResolvedValue(list([retained]))
  const root = await mount()
  all(root)
  await settle()
  button(root, 'Acknowledge').click()
  await settle()
  expect(root.textContent).toContain('Example enquiry')
  expect(root.textContent).toContain('Acknowledged')
  expect(retained).not.toHaveProperty('read_at')
  expect(
    [...root.querySelectorAll('button')].some(
      (item) => item.textContent?.trim() === 'Acknowledge',
    ),
  ).toBe(false)
})

it('retains a failed acknowledgement and can confirm a later idempotent retry', async () => {
  mock.read.mockRejectedValueOnce(new Error('Synthetic response lost'))
  const root = await mount()
  button(root, 'Acknowledge').click()
  await settle()
  expect(root.textContent).toContain('Acknowledgement could not be confirmed')
  expect(root.textContent).toContain('Example enquiry')
  mock.list.mockResolvedValueOnce(list())
  button(root, 'Refresh').click()
  await settle()
  button(root, 'Acknowledge').click()
  await settle()
  expect(mock.read).toHaveBeenCalledTimes(2)
  expect(root.textContent).toContain('Notice acknowledged')
})

it('ignores old filter results and clears a former viewer during pending acknowledgement', async () => {
  const older = deferred()
  mock.list
    .mockReturnValueOnce(older.promise)
    .mockResolvedValueOnce(
      list([notice('fresh', { contact_name: 'Fresh current view' })]),
    )
  const root = await mount()
  all(root)
  await settle()
  older.resolve(list([notice('old', { contact_name: 'Stale private result' })]))
  await settle()
  expect(root.textContent).toContain('Fresh current view')
  expect(root.textContent).not.toContain('Stale private result')
  const pending = deferred()
  mock.read.mockReturnValueOnce(pending.promise)
  button(root, 'Acknowledge').click()
  await settle()
  mock.list.mockReturnValueOnce(new Promise(() => {}))
  mock.auth.user.id = 'viewer-b'
  await settle()
  expect(root.textContent).not.toContain('Fresh current view')
  pending.resolve(envelope({ id: 'fresh', read: true }))
  await settle()
  expect(root.textContent).not.toContain('Notice acknowledged')
})

it('hides retained notices on server-side permission revocation and never fetches without chat access', async () => {
  const root = await mount()
  mock.list.mockRejectedValueOnce({ response: { status: 403 } })
  button(root, 'Refresh').click()
  await settle()
  expect(root.textContent).not.toContain('Example enquiry')
  expect(root.textContent).toContain('current workspace chat access')
  const count = mock.list.mock.calls.length
  mock.auth.read = false
  await settle()
  expect(mock.list).toHaveBeenCalledTimes(count)
  mock.list.mockResolvedValueOnce(
    list([notice('restored', { contact_name: 'Access restored' })]),
  )
  mock.auth.read = true
  await settle()
  expect(root.textContent).toContain('Access restored')
})

it('retains the current page after failed navigation and retries the same keyset', async () => {
  mock.list
    .mockResolvedValueOnce(list([notice()], 'cursor-a'))
    .mockRejectedValueOnce(new Error('Synthetic lookup unavailable'))
    .mockResolvedValueOnce(
      list([notice('older', { contact_name: 'Older enquiry' })]),
    )
  const root = await mount()
  button(root, 'Older').click()
  await settle()
  expect(root.textContent).toContain('Example enquiry')
  expect(root.textContent).toContain('could not be refreshed')
  button(root, 'Older').click()
  await settle()
  expect(root.textContent).toContain('Older enquiry')
  expect(mock.list.mock.calls.slice(1).map((call) => call[2])).toEqual([
    'cursor-a',
    'cursor-a',
  ])
})

it('coalesces live hints, reloads on visible focus and avoids hidden-tab requests', async () => {
  vi.useFakeTimers()
  const root = await mount()
  for (let i = 0; i < 30; i++)
    window.dispatchEvent(new Event('whatomate:inbox-notices-changed'))
  await vi.advanceTimersByTimeAsync(299)
  expect(mock.list).toHaveBeenCalledTimes(1)
  await vi.advanceTimersByTimeAsync(1)
  await settle()
  expect(mock.list).toHaveBeenCalledTimes(2)
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden')
  window.dispatchEvent(new Event('whatomate:inbox-notices-changed'))
  await vi.advanceTimersByTimeAsync(30000)
  await settle()
  expect(mock.list).toHaveBeenCalledTimes(2)
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  window.dispatchEvent(new Event('focus'))
  await vi.advanceTimersByTimeAsync(300)
  await settle()
  expect(mock.list).toHaveBeenCalledTimes(3)
  expect(root.textContent).toContain('Example enquiry')
})

it('does not reorder an older page on hints and explains how to see fresh work', async () => {
  vi.useFakeTimers()
  mock.list
    .mockResolvedValueOnce(list([notice()], 'cursor-a'))
    .mockResolvedValueOnce(
      list([notice('older', { contact_name: 'Older enquiry' })]),
    )
  const root = await mount()
  button(root, 'Older').click()
  await settle()
  window.dispatchEvent(new Event('whatomate:inbox-notices-changed'))
  await vi.advanceTimersByTimeAsync(300)
  await settle()
  expect(mock.list).toHaveBeenCalledTimes(2)
  expect(root.textContent).toContain('Return to the first page')
  expect(root.textContent).toContain('Older enquiry')
})

it('confirms a disabled workspace separately from a missing route and recovers on refresh', async () => {
  mock.list.mockRejectedValueOnce({ response: { status: 404 } })
  mock.availability.mockResolvedValueOnce(envelope({ enabled: false }))
  const root = await mount()
  expect(root.textContent).toContain('Inbox workflows are disabled')
  expect(root.textContent).not.toContain('Example enquiry')
  button(root, 'Refresh').click()
  await settle()
  expect(root.textContent).toContain('Example enquiry')
  expect(root.textContent).not.toContain('Inbox workflows are disabled')
})
it('does not label an unrelated missing route as a disabled workspace', async () => {
  mock.list.mockRejectedValueOnce({ response: { status: 404 } })
  const root = await mount()
  expect(root.textContent).not.toContain('Inbox workflows are disabled')
  expect(root.textContent).toContain('could not be refreshed')
})

it('hides old rows while a missing-route availability check fails', async () => {
  const root = await mount()
  mock.list.mockRejectedValueOnce({ response: { status: 404 } })
  mock.availability.mockRejectedValueOnce(
    new Error('Synthetic availability failure'),
  )
  button(root, 'Refresh').click()
  await settle()
  expect(root.textContent).not.toContain('Example enquiry')
  expect(root.textContent).toContain('could not be refreshed')
  button(root, 'Retry').click()
  await settle()
  expect(root.textContent).toContain('Example enquiry')
})
