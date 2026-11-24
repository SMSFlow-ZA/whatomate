import { beforeEach, expect, it, vi } from 'vitest'
const http = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), put: vi.fn() }))
vi.mock('@/services/api', () => ({ api: http }))
import { inboxService } from './inbox'
beforeEach(() => vi.clearAllMocks())
it('preserves the exact legacy offset request when discovery is omitted', () => {
  inboxService.queue({ queue: 'mine' }, 50, true, 'view')
  expect(http.get).toHaveBeenCalledWith('/inbox/queue', {
    params: {
      filters: '{"queue":"mine"}',
      offset: 50,
      include_counts: true,
      view_id: 'view',
    },
  })
})
it('sends explicit keyset preferences and their unchanged opaque cursor', () => {
  const discovery = {
    order: 'oldest' as const,
    timezone: 'UTC',
    opened_from: '2026-01-01T00:00:00.000001Z',
  }
  inboxService.queue({}, 0, true, undefined, discovery, 'opaque')
  expect(http.get.mock.lastCall![1].params).toMatchObject({
    discovery: JSON.stringify(discovery),
    cursor: 'opaque',
    offset: 0,
  })
})
it('distinguishes legacy omitted view updates from an explicit discovery reset', () => {
  inboxService.updateView('view', 'Rename', {})
  expect(http.put.mock.lastCall![1]).not.toHaveProperty('discovery')
  inboxService.updateView('view', 'Reset', {}, undefined, {})
  expect(http.put.mock.lastCall![1]).toHaveProperty('discovery', {})
  inboxService.saveView('New', {}, undefined, { order: 'newest' })
  expect(http.post.mock.lastCall![1]).toHaveProperty('discovery', {
    order: 'newest',
  })
})
