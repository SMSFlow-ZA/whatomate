// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { defaultBranding, useBrandingStore } from './branding'
const api = vi.hoisted(() => ({ get: vi.fn(), put: vi.fn(), delete: vi.fn() }))
vi.mock('@/services/api', () => ({ api }))
beforeEach(() => {
 setActivePinia(createPinia()); vi.clearAllMocks()
 document.head.innerHTML = '<link rel="icon" href="/favicon.svg"><meta name="description">'
})
describe('runtime branding', () => {
 it('retains Whatomate identity before loading', () => {
  const store = useBrandingStore()
  expect(store.current.display_name).toBe('Whatomate')
  expect(store.current.logo_url).toBe('/branding/whatomate-logo.svg')
  expect(store.current.favicon_url).toBe('/favicon.svg')
 })
 it('fails to safe defaults and disables editing after a metadata failure', async () => {
  api.get.mockRejectedValue(new Error('offline'))
  const store = useBrandingStore(); await store.load()
  expect(store.loadError).toBe(true)
  expect(store.current).toEqual(defaultBranding)
  expect(document.title).toBe('Whatomate')
 })
 it('applies saved identity and reset with server supplied assets', async () => {
  const custom = { ...defaultBranding, display_name: 'Example team', accent_color: '#123456', favicon_url: '/api/branding/assets/favicon?v=hash', custom: true }
  api.put.mockResolvedValue({ data: { data: custom } })
  api.delete.mockResolvedValue({ data: { data: defaultBranding } })
  const store = useBrandingStore(); await store.save(new FormData())
  expect(document.title).toBe('Example team')
  expect(document.querySelector('link')?.getAttribute('href')).toBe(custom.favicon_url)
  expect(document.documentElement.style.getPropertyValue('--primary')).not.toBe('')
  await store.reset()
  expect(document.title).toBe('Whatomate')
  expect(store.current.custom).toBe(false)
 })
})
