import { beforeEach, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useAuthStore } from './auth'

const { post } = vi.hoisted(() => ({ post: vi.fn().mockResolvedValue({}) }))
vi.mock('@/services/api', () => ({ api: { post } }))
vi.mock('@/services/websocket', () => ({ wsService: { disconnect: vi.fn() } }))

beforeEach(() => {
  setActivePinia(createPinia())
  vi.stubGlobal('localStorage', { getItem: vi.fn(), setItem: vi.fn(), removeItem: vi.fn() })
  post.mockClear()
})

it('logs out under the refresh cookie path so the browser sends its actual session', async () => {
  await useAuthStore().logout()
  expect(post).toHaveBeenCalledWith('/auth/refresh/logout', {})
})

it('reports revocation failure instead of claiming that the session was logged out', async () => {
  const store = useAuthStore()
  store.setAuth({ user: { id: 'user', email: 'user@example.test', full_name: 'User', organization_id: 'org' } })
  post.mockRejectedValueOnce(new Error('Session storage unavailable'))
  await expect(store.logout()).rejects.toThrow('Session storage unavailable')
  expect(store.isAuthenticated).toBe(true)
})
