import { api } from '@/services/api'
import type { FieldValue, InboxWorkflowPreview } from '@/services/inbox'

export interface IntegrationInput {
  name: string
  kind: 'text' | 'number' | 'boolean' | 'date'
  required: boolean
}
export interface IntegrationDefinition {
  id: string
  name: string
  description: string
  endpoint_key: string
  auth_mode: 'none' | 'bearer' | 'api_key'
  has_credential: boolean
  enabled: boolean
  inputs: IntegrationInput[]
  outputs: { name: string; definition_id: string }[]
  reference_key: string
  updated_at: string
}
export type IntegrationDefinitionInput = Omit<
  IntegrationDefinition,
  'id' | 'has_credential' | 'updated_at'
> & { credential?: string }
export type IntegrationCatalogueItem = Pick<
  IntegrationDefinition,
  'id' | 'name' | 'description' | 'enabled' | 'inputs'
>
export interface IntegrationEndpoint {
  key: string
  name: string
  method: string
  destination: string
  supports_idempotency: boolean
}
export interface IntegrationIntent {
  id: string
  name: string
  destination: string
  method: string
  inputs: Record<string, FieldValue>
  deferred_inputs?: Record<string, string[]>
  outputs: { id: string; label: string; scope: string; key: string }[]
  available: boolean
  reason?: string
}
export interface IntegrationExecution {
  id: string
  run_id: string
  action_index: number
  integration_id: string
  case_id: string
  account_id: string
  actor_id: string
  state: string
  outcome?: string
  attempt_count: number
  created_at: string
  started_at?: string
  completed_at?: string
  outputs_applied_at?: string
  outputs_discarded_at?: string
  result_origin?: 'provider' | 'manual'
  retry_until?: string
  provider_status?: number
  duration_ms?: number
}
export interface IntegrationRecovery {
  id: string
  execution_id: string
  run_id?: string
  actor_id: string
  kind: string
  from_state: string
  from_outcome?: string
  from_result_origin?: string
  prior_started_at?: string
  prior_completed_at?: string
  provider_status?: number
  duration_ms?: number
  attempt_count: number
  reason: string
  has_evidence: boolean
  created_at: string
}
export type IntegrationRecoveryAction =
  | 'confirm_succeeded'
  | 'confirm_not_applied'
  | 'apply_outputs'
  | 'discard_outputs'
export interface IntegrationRecoveryInput {
  action: IntegrationRecoveryAction
  evidence_reference?: string
  result?: Record<string, Exclude<FieldValue, null>>
}
export interface IntegrationRecoveryPreview {
  execution_id: string
  eligible: boolean
  confirmation: string
  reason?: string
  field_changes: {
    id: string
    label: string
    scope: string
    kind: string
    current: FieldValue
    proposed: FieldValue
  }[]
  evidence_reference?: string
  result_reference?: FieldValue
  manual_attestation: boolean
  fact_only: boolean
  remaining_steps_require_review: boolean
}
type Envelope<T> = { data: T }
const definitionPath = (id: string) =>
  '/inbox/integrations/' + encodeURIComponent(id)
const executionPath = (id: string) =>
  '/inbox/integration-executions/' + encodeURIComponent(id)
const runPath = (id: string) => '/inbox/workflow-runs/' + encodeURIComponent(id)

export const inboxIntegrations = {
  list: () =>
    api.get<
      Envelope<{
        integrations: IntegrationDefinition[]
        endpoints: IntegrationEndpoint[]
      }>
    >('/inbox/integrations'),
  catalogue: () =>
    api.get<Envelope<{ integrations: IntegrationCatalogueItem[] }>>(
      '/inbox/integration-catalogue',
    ),
  create: (input: IntegrationDefinitionInput) =>
    api.post<Envelope<{ integration: IntegrationDefinition }>>(
      '/inbox/integrations',
      input,
    ),
  update: (id: string, input: IntegrationDefinitionInput) =>
    api.put<Envelope<{ integration: IntegrationDefinition }>>(
      definitionPath(id),
      input,
    ),
  remove: (id: string) => api.delete(definitionPath(id)),
  settings: () =>
    api.get<Envelope<{ enabled: boolean; dispatch_available: boolean }>>(
      '/inbox/integration-settings',
    ),
  setEnabled: (enabled: boolean) =>
    api.put<Envelope<{ enabled: boolean }>>('/inbox/integration-settings', {
      enabled,
    }),
  executions: (filters: {
    run_id?: string
    case_id?: string
    state?: string
    cursor?: string
  }) =>
    api.get<
      Envelope<{
        executions: IntegrationExecution[]
        has_more: boolean
        next_cursor: string
      }>
    >('/inbox/integration-executions', { params: filters }),
  recoveries: (id: string, cursor?: string) =>
    api.get<
      Envelope<{
        recoveries: IntegrationRecovery[]
        has_more: boolean
        next_cursor: string
      }>
    >(executionPath(id) + '/recoveries', { params: { cursor } }),
  recoveryEvidence: (id: string, recovery: string) =>
    api.get<
      Envelope<{
        evidence_reference: string
        source: string
        recorded_by: string
      }>
    >(
      executionPath(id) +
        '/recoveries/' +
        encodeURIComponent(recovery) +
        '/evidence',
    ),
  retryPreview: (id: string) =>
    api.post<
      Envelope<{
        eligible: boolean
        reason?: string
        confirmation: string
        retry_until?: string
        intent: IntegrationIntent
        same_operation: boolean
      }>
    >(executionPath(id) + '/retry-preview'),
  retry: (
    id: string,
    input: { confirmation: string; reason: string; operation_id: string },
  ) => api.post(executionPath(id) + '/retry', input),
  recoveryPreview: (id: string, input: IntegrationRecoveryInput) =>
    api.post<Envelope<IntegrationRecoveryPreview>>(
      executionPath(id) + '/recovery-preview',
      input,
    ),
  recover: (
    id: string,
    input: IntegrationRecoveryInput & {
      confirmation: string
      reason: string
      operation_id: string
    },
  ) => api.post(executionPath(id) + '/recover', input),
  sequencePreview: (id: string) =>
    api.post<
      Envelope<
        InboxWorkflowPreview & {
          run_id: string
          next_action: number
          send_will_be_skipped: boolean
        }
      >
    >(runPath(id) + '/preview'),
  resume: (id: string, confirmation: string, reason: string) =>
    api.post(runPath(id) + '/resume', { confirmation, reason }),
  cancelPreview: (id: string) =>
    api.post<
      Envelope<{
        eligible: boolean
        reason?: string
        confirmation: string
        provider_outcome_unresolved: boolean
        submission_in_progress: boolean
      }>
    >(runPath(id) + '/cancel-preview'),
  cancel: (
    id: string,
    input: { confirmation: string; reason: string; operation_id: string },
  ) => api.post(runPath(id) + '/cancel', input),
  runRecoveries: (id: string, cursor?: string) =>
    api.get<
      Envelope<{
        recoveries: IntegrationRecovery[]
        has_more: boolean
        next_cursor: string
      }>
    >(runPath(id) + '/recoveries', { params: { cursor } }),
}
