// @vitest-environment happy-dom
import { createApp, h, nextTick, ref } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
import InboxResolutionDialog from './InboxResolutionDialog.vue'
const mock = vi.hoisted(() => ({
  requirements: vi.fn(),
  resolve: vi.fn(),
  resolved: vi.fn(),
}))
vi.mock('@/services/inbox', () => ({
  inboxService: {
    resolutionRequirements: mock.requirements,
    completeAndResolve: mock.resolve,
  },
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const settle = async () => {
  for (let i = 0; i < 20; i++) {
    await Promise.resolve()
    await nextTick()
  }
}
let app: ReturnType<typeof createApp> | undefined, root: HTMLElement | undefined
afterEach(() => {
  app?.unmount()
  root?.remove()
  vi.clearAllMocks()
})
async function mount() {
  const open = ref(true)
  root = document.createElement('div')
  document.body.append(root)
  app = createApp({
    setup: () => () =>
      h(InboxResolutionDialog, {
        account: 'account-a',
        contact: 'contact-a',
        modelValue: open.value,
        'onUpdate:modelValue': (value) => {
          open.value = value
        },
        onResolved: mock.resolved,
      }),
  })
  app.mount(root)
  await settle()
  return open
}
it('retains entered values after validation failure and allows correction without a reset', async () => {
  mock.requirements.mockResolvedValue({
    data: {
      data: {
        case_id: 'case-a',
        version: 1,
        missing_fields: [
          {
            id: 'reference',
            scope: 'case',
            key: 'reference',
            label: 'Reference',
            kind: 'text',
            required_on_resolve: true,
            can_edit: true,
          },
        ],
      },
    },
  })
  mock.resolve
    .mockRejectedValueOnce(new Error('synthetic validation failure'))
    .mockResolvedValue({
      data: { data: { case: { id: 'case-a', status: 'resolved' } } },
    })
  await mount()
  const input = document.querySelector<HTMLInputElement>('#resolve-reference')!
  expect(input).toBeTruthy()
  input.value = 'Draft reference'
  input.dispatchEvent(new Event('input'))
  await settle()
  document
    .querySelector('#inbox-resolution-form')!
    .dispatchEvent(new Event('submit', { cancelable: true }))
  await settle()
  expect(input.value).toBe('Draft reference')
  input.value = 'Corrected reference'
  input.dispatchEvent(new Event('input'))
  await settle()
  document
    .querySelector('#inbox-resolution-form')!
    .dispatchEvent(new Event('submit', { cancelable: true }))
  await settle()
  expect(mock.resolve).toHaveBeenCalledTimes(2)
  expect(mock.resolve.mock.lastCall![4]).toEqual([
    { definition_id: 'reference', value: 'Corrected reference' },
  ])
  expect(mock.resolved).toHaveBeenCalledOnce()
})
