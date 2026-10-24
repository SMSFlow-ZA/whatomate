import { describe, it, expect } from 'vitest'
import { channelPayload, initialChannelOptions, campaignParameterNames } from './channels'

describe('explicit SMS workflow selection', () => {
  it('imports SMS and fallback personalization columns without a WhatsApp template', () => {
    expect(campaignParameterNames('Hi {{name}}, order {{order}}', 'Hello {{name}}')).toEqual(['name','order'])
    expect(campaignParameterNames('WhatsApp {{1}}', 'SMS {{tracking}}')).toEqual(['1','tracking'])
  })
  it('keeps existing WhatsApp requests unchanged until opted in', () => {
    expect(channelPayload(initialChannelOptions(), false)).toEqual({})
  })
  it('requires consent and a connection for direct SMS', () => {
    const options = { ...initialChannelOptions(), channel: 'sms' as const }
    expect(() => channelPayload(options, false)).toThrow()
    expect(channelPayload({ ...options, connection_id: 'connection-1', consent_confirmed: true }, false)).toEqual({ channel: 'sms', channel_connection_id: 'connection-1', consent_confirmed: true })
  })
  it('requires an authored SMS variant for rich content', () => {
    const options = { ...initialChannelOptions(), fallback_enabled: true, connection_id: 'connection-1', consent_confirmed: true }
    expect(() => channelPayload(options, true)).toThrow()
    const result = channelPayload({ ...options, sms_text: 'Your order is ready', timeout_enabled: true }, true)
    expect(result.sms_fallback).toEqual({ enabled: true, connection_id: 'connection-1', text: 'Your order is ready', consent_confirmed: true, timeout_enabled: true, timeout_minutes: 10 })
  })
})
