import { api } from './api'

export interface InboxNoticeSettings {
  account_id: string
  revision: number
  enabled: boolean
  at_risk: boolean
  breach: boolean
  recipient_ids: string[]
  at_risk_since?: string
  breach_since?: string
  updated_at?: string
}
export type InboxNoticeSettingsInput = Pick<
  InboxNoticeSettings,
  'revision' | 'enabled' | 'at_risk' | 'breach' | 'recipient_ids'
>
export interface InboxNoticePerson {
  id: string
  name: string
  full_contact_read: boolean
}
export interface InboxNotice {
  id: string
  event_id: string
  case_id: string
  account_id: string
  contact_id: string
  account_name: string
  contact_name: string
  policy_name: string
  policy_version_id: string
  threshold: 'at_risk' | 'breach'
  kind: 'first_reply' | 'next_reply' | 'resolution'
  due_at: string
  observed_at: string
  read_at?: string
  target_state: 'active' | 'breached' | 'paused' | 'completed' | 'case_resolved'
}
export interface InboxNoticeEvent {
  id: string
  account_id: string
  case_id: string
  obligation_id: string
  policy_version_id: string
  threshold: InboxNotice['threshold']
  kind: InboxNotice['kind']
  settings_revision: number
  due_at: string
  observed_at: string
  available_count: number
  suppressed_count: number
  recipients: {
    id: string
    event_id: string
    recipient_id: string
    outcome:
      | 'available'
      | 'recipient_unavailable'
      | 'case_access_denied'
      | 'legacy_transfer_covered'
  }[]
}
type Envelope<T> = { data: T }
type Page = { has_more: boolean; next_cursor: string }
const root = (account: string) =>
  '/inbox/sla/escalations/' + encodeURIComponent(account)
export const inboxNoticeService = {
  availability: () =>
    api.get<Envelope<{ enabled: boolean }>>('/inbox/workflows'),
  settings: (account: string) =>
    api.get<
      Envelope<{
        settings: InboxNoticeSettings
        maximum_recipients: number
        delivery: 'in_app'
      }>
    >(root(account)),
  save: (account: string, input: InboxNoticeSettingsInput) =>
    api.put<Envelope<{ settings: InboxNoticeSettings; delivery: 'in_app' }>>(
      root(account),
      input,
    ),
  recipients: (account: string, search: string, cursor?: string) =>
    api.get<Envelope<Page & { recipients: InboxNoticePerson[] }>>(
      root(account) + '/recipients',
      { params: { search: search || undefined, cursor } },
    ),
  history: (account: string, cursor?: string) =>
    api.get<Envelope<Page & { events: InboxNoticeEvent[] }>>(
      root(account) + '/history',
      { params: { cursor } },
    ),
  list: (account?: string, unread = false, cursor?: string) =>
    api.get<Envelope<Page & { notices: InboxNotice[]; delivery: 'in_app' }>>(
      '/inbox/sla/notices',
      {
        params: {
          account_id: account || undefined,
          unread: unread || undefined,
          cursor,
        },
      },
    ),
  read: (id: string) =>
    api.put<Envelope<{ id: string; read: true }>>(
      '/inbox/sla/notices/' + encodeURIComponent(id) + '/read',
      {},
    ),
}

// A hint only requests a scoped reload; it carries no display content or access.
export const inboxNoticesChanged = 'whatomate:inbox-notices-changed'
