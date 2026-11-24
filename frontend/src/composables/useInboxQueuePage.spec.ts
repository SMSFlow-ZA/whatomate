// @vitest-environment happy-dom
import { createApp, h, ref } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
import { useInboxQueuePage, type InboxQueuePage } from './useInboxQueuePage'

const cleanups: (() => void)[] = []
afterEach(() => {
  for (const cleanup of cleanups.splice(0)) cleanup()
})
function page(id: string, next = '', count = 1): InboxQueuePage {
  return {
    cases: [
      {
        id,
        contact_id: 'contact-' + id,
        contact_name: 'Synthetic ' + id,
        account_id: 'account',
        account_name: 'Synthetic inbox',
        version: 1,
        status: 'open',
        priority: 'normal',
      },
    ],
    counts: { all: count },
    has_more: !!next,
    next_cursor: next,
    queue_revision: 'synthetic-revision',
  }
}
function harness(fetch: (cursor?: string) => Promise<InboxQueuePage>) {
  const context = ref('user:org:account-a')
  let pager!: ReturnType<typeof useInboxQueuePage>
  const app = createApp({
    setup() {
      pager = useInboxQueuePage(() => context.value, fetch)
      return () => h('div')
    },
  })
  const root = document.createElement('div')
  document.body.append(root)
  app.mount(root)
  cleanups.push(() => {
    app.unmount()
    root.remove()
  })
  return { pager, context }
}
function pending() {
  let resolve!: (value: InboxQueuePage) => void,
    reject!: (error: unknown) => void
  const promise = new Promise<InboxQueuePage>((yes, no) => {
    resolve = yes
    reject = no
  })
  return { promise, resolve, reject }
}
it('ignores reversed responses and a late failure without replacing current rows or counts', async () => {
  const one = pending(),
    two = pending(),
    three = pending()
  const fetch = vi
    .fn()
    .mockReturnValueOnce(one.promise)
    .mockReturnValueOnce(two.promise)
    .mockReturnValueOnce(three.promise)
  const { pager } = harness(fetch)
  const a = pager.refresh(),
    b = pager.refresh()
  two.resolve(page('newer', 'next', 25))
  await b
  one.resolve(page('old'))
  await a
  expect(pager.rows.value[0].id).toBe('newer')
  expect(pager.counts.value.all).toBe(25)
  const c = pager.refresh()
  pager.invalidate()
  three.reject(new Error('late'))
  await c
  expect(pager.failed.value).toBe(false)
  expect(pager.loading.value).toBe(false)
})
it('cannot repopulate an account after context changes or a pending request is cancelled', async () => {
  const old = pending(),
    fetch = vi
      .fn()
      .mockReturnValueOnce(old.promise)
      .mockResolvedValueOnce(page('account-b'))
  const { pager, context } = harness(fetch)
  const request = pager.refresh()
  context.value = 'user:org:account-b'
  pager.reset()
  await pager.refresh()
  old.resolve(page('account-a'))
  await request
  expect(pager.rows.value[0].id).toBe('account-b')
  expect(pager.counts.value.all).toBe(1)
})
it('preserves a changed older page, blocks further paging and explicitly refreshes from the start', async () => {
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(page('first', 'older', 70))
    .mockResolvedValueOnce(page('older', 'last', 70))
    .mockRejectedValueOnce({ response: { status: 409 } })
    .mockResolvedValueOnce(page('fresh', 'new-next', 71))
  const { pager } = harness(fetch)
  await pager.refresh()
  await pager.older()
  const at = pager.updatedAt.value
  await pager.poll()
  expect(pager.rows.value[0].id).toBe('older')
  expect(pager.position.value).toBe(1)
  expect(pager.updatedAt.value).toBe(at)
  expect(pager.changed.value).toBe(true)
  await pager.older()
  await pager.newer()
  await pager.poll()
  expect(fetch).toHaveBeenCalledTimes(3)
  await pager.refresh()
  expect(fetch.mock.lastCall![0]).toBeUndefined()
  expect(pager.position.value).toBe(0)
  expect(pager.changed.value).toBe(false)
  expect(pager.rows.value[0].id).toBe('fresh')
})
it('stages a new first-page result instead of moving visible rows during a background refresh', async () => {
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(page('one', '', 1))
    .mockResolvedValueOnce(page('two', '', 2))
  const { pager } = harness(fetch)
  await pager.refresh()
  await pager.poll()
  expect(pager.changed.value).toBe(true)
  expect(pager.rows.value[0].id).toBe('one')
  expect(pager.counts.value.all).toBe(1)
})
it.each([401, 403, 404])(
  'clears rows and navigation after confirmed HTTP%s access loss',
  async (status) => {
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(page('visible', 'next', 70))
      .mockRejectedValueOnce({ response: { status } })
    const { pager } = harness(fetch)
    await pager.refresh()
    await pager.older()
    expect(pager.denied.value).toBe(true)
    expect(pager.rows.value).toEqual([])
    expect(pager.counts.value).toEqual({})
    expect(pager.hasMore.value).toBe(false)
    expect(pager.updatedAt.value).toBe('')
  },
)
it('distinguishes an invalid cursor from a transient outage and retains recoverable rows', async () => {
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(page('one', 'next'))
    .mockRejectedValueOnce({ response: { status: 503 } })
    .mockRejectedValueOnce({
      response: { status: 400, data: { code: 'inbox_discovery_invalid' } },
    })
  const { pager } = harness(fetch)
  await pager.refresh()
  await pager.older()
  expect(pager.failed.value).toBe(true)
  expect(pager.changed.value).toBe(false)
  await pager.older()
  expect(pager.invalid.value).toBe(true)
  expect(pager.failed.value).toBe(false)
  expect(pager.rows.value[0].id).toBe('one')
})
it('retains bounded previous-page navigation without limiting forward traversal', async () => {
  const fetch = vi.fn(async (cursor?: string) => {
    const n = Number(cursor ?? 0)
    return page(String(n), String(n + 1), 10000)
  })
  const { pager } = harness(fetch)
  await pager.refresh()
  for (let i = 0; i < 60; i++) await pager.older()
  expect(pager.position.value).toBe(60)
  for (let i = 0; i < 49; i++) await pager.newer()
  expect(pager.position.value).toBe(11)
  expect(pager.historyTrimmed.value).toBe(true)
  expect(pager.hasPrevious.value).toBe(false)
  await pager.refresh()
  expect(pager.position.value).toBe(0)
  expect(pager.historyTrimmed.value).toBe(false)
})

it('rechecks the same revision when navigating back to the cursorless first page', async () => {
  const fetch = vi
    .fn()
    .mockResolvedValueOnce(page('first', 'older'))
    .mockResolvedValueOnce(page('second', ''))
    .mockRejectedValueOnce({ response: { status: 409 } })
  const { pager } = harness(fetch)
  await pager.refresh()
  await pager.older()
  await pager.newer()
  expect(fetch.mock.lastCall).toEqual([undefined, 'synthetic-revision'])
  expect(pager.position.value).toBe(1)
  expect(pager.rows.value[0].id).toBe('second')
  expect(pager.changed.value).toBe(true)
  fetch.mockResolvedValueOnce(page('refreshed'))
  await pager.refresh()
  expect(fetch.mock.lastCall).toEqual([undefined, undefined])
})
