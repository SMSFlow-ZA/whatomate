// @vitest-environment happy-dom
import { createApp, h, nextTick, reactive } from 'vue'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'
import { defaultInboxTargets } from '@/lib/inboxCalendar'
import InboxSLASettings from './InboxSLASettings.vue'
const mock = vi.hoisted(() => ({
  auth: undefined as any,
  org: undefined as any,
  accounts: vi.fn(),
  slaPolicies: vi.fn(),
  setSlaPolicy: vi.fn(),
  deleteSlaPolicy: vi.fn(),
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => mock.auth }))
vi.mock('@/stores/organizations', () => ({
  useOrganizationsStore: () => mock.org,
}))
vi.mock('@/services/api', () => ({ accountsService: { list: mock.accounts } }))
vi.mock('@/services/inbox', () => ({ inboxService: mock }))
vi.mock('vue-router', () => ({
  onBeforeRouteLeave: vi.fn(),
  onBeforeRouteUpdate: vi.fn(),
}))
vi.mock('vue-sonner', () => ({ toast: { success: vi.fn() } }))
vi.mock('./InboxResponsePolicies.vue', () => ({
  default: { setup: () => () => null },
}))
vi.mock('@/components/shared/UnsavedChangesDialog.vue', () => ({
  default: {
    props: ['open'],
    emits: ['leave', 'stay'],
    setup:
      (props: any, { emit }: any) =>
      () =>
        props.open
          ? h('div', [
              h(
                'button',
                { onClick: () => emit('leave') },
                'Discard test draft',
              ),
              h('button', { onClick: () => emit('stay') }, 'Keep test draft'),
            ])
          : null,
  },
}))
const envelope = (data: unknown) => ({ data: { data } })
const saved = () => ({
  ...defaultInboxTargets('account-a'),
  business_hours: true,
  timezone: 'Africa/Johannesburg',
  shifts: [{ Day: 1, StartMinute: 480, EndMinute: 1020 }],
  date_exceptions: [{ date: '2026-12-25', intervals: [] }],
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
  for (let i = 0; i < 20; i++) {
    await Promise.resolve()
    await nextTick()
  }
}
async function mount() {
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp(InboxSLASettings)
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
async function select(root: HTMLElement) {
  const input = root.querySelector('select')!
  input.value = 'account-a'
  input.dispatchEvent(new Event('change', { bubbles: true }))
  await settle()
}
function submit(root: HTMLElement) {
  root
    .querySelector('form')!
    .dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
}
beforeEach(() => {
  vi.resetAllMocks()
  mock.auth = reactive({
    user: { id: 'viewer-a', role_id: 'admin', role: { permissions: [] } },
    organizationId: 'org-a',
    read: true,
    write: true,
    hasPermission: (_: string, action: string) =>
      action === 'read' ? mock.auth.read : mock.auth.write,
  })
  mock.org = reactive({ selectedOrgId: 'org-a' })
  mock.accounts.mockResolvedValue(
    envelope({ accounts: [{ id: 'account-a', name: 'Synthetic inbox' }] }),
  )
  mock.slaPolicies.mockResolvedValue(envelope({ policies: [saved()] }))
})
afterEach(() => {
  for (const { app, root } of mounts.splice(0)) {
    app.unmount()
    root.remove()
  }
})
it('keeps cancelled calendar edits out of the cached saved default', async () => {
  const stored = saved()
  mock.slaPolicies.mockResolvedValueOnce(envelope({ policies: [stored] }))
  const root = await mount()
  await select(root)
  const date = root.querySelector<HTMLInputElement>('input[type="date"]')!
  date.value = '2026-12-24'
  date.dispatchEvent(new Event('input', { bubbles: true }))
  await settle()
  expect(stored.date_exceptions[0]!.date).toBe('2026-12-25')
  button(root, en.common.cancel).click()
  await settle()
  button(root, 'Keep test draft').click()
  await settle()
  expect(date.value).toBe('2026-12-24')
  button(root, en.common.cancel).click()
  await settle()
  button(root, 'Discard test draft').click()
  await settle()
  expect(
    root.querySelector<HTMLInputElement>('input[type="date"]')!.value,
  ).toBe('2026-12-25')
})
it('holds uncertain saves until refreshing the saved default and never automatically repeats them', async () => {
  mock.setSlaPolicy.mockRejectedValueOnce(new Error('response lost'))
  const root = await mount()
  await select(root)
  submit(root)
  await settle()
  expect(root.textContent).toContain(en.inboxPolicy.unconfirmed)
  expect(button(root, en.common.save).disabled).toBe(true)
  submit(root)
  await settle()
  expect(mock.setSlaPolicy).toHaveBeenCalledTimes(1)
  button(root, en.inboxPolicy.refreshSaved).click()
  await settle()
  expect(button(root, en.common.save).disabled).toBe(false)
  expect(mock.slaPolicies).toHaveBeenCalledTimes(2)
})
it('does not adopt an old save acknowledgement after an organization switch', async () => {
  const old = deferred()
  mock.setSlaPolicy.mockReturnValueOnce(old.promise)
  const root = await mount()
  await select(root)
  submit(root)
  await settle()
  mock.accounts.mockResolvedValueOnce(envelope({ accounts: [] }))
  mock.slaPolicies.mockResolvedValueOnce(envelope({ policies: [] }))
  mock.org.selectedOrgId = 'org-b'
  await settle()
  old.resolve(envelope({ policy: saved() }))
  await settle()
  expect(root.textContent).not.toContain('Synthetic inbox')
  expect(root.querySelector('form')).toBeNull()
})
it('ignores reversed initial lookups from a different viewer', async () => {
  const old = deferred()
  mock.accounts.mockReturnValueOnce(old.promise)
  const root = await mount()
  mock.accounts.mockResolvedValueOnce(
    envelope({ accounts: [{ id: 'account-b', name: 'Current inbox' }] }),
  )
  mock.slaPolicies.mockResolvedValueOnce(envelope({ policies: [] }))
  mock.auth.user.id = 'viewer-b'
  await settle()
  old.resolve(
    envelope({
      accounts: [{ id: 'account-old', name: 'STALE PRIVATE INBOX' }],
    }),
  )
  await settle()
  expect(root.textContent).toContain('Current inbox')
  expect(root.textContent).not.toContain('STALE PRIVATE INBOX')
})
it('makes failed lookups recoverable and hides write actions from a read-only viewer', async () => {
  mock.accounts.mockRejectedValueOnce(new Error('lookup'))
  const root = await mount()
  expect(root.textContent).toContain(en.inboxSla.loadError)
  button(root, en.common.retry).click()
  await settle()
  mock.auth.write = false
  await select(root)
  expect(root.textContent).toContain(en.inboxPolicy.readOnly)
  expect(root.querySelector('form fieldset')?.hasAttribute('disabled')).toBe(
    true,
  )
  submit(root)
  await settle()
  expect(mock.setSlaPolicy).not.toHaveBeenCalled()
})
it('clears saved settings and closes the editor on a server access denial', async () => {
  mock.setSlaPolicy.mockRejectedValueOnce({ response: { status: 403 } })
  const root = await mount()
  await select(root)
  submit(root)
  await settle()
  expect(root.textContent).toContain(en.inboxPolicy.accessChanged)
  expect(root.querySelector('form')).toBeNull()
  expect(root.textContent).not.toContain('Synthetic inbox')
})
