import { api } from '@/services/api'

export type FieldKind = 'text' | 'number' | 'date' | 'boolean' | 'select'
export type FieldValue = string | number | boolean | null
export interface FieldDefinition {
  id: string
  scope: 'case' | 'contact'
  key: string
  label: string
  kind: FieldKind
  options?: string[]
  required_on_resolve: boolean
  archived_at?: string
}
export interface FieldMatch { definition_id: string; value: FieldValue }

export type CaseStatus = 'open' | 'pending' | 'waiting_customer' | 'snoozed' | 'resolved'
export type CasePriority = 'low' | 'normal' | 'high' | 'urgent'

export interface InboxSLAPolicy {
  account_id: string
  first_reply_minutes: number
  next_reply_minutes: number
  resolution_minutes: number
  timezone: string
  business_hours: boolean
  shifts: { Day: number; StartMinute: number; EndMinute: number }[]
}

export type InboxSLA = Record<string, { status: string; deadline?: string; remaining_seconds: number }>

export interface InboxCase {
  sla?: InboxSLA
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

const fieldPath = (contact: string, account?: string) => (account ? conversationPath(account, contact) : '/inbox/contacts/' + encodeURIComponent(contact)) + '/fields'

export const inboxService = {
  fieldDefinitions: (scope?: FieldDefinition['scope']) => api.get<Envelope<{ definitions: FieldDefinition[] }>>('/inbox/fields', { params: { scope } }),
  createFieldDefinition: (input: Omit<FieldDefinition, 'id' | 'archived_at'>) => api.post('/inbox/fields', input),
  updateFieldDefinition: (id: string, input: { label?: string; required_on_resolve?: boolean; archive?: boolean }) => api.put('/inbox/fields/' + encodeURIComponent(id), input),
  fieldValues: (contact: string, account?: string) => api.get<Envelope<{ subject_id: string; values: FieldMatch[] }>>(fieldPath(contact, account)),
  setFieldValue: (contact: string, account: string | undefined, definition: string, subject_id: string, value: FieldValue) => api.put(fieldPath(contact, account) + '/' + encodeURIComponent(definition), { subject_id, value }),
  slaPolicies: () => api.get<Envelope<{ policies: InboxSLAPolicy[] }>>('/inbox/sla/policies'),
  setSlaPolicy: (id: string, policy: InboxSLAPolicy) => api.put('/inbox/sla/policies/' + encodeURIComponent(id), policy),
  deleteSlaPolicy: (id: string) => api.delete('/inbox/sla/policies/' + encodeURIComponent(id)),
  slaReport: (account: string, team: string, offset: number) => api.get<Envelope<{ cases: QueueCase[]; has_more: boolean }>>('/inbox/sla/report', { params: { account_id: account || undefined, team_id: team || undefined, offset } }),
  settings: () => api.get<Envelope<{ enabled: boolean }>>('/inbox/workflows'),
  queue: (filters: InboxFilters, offset = 0) =>
    api.get<Envelope<{ cases: QueueCase[]; has_more: boolean }>>('/inbox/queue', {
      params: { filters: JSON.stringify(filters), offset }
    }),
  views: () => api.get<Envelope<{ views: SavedInboxView[] }>>('/inbox/views'),
  saveView: (name: string, filters: InboxFilters, team_id?: string) =>
    api.post<Envelope<{ view: SavedInboxView }>>('/inbox/views', { name, filters, team_id }),
  deleteView: (id: string) => api.delete('/inbox/views/' + encodeURIComponent(id)),
  bulk: (action: BulkInboxAction, items: { id: string; version: number }[], value?: string) =>
    api.post<Envelope<{ results: { id: string; result: BulkInboxResult }[] }>>('/inbox/cases/bulk', {
      action, items, tag: action === 'tag' ? value : undefined,
      assignee_id: action === 'assign' ? value : undefined
    }),
  setEnabled: (enabled: boolean) => api.put<Envelope<{ enabled: boolean }>>('/inbox/workflows', { enabled }),
  summaries: (contacts: string[]) => api.get<Envelope<{ cases: InboxCase[] }>>('/inbox/cases', { params: { contacts: contacts.join(',') } }),
  conversation: (accountId: string, contactId: string) =>
    api.get<Envelope<{ case: InboxCase | null; activity: CaseActivity[] }>>(conversationPath(accountId, contactId)),
  update: (accountId: string, contactId: string, input: CaseUpdate) =>
    api.put<Envelope<{ case: InboxCase }>>(conversationPath(accountId, contactId), input)
}

export type InboxQueue = '' | 'mine' | 'unassigned' | 'awaiting_reply' | 'follow_ups'
export type BulkInboxAction = 'assign' | 'tag' | 'resolve'
export type BulkInboxResult = 'updated' | 'unavailable' | 'stale' | 'invalid' | 'failed' | 'required_fields'

export interface InboxFilters {
  queue?: InboxQueue
  account_id?: string
  team_id?: string
  assignee_id?: string
  status?: CaseStatus
  priority?: CasePriority
  tags?: string[]
  fields?: FieldMatch[]
}

export interface QueueCase extends InboxCase {
  account_name: string
  contact_name: string
}

export interface SavedInboxView {
  id: string
  owner_id: string
  team_id?: string
  name: string
  filters: InboxFilters
}
