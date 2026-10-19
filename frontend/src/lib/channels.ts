export interface SMSFallbackPolicy {
  enabled: boolean
  connection_id: string
  text: string
  consent_confirmed: boolean
  timeout_enabled: boolean
  timeout_minutes: number
}
export interface ChannelSendFields {
  channel?: 'sms'
  channel_connection_id?: string
  consent_confirmed?: boolean
  sms_fallback?: SMSFallbackPolicy
}
export interface ChannelOptions {
  channel: 'whatsapp' | 'sms'
  connection_id: string
  fallback_enabled: boolean
  consent_confirmed: boolean
  sms_text: string
  timeout_enabled: boolean
  timeout_minutes: number
}
export interface ChannelConnection {
  id: string; name: string; provider: string; enabled: boolean; max_segments: number
  capabilities: { text: boolean; inbound: boolean; delivery_reports: boolean }
  balance?: {amount?: string;unit:string;currency?:string}; balance_threshold: string; low_balance: boolean; balance_error_code?:string; validated_at?: string;
  bot_flow_id?: string; bot_consent_confirmed?: boolean
  last_poll_at?: string; poll_failures: number; error_code?: string
}
export interface DeliveryAttempt {
  id: string; channel: string; status: string; text?: string
  error_code?: string; is_fallback: boolean; due_at?: string
}
export const initialChannelOptions = (): ChannelOptions => ({ channel: 'whatsapp', connection_id: '', fallback_enabled: false, consent_confirmed: false, sms_text: '', timeout_enabled: false, timeout_minutes: 10 })

export function campaignParameterNames(...texts: Array<string | undefined>): string[] {
  const names = new Set<string>()
  for (const text of texts) for (const match of (text || '').matchAll(/\{\{([^}]+)\}\}/g)) {
    const name = match[1].trim(); if (name) names.add(name)
  }
  return [...names]
}

export function channelPayload(options: ChannelOptions, rich: boolean): ChannelSendFields {
  if (options.channel === 'sms' || options.fallback_enabled) {
    if (!options.connection_id || !options.consent_confirmed) throw new Error('Select an SMS connection and confirm recipient consent.')
  }
  if (options.channel === 'sms') {
    if (rich) throw new Error('SMS supports plain text only.')
    return { channel: 'sms', channel_connection_id: options.connection_id, consent_confirmed: true }
  }
  if (!options.fallback_enabled) return {}
  if (rich && !options.sms_text.trim()) throw new Error('Add a separate SMS version for this rich message.')
  if (!Number.isInteger(options.timeout_minutes) || options.timeout_minutes < 1 || options.timeout_minutes > 1440) throw new Error('Choose a timeout from 1 to 1440 minutes.')
  return { sms_fallback: { enabled: true, connection_id: options.connection_id, text: options.sms_text, consent_confirmed: true, timeout_enabled: options.timeout_enabled, timeout_minutes: options.timeout_minutes } }
}
