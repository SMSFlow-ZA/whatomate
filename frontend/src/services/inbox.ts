import { api } from '@/services/api'

export type CaseStatus = 'open' | 'pending' | 'waiting_customer' | 'snoozed' | 'resolved'
export type CasePriority = 'low' | 'normal' | 'high' | 'urgent'

export interface InboxCase {
  id: string
  contact_id: string
  account_id: string
  team_id?: string
  assignee_id?: string
  status: CaseStatus
  priority: CasePriority
  version: number
  snoozed_until?: string
}

export interface CaseActivity {
  id: string
  case_id: string
  kind: string
  created_at: string
  from_status?: CaseStatus
  to_status?: CaseStatus
  from_priority?: CasePriority
  to_priority?: CasePriority
}

interface CaseUpdate {
  case_id?: string
  version?: number
  status?: CaseStatus
  priority?: CasePriority
  snoozed_until?: string
}

type Envelope<T> = { data: T }
const conversationPath = (accountId: string, contactId: string) =>
  '/inbox/accounts/' + encodeURIComponent(accountId) + '/contacts/' + encodeURIComponent(contactId)

export const inboxService = {
  settings: () => api.get<Envelope<{ enabled: boolean }>>('/inbox/workflows'),
  setEnabled: (enabled: boolean) => api.put<Envelope<{ enabled: boolean }>>('/inbox/workflows', { enabled }),
  summaries: (contacts: string[]) => api.get<Envelope<{ cases: InboxCase[] }>>('/inbox/cases', { params: { contacts: contacts.join(',') } }),
  conversation: (accountId: string, contactId: string) =>
    api.get<Envelope<{ case: InboxCase | null; activity: CaseActivity[] }>>(conversationPath(accountId, contactId)),
  update: (accountId: string, contactId: string, input: CaseUpdate) =>
    api.put<Envelope<{ case: InboxCase }>>(conversationPath(accountId, contactId), input)
}
