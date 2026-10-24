import { describe, it, expect, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
vi.mock('@/services/api', () => ({ contactsService: {}, messagesService: { send: vi.fn() } }))
import { messagesService } from '@/services/api'
import { useContactsStore } from './contacts'

describe('SMS inbox isolation', () => {
  it('reuses one SMS identifier after a lost inbox response and replaces it for a new send', async () => {
    setActivePinia(createPinia())
    const store=useContactsStore()
    const send=vi.mocked(messagesService.send)
    send.mockReset()
    send.mockRejectedValueOnce(new Error('response lost after commit'))
    send.mockResolvedValue({data:{data:{id:'message-1',contact_id:'contact-1',channel:'sms',direction:'outgoing',content:{body:'Hello'},created_at:''}}} as any)
    const fields={channel:'sms' as const,channel_connection_id:'connection-1',consent_confirmed:true}
    await expect(store.sendMessage('contact-1','text',{body:'Hello'},undefined,undefined,fields)).rejects.toThrow('response lost')
    await store.sendMessage('contact-1','text',{body:'Hello'},undefined,undefined,fields)
    const first=(send.mock.calls[0][1] as any).client_message_id
    expect(first).toMatch(/^[a-f0-9-]{36}$/)
    expect((send.mock.calls[1][1] as any).client_message_id).toBe(first)
    await store.sendMessage('contact-1','text',{body:'Hello'},undefined,undefined,fields)
    expect((send.mock.calls[2][1] as any).client_message_id).not.toBe(first)
    send.mockRejectedValueOnce(new Error('lost again'))
    await expect(store.sendMessage('contact-1','text',{body:'Hello'},undefined,undefined,fields)).rejects.toThrow()
    const uncertain=(send.mock.calls[3][1] as any).client_message_id
    await store.sendMessage('contact-1','text',{body:'Edited'},undefined,undefined,fields)
    expect((send.mock.calls[4][1] as any).client_message_id).not.toBe(uncertain)
  })
  it('does not reopen the WhatsApp window when an SMS arrives', () => {
    setActivePinia(createPinia())
    const store = useContactsStore()
    const contact = { id: 'contact-1', phone_number: '27820000000', name: 'Test', tags: [], status: 'active', metadata: {}, unread_count: 0, service_window_open: false, last_inbound_at: '2026-10-01T08:00:00Z', created_at: '', updated_at: '' }
    store.contacts = [contact]; store.setCurrentContact({ ...contact })
    store.addMessage({ id: 'sms-1', contact_id: contact.id, direction: 'incoming', channel: 'sms', message_type: 'text', content: { body: 'Hello' }, status: 'delivered', created_at: '2026-10-19T08:00:00Z', updated_at: '' })
    expect(store.contacts[0].service_window_open).toBe(false)
    expect(store.currentContact?.service_window_open).toBe(false)
    expect(store.currentContact?.last_inbound_at).toBe('2026-10-01T08:00:00Z')
    expect(store.messages).toHaveLength(1)
  })
})
