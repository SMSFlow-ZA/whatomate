import { api } from '@/services/api'

export interface InboxAction { type: 'assign' | 'tag' | 'priority' | 'status' | 'note' | 'send_text' | 'send_template'; value: string; consent_confirmed?: boolean }
export interface InboxWorkflowInput { kind: 'macro' | 'rule'; name: string; trigger: string; position: number; actions: InboxAction[]; conditions: InboxFilters; enabled?: boolean }
export interface InboxWorkflow extends InboxWorkflowInput { id: string; enabled_at?: string }
export interface InboxWorkflowRun { id: string; workflow_name: string; state: string; created_at: string; outcome?: string }
export interface InboxWorkflowPreview { matches: boolean; confirmation: string; actions: InboxAction[]; send_unavailable: boolean; recipient: { name: string; phone: string }; account: string; reply?: { text: string; header?: string; footer?: string; template?: string; template_key?: string; language?: string } }
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

const workflowPath = (id: string) => '/inbox/workflow-definitions/' + encodeURIComponent(id)
export const inboxService = {
  workflowDefinitions: (kind: 'macro' | 'rule') => api.get<Envelope<{ workflows: InboxWorkflow[] }>>('/inbox/workflow-definitions', { params: {kind} }),
  createWorkflow: (input: InboxWorkflowInput) => api.post('/inbox/workflow-definitions', input),
  updateWorkflow: (id: string, input: InboxWorkflowInput) => api.put(workflowPath(id), input),
  deleteWorkflow: (id: string) => api.delete(workflowPath(id)),
  previewWorkflow: (id: string, case_id: string) => api.post<Envelope<InboxWorkflowPreview>>(workflowPath(id) + '/preview', { case_id }),
  executeMacro: (id: string, case_id: string, run_id: string, confirmation: string) => api.post<Envelope<{ run: InboxWorkflowRun }>>(workflowPath(id) + '/execute', { case_id, run_id, confirmation }),
  workflowRuns: (case_id?: string) => api.get<Envelope<{ runs: InboxWorkflowRun[] }>>('/inbox/workflow-runs', { params: {case_id} }),
  automationState: () => api.get<Envelope<{ enabled: boolean; dispatch_available: boolean }>>('/inbox/automation'),
  setAutomation: (enabled: boolean) => api.put<Envelope<{ enabled: boolean; dispatch_available: boolean }>>('/inbox/automation', { enabled }),
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
export type BulkInboxResult = 'updated' | 'unavailable' | 'stale' | 'invalid' | 'failed'

export interface InboxFilters {
  queue?: InboxQueue
  account_id?: string
  team_id?: string
  assignee_id?: string
  status?: CaseStatus
  priority?: CasePriority
  tags?: string[]
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
