// @vitest-environment happy-dom
import { createApp, h, nextTick, reactive } from 'vue'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'
import InboxResponsePolicyPreview from './InboxResponsePolicyPreview.vue'

const mock = vi.hoisted(() => ({
  auth: undefined as any,
  org: undefined as any,
  queue: vi.fn(),
  preview: vi.fn(),
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => mock.auth }))
vi.mock('@/stores/organizations', () => ({
  useOrganizationsStore: () => mock.org,
}))
vi.mock('@/services/inbox', () => ({ inboxService: { queue: mock.queue } }))
vi.mock('@/services/inboxResponsePolicies', () => ({
  responsePolicyService: { preview: mock.preview },
}))
vi.mock('vue-router', () => ({
  RouterLink: {
    props: ['to'],
    setup:
      (_: any, { slots }: any) =>
      () =>
        h('a', slots.default?.()),
  },
}))
const envelope = (data: unknown) => ({ data: { data } })
const sample = (id = 'case-a', account = 'account-a') => ({
  id,
  account_id: account,
  contact_id: 'contact-' + id,
  contact_name: 'Synthetic ' + id,
  account_name: 'Synthetic inbox',
  status: 'open',
})
const page = (id = 'case-a', account = 'account-a') =>
  envelope({ cases: [sample(id, account)], has_more: false })
const result = (
  id = 'case-a',
  account = 'account-a',
  name = 'Proposed next-case policy',
) =>
  envelope({
    account_id: account,
    case_id: id,
    prospective_only: true,
    existing_policy_version: 'retained-version',
    default_policy: null,
    evaluations: [
      {
        policy_id: 'policy-a',
        name,
        position: 0,
        selected: true,
        reason: 'first_matching_policy',
        conditions: [],
      },
    ],
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
  for (let i = 0; i < 15; i++) {
    await Promise.resolve()
    await nextTick()
  }
}
async function mount() {
  const props = reactive({ account: 'account-a' }),
    root = document.createElement('div')
  document.body.append(root)
  const app = createApp({ render: () => h(InboxResponsePolicyPreview, props) })
  app.use(createI18n({ legacy: false, locale: 'en', messages: { en } }))
  app.mount(root)
  mounts.push({ app, root })
  await settle()
  return { props, root }
}
function button(root: HTMLElement, text: string) {
  const found = [...root.querySelectorAll('button')].find(
    (item) => item.textContent?.trim() === text,
  )
  expect(found, text).toBeTruthy()
  return found!
}
async function choose(root: HTMLElement, id = 'case-a') {
  const select = root.querySelector('select')!
  select.value = id
  select.dispatchEvent(new Event('change', { bubbles: true }))
  await settle()
}
beforeEach(() => {
  vi.resetAllMocks()
  mock.auth = reactive({
    user: { id: 'viewer-a', role_id: 'admin', role: { permissions: [] } },
    organizationId: 'org-a',
    read: true,
    hasPermission: () => mock.auth.read,
  })
  mock.org = reactive({ selectedOrgId: 'org-a' })
  mock.queue.mockResolvedValue(page())
  mock.preview.mockResolvedValue(result())
})
afterEach(() => {
  for (const { app, root } of mounts.splice(0)) {
    app.unmount()
    root.remove()
  }
})
it('separates prospective choice from the retained version and performs only an explicit preview', async () => {
  const { root } = await mount()
  expect(mock.preview).not.toHaveBeenCalled()
  await choose(root)
  button(root, en.inboxPolicy.runPreview).click()
  await settle()
  expect(mock.preview).toHaveBeenCalledExactlyOnceWith('account-a', 'case-a')
  expect(root.textContent).toContain('Proposed next-case policy')
  expect(root.textContent).toContain('retained-version')
  expect(root.querySelector('a')?.textContent).toBe(en.inboxPolicy.openSample)
})
it('rejects a mismatched case response and keeps the retry action available', async () => {
  mock.preview.mockResolvedValueOnce(result('different-case'))
  const { root } = await mount()
  await choose(root)
  button(root, en.inboxPolicy.runPreview).click()
  await settle()
  expect(root.textContent).toContain(en.inboxPolicy.previewError)
  expect(root.textContent).not.toContain('Proposed next-case policy')
  button(root, en.inboxPolicy.runPreview).click()
  await settle()
  expect(root.textContent).toContain('retained-version')
})
it('ignores reversed search results and clears the selected case before another search', async () => {
  const old = deferred()
  mock.queue.mockReturnValueOnce(old.promise)
  const { root } = await mount()
  const input = root.querySelector('input')!
  input.value = 'new'
  input.dispatchEvent(new Event('input', { bubbles: true }))
  await settle()
  mock.queue.mockResolvedValueOnce(page('case-new'))
  root
    .querySelector('form')!
    .dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  await settle()
  old.resolve(page('case-old'))
  await settle()
  expect(root.textContent).toContain('Synthetic case-new')
  expect(root.textContent).not.toContain('Synthetic case-old')
  expect(mock.preview).not.toHaveBeenCalled()
})
it('ignores preview results after account context changes', async () => {
  const old = deferred()
  mock.preview.mockReturnValueOnce(old.promise)
  const { root, props } = await mount()
  await choose(root)
  button(root, en.inboxPolicy.runPreview).click()
  await settle()
  mock.queue.mockResolvedValueOnce(page('case-b', 'account-b'))
  props.account = 'account-b'
  await settle()
  old.resolve(result('case-a', 'account-a', 'STALE PRIVATE POLICY'))
  await settle()
  expect(root.textContent).not.toContain('STALE PRIVATE POLICY')
  expect(root.textContent).not.toContain('retained-version')
  expect(root.textContent).toContain('Synthetic case-b')
})
it('invalidates a pending preview immediately when a different sample is selected', async () => {
  mock.queue.mockResolvedValueOnce(
    envelope({ cases: [sample(), sample('case-b')], has_more: false }),
  )
  const old = deferred()
  mock.preview.mockReturnValueOnce(old.promise)
  const { root } = await mount()
  await choose(root)
  button(root, en.inboxPolicy.runPreview).click()
  await settle()
  await choose(root, 'case-b')
  mock.preview.mockResolvedValueOnce(
    result('case-b', 'account-a', 'Current sample policy'),
  )
  button(root, en.inboxPolicy.runPreview).click()
  await settle()
  old.resolve(result('case-a', 'account-a', 'STALE CASE POLICY'))
  await settle()
  expect(root.textContent).toContain('Current sample policy')
  expect(root.textContent).not.toContain('STALE CASE POLICY')
})
it('clears private sample data and invalidates in-flight results when read access is revoked', async () => {
  const old = deferred()
  mock.preview.mockReturnValueOnce(old.promise)
  const { root } = await mount()
  await choose(root)
  button(root, en.inboxPolicy.runPreview).click()
  await settle()
  mock.auth.read = false
  await settle()
  old.resolve(result())
  await settle()
  expect(root.textContent).not.toContain('Synthetic case-a')
  expect(root.textContent).not.toContain('retained-version')
  expect(button(root, en.inboxPolicy.runPreview).disabled).toBe(true)
})
it('recovers from a failed case lookup and filters foreign-account rows defensively', async () => {
  mock.queue.mockRejectedValueOnce(new Error('offline'))
  const { root } = await mount()
  expect(root.textContent).toContain(en.inboxPolicy.caseError)
  mock.queue.mockResolvedValueOnce(
    envelope({
      cases: [sample(), sample('foreign', 'account-b')],
      has_more: false,
    }),
  )
  button(root, en.common.retry).click()
  await settle()
  expect(root.textContent).toContain('Synthetic case-a')
  expect(root.textContent).not.toContain('Synthetic foreign')
})
