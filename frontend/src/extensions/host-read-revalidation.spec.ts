// @vitest-environment happy-dom
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { defineComponent, nextTick } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import axios, { AxiosError, type InternalAxiosRequestConfig } from 'axios'
import { api } from '@/services/api'
import { bindExtensionRouter, canEnterExtensionRoute } from './router'
import { createExtensionRegistry } from './registry'
import { clearExtensionSession, extensionPermission, extensionSession, isExtensionRevalidating, refreshExtensionSession } from './session'

const catalog = (allowed = true) => ({ mfa_verified: true, generic_host_access: false, modules: [{ id: 'cases', api_version: 1 }], permissions: allowed ? ['cases:read'] : [], grants: [] })
const reply = (config: InternalAxiosRequestConfig, data: unknown) => ({ data: { data }, status: 200, statusText: 'OK', headers: {}, config })
const deny = (config: InternalAxiosRequestConfig, status = 401) => Promise.reject(new AxiosError('denied', 'ERR_BAD_REQUEST', config, undefined, { status, statusText: 'Denied', headers: {}, data: {}, config }))
const settle = async () => { for (let i = 0; i < 8; i++) { await Promise.resolve(); await nextTick() }; await new Promise(resolve => setTimeout(resolve, 0)) }
let cleanup: () => void
async function activeRoute() {
  const component = defineComponent({ template: '<div />' })
  const registry = createExtensionRegistry([{
    apiVersion: 1, id: 'cases', version: '1.0.0', requires: [],
    routes: [{ id: 'workspace', path: 'workspace', title: 'Cases', component: 'cases/Page', permission: 'cases:read' }], navigation: [], panels: []
  }], { 'cases/Page': component }, ['cases'])
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', name: 'app-layout', component, children: [] }, { path: '/profile', component }, { path: '/login', component }] })
  const auth = {
    user: { id: 'user' }, isAuthenticated: true,
    get extensionRevision() { return extensionSession.revision },
    get extensionAccessRefreshing() { return isExtensionRevalidating('org:user') },
    hasExtensionPermission: extensionPermission
  }
  const binding = bindExtensionRouter(router, registry, auth)
  cleanup = () => { binding.stop(); router.options.history.destroy() }
  await router.push('/extensions/cases/workspace'); await settle()
  return { router, auth }
}
beforeEach(async () => {
  clearExtensionSession('org:user'); await refreshExtensionSession(async () => catalog())
  vi.spyOn(axios, 'post').mockResolvedValue({ data: {} })
})
afterEach(() => { cleanup?.(); vi.restoreAllMocks(); clearExtensionSession('') })

it.each(['/plan', '/auth/ws-token'])('retains route but hides authority during a current %s 401', async url => {
  const { router, auth } = await activeRoute()
  let finish!: () => void, attempts = 0
  api.defaults.adapter = config => {
    if (config.url === '/extensions/catalog') return new Promise(resolve => { finish = () => resolve(reply(config, catalog())) })
    return ++attempts === 1 ? deny(config) : Promise.resolve(reply(config, {}))
  }
  const pending = api.get(url).catch(error => error)
  await vi.waitFor(() => expect(finish).toBeTypeOf('function')); await settle()
  expect(extensionSession.ready).toBe(false)
  expect(canEnterExtensionRoute(router.currentRoute.value, auth)).toBe(false)
  expect(router.currentRoute.value.path).toBe('/extensions/cases/workspace')
  finish(); await pending; await settle()
  expect(canEnterExtensionRoute(router.currentRoute.value, auth)).toBe(true)
  expect(router.currentRoute.value.path).toBe('/extensions/cases/workspace')
  expect(attempts).toBe(url === '/auth/ws-token' ? 1 : 2)
  expect(isExtensionRevalidating('org:user')).toBe(false)
})

it('refreshes through one current catalog read without replaying the socket-token request', async () => {
  const { router } = await activeRoute(); let host = 0, catalogs = 0
  api.defaults.adapter = config => {
    if (config.url !== '/extensions/catalog') { host++; return deny(config) }
    return ++catalogs === 1 ? deny(config) : Promise.resolve(reply(config, catalog()))
  }
  await expect(api.get('/auth/ws-token')).rejects.toThrow('denied'); await settle()
  expect(host).toBe(1); expect(catalogs).toBe(2); expect(axios.post).toHaveBeenCalledTimes(1)
  expect(extensionSession.ready).toBe(true)
  expect(router.currentRoute.value.path).toBe('/extensions/cases/workspace')
})

it.each(['revoked', 'forbidden', 'failed'])('denies the route after %s catalog validation', async outcome => {
  const { router } = await activeRoute(); let catalogs = 0
  api.defaults.adapter = config => {
    if (config.url !== '/extensions/catalog') return deny(config)
    catalogs++
    return outcome === 'revoked' ? Promise.resolve(reply(config, catalog(false))) : deny(config, outcome === 'forbidden' ? 403 : 503)
  }
  await expect(api.get('/auth/ws-token')).rejects.toThrow(); await settle()
  expect(catalogs).toBe(1); expect(extensionPermission('cases:read')).toBe(false)
  expect(router.currentRoute.value.path).toBe('/profile')
  expect(isExtensionRevalidating('org:user')).toBe(false)
})

it('cannot apply a previous identity catalog after logout or account change', async () => {
  await activeRoute(); let finish!: () => void
  api.defaults.adapter = config => config.url === '/extensions/catalog'
    ? new Promise(resolve => { finish = () => resolve(reply(config, catalog())) }) : deny(config)
  const pending = api.get('/auth/ws-token').catch(error => error)
  await vi.waitFor(() => expect(finish).toBeTypeOf('function'))
  clearExtensionSession('other:user'); finish(); await pending; await settle()
  expect(extensionSession.identity).toBe('other:user'); expect(extensionSession.ready).toBe(false)
  expect(isExtensionRevalidating('org:user')).toBe(false)
})

it('serializes queued host reads and accepts only one fresh current catalog', async () => {
  const { router } = await activeRoute(); let finish!: () => void, catalogs = 0
  vi.mocked(axios.post).mockImplementation(() => new Promise(resolve => { finish = () => resolve({ data: {} }) }))
  const attempts = new Map<string, number>()
  api.defaults.adapter = config => {
    if (config.url === '/extensions/catalog') { catalogs++; return Promise.resolve(reply(config, catalog())) }
    const count = (attempts.get(config.url!) || 0) + 1; attempts.set(config.url!, count)
    return count === 1 ? deny(config) : Promise.resolve(reply(config, {}))
  }
  const pending = Promise.all([api.get('/plan'), api.get('/accounts')])
  await vi.waitFor(() => expect(finish).toBeTypeOf('function')); await settle()
  expect(extensionSession.ready).toBe(false); expect(router.currentRoute.value.path).toBe('/extensions/cases/workspace')
  finish(); await pending; await settle()
  expect(axios.post).toHaveBeenCalledTimes(1); expect(catalogs).toBe(1)
  expect(extensionSession.ready).toBe(true); expect(isExtensionRevalidating('org:user')).toBe(false)
})

it('does not try to recover catalog authority after a failed primary refresh', async () => {
  const { router } = await activeRoute(); let catalogs = 0
  vi.mocked(axios.post).mockRejectedValue(new Error('Refresh rejected'))
  api.defaults.adapter = config => { if (config.url === '/extensions/catalog') catalogs++; return deny(config) }
  await expect(api.get('/plan')).rejects.toThrow(); await settle()
  expect(catalogs).toBe(0); expect(extensionSession.ready).toBe(false)
  expect(isExtensionRevalidating('org:user')).toBe(false)
  expect(router.currentRoute.value.path).toBe('/profile')
})
