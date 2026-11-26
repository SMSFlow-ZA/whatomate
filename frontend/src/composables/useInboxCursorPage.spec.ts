// @vitest-environment happy-dom
import { createApp, nextTick } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
import { useInboxCursorPage } from './useInboxCursorPage'

let app: ReturnType<typeof createApp> | undefined
let root: HTMLElement
afterEach(() => {
  app?.unmount()
  root?.remove()
})
async function settle() {
  for (let i = 0; i < 8; i++) {
    await Promise.resolve()
    await nextTick()
  }
}
function mount(fetch: Parameters<typeof useInboxCursorPage<string>>[1]) {
  let state!: ReturnType<typeof useInboxCursorPage<string>>
  app = createApp({
    setup() {
      state = useInboxCursorPage(() => 'viewer/run', fetch)
      return () => null
    },
  })
  root = document.createElement('div')
  document.body.append(root)
  app.mount(root)
  return state
}
for (const status of [401, 403, 404]) {
  it(`clears private records and pagination after confirmed HTTP ${status}`, async () => {
    const fetch = vi
      .fn()
      .mockResolvedValue({
        items: ['private result'],
        hasMore: true,
        nextCursor: 'older',
      })
    const state = mount(fetch)
    await settle()
    expect(state.items.value).toEqual(['private result'])
    fetch.mockRejectedValueOnce({ response: { status } })
    await state.refresh()
    expect(state.items.value).toEqual([])
    expect(state.ready.value).toBe(false)
    expect(state.hasOlder.value).toBe(false)
    expect(state.updatedAt.value).toBe('')
    expect(state.failed.value).toBe(true)
  })
}
it('retains the current records and navigation on a temporary outage', async () => {
  const fetch = vi
    .fn()
    .mockResolvedValue({
      items: ['private result'],
      hasMore: true,
      nextCursor: 'older',
    })
  const state = mount(fetch)
  await settle()
  fetch.mockRejectedValueOnce({ response: { status: 503 } })
  await state.refresh()
  expect(state.items.value).toEqual(['private result'])
  expect(state.ready.value).toBe(true)
  expect(state.hasOlder.value).toBe(true)
})
