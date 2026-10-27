// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { defineComponent } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
const { get } = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('@/services/api', () => ({ api: { get, post: vi.fn() } }))
import { useAuthStore, type User } from '@/stores/auth'
import { clearExtensionSession, extensionSession } from './session'
import { bindExtensionRouter, canEnterExtensionRoute } from './router'
import { createExtensionRegistry } from './registry'

const Page = defineComponent({ render: () => null })
const catalog = (allowed = true) => ({ data: { data: {
  modules: [{ id: 'reports', api_version: 1 }], permissions: allowed ? ['reports:operational'] : [],
  grants: [], mfa_verified: true
} } })
const operator: User = { id: 'operator', email: 'operator@synthetic.invalid', full_name: 'Operator', organization_id: 'org',
  role: { id: 'role', name: 'Operator', description: '', is_system: false,
    permissions: [{ id: 'permission', resource: 'reports', action: 'operational', description: '' }] } }
const settle = () => new Promise(resolve => setTimeout(resolve, 0))

describe('active extension route during catalog access-cookie refresh', () => {
  beforeEach(() => { setActivePinia(createPinia()); localStorage.clear(); get.mockReset(); clearExtensionSession('') })
  async function activeRoute() {
    const auth = useAuthStore(); auth.setAuth({ user: operator })
    get.mockResolvedValue(catalog()); await auth.refreshExtensionAccess()
    const router = createRouter({ history: createMemoryHistory(), routes: [
      { path: '/', name: 'app-layout', component: Page, children: [{ path: 'profile', component: Page }] },
      { path: '/login', component: Page }, { path: '/:pathMatch(.*)*', component: Page }
    ] })
    const registry = createExtensionRegistry([{ id: 'reports', apiVersion: 1, version: '1.0.0', requires: [],
      routes: [{ id: 'operational', path: 'operational', title: 'Reports', component: 'reports/Page', permission: 'reports:operational' }],
      navigation: [], panels: [] }], { 'reports/Page': Page }, ['reports'])
    const binding = bindExtensionRouter(router, registry, auth)
    await router.push('/extensions/reports/operational')
    return { auth, router, binding }
  }

  it('hides protected content immediately but preserves the URL until the fresh catalog is ready', async () => {
    const { auth, router, binding } = await activeRoute()
    let finish!: (value: ReturnType<typeof catalog>) => void
    get.mockImplementationOnce(async () => { clearExtensionSession(); throw new Error('Cookie refresh completed; request not replayed') })
      .mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
    const refresh = auth.refreshExtensionAccess()
    try {
      await settle()
      expect(extensionSession.ready).toBe(false)
      expect(canEnterExtensionRoute(router.resolve('/extensions/reports/operational'), auth)).toBe(false)
      expect(router.currentRoute.value.path).toBe('/extensions/reports/operational')
      finish(catalog()); expect(await refresh).toBe(true); await settle()
      expect(router.currentRoute.value.path).toBe('/extensions/reports/operational')
      expect(canEnterExtensionRoute(router.currentRoute.value, auth)).toBe(true)
    } finally { finish?.(catalog()); await refresh; binding.stop() }
  })

  it.each(['revoked', 'failed', 'logout', 'identity'] as const)('leaves the protected route after %s revalidation', async outcome => {
    const { auth, router, binding } = await activeRoute()
    let finish!: (value: ReturnType<typeof catalog>) => void
    let fail!: (error: Error) => void
    get.mockImplementationOnce(async () => { clearExtensionSession(); throw new Error('Refresh required') })
      .mockImplementationOnce(() => new Promise((resolve, reject) => { finish = resolve; fail = reject }))
    const refresh = auth.refreshExtensionAccess()
    try {
      await settle()
      if (outcome === 'logout') { auth.clearAuth(); await settle(); expect(router.currentRoute.value.path).toBe('/login') }
      if (outcome === 'identity') { auth.setAuth({ user: { ...operator, id: 'different-operator' } }); await settle(); expect(router.currentRoute.value.path).toBe('/profile') }
      if (outcome === 'failed') fail(new Error('Denied'))
      else finish(catalog(outcome !== 'revoked'))
      await refresh; await settle()
      expect(router.currentRoute.value.path).toBe(outcome === 'logout' ? '/login' : '/profile')
      expect(extensionSession.ready).toBe(outcome === 'revoked')
    } finally { finish?.(catalog(false)); await refresh; binding.stop() }
  })
})
