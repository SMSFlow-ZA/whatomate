// @vitest-environment happy-dom
import { createApp, h, nextTick, reactive } from 'vue'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import InboxWorkflowStop from './InboxWorkflowStop.vue'

const state = vi.hoisted(() => ({
  auth: undefined as any,
  org: undefined as any,
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => state.auth }))
vi.mock('@/stores/organizations', () => ({
  useOrganizationsStore: () => state.org,
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const id = '12345678-abcd-4000-8000-000000000001'
const mounts: { app: ReturnType<typeof createApp>; root: HTMLElement }[] = []
const settle = async () => {
  await nextTick()
  await nextTick()
}
beforeEach(() => {
  state.auth = reactive({
    user: { id: 'operator-a', role: { permissions: [] } },
    organizationId: 'org-a',
    denied: '',
    hasPermission: (resource: string, action: string) =>
      action === 'write' && resource !== state.auth.denied,
  })
  state.org = reactive({ selectedOrgId: 'org-a' })
})
afterEach(() => {
  for (const { app, root } of mounts.splice(0)) {
    app.unmount()
    root.remove()
  }
})
async function mount() {
  const props = reactive({ disabled: false }),
    emitted = vi.fn(),
    root = document.createElement('div')
  document.body.append(root)
  const app = createApp({
    render: () => h(InboxWorkflowStop, { ...props, onReview: emitted }),
  })
  app.mount(root)
  mounts.push({ app, root })
  await settle()
  const input = () => root.querySelector<HTMLInputElement>('input')!
  const fill = async (value: string) => {
    input().value = value
    input().dispatchEvent(new Event('input', { bubbles: true }))
    await settle()
  }
  const submit = async () => {
    root
      .querySelector('form')!
      .dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await settle()
  }
  return { root, props, emitted, input, fill, submit }
}
it('offers a read-only review without configuration or history lookups', async () => {
  const { root, emitted } = await mount()
  expect(root.querySelector('details')).toBeTruthy()
  expect(root.textContent).toContain('inboxStop.identifierHelp')
  expect(root.querySelector<HTMLButtonElement>('button')!.disabled).toBe(true)
  expect(emitted).not.toHaveBeenCalled()
})
it.each([
  'not-a-run',
  '00000000-0000-0000-0000-000000000000',
  'https://service.example.invalid/run',
])('rejects invalid run identifier %s', async (value) => {
  const { root, emitted, input, fill, submit } = await mount()
  await fill(value)
  input().dispatchEvent(new Event('blur'))
  await settle()
  await submit()
  expect(root.querySelector<HTMLButtonElement>('button')!.disabled).toBe(true)
  expect(input().getAttribute('aria-invalid')).toBe('true')
  expect(root.querySelector('[role=alert]')?.textContent).toBe(
    'inboxStop.invalidId',
  )
  expect(emitted).not.toHaveBeenCalled()
})
it('normalizes a copied UUID and passes only the known run identity to the shared review', async () => {
  const { emitted, fill, submit } = await mount()
  await fill('  ' + id.toUpperCase() + '  ')
  await submit()
  expect(emitted).toHaveBeenCalledExactlyOnceWith({
    kind: 'cancel',
    run: { id },
  })
})
it.each(['settings.general', 'chat', 'custom_actions'])(
  'requires current %s write access',
  async (resource) => {
    state.auth.denied = resource
    const { root } = await mount()
    expect(root.querySelector('details')).toBeNull()
  },
)
it('clears copied identity when organisation or permission context changes', async () => {
  const { input, fill, root } = await mount()
  await fill(id)
  state.org.selectedOrgId = 'org-b'
  await settle()
  expect(input().value).toBe('')
  await fill(id)
  state.auth.denied = 'chat'
  await settle()
  expect(root.querySelector('details')).toBeNull()
  state.auth.denied = ''
  await settle()
  expect(input().value).toBe('')
})
it('does not replace a pending reviewed decision', async () => {
  const { props, fill, submit, emitted, input } = await mount()
  await fill(id)
  props.disabled = true
  await settle()
  await submit()
  expect(input().disabled).toBe(true)
  expect(emitted).not.toHaveBeenCalled()
})
