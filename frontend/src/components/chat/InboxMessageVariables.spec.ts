// @vitest-environment happy-dom
import { createApp, h, nextTick } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
import InboxMessageVariables from './InboxMessageVariables.vue'

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  auth: { organizationId: 'org-a', user: { id: 'user-a' } },
}))
vi.mock('@/services/inbox', () => ({
  inboxService: { messageVariables: mocks.list },
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => mocks.auth }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const cleanups: (() => void)[] = []
afterEach(() => {
  cleanups.splice(0).forEach((cleanup) => cleanup())
  vi.clearAllMocks()
  mocks.auth.organizationId = 'org-a'
})
const settle = async () => {
  for (let i = 0; i < 10; i++) {
    await Promise.resolve()
    await nextTick()
  }
}
function mount() {
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp({ render: () => h(InboxMessageVariables) })
  const vm = app.mount(root)
  cleanups.push(() => {
    app.unmount()
    root.remove()
  })
  return (
    vm.$.subTree.component as unknown as {
      setupState: {
        open: boolean
        busy: boolean
        failed: boolean
        variables: { key: string }[]
        load: () => Promise<void>
        selected: string
        query: string
        navigate: (event: KeyboardEvent) => void
      }
    }
  ).setupState
}
it('ignores a slow old response after closing and reopening the picker', async () => {
  let finishOld!: (value: unknown) => void
  mocks.list.mockReturnValueOnce(
    new Promise((resolve) => {
      finishOld = resolve
    }),
  )
  mocks.list.mockResolvedValueOnce({
    data: {
      data: {
        variables: [
          {
            key: 'account_name',
            label: 'Account',
            scope: 'account',
            kind: 'text',
          },
        ],
      },
    },
  })
  const state = mount()
  state.open = true
  await settle()
  state.open = false
  await settle()
  state.open = true
  await settle()
  expect(state.variables.map((variable) => variable.key)).toEqual([
    'account_name',
  ])
  finishOld({
    data: {
      data: {
        variables: [
          { key: 'contact_name', label: 'Old', scope: 'contact', kind: 'text' },
        ],
      },
    },
  })
  await settle()
  expect(state.variables.map((variable) => variable.key)).toEqual([
    'account_name',
  ])
  expect(state.busy).toBe(false)
})
it('keeps keyboard selection within the visible results and clears it on an empty search', async () => {
  mocks.list.mockResolvedValueOnce({
    data: {
      data: {
        variables: [
          {
            key: 'contact_name',
            label: 'Customer name',
            scope: 'contact',
            kind: 'text',
          },
          {
            key: 'case_id',
            label: 'Case reference',
            scope: 'case',
            kind: 'text',
          },
        ],
      },
    },
  })
  const state = mount()
  state.open = true
  await settle()
  expect(state.selected).toBe('contact_name')
  state.navigate(new KeyboardEvent('keydown', { key: 'End' }))
  await settle()
  expect(state.selected).toBe('case_id')
  state.navigate(new KeyboardEvent('keydown', { key: 'ArrowDown' }))
  expect(state.selected).toBe('case_id')
  state.navigate(new KeyboardEvent('keydown', { key: 'Home' }))
  expect(state.selected).toBe('contact_name')
  state.query = 'no matching field'
  await settle()
  expect(state.selected).toBe('')
  state.navigate(new KeyboardEvent('keydown', { key: 'Enter' }))
  expect(state.open).toBe(true)
})
it('shows a recoverable lookup failure and refuses old-organisation results', async () => {
  mocks.list.mockRejectedValueOnce(new Error('offline'))
  const state = mount()
  state.open = true
  await settle()
  expect(state.failed).toBe(true)
  expect(state.variables).toEqual([])
  mocks.list.mockResolvedValueOnce({
    data: {
      data: {
        variables: [
          { key: 'case_id', label: 'Case', scope: 'case', kind: 'text' },
        ],
      },
    },
  })
  await state.load()
  await settle()
  expect(state.failed).toBe(false)
  expect(state.variables.map((variable) => variable.key)).toEqual(['case_id'])
  let finish!: (value: unknown) => void
  mocks.list.mockReturnValueOnce(
    new Promise((resolve) => {
      finish = resolve
    }),
  )
  const pending = state.load()
  mocks.auth.organizationId = 'org-b'
  finish({ data: { data: { variables: [{ key: 'phone_number' }] } } })
  await pending
  await settle()
  expect(state.variables).toEqual([])
})
