import { beforeEach, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useContactsStore, type Contact } from './contacts'

const api = vi.hoisted(() => ({ get: vi.fn(), list: vi.fn() }))
vi.mock('@/services/api', () => ({
  contactsService: { get: api.get },
  messagesService: { list: api.list },
}))
beforeEach(() => {
  setActivePinia(createPinia())
  vi.clearAllMocks()
})
function deferred() {
  let resolve!: (value: unknown) => void
  const promise = new Promise((done) => {
    resolve = done
  })
  return { promise, resolve }
}
const contact = (id: string) =>
  ({ id, name: 'Synthetic contact', unread_count: 0 }) as Contact

it('does not resurrect a contact lookup after the selection is revoked', async () => {
  const store = useContactsStore(),
    response = deferred()
  store.setCurrentContact(contact('a'))
  api.get.mockReturnValueOnce(response.promise)
  const request = store.fetchContact('a', 'inbox-a')
  store.setCurrentContact(null)
  store.clearMessages()
  response.resolve({ data: { data: contact('a') } })
  expect(await request).toBeNull()
  expect(store.currentContact).toBeNull()
})

it('ignores reversed message responses across accounts and explicit clearing', async () => {
  const store = useContactsStore(),
    first = deferred(),
    second = deferred()
  store.setCurrentContact(contact('a'))
  api.list
    .mockReturnValueOnce(first.promise)
    .mockReturnValueOnce(second.promise)
  const oldRequest = store.fetchMessages('a', { account: 'inbox-a' })
  const newRequest = store.fetchMessages('a', { account: 'inbox-b' })
  second.resolve({
    data: { data: { messages: [{ id: 'new' }], has_more: false } },
  })
  await newRequest
  first.resolve({
    data: { data: { messages: [{ id: 'old' }], has_more: true } },
  })
  await oldRequest
  expect(store.messages.map((message) => message.id)).toEqual(['new'])
  expect(store.hasMoreMessages).toBe(false)
  const third = deferred()
  api.list.mockReturnValueOnce(third.promise)
  const revoked = store.fetchMessages('a')
  store.setCurrentContact(null)
  store.clearMessages()
  third.resolve({
    data: { data: { messages: [{ id: 'revoked' }], has_more: true } },
  })
  await revoked
  expect(store.messages).toEqual([])
  expect(store.isLoadingMessages).toBe(false)
})

it('does not prepend older private messages after a different contact is selected', async () => {
  const store = useContactsStore(),
    response = deferred()
  store.setCurrentContact(contact('a'))
  store.messages = [{ id: 'current' }] as typeof store.messages
  store.hasMoreMessages = true
  api.list.mockReturnValueOnce(response.promise)
  const request = store.fetchOlderMessages('a', 'inbox-a')
  store.setCurrentContact(contact('b'))
  store.clearMessages()
  response.resolve({
    data: { data: { messages: [{ id: 'old-private' }], has_more: true } },
  })
  await request
  expect(store.messages).toEqual([])
  expect(store.hasMoreMessages).toBe(false)
  expect(store.isLoadingOlderMessages).toBe(false)
})
