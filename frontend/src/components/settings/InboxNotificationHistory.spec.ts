// @vitest-environment happy-dom
import { createApp, h, nextTick, reactive, ref } from 'vue'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'
import InboxNotificationHistory from './InboxNotificationHistory.vue'
const mock = vi.hoisted(() => ({
  auth: undefined as any,
  org: undefined as any,
  history: vi.fn(),
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => mock.auth }))
vi.mock('@/stores/organizations', () => ({
  useOrganizationsStore: () => mock.org,
}))
vi.mock('@/services/inboxNotices', () => ({ inboxNoticeService: mock }))
const envelope = (data: unknown) => ({ data: { data } })
const event = (account = 'account-a') => ({
  id: 'event-a',
  account_id: account,
  case_id: 'case-a',
  policy_version_id: 'version-a',
  threshold: 'breach',
  kind: 'resolution',
  settings_revision: 2,
  due_at: '2026-11-24T10:00:00Z',
  observed_at: '2026-11-24T10:05:00Z',
  available_count: 1,
  suppressed_count: 1,
  recipients: [
    {
      id: 'receipt-a',
      recipient_id: 'staff-a',
      outcome: 'legacy_transfer_covered',
    },
  ],
})
const mounts: { app: ReturnType<typeof createApp>; root: HTMLElement }[] = []
async function settle() {
  for (let i = 0; i < 20; i++) {
    await Promise.resolve()
    await nextTick()
  }
}
async function mount() {
  const account = ref('account-a'),
    root = document.createElement('div')
  document.body.append(root)
  const app = createApp({
    setup: () => () => h(InboxNotificationHistory, { account: account.value }),
  })
  app.use(createI18n({ legacy: false, locale: 'en', messages: { en } }))
  app.mount(root)
  mounts.push({ app, root })
  await settle()
  return { root, account }
}
beforeEach(() => {
  vi.resetAllMocks()
  mock.auth = reactive({ user: { id: 'viewer-a' }, organizationId: 'org-a' })
  mock.org = reactive({ selectedOrgId: '' })
  mock.history.mockResolvedValue(
    envelope({ events: [event()], has_more: false, next_cursor: '' }),
  )
})
afterEach(() => {
  for (const { app, root } of mounts.splice(0)) {
    app.unmount()
    root.remove()
  }
})
it('distinguishes retained notices and suppression from human delivery', async () => {
  const { root } = await mount()
  expect(root.textContent).toContain('not delivered to a person')
  expect(root.textContent).toContain('Available in app: 1')
  expect(root.textContent).toContain('Suppressed: 1')
  expect(root.textContent).toContain(
    'Covered by the existing transfer escalation',
  )
})
it('hides retained references on permission failure and clears scope on account change', async () => {
  const { root, account } = await mount()
  mock.history.mockRejectedValueOnce({ response: { status: 403 } })
  root.querySelector('button')!.click()
  await settle()
  expect(root.textContent).not.toContain('case-a')
  expect(root.textContent).toContain('general-settings access')
  mock.history.mockResolvedValueOnce(
    envelope({
      events: [event('account-b')],
      has_more: false,
      next_cursor: '',
    }),
  )
  account.value = 'account-b'
  await settle()
  expect(root.textContent).toContain('case-a')
})
it('rejects an observation returned for another account without rendering it', async () => {
  mock.history.mockResolvedValueOnce(
    envelope({
      events: [event('account-b')],
      has_more: false,
      next_cursor: '',
    }),
  )
  const { root } = await mount()
  expect(root.textContent).not.toContain('case-a')
  expect(root.textContent).toContain(
    'Observation history could not be refreshed',
  )
})
