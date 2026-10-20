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

it('requires explicit extension grants even for superadmins and organization administrators', () => {
  const store = useAuthStore()
  const user = { id: 'user', email: 'user@example.test', full_name: 'User', organization_id: 'org', is_super_admin: true,
    role: { id: 'admin', name: 'admin', is_system: true, permissions: [] as {id:string;resource:string;action:string}[] } }
  store.setAuth({ user })
  expect(store.hasPermission('anything')).toBe(true)
  expect(store.hasExtensionPermission('cases:read')).toBe(false)
  user.is_super_admin = false
  store.setAuth({ user })
  expect(store.hasExtensionPermission('cases:read')).toBe(false)
  user.role.permissions = [{ id: 'explicit', resource: 'cases', action: 'work.assigned' }]
  store.setAuth({ user })
  expect(store.hasExtensionPermission('cases:work.assigned')).toBe(true)
  for (const permission of ['cases:read', 'cases:work', 'cases:*', 'cases:work..assigned', 'cases:work.assigned:extra', '../cases:work.assigned']) {
    expect(store.hasExtensionPermission(permission)).toBe(false)
  }
  store.user!.role!.permissions = []
  expect(store.hasExtensionPermission('cases:work.assigned')).toBe(false)
  store.clearAuth()
  expect(store.hasExtensionPermission('cases:work.assigned')).toBe(false)
})
