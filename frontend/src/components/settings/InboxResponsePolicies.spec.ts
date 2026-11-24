// @vitest-environment happy-dom
import { createApp, h, nextTick, reactive } from 'vue'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'
import { defaultInboxTargets } from '@/lib/inboxCalendar'
import InboxResponsePolicies from './InboxResponsePolicies.vue'

const mock = vi.hoisted(() => ({
  auth: undefined as any,
  org: undefined as any,
  list: vi.fn(),
  create: vi.fn(),
  update: vi.fn(),
  clone: vi.fn(),
  reorder: vi.fn(),
  archive: vi.fn(),
  teams: vi.fn(),
  fields: vi.fn(),
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => mock.auth }))
vi.mock('@/stores/organizations', () => ({
  useOrganizationsStore: () => mock.org,
}))
vi.mock('@/services/inboxResponsePolicies', () => ({
  responsePolicyService: mock,
}))
vi.mock('@/services/api', () => ({ teamsService: { list: mock.teams } }))
vi.mock('@/services/inbox', () => ({
  inboxService: { fieldDefinitions: mock.fields },
}))
vi.mock('vue-router', () => ({
  onBeforeRouteLeave: vi.fn(),
  onBeforeRouteUpdate: vi.fn(),
  RouterLink: { setup: () => () => null },
}))
vi.mock('vue-sonner', () => ({ toast: { success: vi.fn() } }))
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
const policy = (
  id = 'policy-a',
  account = 'account-a',
  name = 'Synthetic policy',
) => ({
  id,
  account_id: account,
  version_id: 'version-' + id,
  revision: 1,
  position: 0,
  enabled: false,
  definition: {
    policy_id: id,
    name,
    description: '',
    conditions: { match: 'all', predicates: [] },
  },
  targets: defaultInboxTargets(account),
})
const page = (rows = [policy()]) => envelope({ policies: rows, maximum: 20 })
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
  const props = reactive({ account: 'account-a' }),
    root = document.createElement('div')
  document.body.append(root)
  const app = createApp({ render: () => h(InboxResponsePolicies, props) })
  app.use(createI18n({ legacy: false, locale: 'en', messages: { en } }))
  app.mount(root)
  mounts.push({ app, root })
  await settle()
  return { root, props }
}
function button(root: HTMLElement, label: string) {
  const found = [...root.querySelectorAll('button')].find(
    (item) =>
      item.textContent?.trim() === label ||
      item.getAttribute('aria-label') === label,
  )
  expect(found, label).toBeTruthy()
  return found!
}
function changeName(root: HTMLElement, text: string) {
  const input = root.querySelector<HTMLInputElement>('input[maxlength="100"]')!
  input.value = text
  input.dispatchEvent(new Event('input', { bubbles: true }))
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
  mock.list.mockResolvedValue(page())
  mock.teams.mockResolvedValue(envelope({ teams: [], total: 0 }))
  mock.fields.mockResolvedValue(envelope({ definitions: [] }))
})
afterEach(() => {
  for (const { app, root } of mounts.splice(0)) {
    app.unmount()
    root.remove()
  }
})
it('ignores an old account list arriving after the current account list', async () => {
  const old = deferred()
  mock.list.mockReturnValueOnce(old.promise)
  const { root, props } = await mount()
  mock.list.mockResolvedValueOnce(
    page([policy('policy-b', 'account-b', 'Current account policy')]),
  )
  props.account = 'account-b'
  await settle()
  old.resolve(page([policy('old', 'account-a', 'STALE PRIVATE NAME')]))
  await settle()
  expect(root.textContent).toContain('Current account policy')
  expect(root.textContent).not.toContain('STALE PRIVATE NAME')
})
it('does not display mismatched account configuration and can recover from a lookup failure', async () => {
  mock.list.mockResolvedValueOnce(page([policy('foreign', 'account-b')]))
  const { root } = await mount()
  expect(root.textContent).toContain(en.inboxPolicy.loadError)
  button(root, en.common.retry).click()
  await settle()
  expect(root.textContent).toContain('Synthetic policy')
})
it('keeps the selected policy through async archive confirmation and waits for its acknowledgement', async () => {
  const { root } = await mount()
  const pending = deferred()
  mock.archive.mockReturnValueOnce(pending.promise)
  button(
    root,
    en.inboxPolicy.archiveNamed.replace('{name}', 'Synthetic policy'),
  ).click()
  await settle()
  const dialog = document.body.querySelector<HTMLElement>(
    '[role="alertdialog"]',
  )!
  expect(dialog).toBeTruthy()
  button(dialog, en.inboxPolicy.archive).click()
  await settle()
  expect(mock.archive).toHaveBeenCalledExactlyOnceWith('account-a', policy())
  expect(document.body.querySelector('[role="alertdialog"]')).toBeTruthy()
  mock.list.mockResolvedValueOnce(page([]))
  pending.resolve(envelope({ archived: true }))
  await settle()
  expect(document.body.querySelector('[role="alertdialog"]')).toBeNull()
  expect(root.textContent).not.toContain('Synthetic policy')
})
it('cannot open a stale definition while a list refresh is in flight', async () => {
  const { root } = await mount()
  const oldEdit = button(
    root,
    en.inboxPolicy.editNamed.replace('{name}', 'Synthetic policy'),
  )
  const fresh = deferred()
  mock.list.mockReturnValueOnce(fresh.promise)
  button(root, en.common.refresh).click()
  oldEdit.click()
  await settle()
  expect(root.querySelector('form')).toBeNull()
  const row = policy()
  row.definition.description = 'Freshly loaded description'
  fresh.resolve(page([row]))
  await settle()
  button(
    root,
    en.inboxPolicy.editNamed.replace('{name}', 'Synthetic policy'),
  ).click()
  await settle()
  expect(root.querySelector('textarea')!.value).toBe(
    'Freshly loaded description',
  )
})
it('keeps read-only viewing available without exposing mutation controls', async () => {
  mock.auth.write = false
  const { root } = await mount()
  expect(root.textContent).not.toContain(en.inboxPolicy.new)
  button(
    root,
    en.inboxPolicy.viewNamed.replace('{name}', 'Synthetic policy'),
  ).click()
  await settle()
  expect(root.querySelector('fieldset')?.disabled).toBe(true)
  expect(
    [...root.querySelectorAll('button')].some((item) => item.type === 'submit'),
  ).toBe(false)
  submit(root)
  await settle()
  expect(mock.update).not.toHaveBeenCalled()
})
it('retains a stale revision draft and requires a reviewed refresh instead of another save', async () => {
  mock.update.mockRejectedValueOnce({ response: { status: 409 } })
  const { root } = await mount()
  button(
    root,
    en.inboxPolicy.editNamed.replace('{name}', 'Synthetic policy'),
  ).click()
  await settle()
  changeName(root, 'Authored draft')
  submit(root)
  await settle()
  expect(root.textContent).toContain(en.inboxPolicy.conflict)
  expect(
    root.querySelector<HTMLInputElement>('input[maxlength="100"]')!.value,
  ).toBe('Authored draft')
  submit(root)
  await settle()
  expect(mock.update).toHaveBeenCalledTimes(1)
  button(root, en.inboxPolicy.refreshSaved).click()
  await settle()
  button(root, 'Keep test draft').click()
  await settle()
  expect(
    root.querySelector<HTMLInputElement>('input[maxlength="100"]')!.value,
  ).toBe('Authored draft')
  button(root, en.inboxPolicy.refreshSaved).click()
  await settle()
  button(root, 'Discard test draft').click()
  await settle()
  expect(root.querySelector('form')).toBeNull()
  expect(mock.list).toHaveBeenCalledTimes(2)
})
it('holds an uncertain create result without duplicate submission', async () => {
  mock.create.mockRejectedValueOnce(new Error('lost response'))
  const { root } = await mount()
  button(root, en.inboxPolicy.new).click()
  await settle()
  changeName(root, 'Synthetic new')
  submit(root)
  await settle()
  expect(mock.create).toHaveBeenCalledTimes(1)
  expect(mock.create.mock.calls[0]![1].enabled).toBe(false)
  expect(root.textContent).toContain(en.inboxPolicy.unconfirmed)
  submit(root)
  await settle()
  expect(mock.create).toHaveBeenCalledTimes(1)
})
it('holds policy saving until its recorded custom-field definition is available', async () => {
  const row = policy()
  mock.list.mockResolvedValueOnce(
    page([
      {
        ...row,
        definition: {
          ...row.definition,
          conditions: {
            match: 'all',
            predicates: [
              {
                field: 'custom_field',
                definition_id: 'missing',
                operator: 'eq',
                value: false,
              },
            ],
          },
        },
      } as any,
    ]),
  )
  const { root } = await mount()
  button(
    root,
    en.inboxPolicy.editNamed.replace('{name}', 'Synthetic policy'),
  ).click()
  await settle()
  expect(root.textContent).toContain('Recorded field · missing')
  expect(button(root, en.common.save).disabled).toBe(true)
  submit(root)
  await settle()
  expect(mock.update).not.toHaveBeenCalled()
  mock.fields.mockResolvedValueOnce(
    envelope({
      definitions: [
        {
          id: 'missing',
          scope: 'case',
          label: 'Recovered consent',
          kind: 'boolean',
        },
      ],
    }),
  )
  button(root, en.common.retry).click()
  await settle()
  expect(button(root, en.common.save).disabled).toBe(false)
})
it('distinguishes an acknowledged save from a failed subsequent refresh', async () => {
  mock.update.mockResolvedValueOnce(envelope({ policy: policy() }))
  const { root } = await mount()
  button(
    root,
    en.inboxPolicy.editNamed.replace('{name}', 'Synthetic policy'),
  ).click()
  await settle()
  mock.list.mockRejectedValueOnce(new Error('lookup unavailable'))
  changeName(root, 'Saved title')
  submit(root)
  await settle()
  expect(root.textContent).toContain(en.inboxPolicy.savedRefreshFailed)
  expect(root.querySelector('form')).toBeNull()
  expect(mock.update).toHaveBeenCalledTimes(1)
})
it('rejects an old mutation acknowledgement after a viewer change and clears private drafts', async () => {
  const old = deferred()
  mock.update.mockReturnValueOnce(old.promise)
  const { root } = await mount()
  button(
    root,
    en.inboxPolicy.editNamed.replace('{name}', 'Synthetic policy'),
  ).click()
  await settle()
  changeName(root, 'PRIVATE DRAFT')
  submit(root)
  await settle()
  mock.list.mockResolvedValueOnce(page([]))
  mock.auth.user.id = 'viewer-b'
  await settle()
  old.resolve(envelope({ policy: policy() }))
  await settle()
  expect(root.textContent).not.toContain('PRIVATE DRAFT')
  expect(root.textContent).not.toContain('Synthetic policy')
  expect(root.querySelector('form')).toBeNull()
})
it('clears editor and data when the server denies a mutation', async () => {
  mock.update.mockRejectedValueOnce({ response: { status: 403 } })
  const { root } = await mount()
  button(
    root,
    en.inboxPolicy.editNamed.replace('{name}', 'Synthetic policy'),
  ).click()
  await settle()
  changeName(root, 'PRIVATE')
  submit(root)
  await settle()
  expect(root.textContent).toContain(en.inboxPolicy.accessNeeded)
  expect(root.textContent).not.toContain('Synthetic policy')
  expect(root.querySelector('form')).toBeNull()
})
it('retains the selected team on failed lookup and makes recovery explicit', async () => {
  const row = {
    ...policy(),
    definition: {
      ...policy().definition,
      conditions: { match: 'all', predicates: [], team_id: 'recorded-team' },
    },
  }
  mock.list.mockResolvedValueOnce(page([row]))
  mock.teams.mockRejectedValueOnce(new Error('team lookup'))
  const { root } = await mount()
  button(
    root,
    en.inboxPolicy.editNamed.replace('{name}', 'Synthetic policy'),
  ).click()
  await settle()
  expect(root.textContent).toContain(en.inboxPolicy.teamError)
  expect(root.textContent).toContain('recorded-team')
  mock.teams.mockResolvedValueOnce(
    envelope({
      teams: [{ id: 'recorded-team', name: 'Recovered team', is_active: true }],
      total: 1,
    }),
  )
  button(root, en.common.retry).click()
  await settle()
  expect(root.textContent).toContain('Recovered team')
  expect(root.textContent).not.toContain(en.inboxPolicy.teamError)
})
