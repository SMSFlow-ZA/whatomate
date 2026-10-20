import { describe, expect, it, vi } from 'vitest'
const { post, get } = vi.hoisted(() => ({ post: vi.fn(), get: vi.fn() }))
vi.mock('@/services/api', () => ({ api: { post, get } }))
import { loadJob, transition, uploadKnowledge, searchKnowledge, type KnowledgeJob } from './api'
describe('governed knowledge HTTP client', () => {
 it('uploads original bytes and metadata without client processing evidence', async () => {
  post.mockResolvedValueOnce({ data: { data: { id: 'quarantine', status: 'queued' } } })
  const metadata = { document_id: 'synthetic', version: '1', filename: 'policy.pdf', media_type: 'application/pdf', audiences: ['staff'], effective_at: '2026-01-01T00:00:00Z' }
  await uploadKnowledge(new File(['synthetic'], 'policy.pdf'), metadata)
  const [path, form] = post.mock.calls.at(-1) as [string, FormData]
  expect(path).toBe('/extensions/knowledge/uploads')
  expect(JSON.parse(form.get('metadata') as string)).toEqual(metadata)
  expect(form.has('file')).toBe(true)
  expect(form.has('evidence')).toBe(false)
  expect(form.has('pages')).toBe(false)
 })
 it('preserves exact revision and only sends transition intent', async () => {
  post.mockResolvedValueOnce({ data: { data: {} } })
  await transition({ id: 'job/id', version: 7 } as KnowledgeJob, 'approve')
  expect(post).toHaveBeenLastCalledWith('/extensions/knowledge/jobs/job%2Fid/approve', { expected_version: 7 })
 })
 it('loads authoritative job state and searches with an explicit audience', async () => {
  get.mockResolvedValueOnce({ data: { data: {} } })
  await loadJob('job/id')
  expect(get).toHaveBeenLastCalledWith('/extensions/knowledge/jobs/job%2Fid')
  get.mockResolvedValueOnce({ data: { data: [] } })
  await searchKnowledge('staff', 'policy')
  expect(get).toHaveBeenLastCalledWith('/extensions/knowledge/search', { params: { audience: 'staff', q: 'policy' } })
 })
})
