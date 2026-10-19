import { describe, it, expect, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
vi.mock('@/services/api', () => ({ contactsService: {}, messagesService: {} }))
import { useContactsStore } from './contacts'

describe('SMS inbox isolation', () => {
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
