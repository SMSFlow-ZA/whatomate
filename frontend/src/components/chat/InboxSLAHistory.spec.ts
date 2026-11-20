// @vitest-environment happy-dom
import { createApp, nextTick } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
import InboxSLAHistory from './InboxSLAHistory.vue'
const mock = vi.hoisted(() => ({ history: vi.fn(), export: vi.fn() }))
vi.mock('@/services/inbox', () => ({
  inboxService: { slaHistory: mock.history, exportSlaHistory: mock.export },
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const settle = async () => {
  for (let i = 0; i < 12; i++) {
    await Promise.resolve()
    await nextTick()
  }
}
let app: ReturnType<typeof createApp> | undefined, root: HTMLElement | undefined
afterEach(() => {
  app?.unmount()
  root?.remove()
  vi.restoreAllMocks()
  vi.clearAllMocks()
})
it('exports and paginates the applied snapshot while controls contain unsubmitted edits', async () => {
  mock.history.mockResolvedValue({
    data: {
      data: {
        results: [],
        summary: { total: 0, met: 0, breached: 0, unanswered: 0, legacy: 0 },
        has_more: true,
        filters: { agents: [], policy_versions: [] },
      },
    },
  })
  mock.export.mockResolvedValue({ data: new Blob(['synthetic']) })
  vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:synthetic')
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
  root = document.createElement('div')
  document.body.append(root)
  app = createApp(InboxSLAHistory)
  app.mount(root)
  await settle()
  const original = { ...mock.history.mock.calls[0]![0] }
  const from = root.querySelector<HTMLInputElement>('input[type="date"]')!
  from.value = '2026-01-01'
  from.dispatchEvent(new Event('input'))
  await settle()
  const button = (key: string) =>
    Array.from(root!.querySelectorAll('button')).find(
      (item) => item.textContent?.trim() === key,
    )!
  button('inboxQuality.exportCsv').click()
  await settle()
  expect(mock.export.mock.calls[0]![0].from).toBe(original.from)
  button('inboxQueue.next').click()
  await settle()
  expect(mock.history.mock.lastCall![0]).toMatchObject({
    ...original,
    offset: 50,
  })
  root
    .querySelector('form')!
    .dispatchEvent(new Event('submit', { cancelable: true }))
  await settle()
  expect(mock.history.mock.lastCall![0].from).not.toBe(original.from)
  expect(mock.history.mock.lastCall![0].offset).toBe(0)
})
