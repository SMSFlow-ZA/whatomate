// @vitest-environment happy-dom
import { createApp, h, nextTick, reactive } from 'vue'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import InboxIntegrationRecoveryForm from './InboxIntegrationRecoveryForm.vue'

const mock = vi.hoisted(() => ({
  auth: undefined as any,
  org: undefined as any,
  list: vi.fn(),
  fields: vi.fn(),
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => mock.auth }))
vi.mock('@/stores/organizations', () => ({
  useOrganizationsStore: () => mock.org,
}))
vi.mock('@/services/inboxIntegrations', () => ({
  inboxIntegrations: { list: mock.list },
}))
vi.mock('@/services/inbox', () => ({
  inboxService: { fieldDefinitions: mock.fields },
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const envelope = (data: unknown) => ({ data: { data } })
const definitions = () =>
  envelope({
    integrations: [
      {
        id: 'definition-a',
        reference_key: 'reference',
        outputs: [
          { name: 'score', definition_id: 'score' },
          { name: 'active', definition_id: 'active' },
        ],
      },
    ],
  })
const fields = () =>
  envelope({
    definitions: [
      {
        id: 'score',
        label: 'Score',
        scope: 'case',
        kind: 'number',
        key: 'score',
      },
      {
        id: 'active',
        label: 'Active',
        scope: 'contact',
        kind: 'boolean',
        key: 'active',
      },
    ],
  })
const mounted: { app: ReturnType<typeof createApp>; root: HTMLElement }[] = []
async function settle() {
  for (let i = 0; i < 25; i++) {
    await Promise.resolve()
    await nextTick()
  }
}
async function mount() {
  const props = reactive<any>({
    execution: { id: 'execution-a', integration_id: 'definition-a' },
    action: 'confirm_succeeded',
    disabled: false,
  })
  const changed = vi.fn(),
    reviewed = vi.fn(),
    root = document.createElement('div')
  document.body.append(root)
  const app = createApp({
    render: () =>
      h(InboxIntegrationRecoveryForm, {
        ...props,
        onChange: changed,
        onReview: reviewed,
      }),
  })
  app.mount(root)
  mounted.push({ app, root })
  await settle()
  return { props, root, changed, reviewed }
}
async function set(
  element: HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement,
  value: string,
) {
  element.value = value
  element.dispatchEvent(
    new Event(element.tagName === 'SELECT' ? 'change' : 'input', {
      bubbles: true,
    }),
  )
  await settle()
}
async function include(root: HTMLElement, checked = true) {
  const box = root.querySelector<HTMLInputElement>('[type=checkbox]')!
  box.checked = checked
  box.dispatchEvent(new Event('change', { bubbles: true }))
  await settle()
}
function review(root: HTMLElement) {
  return [...root.querySelectorAll('button')].find(
    (item) => item.textContent?.trim() === 'inboxInvestigation.review',
  )!
}
function last(changed: ReturnType<typeof vi.fn>) {
  return changed.mock.calls.at(-1)![0]
}
beforeEach(() => {
  vi.resetAllMocks()
  mock.auth = reactive({
    user: { id: 'operator', role_id: 'admin', role: { permissions: [] } },
    organizationId: 'org-a',
    allowed: true,
    hasPermission: () => mock.auth.allowed,
  })
  mock.org = reactive({ selectedOrgId: 'org-a' })
  mock.list.mockResolvedValue(definitions())
  mock.fields.mockResolvedValue(fields())
})
afterEach(() => {
  for (const { app, root } of mounted.splice(0)) {
    app.unmount()
    root.remove()
  }
})

it('defaults to a fact-only finding and loads no result schema without explicit opt-in', async () => {
  const { root, changed, reviewed } = await mount()
  expect(review(root).disabled).toBe(true)
  expect(mock.list).not.toHaveBeenCalled()
  expect(mock.fields).not.toHaveBeenCalled()
  const reference = root.querySelector<HTMLInputElement>('[type=text]')!
  expect(reference.required).toBe(true)
  await set(reference, 'INVESTIGATION-1')
  expect(last(changed)).toEqual({
    action: 'confirm_succeeded',
    evidence_reference: 'INVESTIGATION-1',
  })
  review(root).click()
  await settle()
  expect(reviewed).toHaveBeenCalledTimes(1)
  expect(mock.list).not.toHaveBeenCalled()
})
it('loads only declared typed fields and preserves authored false and zero in the review input', async () => {
  const { root, changed } = await mount()
  await set(
    root.querySelector<HTMLInputElement>('[type=text]')!,
    'INVESTIGATION-2',
  )
  await include(root)
  expect(mock.list).toHaveBeenCalledTimes(1)
  expect(mock.fields).toHaveBeenCalledTimes(1)
  expect(review(root).disabled).toBe(true)
  await set(root.querySelector<HTMLInputElement>('[type=number]')!, '0')
  await set(root.querySelector<HTMLSelectElement>('select')!, 'false')
  await set(root.querySelector<HTMLTextAreaElement>('textarea')!, 'EXTERNAL-1')
  expect(last(changed)).toBeDefined()
  expect(last(changed).result).toEqual({
    score: 0,
    active: false,
    reference: 'EXTERNAL-1',
  })
  expect(review(root).disabled).toBe(false)
  expect(root.textContent).not.toContain('credential')
})
it('shows associated validation errors rather than silently disabling an invalid authored result', async () => {
  const { root, changed } = await mount()
  await set(
    root.querySelector<HTMLInputElement>('[type=text]')!,
    'INVESTIGATION-3',
  )
  await include(root)
  const result = root.querySelector<HTMLTextAreaElement>('textarea')!
  await set(result, 'é'.repeat(101))
  expect(result.getAttribute('aria-invalid')).toBe('true')
  const error = root.querySelector<HTMLElement>('[role=alert]')!
  expect(error.textContent).toBe('inboxInvestigation.valueError.reference')
  expect(result.getAttribute('aria-describedby')).toContain(error.id)
  expect(last(changed)).toBeUndefined()
})
it('never silently falls back after a schema failure and permits explicit fact-only selection', async () => {
  mock.list.mockRejectedValue(new Error('PRIVATE_SCHEMA_ERROR'))
  const { root, changed } = await mount()
  await set(
    root.querySelector<HTMLInputElement>('[type=text]')!,
    'INVESTIGATION-4',
  )
  await include(root)
  expect(root.textContent).toContain('inboxInvestigation.schemaError')
  expect(root.textContent).not.toContain('PRIVATE_SCHEMA_ERROR')
  expect(last(changed)).toBeUndefined()
  expect(review(root).disabled).toBe(true)
  await include(root, false)
  expect(last(changed)).toEqual({
    action: 'confirm_succeeded',
    evidence_reference: 'INVESTIGATION-4',
  })
})
it('discards late schema and private values after an execution or viewer change', async () => {
  let resolve!: (value: unknown) => void
  mock.list.mockReturnValueOnce(
    new Promise((done) => {
      resolve = done
    }),
  )
  const { root, props, changed } = await mount()
  await set(
    root.querySelector<HTMLInputElement>('[type=text]')!,
    'OLD_PRIVATE_REFERENCE',
  )
  await include(root)
  props.execution = { id: 'execution-b', integration_id: 'definition-b' }
  await settle()
  resolve(definitions())
  await settle()
  expect(root.textContent).not.toContain('Score')
  expect(root.querySelector<HTMLInputElement>('[type=text]')!.value).toBe('')
  expect(last(changed)).toBeUndefined()
  await set(
    root.querySelector<HTMLInputElement>('[type=text]')!,
    'NEW_PRIVATE_REFERENCE',
  )
  mock.auth.user.role.permissions = ['revoked']
  mock.auth.allowed = false
  await settle()
  expect(root.querySelector<HTMLInputElement>('[type=text]')!.value).toBe('')
  expect(last(changed)).toBeUndefined()
})
it('disables authored fields while its exact decision is pending and excludes results from a no-effect finding', async () => {
  const { root, props, changed } = await mount()
  props.action = 'confirm_not_applied'
  await settle()
  expect(root.querySelector('[type=checkbox]')).toBeNull()
  await set(root.querySelector<HTMLInputElement>('[type=text]')!, 'NO-EFFECT-1')
  expect(last(changed)).toEqual({
    action: 'confirm_not_applied',
    evidence_reference: 'NO-EFFECT-1',
  })
  props.disabled = true
  await settle()
  expect(root.querySelector<HTMLInputElement>('[type=text]')!.disabled).toBe(
    true,
  )
  expect(review(root).disabled).toBe(true)
})
