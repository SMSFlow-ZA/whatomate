// @vitest-environment happy-dom
import { createApp, h, nextTick } from 'vue'
import { createPinia } from 'pinia'
import { RouterView } from 'vue-router'
import { expect, it, vi } from 'vitest'
const { get, mounted } = vi.hoisted(() => ({ get: vi.fn(), mounted: vi.fn() }))
vi.mock('@/services/api', () => ({ api: { get } }))
vi.mock('@/services/websocket', () => ({ wsService: { disconnect: vi.fn() } }))
vi.mock('@/views/profile/ProfileView.vue', () => ({ default: { render: () => 'Profile' } }))
vi.mock('@/views/auth/LoginView.vue', () => ({ default: { render: () => 'Login' } }))
vi.mock('@/views/NotFoundView.vue', () => ({ default: { render: () => 'Not found' } }))
vi.mock('@/extensions/modules/reports/ReportsPage.vue', () => ({ default: { setup() { mounted(); return () => h('main', 'Authorized reports') } } }))
// Preserve the real layout's background identity refresh and guarded outlet,
// without starting unrelated socket, branding or organization-selector requests.
vi.mock('@/components/layout/AppLayout.vue', async () => {
 const { computed, h, onMounted } = await import('vue')
 const { RouterView, useRoute } = await import('vue-router')
 const { useAuthStore } = await import('@/stores/auth')
 const { canEnterExtensionRoute } = await import('@/extensions/router')
 return { default: { setup() {
  const auth = useAuthStore(), route = useRoute()
  const allowed = computed(() => canEnterExtensionRoute(route, auth))
  onMounted(() => { void auth.refreshUserData() })
  return () => allowed.value ? h(RouterView) : null
 } } }
})
it('preserves an authenticated reports reload through real web-history router installation and layout identity refresh', async () => {
 vi.stubEnv('VITE_EXTENSION_MODULES', 'directory,cases,knowledge,ai,conversation,reports,governance')
 vi.spyOn(window, 'setInterval').mockReturnValue(0 as unknown as ReturnType<typeof window.setInterval>)
 const permissions = ['security:mfa.enroll','security:mfa.revoke','cases:read','cases:read.triage','cases:assign.triage','cases:work.assigned','knowledge:read','ai:assist','ai:review','reports:operational','conversation:simulate']
 const user = { id: 'synthetic-er', email: 'er@example.test', full_name: 'ER operator', organization_id: 'synthetic-org', role: { id: 'er', name: 'ER', is_system: false, permissions: permissions.map((permission, index) => { const [resource, action] = permission.split(':') as [string,string]; return { id: String(index), resource, action } }) } }
 localStorage.clear(); localStorage.setItem('user', JSON.stringify(user))
 window.history.replaceState({}, '', '/extensions/reports/workspace?tab=trend#summary')
 const catalog = { data: { data: { mfa_verified: true, modules: ['directory','cases','knowledge','ai','conversation','reports','governance'].map(id => ({ id, api_version: 1 })), permissions, grants: [{ permission: 'reports:operational', unit_id: 'north' }, { permission: 'knowledge:read', audience: 'staff' }] } } }
 let finish!: (value: typeof catalog) => void
 get.mockImplementation((url: string) => url === '/me' ? Promise.resolve({ data: { data: structuredClone(user) } }) : new Promise(resolve => { finish = resolve }))
 const { default: router } = await import('@/router')
 const app = createApp({ render: () => h(RouterView) })
 app.use(createPinia())
 app.use(router)
 const host = document.createElement('div'); document.body.append(host); app.mount(host)
 try {
  while (!finish) await Promise.resolve()
  expect(mounted).not.toHaveBeenCalled()
  expect(router.currentRoute.value.meta.extensionModule).toBeUndefined()
  get.mockImplementation((url: string) => Promise.resolve(url === '/me' ? { data: { data: structuredClone(user) } } : catalog))
  finish(catalog)
  await router.isReady(); await nextTick(); await new Promise(resolve => setTimeout(resolve, 0)); await nextTick()
  expect(get).toHaveBeenCalledWith('/me')
  expect(router.currentRoute.value.fullPath).toBe('/extensions/reports/workspace?tab=trend#summary')
  expect(window.location.pathname).toBe('/extensions/reports/workspace')
  expect(router.currentRoute.value.meta.extensionModule).toBe('reports')
  expect(host.textContent).toBe('Authorized reports')
  expect(mounted).toHaveBeenCalledTimes(1)
 } finally { app.unmount(); host.remove(); router.options.history.destroy(); localStorage.clear(); vi.restoreAllMocks() }
})

