// @vitest-environment happy-dom
import { createApp, h, nextTick, reactive } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
import CannedResponsePicker from './CannedResponsePicker.vue'

const mocks = vi.hoisted(() => ({ list: vi.fn(), auth: { organizationId: 'org-a', user: { id: 'user-a' } } }))
vi.mock('@/services/api', () => ({ cannedResponsesService: { list: mocks.list } }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => reactive(mocks.auth) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const cleanups: (() => void)[] = []
afterEach(() => {
  cleanups.splice(0).forEach(cleanup => cleanup())
  vi.resetAllMocks()
  mocks.auth.organizationId = 'org-a'
})
const settle = async () => { for (let i = 0; i < 8; i++) { await Promise.resolve(); await nextTick() } }
function mount() {
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp({ render: () => h(CannedResponsePicker) })
  const vm = app.mount(root)
  cleanups.push(() => { app.unmount(); root.remove() })
  return (vm.$.subTree.component as unknown as { setupState: {
    fetchResponses: () => Promise<void>; responses: { id: string }[]; failed: boolean; isLoading: boolean;
  } }).setupState
}
it('discards old lookups and clears saved replies when the viewer changes', async () => {
  let finish!: (value: unknown) => void
  mocks.list.mockReturnValueOnce(new Promise(resolve => { finish = resolve }))
  mocks.list.mockResolvedValueOnce({ data: { data: { canned_responses: [{ id: 'current', name: 'Current', content: 'Current' }] } } })
  const state = mount()
  const old = state.fetchResponses()
  await state.fetchResponses()
  finish({ data: { data: { canned_responses: [{ id: 'old', name: 'Old', content: 'Old' }] } } })
  await old
  expect(state.responses.map(reply => reply.id)).toEqual(['current'])
  reactive(mocks.auth).organizationId = 'org-b'
  await settle()
  expect(state.responses).toEqual([])
  expect(state.isLoading).toBe(false)
})
it('shows a recoverable error and rejects a lookup from the former organisation', async () => {
  mocks.list.mockRejectedValueOnce(new Error('Offline'))
  const state = mount()
  await state.fetchResponses()
  expect(state.failed).toBe(true)
  let finish!: (value: unknown) => void
  mocks.list.mockReturnValueOnce(new Promise(resolve => { finish = resolve }))
  const pending = state.fetchResponses()
  reactive(mocks.auth).organizationId = 'org-b'
  await settle()
  finish({ data: { data: { canned_responses: [{ id: 'old', name: 'Old', content: 'Old' }] } } })
  await pending
  expect(state.responses).toEqual([])
  expect(state.failed).toBe(false)
})
