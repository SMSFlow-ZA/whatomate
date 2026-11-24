import { api, type messagesService } from '@/services/api'

export type FieldKind = 'text' | 'number' | 'date' | 'boolean' | 'select'
export type FieldValue = string | number | boolean | null
export interface FieldDefinition {
  id: string
  scope: 'case' | 'contact'
  key: string
  label: string
  description?: string
  kind: FieldKind
  options?: string[]
  required_on_resolve: boolean
  archived_at?: string
}
export interface FieldMatch {
  definition_id: string
  value: FieldValue
  operator?: string
}
export interface InboxPredicate {
  field: 'custom_field' | 'status' | 'priority' | 'tag'
  definition_id?: string
  operator: string
  value?: FieldValue
}
export interface InboxConditionOutcome {
  field: string
  label?: string
  group: string
  matched: boolean
}
export interface InboxActionOutcome {
  index: number
  type: string
  status: string
  reason: string
  detail?: string
}
export interface MissingInboxField extends FieldDefinition {
  can_edit: boolean
  editor_path?: string
  handoff?: string
}
export interface CapacityPreset {
  id: string
  name: string
  max_active: number
}
export interface CapacityChange {
  agent_id: string
  name: string
  before: number
  after: number
}

export interface InboxAction {
  personalise?: boolean
  body_parameters?: Record<string, string>
  header_parameters?: Record<string, string>
  type:
    | 'assign'
    | 'assign_team'
    | 'snooze'
    | 'tag'
    | 'priority'
    | 'status'
    | 'note'
    | 'send_text'
    | 'send_template'
  value: string
  duration_minutes?: number
  consent_confirmed?: boolean
}
export interface InboxWorkflowInput {
  kind: 'macro' | 'rule'
  name: string
  description?: string
  visibility?: 'personal' | 'team' | 'organization'
  team_id?: string
  trigger: string
  position: number
  actions: InboxAction[]
  conditions: InboxFilters
  enabled?: boolean
}
export interface InboxWorkflow extends InboxWorkflowInput {
  id: string
  enabled_at?: string
}
export interface InboxWorkflowRun {
  id: string
  workflow_name: string
  state: string
  created_at: string
  outcome?: string
  actor_name?: string
  account_name?: string
  account_id?: string
  contact_id?: string
  contact_name?: string
  case_id?: string
  conditions_matched?: boolean
  condition_outcomes?: InboxConditionOutcome[]
  action_outcomes?: InboxActionOutcome[]
  message_status?: string
}
export interface InboxWorkflowPreview {
  case_id?: string
  case_version?: number
  condition_outcomes?: InboxConditionOutcome[]
  action_outcomes?: InboxActionOutcome[]
  actions_eligible?: boolean
  trigger_text_is_synthetic?: boolean
  matches: boolean
  confirmation: string
  actions: InboxAction[]
  send_unavailable: boolean
  send_unavailable_reason?: string
  recipient: { name: string; phone: string }
  account: string
  reply?: {
    canned_response_id?: string
    canned_name?: string
    interactive?: Parameters<typeof messagesService.send>[1]['interactive']
    missing_variables?: { key: string; reason: string }[]
    text: string
    header?: string
    footer?: string
    template?: string
    template_key?: string
    language?: string
  }
}

export type CaseStatus =
  | 'open'
  | 'pending'
  | 'waiting_customer'
  | 'snoozed'
  | 'resolved'
export type CasePriority = 'low' | 'normal' | 'high' | 'urgent'

export interface InboxSLAPolicy {
  account_id: string
  version_id?: string
  first_reply_minutes: number
  next_reply_minutes: number
  resolution_minutes: number
  timezone: string
  business_hours: boolean
  shifts: { Day: number; StartMinute: number; EndMinute: number }[]
}

export interface InboxSLATarget {
  status: string
  deadline?: string
  remaining_seconds: number
  timezone?: string
}
export interface InboxSLA {
  snooze_options?: { key: string; until: string; timezone: string }[]
  first_reply?: InboxSLATarget
  next_reply?: InboxSLATarget
  resolution?: InboxSLATarget
  policy_version?: string
  legacy?: boolean
}
export interface InboxSLAHistoryRow {
  id: string
  case_id: string
  contact_id: string
  contact_name: string
  account_id: string
  account_name: string
  agent_name: string
  team_name: string
  policy_id: string
  kind: string
  started_at: string
  deadline?: string
  completed_at: string
  working_seconds: number
  budget_minutes: number
  outcome: string
  legacy: boolean
}

export interface InboxCapacity {
  agent_id: string
  name: string
  max_active: number
  active: number
  available: boolean
  inactive?: boolean
}

export interface InboxCase {
  routing_reason?: string
  opened_at?: string
  resolved_at?: string
  next_reply_from?: string
  first_human_reply_at?: string
  last_inbound_at?: string
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
  detail?: string
  actor_name?: string
  actor_id?: string
  assignee_name?: string
  team_name?: string
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
  '/inbox/accounts/' +
  encodeURIComponent(accountId) +
  '/contacts/' +
  encodeURIComponent(contactId)

const fieldPath = (contact: string, account?: string) =>
  (account
    ? conversationPath(account, contact)
    : '/inbox/contacts/' + encodeURIComponent(contact)) + '/fields'

const workflowPath = (id: string) =>
  '/inbox/workflow-definitions/' + encodeURIComponent(id)

export const inboxService = {
  previewMessage: (
    case_id: string,
    text: string,
    canned_response_id?: string,
  ) =>
    api.post<Envelope<InboxWorkflowPreview>>('/inbox/message-preview', {
      case_id,
      text,
      ...(canned_response_id ? { canned_response_id } : {}),
    }),
  messageVariables: () =>
    api.get<Envelope<{ variables: InboxMessageVariable[] }>>(
      '/inbox/message-variables',
    ),
  workflowDefinitions: (kind: 'macro' | 'rule') =>
    api.get<Envelope<{ workflows: InboxWorkflow[] }>>(
      '/inbox/workflow-definitions',
      { params: { kind } },
    ),
  createWorkflow: (input: InboxWorkflowInput) =>
    api.post('/inbox/workflow-definitions', input),
  updateWorkflow: (id: string, input: InboxWorkflowInput) =>
    api.put(workflowPath(id), input),
  deleteWorkflow: (id: string) => api.delete(workflowPath(id)),
  previewWorkflow: (id: string, case_id: string, trigger_text?: string) =>
    api.post<Envelope<InboxWorkflowPreview>>(workflowPath(id) + '/preview', {
      case_id,
      trigger_text,
    }),
  executeMacro: (
    id: string,
    case_id: string,
    run_id: string,
    confirmation: string,
  ) =>
    api.post<Envelope<{ run: InboxWorkflowRun }>>(
      workflowPath(id) + '/execute',
      { case_id, run_id, confirmation },
    ),
  workflowRuns: (case_id?: string, offset = 0) =>
    api.get<Envelope<{ runs: InboxWorkflowRun[]; has_more: boolean }>>(
      '/inbox/workflow-runs',
      { params: { case_id, offset } },
    ),
  automationState: () =>
    api.get<Envelope<{ enabled: boolean; dispatch_available: boolean }>>(
      '/inbox/automation',
    ),
  setAutomation: (enabled: boolean) =>
    api.put<Envelope<{ enabled: boolean; dispatch_available: boolean }>>(
      '/inbox/automation',
      { enabled },
    ),
  teamCapacity: (team: string, account: string) =>
    api.get<Envelope<{ agents: InboxCapacity[] }>>(
      '/inbox/teams/' +
        encodeURIComponent(team) +
        '/capacity/' +
        encodeURIComponent(account),
    ),
  setTeamCapacity: (
    team: string,
    account: string,
    agent_id: string,
    max_active: number,
  ) =>
    api.put(
      '/inbox/teams/' +
        encodeURIComponent(team) +
        '/capacity/' +
        encodeURIComponent(account),
      { agent_id, max_active },
    ),
  fieldDefinitions: (scope?: FieldDefinition['scope']) =>
    api.get<Envelope<{ definitions: FieldDefinition[] }>>('/inbox/fields', {
      params: { scope },
    }),
  createFieldDefinition: (input: Omit<FieldDefinition, 'id' | 'archived_at'>) =>
    api.post('/inbox/fields', input),
  updateFieldDefinition: (
    id: string,
    input: {
      description?: string
      label?: string
      required_on_resolve?: boolean
      archive?: boolean
    },
  ) => api.put('/inbox/fields/' + encodeURIComponent(id), input),
  fieldValues: (contact: string, account?: string, case_id?: string) =>
    api.get<Envelope<{ subject_id: string; values: FieldMatch[] }>>(
      fieldPath(contact, account),
      { params: { case_id } },
    ),
  setFieldValue: (
    contact: string,
    account: string | undefined,
    definition: string,
    subject_id: string,
    value: FieldValue,
  ) =>
    api.put(
      fieldPath(contact, account) + '/' + encodeURIComponent(definition),
      { subject_id, value },
    ),
  slaPolicies: () =>
    api.get<Envelope<{ policies: InboxSLAPolicy[] }>>('/inbox/sla/policies'),
  setSlaPolicy: (id: string, policy: InboxSLAPolicy) =>
    api.put('/inbox/sla/policies/' + encodeURIComponent(id), policy),
  deleteSlaPolicy: (id: string) =>
    api.delete('/inbox/sla/policies/' + encodeURIComponent(id)),
  slaReport: (account: string, team: string, offset: number, all = false) =>
    api.get<
      Envelope<{
        cases: QueueCase[]
        has_more: boolean
        summary: { total: number; breached: number; at_risk: number }
        updated_at: string
      }>
    >('/inbox/sla/report', {
      params: {
        account_id: account || undefined,
        team_id: team || undefined,
        offset,
        all,
      },
    }),
  assignmentOptions: (account: string, contact: string) =>
    api.get<
      Envelope<{
        case: InboxCase
        agents: InboxCapacity[]
        can_override: boolean
      }>
    >(conversationPath(account, contact) + '/assignment'),
  assignConversation: (
    account: string,
    contact: string,
    case_id: string,
    version: number,
    assignee_id: string | null,
    override_reason?: string,
  ) =>
    api.post<Envelope<{ case: InboxCase }>>(
      conversationPath(account, contact) + '/assignment',
      { case_id, version, assignee_id, override_reason },
    ),
  resolutionRequirements: (account: string, contact: string) =>
    api.get<
      Envelope<{
        case_id: string
        version: number
        status?: CaseStatus
        missing_fields: MissingInboxField[]
        contact_name?: string
        account_name?: string
      }>
    >(conversationPath(account, contact) + '/resolution'),
  completeAndResolve: (
    account: string,
    contact: string,
    case_id: string,
    version: number,
    values: FieldMatch[],
  ) =>
    api.post<Envelope<{ case: InboxCase }>>(
      conversationPath(account, contact) + '/resolve',
      { case_id, version, values },
    ),
  capacityPresets: () =>
    api.get<Envelope<{ presets: CapacityPreset[] }>>('/inbox/capacity-presets'),
  saveCapacityPreset: (
    input: { name: string; max_active: number },
    id?: string,
  ) =>
    id
      ? api.put('/inbox/capacity-presets/' + encodeURIComponent(id), input)
      : api.post('/inbox/capacity-presets', input),
  deleteCapacityPreset: (id: string) =>
    api.delete('/inbox/capacity-presets/' + encodeURIComponent(id)),
  previewCapacityPreset: (
    id: string,
    input: { team_id: string; account_id: string; agent_ids: string[] },
  ) =>
    api.post<Envelope<{ changes: CapacityChange[]; confirmation: string }>>(
      '/inbox/capacity-presets/' + encodeURIComponent(id) + '/preview',
      input,
    ),
  applyCapacityPreset: (
    id: string,
    input: {
      team_id: string
      account_id: string
      agent_ids: string[]
      confirmation: string
    },
  ) =>
    api.post(
      '/inbox/capacity-presets/' + encodeURIComponent(id) + '/apply',
      input,
    ),
  workload: (team_id?: string, account_id?: string, offset = 0) =>
    api.get<
      Envelope<{
        agents: (InboxCapacity & {
          team_id: string
          team_name: string
          account_id: string
          account_name: string
        })[]
        has_more: boolean
        updated_at: string
        unique_agents: number
      }>
    >('/inbox/workload', {
      params: {
        team_id: team_id || undefined,
        account_id: account_id || undefined,
        offset,
      },
    }),
  slaHistory: (filters: Record<string, string | number | undefined>) =>
    api.get<
      Envelope<{
        results: InboxSLAHistoryRow[]
        summary: {
          total: number
          met: number
          breached: number
          unanswered: number
          legacy: number
        }
        has_more: boolean
        filters: {
          agents: { id: string; name: string }[]
          policy_versions: {
            id: string
            created_at: string
            legacy: boolean
          }[]
        }
      }>
    >('/inbox/sla/history', { params: filters }),
  exportSlaHistory: (filters: Record<string, string | number | undefined>) =>
    api.get<Blob>('/inbox/sla/history', {
      params: { ...filters, format: 'csv' },
      responseType: 'blob',
    }),
  settings: () => api.get<Envelope<{ enabled: boolean }>>('/inbox/workflows'),
  queue: (
    filters: InboxFilters,
    offset = 0,
    include_counts = false,
    view_id?: string,
  ) =>
    api.get<
      Envelope<{
        cases: QueueCase[]
        has_more: boolean
        counts?: Record<string, number>
      }>
    >('/inbox/queue', {
      params: {
        filters: JSON.stringify(filters),
        offset,
        include_counts,
        view_id,
      },
    }),
  views: () =>
    api.get<
      Envelope<{
        views: SavedInboxView[]
        publish_teams?: { id: string; name: string }[]
      }>
    >('/inbox/views'),
  saveView: (name: string, filters: InboxFilters, team_id?: string) =>
    api.post<Envelope<{ view: SavedInboxView }>>('/inbox/views', {
      name,
      filters,
      team_id,
    }),
  updateView: (
    id: string,
    name: string,
    filters: InboxFilters,
    team_id?: string,
  ) =>
    api.put('/inbox/views/' + encodeURIComponent(id), {
      name,
      filters,
      team_id,
    }),
  deleteView: (id: string) =>
    api.delete('/inbox/views/' + encodeURIComponent(id)),
  bulk: (
    action: BulkInboxAction,
    items: { id: string; version: number }[],
    value?: string,
    override_reason?: string,
  ) =>
    api.post<Envelope<{ results: { id: string; result: BulkInboxResult }[] }>>(
      '/inbox/cases/bulk',
      {
        action,
        items,
        tag: action === 'tag' ? value : undefined,
        assignee_id: action === 'assign' ? value : undefined,
        override_reason,
      },
    ),
  setEnabled: (enabled: boolean) =>
    api.put<Envelope<{ enabled: boolean }>>('/inbox/workflows', { enabled }),
  summaries: (contacts: string[]) =>
    api.get<Envelope<{ cases: InboxCase[] }>>('/inbox/cases', {
      params: { contacts: contacts.join(',') },
    }),
  conversation: (
    accountId: string,
    contactId: string,
    case_id?: string,
    activity_before?: string,
  ) =>
    api.get<
      Envelope<{
        case: InboxCase | null
        activity: CaseActivity[]
        current_case_id?: string
        read_only?: boolean
        activity_has_more?: boolean
        activity_next_cursor?: string
      }>
    >(conversationPath(accountId, contactId), {
      params: { case_id, activity_before },
    }),
  update: (accountId: string, contactId: string, input: CaseUpdate) =>
    api.put<Envelope<{ case: InboxCase }>>(
      conversationPath(accountId, contactId),
      input,
    ),
}

export interface InboxMessageVariable {
  key: string
  label: string
  scope: string
  kind: string
  archived?: boolean
}

export type InboxQueue =
  | ''
  | 'mine'
  | 'unassigned'
  | 'awaiting_reply'
  | 'follow_ups'
export type BulkInboxAction = 'assign' | 'tag' | 'resolve'
export type BulkInboxResult =
  | 'updated'
  | 'unavailable'
  | 'stale'
  | 'invalid'
  | 'failed'
  | 'required_fields'
  | 'capacity'

export interface InboxFilters {
  search?: string
  queue?: InboxQueue
  account_id?: string
  team_id?: string
  assignee_id?: string
  status?: CaseStatus
  priority?: CasePriority
  tags?: string[]
  fields?: FieldMatch[]
  match?: 'all' | 'any'
  predicates?: InboxPredicate[]
  message_contains?: string
}

export interface QueueCase extends InboxCase {
  account_name: string
  contact_name: string
  message_preview?: string
  assignee_name?: string
  team_name?: string
}

export interface SavedInboxView {
  id: string
  owner_id: string
  team_id?: string
  name: string
  filters: InboxFilters
  capabilities?: { edit: boolean; remove: boolean; share: boolean }
}
