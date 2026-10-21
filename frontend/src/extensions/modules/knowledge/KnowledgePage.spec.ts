// @vitest-environment happy-dom
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createApp, type App } from 'vue'
const mocks = vi.hoisted(() => ({ loadJobs: vi.fn(), loadJob: vi.fn() }))
vi.mock('./api', () => ({ ...mocks, uploadKnowledge: vi.fn(), transition: vi.fn(), searchKnowledge: vi.fn(), downloadSource: vi.fn() }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ hasExtensionPermission: () => false }) }))
import KnowledgePage from './KnowledgePage.vue'
let app: App, host: HTMLElement
beforeEach(() => { vi.clearAllMocks(); host = document.createElement('div'); document.body.append(host) })
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
