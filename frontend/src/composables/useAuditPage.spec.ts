// @vitest-environment happy-dom
import { createApp, h, ref } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
import { useAuditPage } from './useAuditPage'
import type { AuditPage } from '@/services/api'
const mock = vi.hoisted(() => ({ list: vi.fn() }))
vi.mock('@/services/api', () => ({ auditLogsService: { list: mock.list } }))
const cleanups: (() => void)[] = []
afterEach(() => {
  cleanups.splice(0).forEach((fn) => fn())
  vi.clearAllMocks()
})
const reply = (id: string, next = '', allowExport = true) => ({
  data: {
    data: {
      audit_logs: [
        {
          id,
          user_name: `Synthetic ${id}`,
          changes: [],
          resource_type: 'user',
          resource_id: 'synthetic-user',
          user_id: 'synthetic-actor',
          action: 'event',
          created_at: '2026-11-25T09:00:00Z',
        },
      ],
      total: 2,
      has_more: !!next,
      next_cursor: next,
      first_cursor: 'first-snapshot-cursor',
      as_of: '2026-11-25T09:00:00Z',
      capabilities: { export: allowExport, actor_details: false },
    } as AuditPage,
  },
})
function harness() {
  const context = ref('user:org-one')
  let page!: ReturnType<typeof useAuditPage>
  const app = createApp({
    setup() {
      page = useAuditPage(() => context.value)
      return () => h('div')
    },
  })
  const root = document.createElement('div')
  app.mount(root)
  cleanups.push(() => {
    app.unmount()
    root.remove()
  })
  return { page, context }
}
function pending() {
  let resolve!: (data: ReturnType<typeof reply>) => void,
    reject!: (error: unknown) => void
  const promise = new Promise<ReturnType<typeof reply>>((yes, no) => {
    resolve = yes
    reject = no
  })
  return { promise, resolve, reject }
}
it('does not restore obsolete rows or export authority after reversed responses', async () => {
  const one = pending(),
    two = pending()
  mock.list.mockReturnValueOnce(one.promise).mockReturnValueOnce(two.promise)
  const { page } = harness(),
    first = page.load({}),
    second = page.load({})
  two.resolve(reply('current', '', false))
  await second
  one.resolve(reply('obsolete'))
  await first
  expect(page.rows.value.map((row) => row.id)).toEqual(['current'])
  expect(page.canExport.value).toBe(false)
})
it('discards data and failures after viewer changes and unmount', async () => {
  const one = pending(),
    two = pending()
  mock.list.mockReturnValueOnce(one.promise).mockReturnValueOnce(two.promise)
  const { page, context } = harness(),
    first = page.load({})
  context.value = 'user:org-two'
  page.reset()
  one.resolve(reply('private-old-organisation'))
  await first
  expect(page.rows.value).toEqual([])
  const second = page.load({})
  page.reset()
  two.reject(new Error('old failure'))
  await second
  expect(page.failure.value).toBe('')
  expect(page.canExport.value).toBe(false)
})
it('uses issued cursors, keeps pages distinct and clears failed sensitive results', async () => {
  mock.list
    .mockResolvedValueOnce(reply('first', 'server-cursor'))
    .mockRejectedValueOnce({ response: { status: 503 } })
    .mockResolvedValueOnce(reply('second', '', false))
  const { page } = harness()
  await page.load({ action: 'updated', limit: 25 })
  expect(page.canExport.value).toBe(true)
  await page.load({ action: 'updated', limit: 25 }, 1)
  expect(mock.list).toHaveBeenLastCalledWith({
    action: 'updated',
    limit: 25,
    cursor: 'server-cursor',
  })
  expect(page.rows.value).toEqual([])
  expect(page.canExport.value).toBe(false)
  expect(page.failure.value).toContain('temporarily unavailable')
  await page.retry({ action: 'updated', limit: 25 })
  expect(page.position.value).toBe(1)
  expect(page.rows.value[0].id).toBe('second')
  expect(page.hasMore.value).toBe(false)
  expect(page.failure.value).toBe('')
  mock.list.mockResolvedValueOnce(reply('first', 'server-cursor'))
  await page.load({ action: 'updated', limit: 25 }, 0)
  expect(mock.list).toHaveBeenLastCalledWith({
    action: 'updated',
    limit: 25,
    cursor: 'first-snapshot-cursor',
  })
})
it('does not interpret permission or malformed-response failures as an empty successful list', async () => {
  mock.list
    .mockRejectedValueOnce({ response: { status: 403 } })
    .mockResolvedValueOnce({ data: { data: { audit_logs: null } } })
  const { page } = harness()
  await page.load({})
  expect(page.failure.value).toContain('access changed')
  await page.load({})
  expect(page.failure.value).toContain('temporarily unavailable')
  expect(page.rows.value).toEqual([])
})
