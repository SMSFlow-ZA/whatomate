// @vitest-environment happy-dom
import { createApp, h, ref } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
import { useAuditExport } from './useAuditExport'
const mock = vi.hoisted(() => ({ export: vi.fn() }))
vi.mock('@/services/api', () => ({ auditLogsService: { export: mock.export } }))
const cleanups: (() => void)[] = []
afterEach(() => {
  cleanups.splice(0).forEach((fn) => fn())
  vi.clearAllMocks()
})
function reply(rows: number, next = '') {
  return {
    data: new Blob(['Synthetic CSV'], { type: 'text/csv' }),
    headers: {
      'content-type': 'text/csv',
      'x-audit-export-rows': String(rows),
      'x-audit-has-more': String(!!next),
      'x-audit-next-cursor': next,
    },
  }
}
function harness() {
  const context = ref('viewer:org-one'),
    permitted = ref(true),
    save = vi.fn()
  let exporter!: ReturnType<typeof useAuditExport>
  const app = createApp({
    setup() {
      exporter = useAuditExport(
        () => context.value,
        () => permitted.value,
        save,
      )
      return () => h('div')
    },
  })
  const root = document.createElement('div')
  app.mount(root)
  cleanups.push(() => {
    app.unmount()
    root.remove()
  })
  return { exporter, context, permitted, save }
}
it('retries exactly the failed CSV part and distinguishes starting over', async () => {
  mock.export
    .mockResolvedValueOnce(reply(100, 'part-two-snapshot'))
    .mockRejectedValueOnce({ response: { status: 503 } })
    .mockResolvedValueOnce(reply(30))
    .mockResolvedValueOnce(reply(25))
  const { exporter, save } = harness()
  await exporter.start({ action: 'updated' }, 100)
  expect(exporter.rows.value).toBe(100)
  await exporter.next({ action: 'updated' }, 100)
  expect(exporter.retryAvailable.value).toBe(true)
  expect(exporter.cursor.value).toBe('part-two-snapshot')
  await exporter.retry()
  expect(mock.export).toHaveBeenNthCalledWith(3, {
    action: 'updated',
    limit: 100,
    cursor: 'part-two-snapshot',
  })
  expect(exporter.rows.value).toBe(130)
  expect(exporter.parts.value).toBe(2)
  expect(save.mock.calls.map((call) => call[1])).toEqual([1, 2])
  await exporter.start({ action: 'updated' }, 100)
  expect(mock.export).toHaveBeenLastCalledWith({
    action: 'updated',
    limit: 100,
    cursor: undefined,
  })
  expect(exporter.rows.value).toBe(25)
  expect(exporter.parts.value).toBe(1)
})
it('does not save a late export after viewer change and refuses revoked UI authority', async () => {
  let complete!: (value: ReturnType<typeof reply>) => void
  mock.export.mockReturnValueOnce(
    new Promise((resolve) => {
      complete = resolve
    }),
  )
  const { exporter, context, permitted, save } = harness()
  const request = exporter.start({}, 100)
  context.value = 'viewer:org-two'
  exporter.reset()
  complete(reply(1))
  await request
  expect(save).not.toHaveBeenCalled()
  expect(exporter.rows.value).toBe(0)
  permitted.value = false
  await exporter.start({}, 100)
  expect(mock.export).toHaveBeenCalledTimes(1)
})
it('does not save invalid CSV headers or claim a silently incomplete export', async () => {
  mock.export.mockResolvedValueOnce({
    ...reply(100, 'ignored'),
    headers: {
      'content-type': 'text/csv',
      'x-audit-export-rows': '100',
      'x-audit-has-more': 'true',
    },
  })
  const { exporter, save } = harness()
  await exporter.start({}, 100)
  expect(save).not.toHaveBeenCalled()
  expect(exporter.error.value).toContain('temporarily unavailable')
  expect(exporter.notice.value).toBe('')
  expect(exporter.retryAvailable.value).toBe(true)
})
