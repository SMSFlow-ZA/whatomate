// @vitest-environment happy-dom
import { createApp, h, nextTick, ref } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
import InboxPersonalisedReply from './InboxPersonalisedReply.vue'

const mocks = vi.hoisted(() => ({ conversation: vi.fn(), preview: vi.fn() }))
vi.mock('@/services/inbox', () => ({
  inboxService: {
    conversation: mocks.conversation,
    previewMessage: mocks.preview,
  },
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('./InboxWorkflowPreview.vue', () => ({
  default: { template: '<div>Reply preview</div>' },
}))
const cleanups: (() => void)[] = []
afterEach(() => {
  cleanups.splice(0).forEach((cleanup) => cleanup())
  vi.clearAllMocks()
})
const settle = async () => {
  for (let i = 0; i < 10; i++) {
    await Promise.resolve()
    await nextTick()
  }
}
function mount() {
  const text = ref('Hi {{contact_name}}'),
    sending = ref(false),
    contact = ref('contact-a'),
    viewer = ref('org-a/user-a'),
    canned = ref<string>()
  const confirm = vi.fn()
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp({
    render: () =>
      h(InboxPersonalisedReply, {
        text: text.value,
        sending: sending.value,
        contact: contact.value,
        account: 'account-a',
        viewer: viewer.value,
        cannedResponseId: canned.value,
        onConfirm: confirm,
      }),
  })
  const vm = app.mount(root)
  cleanups.push(() => {
    app.unmount()
    root.remove()
  })
  const state = (
    vm.$.subTree.component as unknown as {
      setupState: {
        review: () => Promise<void>
        confirm: () => void
        open: boolean
        preview: unknown
      }
    }
  ).setupState
  return { state, text, sending, contact, viewer, canned, confirm }
}
const result = {
  data: {
    data: {
      case_id: 'case-a',
      case_version: 1,
      confirmation: 'fingerprint',
      reply: { text: 'Hi Example' },
      send_unavailable: false,
    },
  },
}
it('reviews the current case and reuses an operation identifier after an uncertain result', async () => {
  mocks.conversation.mockResolvedValue({
    data: { data: { case: { id: 'case-a', status: 'open' } } },
  })
  mocks.preview.mockResolvedValue(result)
  const { state, sending, confirm } = mount()
  await state.review()
  await settle()
  const dialog = document.querySelector('[role="dialog"]')!
  const heading = document.getElementById(
    dialog.getAttribute('aria-labelledby')!,
  )
  expect(heading?.textContent).toContain('inboxPersonalisation.review')
  state.confirm()
  state.confirm()
  expect(confirm).toHaveBeenCalledTimes(1)
  const operation = confirm.mock.calls[0]![0].client_message_id
  sending.value = true
  await settle()
  sending.value = false
  await settle()
  // A re-preview can recover a stale/uncertain result, but is the same send.
  await state.review()
  await settle()
  state.confirm()
  expect(confirm).toHaveBeenCalledTimes(2)
  expect(confirm.mock.calls[1]![0].client_message_id).toBe(operation)
})
it('discards a response when the customer changes and requires an active case', async () => {
  let finish!: (value: unknown) => void
  mocks.conversation.mockReturnValueOnce(
    new Promise((resolve) => {
      finish = resolve
    }),
  )
  const { state, contact, confirm } = mount()
  const pending = state.review()
  contact.value = 'contact-b'
  await settle()
  finish({ data: { data: { case: { id: 'case-a', status: 'open' } } } })
  await pending
  await settle()
  expect(state.open).toBe(false)
  expect(mocks.preview).not.toHaveBeenCalled()
  mocks.conversation.mockResolvedValueOnce({
    data: { data: { case: { id: 'case-b', status: 'resolved' } } },
  })
  await state.review()
  await settle()
  state.confirm()
  expect(confirm).not.toHaveBeenCalled()
  expect(mocks.preview).not.toHaveBeenCalled()
})

it('clears an in-flight preview when the signed-in viewer changes', async () => {
  mocks.conversation.mockResolvedValue({
    data: { data: { case: { id: 'case-a', status: 'open' } } },
  })
  let finish!: (value: unknown) => void
  mocks.preview.mockReturnValueOnce(
    new Promise((resolve) => {
      finish = resolve
    }),
  )
  const { state, viewer, confirm } = mount()
  const pending = state.review()
  await settle()
  viewer.value = 'org-b/user-b'
  await settle()
  finish(result)
  await pending
  await settle()
  expect(state.open).toBe(false)
  expect(state.preview).toBeUndefined()
  state.confirm()
  expect(confirm).not.toHaveBeenCalled()
})

it('loads saved replies by ID and preserves the exact reviewed interactive payload', async () => {
  mocks.conversation.mockResolvedValue({
    data: { data: { case: { id: 'case-a', status: 'open' } } },
  })
  const interactive = {
    type: 'cta_url',
    body: 'Hi Example',
    button_text: 'Open',
    url: 'https://example.invalid/case',
  }
  mocks.preview.mockResolvedValue({
    data: {
      data: {
        ...result.data.data,
        reply: {
          text: 'Hi Example',
          canned_response_id: 'saved-a',
          interactive,
        },
      },
    },
  })
  const { state, canned, confirm, text } = mount()
  canned.value = 'saved-a'
  await settle()
  await state.review()
  state.confirm()
  expect(mocks.preview).toHaveBeenCalledWith('case-a', '', 'saved-a')
  expect(confirm).toHaveBeenCalledWith(
    expect.objectContaining({
      interactive,
      personalisation: expect.objectContaining({
        authored_text: '',
        canned_response_id: 'saved-a',
      }),
    }),
  )
  expect(text.value).toBe('Hi {{contact_name}}')
})

it('rejects a mismatched saved-reply source and discards old source lookups', async () => {
  mocks.conversation.mockResolvedValue({
    data: { data: { case: { id: 'case-a', status: 'open' } } },
  })
  mocks.preview.mockResolvedValue(result)
  const { state, canned, confirm } = mount()
  canned.value = 'saved-a'
  await settle()
  await state.review()
  state.confirm()
  expect(confirm).not.toHaveBeenCalled()
  let finish!: (value: unknown) => void
  mocks.preview.mockReturnValueOnce(
    new Promise((resolve) => {
      finish = resolve
    }),
  )
  const pending = state.review()
  await settle()
  canned.value = 'saved-b'
  await settle()
  finish({
    data: {
      data: {
        ...result.data.data,
        reply: { text: 'Old reply', canned_response_id: 'saved-a' },
      },
    },
  })
  await pending
  expect(state.open).toBe(false)
  expect(state.preview).toBeUndefined()
})
