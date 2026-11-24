// @vitest-environment happy-dom
import { createApp, h, nextTick, reactive, ref } from 'vue'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'
import InboxInternalNotifications from './InboxInternalNotifications.vue'
const mock = vi.hoisted(() => ({
  auth: undefined as any,
  org: undefined as any,
  settings: vi.fn(),
  save: vi.fn(),
  recipients: vi.fn(),
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => mock.auth }))
vi.mock('@/stores/organizations', () => ({
  useOrganizationsStore: () => mock.org,
}))
vi.mock('@/services/inboxNotices', () => ({ inboxNoticeService: mock }))
vi.mock('vue-router', () => ({
  onBeforeRouteLeave: vi.fn(),
  onBeforeRouteUpdate: vi.fn(),
}))
vi.mock('./InboxNotificationHistory.vue', () => ({
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
const settings = (
  account = 'account-a',
  overrides: Record<string, unknown> = {},
) => ({
  account_id: account,
  revision: 0,
  enabled: false,
  at_risk: true,
  breach: true,
  recipient_ids: [],
  ...overrides,
})
const people = (
  rows = [{ id: 'person-a', name: 'Example agent', full_contact_read: false }],
  cursor = '',
) => envelope({ recipients: rows, has_more: !!cursor, next_cursor: cursor })
const mounts: { app: ReturnType<typeof createApp>; root: HTMLElement }[] = []
async function settle() {
  for (let i = 0; i < 20; i++) {
    await Promise.resolve()
    await nextTick()
  }
}
async function mount() {
  const account = ref('account-a'),
    root = document.createElement('div'),
    busy = vi.fn()
  document.body.append(root)
  const app = createApp({
    setup: () => () =>
      h(InboxInternalNotifications, { account: account.value, onBusy: busy }),
  })
  app.use(createI18n({ legacy: false, locale: 'en', messages: { en } }))
  app.mount(root)
  mounts.push({ app, root })
  await settle()
  return { root, account, busy }
}
function button(root: HTMLElement, text: string) {
  const found = [...root.querySelectorAll('button')].find(
    (item) => item.textContent?.trim() === text,
  )
  expect(found, text).toBeTruthy()
  return found!
}
function check(root: HTMLElement, label: string, value: boolean) {
  const found = [...root.querySelectorAll('label')]
    .find((item) => item.textContent?.trim().startsWith(label))
    ?.querySelector<HTMLInputElement>('input[type=checkbox]')
  expect(found, label).toBeTruthy()
  found!.checked = value
  found!.dispatchEvent(new Event('change', { bubbles: true }))
}
function submit(root: HTMLElement) {
  root
    .querySelector('form')!
    .dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
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
    user: { id: 'viewer-a', role_id: 'admin', role: { permissions: [] } },
    organizationId: 'org-a',
    read: true,
    write: true,
    browse: true,
    hasPermission(resource: string, action: string) {
      return resource === 'users'
        ? this.browse
        : action === 'write'
          ? this.write
          : this.read
    },
  })
  mock.org = reactive({ selectedOrgId: '' })
  mock.settings.mockImplementation((account: string) =>
    Promise.resolve(
      envelope({
        settings: settings(account),
        maximum_recipients: 20,
        delivery: 'in_app',
      }),
    ),
  )
  mock.recipients.mockResolvedValue(people())
  mock.save.mockImplementation((account: string, input: any) =>
    Promise.resolve(
      envelope({
        settings: settings(account, { ...input, revision: input.revision + 1 }),
        delivery: 'in_app',
      }),
    ),
  )
})
afterEach(() => {
  for (const { app, root } of mounts.splice(0)) {
    app.unmount()
    root.remove()
  }
  vi.restoreAllMocks()
})

it('starts disabled and saves explicit recipients and thresholds without any messaging operation', async () => {
  const { root, busy } = await mount()
  expect(root.textContent).toContain('does not send email or customer messages')
  expect(
    root.querySelector<HTMLInputElement>('input[type=checkbox]')!.checked,
  ).toBe(false)
  check(root, 'Enable internal notifications', true)
  await settle()
  expect(button(root, 'Save').disabled).toBe(true)
  check(root, 'Example agent', true)
  await settle()
  submit(root)
  await settle()
  expect(mock.save).toHaveBeenCalledExactlyOnceWith('account-a', {
    revision: 0,
    enabled: true,
    at_risk: true,
    breach: true,
    recipient_ids: ['person-a'],
  })
  expect(root.textContent).toContain('Notification routing saved')
  expect(busy).toHaveBeenCalledWith(true)
  expect(busy).toHaveBeenLastCalledWith(false)
})

it.each([409, 503])(
  'holds a %i save until a fresh saved revision is reviewed, without replay',
  async (code) => {
    mock.save.mockRejectedValueOnce({ response: { status: code } })
    const { root } = await mount()
    check(root, 'Example agent', true)
    check(root, 'Enable internal notifications', true)
    submit(root)
    await settle()
    expect(root.textContent).toContain(
      code === 409
        ? 'Another administrator changed'
        : 'save outcome is unknown',
    )
    expect(
      root
        .querySelector<HTMLInputElement>('input[type=checkbox]')!
        .closest('fieldset')!.disabled,
    ).toBe(true)
    button(root, 'Refresh saved settings').click()
    await settle()
    button(root, 'Keep test draft').click()
    await settle()
    expect(mock.settings).toHaveBeenCalledTimes(1)
    mock.settings.mockResolvedValueOnce(
      envelope({
        settings: settings('account-a', {
          revision: 2,
          recipient_ids: ['person-b'],
        }),
      }),
    )
    button(root, 'Refresh saved settings').click()
    await settle()
    button(root, 'Discard test draft').click()
    await settle()
    expect(mock.save).toHaveBeenCalledTimes(1)
    expect(root.textContent).toContain('person-b')
    expect(root.textContent).not.toContain('save outcome is unknown')
    submit(root)
    await settle()
    expect(mock.save.mock.calls[1]![1].revision).toBe(2)
  },
)

it('rejects a wrong-account save acknowledgement as unknown rather than installing it', async () => {
  mock.save.mockResolvedValueOnce(
    envelope({
      settings: settings('account-b', {
        revision: 1,
        recipient_ids: ['foreign-person'],
      }),
    }),
  )
  const { root } = await mount()
  check(root, 'Example agent', true)
  submit(root)
  await settle()
  expect(root.textContent).toContain('save outcome is unknown')
  expect(root.textContent).not.toContain('foreign-person')
})

it('ignores reversed account responses and removes old recipient names immediately', async () => {
  const initial = deferred(),
    directory = deferred()
  mock.settings.mockReturnValueOnce(initial.promise)
  mock.recipients.mockReturnValueOnce(directory.promise)
  const { root, account } = await mount()
  account.value = 'account-b'
  await settle()
  expect(mock.settings).toHaveBeenLastCalledWith('account-b')
  initial.resolve(
    envelope({
      settings: settings('account-a', { recipient_ids: ['foreign-person'] }),
    }),
  )
  directory.resolve(
    people([
      {
        id: 'foreign-person',
        name: 'Old account staff',
        full_contact_read: true,
      },
    ]),
  )
  await settle()
  expect(root.textContent).not.toContain('Old account staff')
  expect(root.textContent).not.toContain('foreign-person')
  submit(root)
  await settle()
  expect(mock.save.mock.calls[0]![0]).toBe('account-b')
})

it('drops stale drafts and pending saves when write access changes', async () => {
  const pending = deferred()
  mock.save.mockReturnValueOnce(pending.promise)
  const { root } = await mount()
  check(root, 'Example agent', true)
  submit(root)
  await settle()
  mock.auth.write = false
  await settle()
  pending.resolve(
    envelope({
      settings: settings('account-a', {
        revision: 1,
        recipient_ids: ['late-private-id'],
      }),
    }),
  )
  await settle()
  expect(root.textContent).not.toContain('late-private-id')
  expect(
    [...root.querySelectorAll('button')].some(
      (item) => item.textContent?.trim() === 'Save',
    ),
  ).toBe(false)
  expect(root.textContent).toContain(
    'Saving changes requires settings write access',
  )
})

it('retains selected references without directory rights and never fetches names', async () => {
  mock.auth.browse = false
  mock.auth.write = false
  mock.settings.mockResolvedValueOnce(
    envelope({
      settings: settings('account-a', { recipient_ids: ['retained-person'] }),
    }),
  )
  const { root } = await mount()
  expect(mock.recipients).not.toHaveBeenCalled()
  expect(root.textContent).toContain('retained-person')
  expect(root.textContent).toContain('User-directory read access')
  expect(root.textContent).not.toContain('Example agent')
})

it('keeps selections on directory failure and ignores an older search response', async () => {
  const { root } = await mount()
  check(root, 'Example agent', true)
  await settle()
  const older = deferred()
  mock.recipients
    .mockReturnValueOnce(older.promise)
    .mockRejectedValueOnce(new Error('Synthetic lookup outage'))
  const search = root.querySelector<HTMLInputElement>('input[type=search]')!
  search.value = 'older'
  search.dispatchEvent(new Event('input', { bubbles: true }))
  search.dispatchEvent(
    new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }),
  )
  await settle()
  search.value = 'newer'
  search.dispatchEvent(new Event('input', { bubbles: true }))
  search.dispatchEvent(
    new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }),
  )
  await settle()
  older.resolve(
    people([
      { id: 'old-result', name: 'Stale staff result', full_contact_read: true },
    ]),
  )
  await settle()
  expect(root.textContent).toContain('Staff lookup failed')
  expect(root.textContent).toContain('Example agent')
  expect(root.textContent).not.toContain('Stale staff result')
  submit(root)
  await settle()
  expect(mock.save.mock.calls[0]![1].recipient_ids).toEqual(['person-a'])
})

it('limits explicit staff selection to twenty and supports guarded cancel', async () => {
  mock.recipients.mockResolvedValueOnce(
    people(
      Array.from({ length: 21 }, (_, i) => ({
        id: 'person-' + i,
        name: 'Agent ' + i,
        full_contact_read: true,
      })),
    ),
  )
  const { root } = await mount()
  for (let i = 0; i < 20; i++) check(root, 'Agent ' + i, true)
  await settle()
  expect(root.textContent).toContain('20 of 20 selected')
  const last = [...root.querySelectorAll('label')]
    .find((item) => item.textContent?.trim() === 'Agent 20')!
    .querySelector<HTMLInputElement>('input')!
  expect(last.disabled).toBe(true)
  button(root, 'Cancel').click()
  await settle()
  button(root, 'Keep test draft').click()
  await settle()
  expect(root.textContent).toContain('20 of 20 selected')
  button(root, 'Cancel').click()
  await settle()
  button(root, 'Discard test draft').click()
  await settle()
  expect(root.textContent).toContain('0 of 20 selected')
})

it('removes cached directory names after a server-side permission revocation and can reload after access returns', async () => {
  const { root } = await mount()
  check(root, 'Example agent', true)
  await settle()
  mock.recipients.mockRejectedValueOnce({ response: { status: 403 } })
  button(root, 'Search').click()
  await settle()
  expect(root.textContent).not.toContain('Example agent')
  expect(root.textContent).toContain('person-a')
  expect(root.textContent).toContain('User-directory read access')
  button(root, 'Retry').click()
  await settle()
  expect(root.textContent).toContain('Example agent')
  expect(root.textContent).not.toContain('User-directory read access')
})
