// @vitest-environment happy-dom
import { createPinia, setActivePinia } from 'pinia'
import { describe, it, expect, vi } from 'vitest'
vi.mock('@/router', () => ({ default: { push: vi.fn() } }))
vi.mock('@/services/api', () => ({ contactsService: { markRead: vi.fn().mockResolvedValue({}) }, messagesService: {} }))
import { wsService } from './websocket'
import { useContactsStore } from '@/stores/contacts'

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
