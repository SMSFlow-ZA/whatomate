// @vitest-environment happy-dom
import { createApp, nextTick, reactive } from 'vue'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import InboxIntegrations from './InboxIntegrations.vue'
import InboxIntegrationAction from './InboxIntegrationAction.vue'
import type { InboxAction } from '@/services/inbox'

const mock = vi.hoisted(() => ({
  auth: undefined as any,
  org: undefined as any,
  list: vi.fn(),
  settings: vi.fn(),
  fields: vi.fn(),
  create: vi.fn(),
  update: vi.fn(),
  remove: vi.fn(),
  setEnabled: vi.fn(),
  catalogue: vi.fn(),
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => mock.auth }))
vi.mock('@/stores/organizations', () => ({
  useOrganizationsStore: () => mock.org,
}))
vi.mock('@/services/inbox', () => ({
  inboxService: { fieldDefinitions: mock.fields },
}))
vi.mock('@/services/inboxIntegrations', () => ({
  inboxIntegrations: {
    list: mock.list,
    settings: mock.settings,
    create: mock.create,
    update: mock.update,
    remove: mock.remove,
    setEnabled: mock.setEnabled,
    catalogue: mock.catalogue,
  },
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-router', () => ({
  onBeforeRouteLeave: vi.fn(),
  onBeforeRouteUpdate: vi.fn(),
}))
vi.mock('@/components/settings/InboxMessageText.vue', () => ({
  default: {
    props: ['modelValue', 'label'],
    emits: ['update:modelValue'],
    template:
      '<label>{{ label }}<textarea :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" /></label>',
  },
}))

const envelope = (data: unknown) => ({ data: { data } })
const endpoint = {
  key: 'crm',
  name: 'Approved service',
  method: 'POST',
  destination: 'https://service.example.invalid',
  supports_idempotency: false,
}
const definition = (name = 'Example integration') => ({
  id: name,
  name,
  description: 'Synthetic service',
  endpoint_key: 'crm',
  auth_mode: 'bearer',
  has_credential: true,
  enabled: false,
  inputs: [],
  outputs: [],
  reference_key: '',
  updated_at: '2026-01-01T00:00:00Z',
})
function deferred() {
  let resolve!: (value: any) => void
  const promise = new Promise<any>((done) => {
    resolve = done
  })
  return { promise, resolve }
}
const mounted: { app: ReturnType<typeof createApp>; root: HTMLElement }[] = []
const settle = async () => {
  for (let i = 0; i < 25; i++) {
    await Promise.resolve()
    await nextTick()
  }
}
async function mount(
  component: any = InboxIntegrations,
  props: Record<string, unknown> = {},
) {
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp(component, props)
  app.config.globalProperties.$t = (key: string) => key
  app.mount(root)
  mounted.push({ app, root })
  await settle()
  return root
}
function button(root: HTMLElement, label: string) {
  const found = Array.from(root.querySelectorAll('button')).find((item) =>
    item.textContent?.trim().startsWith(label),
  )
  expect(found, label).toBeTruthy()
  return found!
}
function control(root: HTMLElement, label: string) {
  const found = Array.from(root.querySelectorAll('label'))
    .find((item) => item.textContent?.includes(label))
    ?.querySelector<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>(
      'input,select,textarea',
    )
  expect(found, label).toBeTruthy()
  return found!
}
async function input(root: HTMLElement, label: string, value: string) {
  const field = control(root, label)
  field.value = value
  field.dispatchEvent(
    new Event(field.tagName === 'SELECT' ? 'change' : 'input', {
      bubbles: true,
    }),
  )
  await settle()
}
beforeEach(() => {
  vi.resetAllMocks()
  mock.auth = reactive({
    user: {
      id: 'viewer-a',
      role_id: 'admin',
      is_super_admin: false,
      role: { permissions: [] },
    },
    organizationId: 'org-a',
    hasPermission: () => true,
  })
  mock.org = reactive({ selectedOrgId: 'org-a' })
  mock.list.mockResolvedValue(
    envelope({ integrations: [definition()], endpoints: [endpoint] }),
  )
  mock.settings.mockResolvedValue(
    envelope({ enabled: false, dispatch_available: false }),
  )
  mock.fields.mockResolvedValue(envelope({ definitions: [] }))
  mock.catalogue.mockResolvedValue(envelope({ integrations: [] }))
})
afterEach(() => {
  for (const { app, root } of mounted.splice(0)) {
    app.unmount()
    root.remove()
  }
})

it('keeps stored credentials write-only and omits unchanged secrets from updates', async () => {
  mock.list.mockResolvedValue(
    envelope({
      integrations: [
        { ...definition(), credential: 'SERVER_SECRET_MUST_NOT_COPY' },
      ],
      endpoints: [endpoint],
    }),
  )
  mock.update.mockResolvedValue(envelope({ integration: definition() }))
  const root = await mount()
  button(root, 'common.edit').click()
  await settle()
  expect(root.querySelector('input[type=password]')).toBeNull()
  expect(root.textContent).not.toContain('SERVER_SECRET_MUST_NOT_COPY')
  await input(root, 'common.description', 'Updated description')
  button(root, 'common.save').click()
  await settle()
  expect(mock.update).toHaveBeenCalledOnce()
  expect(mock.update.mock.calls[0]![1]).not.toHaveProperty('credential')
  expect(JSON.stringify(mock.update.mock.calls)).not.toContain(
    'SERVER_SECRET_MUST_NOT_COPY',
  )
  expect(mock.setEnabled).not.toHaveBeenCalled()
})

it('replaces a credential explicitly, clears its input on success and preserves held defaults', async () => {
  mock.update.mockResolvedValue(envelope({ integration: definition() }))
  const root = await mount()
  button(root, 'common.edit').click()
  await settle()
  const replace = control(
    root,
    'inboxIntegrations.replaceSecret',
  ) as HTMLInputElement
  replace.checked = true
  replace.dispatchEvent(new Event('change', { bubbles: true }))
  await settle()
  await input(root, 'inboxIntegrations.credential', 'synthetic-replacement')
  button(root, 'common.save').click()
  await settle()
  expect(mock.update.mock.calls[0]![1].credential).toBe('synthetic-replacement')
  expect(root.querySelector('input[type=password]')).toBeNull()
  expect(root.textContent).toContain('inboxIntegrations.saved')
  expect(mock.setEnabled).not.toHaveBeenCalled()
})

it('binds updates to their loaded stamp and preserves drafts with actionable conflict recovery', async () => {
  const message =
    'This integration changed. Cancel, refresh the integration list, then reopen it to review the latest settings before saving.'
  mock.update.mockRejectedValueOnce({
    response: { status: 409, data: { message } },
  })
  const root = await mount()
  button(root, 'common.edit').click()
  await settle()
  await input(root, 'common.description', 'Authored draft retained')
  button(root, 'common.save').click()
  await settle()
  expect(mock.update.mock.calls[0]![1].updated_at).toBe(definition().updated_at)
  expect(control(root, 'common.description').value).toBe(
    'Authored draft retained',
  )
  expect(root.textContent).toContain(message)
  button(root, 'common.cancel').click()
  await settle()
  const leave = [...document.querySelectorAll('button')].find(
    (item) => item.textContent?.trim() === 'common.leave',
  )!
  expect(leave).toBeTruthy()
  leave.click()
  await settle()
  const latest = {
    ...definition(),
    enabled: false,
    auth_mode: 'api_key',
    updated_at: '2026-02-01T00:00:00Z',
  }
  mock.list.mockResolvedValueOnce(
    envelope({ integrations: [latest], endpoints: [endpoint] }),
  )
  button(root, 'inboxQuality.refresh').click()
  await settle()
  button(root, 'common.edit').click()
  await settle()
  await input(root, 'common.description', 'New reviewed draft')
  mock.update.mockResolvedValueOnce(envelope({ integration: latest }))
  button(root, 'common.save').click()
  await settle()
  expect(mock.update.mock.lastCall![1]).toMatchObject({
    enabled: false,
    auth_mode: 'api_key',
    updated_at: latest.updated_at,
  })
})

it('never enables new definitions or dispatch while saving configuration', async () => {
  mock.create.mockResolvedValue(
    envelope({
      integration: {
        ...definition('New definition'),
        has_credential: false,
        auth_mode: 'none',
      },
    }),
  )
  const root = await mount()
  button(root, 'inboxIntegrations.new').click()
  await settle()
  await input(root, 'common.name', 'New definition')
  await input(root, 'inboxIntegrations.endpoint', 'crm')
  await input(root, 'inboxIntegrations.authMode', 'none')
  expect(button(root, 'common.save').disabled).toBe(false)
  button(root, 'common.save').click()
  await settle()
  expect(mock.create.mock.calls[0]![0].enabled).toBe(false)
  expect(mock.setEnabled).not.toHaveBeenCalled()
  expect(root.textContent).toContain('inboxIntegrations.createdDisabled')
})

it('rejects duplicate schema names and keeps the edited draft separate from list data', async () => {
  const root = await mount()
  button(root, 'common.edit').click()
  await settle()
  button(root, 'inboxIntegrations.addInput').click()
  await settle()
  button(root, 'inboxIntegrations.addInput').click()
  await settle()
  const names = Array.from(
    root.querySelectorAll<HTMLInputElement>('input[pattern]'),
  )
  for (const field of names.slice(0, 2)) {
    field.value = 'duplicate'
    field.dispatchEvent(new Event('input', { bubbles: true }))
  }
  await settle()
  expect(button(root, 'common.save').disabled).toBe(true)
  expect(root.textContent).toContain('inboxIntegrations.inputSchemaError')
  expect(mock.update).not.toHaveBeenCalled()
  expect((await mock.list()).data.data.integrations[0].inputs).toEqual([])
})

it('discards late lists after the viewer or selected organisation changes', async () => {
  const old = deferred()
  mock.list.mockReturnValueOnce(old.promise).mockResolvedValue(
    envelope({
      integrations: [definition('Current workspace')],
      endpoints: [endpoint],
    }),
  )
  const root = await mount()
  mock.org.selectedOrgId = 'org-b'
  await settle()
  old.resolve(
    envelope({
      integrations: [definition('Stale workspace')],
      endpoints: [endpoint],
    }),
  )
  await settle()
  expect(root.textContent).toContain('Current workspace')
  expect(root.textContent).not.toContain('Stale workspace')
})

it('clears a secret draft and ignores a late save after a viewer change', async () => {
  const save = deferred()
  mock.update.mockReturnValue(save.promise)
  const root = await mount()
  button(root, 'common.edit').click()
  await settle()
  const replace = control(
    root,
    'inboxIntegrations.replaceSecret',
  ) as HTMLInputElement
  replace.checked = true
  replace.dispatchEvent(new Event('change', { bubbles: true }))
  await settle()
  await input(root, 'inboxIntegrations.credential', 'synthetic-pending-secret')
  button(root, 'common.save').click()
  await settle()
  mock.list.mockResolvedValue(
    envelope({
      integrations: [definition('Current viewer')],
      endpoints: [endpoint],
    }),
  )
  mock.auth.user.id = 'viewer-b'
  await settle()
  save.resolve(envelope({ integration: definition('OLD_SAVE_MUST_NOT_SHOW') }))
  await settle()
  expect(root.querySelector('input[type=password]')).toBeNull()
  expect(root.textContent).toContain('Current viewer')
  expect(root.textContent).not.toContain('OLD_SAVE_MUST_NOT_SHOW')
  expect(root.textContent).not.toContain('inboxIntegrations.saved')
})

it('offers lookup retry and never renders raw error details', async () => {
  mock.list.mockRejectedValueOnce(new Error('SECRET_DIAGNOSTIC_BODY'))
  const root = await mount()
  expect(root.textContent).toContain('inboxIntegrations.loadError')
  expect(root.textContent).not.toContain('SECRET_DIAGNOSTIC_BODY')
  button(root, 'common.retry').click()
  await settle()
  expect(root.textContent).toContain('Example integration')
})

it('hides administration and drops loaded data when its permission is revoked', async () => {
  const root = await mount()
  mock.auth.hasPermission = () => false
  await settle()
  expect(root.textContent).toContain('inboxIntegrations.permission')
  expect(root.textContent).not.toContain('Example integration')
  expect(root.querySelectorAll('form')).toHaveLength(0)
})

it('cannot enable host-held integrations and requires review for an available host', async () => {
  const root = await mount()
  expect(button(root, 'inboxIntegrations.allow').disabled).toBe(true)
  mock.settings.mockResolvedValue(
    envelope({ enabled: false, dispatch_available: true }),
  )
  button(root, 'inboxQuality.refresh').click()
  await settle()
  button(root, 'inboxIntegrations.allow').click()
  await settle()
  expect(mock.setEnabled).not.toHaveBeenCalled()
  expect(root.textContent).toContain('inboxIntegrations.enableReview')
})

it('discards a late integration catalogue without silently deleting saved mappings', async () => {
  const old = deferred()
  mock.catalogue.mockReturnValueOnce(old.promise).mockResolvedValue(
    envelope({
      integrations: [
        {
          id: 'new',
          name: 'Current approved action',
          enabled: false,
          description: '',
          inputs: [],
        },
      ],
    }),
  )
  const action = reactive<InboxAction>({
    type: 'integration',
    value: 'existing',
    integration_inputs: { account: 'Existing literal' },
  })
  const root = await mount(InboxIntegrationAction, { modelValue: action })
  mock.auth.user.id = 'viewer-b'
  await settle()
  old.resolve(
    envelope({
      integrations: [
        { id: 'old', name: 'OLD_DEFINITION_MUST_NOT_SHOW', inputs: [] },
      ],
    }),
  )
  await settle()
  expect(root.textContent).not.toContain('OLD_DEFINITION_MUST_NOT_SHOW')
  expect(root.textContent).toContain('Current approved action')
  expect(action.integration_inputs).toEqual({ account: 'Existing literal' })
})

it('supports approved input names matching object properties without reading inherited values', async () => {
  mock.catalogue.mockResolvedValue(
    envelope({
      integrations: [
        {
          id: 'approved',
          name: 'Approved definition',
          description: '',
          enabled: false,
          inputs: [{ name: 'constructor', kind: 'text', required: true }],
        },
      ],
    }),
  )
  const action = reactive<InboxAction>({
    type: 'integration',
    value: 'approved',
    integration_inputs: {},
  })
  const root = await mount(InboxIntegrationAction, { modelValue: action })
  expect(root.querySelector('textarea')!.value).toBe('')
  expect(root.textContent).not.toContain('native code')
  const field = root.querySelector('textarea')!
  field.value = 'Authored literal'
  field.dispatchEvent(new Event('input', { bubbles: true }))
  await settle()
  expect(
    Object.prototype.hasOwnProperty.call(
      action.integration_inputs,
      'constructor',
    ),
  ).toBe(true)
  expect(action.integration_inputs!.constructor).toBe('Authored literal')
})

it('repairs obsolete schema mappings explicitly while retaining current mappings', async () => {
  mock.catalogue.mockResolvedValue(
    envelope({
      integrations: [
        {
          id: 'approved',
          name: 'Approved definition',
          description: '',
          enabled: false,
          inputs: [
            { name: 'client', kind: 'text', required: true },
            { name: 'keep', kind: 'text', required: false },
          ],
        },
      ],
    }),
  )
  const action = reactive<InboxAction>({
    type: 'integration',
    value: 'approved',
    integration_inputs: {
      customer: 'Old authored value',
      keep: 'Current authored value',
    },
  })
  const root = await mount(InboxIntegrationAction, { modelValue: action })
  expect(root.textContent).toContain('inboxIntegrations.obsoleteTitle')
  expect(root.textContent).toContain('customer')
  expect(action.integration_inputs!.customer).toBe('Old authored value')
  const client = Array.from(root.querySelectorAll('label'))
    .find((label) => label.textContent?.startsWith('client'))!
    .querySelector('textarea')!
  client.value = '{{contact_name}}'
  client.dispatchEvent(new Event('input', { bubbles: true }))
  await settle()
  button(root, 'inboxIntegrations.removeMapping').click()
  await settle()
  expect(action.integration_inputs).toEqual({
    client: '{{contact_name}}',
    keep: 'Current authored value',
  })
  expect(root.textContent).not.toContain('inboxIntegrations.obsoleteTitle')
})

it('refreshes a changed schema without silently deleting obsolete authored values', async () => {
  mock.catalogue
    .mockResolvedValueOnce(
      envelope({
        integrations: [
          {
            id: 'approved',
            name: 'Approved definition',
            description: '',
            enabled: false,
            inputs: [{ name: 'customer', kind: 'text', required: true }],
          },
        ],
      }),
    )
    .mockResolvedValue(
      envelope({
        integrations: [
          {
            id: 'approved',
            name: 'Approved definition',
            description: '',
            enabled: false,
            inputs: [{ name: 'client', kind: 'text', required: true }],
          },
        ],
      }),
    )
  const action = reactive<InboxAction>({
    type: 'integration',
    value: 'approved',
    integration_inputs: { customer: 'Authored before schema change' },
  })
  const root = await mount(InboxIntegrationAction, { modelValue: action })
  expect(root.textContent).not.toContain('inboxIntegrations.obsoleteTitle')
  button(root, 'inboxIntegrations.refreshDefinitions').click()
  await settle()
  expect(root.textContent).toContain('inboxIntegrations.obsoleteTitle')
  expect(action.integration_inputs).toEqual({
    customer: 'Authored before schema change',
  })
})

it('associates invalid reference-property errors with the input and enables save after correction', async () => {
  const root = await mount()
  button(root, 'common.edit').click()
  await settle()
  const reference = control(
    root,
    'inboxIntegrations.referenceKey',
  ) as HTMLInputElement
  for (const value of ['request-id', 'Reference', 'result.id']) {
    reference.value = value
    reference.dispatchEvent(new Event('input', { bubbles: true }))
    await settle()
    expect(reference.getAttribute('aria-invalid')).toBe('true')
    const descriptions = reference
      .getAttribute('aria-describedby')!
      .split(' ')
      .map((id) => document.getElementById(id)?.textContent)
    expect(descriptions).toContain('inboxIntegrations.referenceError')
    expect(button(root, 'common.save').disabled).toBe(true)
  }
  for (const value of ['request_id', '']) {
    reference.value = value
    reference.dispatchEvent(new Event('input', { bubbles: true }))
    await settle()
    expect(reference.getAttribute('aria-invalid')).toBeNull()
    expect(root.textContent).not.toContain('inboxIntegrations.referenceError')
    expect(button(root, 'common.save').disabled).toBe(false)
  }
})
