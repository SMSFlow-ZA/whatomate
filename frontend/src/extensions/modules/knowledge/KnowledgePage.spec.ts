// @vitest-environment happy-dom
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createApp, type App } from 'vue'
const mocks = vi.hoisted(() => ({ loadJobs: vi.fn(), loadJob: vi.fn(), transition: vi.fn(), searchKnowledge: vi.fn(), can: vi.fn() }))
vi.mock('./api', () => ({ ...mocks, uploadKnowledge: vi.fn(), downloadSource: vi.fn() }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ hasExtensionPermission: mocks.can }) }))
import KnowledgePage from './KnowledgePage.vue'
let app: App, host: HTMLElement
beforeEach(() => { vi.resetAllMocks(); mocks.can.mockReturnValue(false); host = document.createElement('div'); document.body.append(host) })
afterEach(() => { app.unmount(); host.remove() })
it.each([undefined, '0001-01-01T00:00:00Z', '2026-12-31T12:00:00Z'])('shows only a scheduled withdrawal, preserving real dates (%s)', async withdrawAt => {
  const job = { id: 'synthetic-job', status: 'published', version: 5, metadata: { document_id: 'DEMO-DATE', version: '1', effective_at: '2026-10-20T00:00:00Z', audiences: ['employees'], withdraw_at: withdrawAt } }
  mocks.loadJobs.mockResolvedValue([job]); mocks.loadJob.mockResolvedValue(job)
  app = createApp(KnowledgePage); app.mount(host)
  await vi.waitFor(() => expect(host.querySelector('tbody button')).not.toBeNull())
  ;(host.querySelector('tbody button') as HTMLButtonElement).click()
  await vi.waitFor(() => expect(host.textContent).toContain('Status: published'))
  expect(host.textContent?.includes('Withdraws ')).toBe(withdrawAt === '2026-12-31T12:00:00Z')
  if (withdrawAt === '2026-12-31T12:00:00Z') expect(host.textContent).toContain(new Date(withdrawAt).toLocaleString())
})
it.each(['withdraw', 'refresh'])('removes previously retrieved source cards on %s', async action => {
  const job = { id: 'synthetic-job', status: 'published', version: 5, metadata: { document_id: 'DEMO-WITHDRAW', version: '1', effective_at: '2026-10-20T00:00:00Z', audiences: ['employees'] } }
  mocks.can.mockReturnValue(true)
  mocks.loadJobs.mockResolvedValue([job]); mocks.loadJob.mockResolvedValue(job)
  mocks.transition.mockResolvedValue({ ...job, status: 'retired', version: 6 })
  mocks.searchKnowledge.mockResolvedValue([{ Reference: 'manifest:1', DocumentID: 'DEMO-WITHDRAW', Version: '1', ContentHash: 'hash', Page: { Number: 1, Text: 'Synthetic publication excerpt' } }])
  app = createApp(KnowledgePage); app.mount(host)
  await vi.waitFor(() => expect(host.querySelector('tbody button')).not.toBeNull())
  ;(host.querySelector('tbody button') as HTMLButtonElement).click()
  await vi.waitFor(() => expect(host.textContent).toContain('Status: published'))
  const form = Array.from(host.querySelectorAll('form')).find(f => f.textContent?.includes('Search current publications'))!
  const audience = form.querySelector('input')!; audience.value = 'employees'; audience.dispatchEvent(new Event('input'))
  const words = form.querySelector<HTMLInputElement>('input[type=search]')!; words.value = 'Synthetic'; words.dispatchEvent(new Event('input'))
  form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  await vi.waitFor(() => expect(host.querySelectorAll('h3')).toHaveLength(1))
  const label = action === 'withdraw' ? 'Withdraw publication' : 'Refresh status'
  Array.from(host.querySelectorAll('button')).find(b => b.textContent === label)!.click()
  await vi.waitFor(() => expect(host.querySelectorAll('h3')).toHaveLength(0))
  expect(host.textContent).toContain('No current results loaded.')
})
