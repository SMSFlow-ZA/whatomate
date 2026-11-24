import { beforeEach, expect, it, vi } from 'vitest'
import { inboxIntegrations } from './inboxIntegrations'

const mock = vi.hoisted(() => ({ post: vi.fn() }))
vi.mock('@/services/api', () => ({ api: mock }))

beforeEach(() => vi.resetAllMocks())

it('preserves the legacy resume body when no receipt identifier was provided', async () => {
  const response = { data: { data: { run: { id: 'run-id' } } } }
  mock.post.mockResolvedValue(response)
  expect(
    await inboxIntegrations.resume('run/id', 'confirmation', 'reason'),
  ).toBe(response)
  expect(mock.post).toHaveBeenCalledWith(
    '/inbox/workflow-runs/run%2Fid/resume',
    {
      confirmation: 'confirmation',
      reason: 'reason',
    },
  )
})

it('forwards the chosen resume receipt UUID unchanged and returns the server acknowledgement', async () => {
  const operation = '22222222-2222-4222-8222-222222222222'
  const response = {
    data: { data: { run: { id: 'run-id' }, recovery: { id: operation } } },
  }
  mock.post.mockResolvedValue(response)
  expect(
    await inboxIntegrations.resume(
      'run-id',
      'signed-review',
      'reason',
      operation,
    ),
  ).toBe(response)
  expect(mock.post).toHaveBeenCalledWith('/inbox/workflow-runs/run-id/resume', {
    confirmation: 'signed-review',
    reason: 'reason',
    operation_id: operation,
  })
  expect(mock.post).toHaveBeenCalledTimes(1)
})
