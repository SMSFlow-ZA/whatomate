// @vitest-environment happy-dom
import { createPinia, setActivePinia } from 'pinia'
import { describe, it, expect, vi } from 'vitest'
vi.mock('@/router', () => ({ default: { push: vi.fn() } }))
vi.mock('@/services/api', () => ({ contactsService: { markRead: vi.fn().mockResolvedValue({}) }, messagesService: {} }))
import { wsService } from './websocket'
import { useContactsStore } from '@/stores/contacts'
import { contactsService } from '@/services/api'
import { inboxNoticesChanged } from './inboxNotices'

it('treats internal notice hints as content-free reloads without a toast or navigation', () => {
  setActivePinia(createPinia())
  const listener = vi.fn()
  window.addEventListener(inboxNoticesChanged, listener)
  try {
    ;(wsService as any).handleMessage(JSON.stringify({ type: 'inbox_notice_available', payload: { id: 'synthetic-notice', body: 'Must never be displayed', url: 'https://example.invalid' } }))
    expect(listener).toHaveBeenCalledOnce()
    expect(listener.mock.calls[0]![0].detail).toBeUndefined()
    expect(document.body.textContent).not.toContain('Must never be displayed')
  } finally { window.removeEventListener(inboxNoticesChanged, listener) }
})

describe('SMS websocket wire mapping', () => {
  it('preserves SMS identity and attempts before updating the open inbox', () => {
    setActivePinia(createPinia()); const store = useContactsStore()
    const contact = { id:'c1', phone_number:'27820000000', name:'Fixture', tags:[],status:'active',metadata:{},unread_count:0,service_window_open:false,created_at:'',updated_at:'' }
    store.contacts=[contact];store.setCurrentContact({...contact})
    const payload = { id:'m1',contact_id:'c1',channel:'sms',channel_connection_id:'connection',delivery_attempts:[{id:'a1',channel:'sms',status:'delivered',is_fallback:false}],direction:'incoming',status:'read',message_type:'text',content:{body:'Hello'},created_at:'2026-10-19T10:00:00Z',updated_at:'' }
    ;(wsService as any).handleNewMessage(store,payload)
    expect(store.messages[0].channel).toBe('sms')
    expect(store.messages[0].channel_connection_id).toBe('connection')
    expect(store.messages[0].delivery_attempts).toEqual(payload.delivery_attempts)
    expect(store.currentContact?.service_window_open).toBe(false)
  })
})

it('marks only the selected WhatsApp account read and leaves SMS receipts alone', () => {
  setActivePinia(createPinia())
  const store = useContactsStore()
  const contact = { id: 'c1', phone_number: '12025550191', name: 'Example', tags: [], status: 'active', metadata: {}, unread_count: 0, service_window_open: false, created_at: '', updated_at: '' }
  store.contacts = [contact]
  store.setCurrentContact({ ...contact })
  store.setAccountFilter('inbox-a')
  const mark = vi.mocked(contactsService.markRead)
  mark.mockClear()
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
  vi.spyOn(document, 'hasFocus').mockReturnValue(true)
  const message = { id: 'm1', contact_id: 'c1', channel: 'whatsapp', whatsapp_account: 'inbox-b', direction: 'incoming', status: 'delivered', message_type: 'text', content: { body: 'Synthetic reply' }, created_at: '2026-11-19T10:00:00Z', updated_at: '' }
  try {
    ;(wsService as any).handleNewMessage(store, message)
    expect(mark).not.toHaveBeenCalled()
    ;(wsService as any).handleNewMessage(store, { ...message, id: 'm2', whatsapp_account: 'inbox-a' })
    expect(mark).toHaveBeenCalledWith('c1', 'inbox-a')
    mark.mockClear()
    ;(wsService as any).handleNewMessage(store, { ...message, id: 'm3', channel: 'sms', whatsapp_account: '' })
    expect(mark).not.toHaveBeenCalled()
  } finally { vi.restoreAllMocks() }
})
