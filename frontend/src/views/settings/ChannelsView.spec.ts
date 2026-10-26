// @vitest-environment happy-dom
import { createApp, nextTick } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import ChannelsView from './ChannelsView.vue'
import api from '@/services/api'

const state = vi.hoisted(() => ({ write: true }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ hasPermission: () => state.write }) }))
vi.mock('@/services/api', () => ({ default: { get: vi.fn(), put: vi.fn(), post: vi.fn() } }))
vi.mock('vue-sonner', () => ({ toast: { success: vi.fn() } }))
let inbound = false, enabled = false, health: unknown = { dispatch_stopped: true }, healthFails = false
const connection = () => ({ id: 'c1', name: 'Test SMS', provider: 'smsflow', enabled, max_segments: 3,
  capabilities: { text: true, inbound, delivery_reports: true }, balance_threshold: '100',
  bot_flow_id: 'flow1', bot_consent_confirmed: true, poll_failures: 0 })
let root: HTMLDivElement, app: ReturnType<typeof createApp>
async function flush() { for (let i = 0; i < 8; i++) { await Promise.resolve(); await nextTick() } }
async function mount() { root = document.createElement('div'); document.body.append(root); app = createApp(ChannelsView); app.mount(root); await flush() }
function button(text: string) { return Array.from(root.querySelectorAll('button')).find(b => b.textContent?.includes(text))! }
async function edit() { button('Edit / rotate').click(); await flush() }
async function save() { root.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await flush() }
beforeEach(() => {
  inbound = false; enabled = false; health = { dispatch_stopped: true }; healthFails = false; state.write = true
  vi.clearAllMocks()
  vi.mocked(api.get).mockImplementation(async path => {
    if (path === '/channels/health') { if (healthFails) throw Error('offline'); return { data: health } as any }
    if (path === '/channel-connections') return { data: { connections: [connection()] } } as any
    if (path === '/channels/providers') return { data: { credentials_configured: true } } as any
    return { data: { flows: [{ id: 'flow1', name: 'Reply flow', channel: 'sms', enabled: true }] } } as any
  })
  vi.mocked(api.put).mockResolvedValue({ data: {} } as any)
})
afterEach(() => { app?.unmount(); root?.remove() })
describe('one-way settings and dispatch hold', () => {
  it('explains the global pause and prevents enablement', async () => {
    await mount(); expect(root.textContent).toContain('SMS sending is paused'); expect(button('Enable dispatch').disabled).toBe(true)
  })
  it('keeps stop available while paused', async () => {
    enabled = true; await mount(); expect(button('Stop pending').disabled).toBe(false)
    button('Stop pending').click(); await flush(); expect(api.put).toHaveBeenCalledWith('/channel-connections/c1', expect.objectContaining({ enabled: false }))
  })
  it.each([null, {}, { dispatch_stopped: 'false' }])('fails closed for malformed health %j', async value => {
    health = value; await mount(); expect(button('Enable dispatch').disabled).toBe(true); expect(root.textContent).toContain('Sending status is unavailable')
  })
  it('fails closed for a failed health request without hiding configuration', async () => {
    healthFails = true; await mount(); expect(button('Enable dispatch').disabled).toBe(true); expect(button('Save connection').disabled).toBe(false)
  })
  it('allows explicit enable only after healthy ready status', async () => {
    health = { dispatch_stopped: false }; await mount(); expect(button('Enable dispatch').disabled).toBe(false)
    button('Enable dispatch').click(); await flush(); expect(api.put).toHaveBeenCalledWith('/channel-connections/c1', expect.objectContaining({ enabled: true }))
  })
  it('hides reply controls for one-way and clears stale bot settings on explicit save during hold', async () => {
    await mount(); await edit(); expect(root.querySelector('select')).toBeNull(); expect(root.textContent).toContain('One-way SMS')
    await save(); expect(api.put).toHaveBeenCalledWith('/channel-connections/c1', expect.objectContaining({ bot_flow_id: '', bot_consent_confirmed: false, enabled: false }))
  })
  it('retains reply controls and values for a confirmed inbound route', async () => {
    inbound = true; await mount(); await edit(); expect(root.querySelector('select')).not.toBeNull(); await save()
    expect(api.put).toHaveBeenCalledWith('/channel-connections/c1', expect.objectContaining({ bot_flow_id: 'flow1', bot_consent_confirmed: true }))
  })
  it('refreshes a capability downgrade and failed health without stale enablement', async () => {
    inbound = true; health = { dispatch_stopped: false }; await mount(); await edit()
    inbound = false; healthFails = true; button('Refresh status').click(); await flush()
    expect(root.querySelector('select')).toBeNull(); expect(button('Enable dispatch').disabled).toBe(true)
    await save(); expect(api.put).toHaveBeenCalledWith('/channel-connections/c1', expect.objectContaining({ bot_flow_id: '', bot_consent_confirmed: false }))
  })
  it('does not require reply capability in a save error', async () => {
    await mount(); await edit(); vi.mocked(api.put).mockRejectedValueOnce(Error('invalid')); await save()
    expect(root.textContent).toContain('Connection could not be saved'); expect(root.textContent).not.toContain('reply-capable route')
  })
  it('blocks stale editing and enablement when settings reload fails', async () => {
    inbound = true; health = { dispatch_stopped: false }; await mount(); await edit()
    const original = vi.mocked(api.get).getMockImplementation()!
    vi.mocked(api.get).mockImplementation(async path => { if (path === '/channel-connections') throw Error('offline'); return original(path) })
    button('Refresh status').click(); await flush()
    expect(root.querySelector('select')).toBeNull(); expect(button('Save connection').disabled).toBe(true)
    await save(); expect(api.put).not.toHaveBeenCalled()
  })
  it('keeps settings read only for agents', async () => {
    state.write = false; await mount(); expect(root.querySelector('form')).toBeNull(); expect(button('Enable dispatch')).toBeUndefined()
  })
})
