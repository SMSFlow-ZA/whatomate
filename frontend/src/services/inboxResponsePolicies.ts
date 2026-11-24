import { api } from './api'
import type { InboxFilters, InboxSLAPolicy } from './inbox'

export type ResponsePolicyConditions = Pick<
  InboxFilters,
  'team_id' | 'match' | 'predicates'
>
export interface ResponsePolicyInput {
  revision?: number
  name: string
  description: string
  enabled: boolean
  conditions: ResponsePolicyConditions
  targets: InboxSLAPolicy
}
export interface ResponsePolicy {
  id: string
  account_id: string
  version_id: string
  position: number
  revision: number
  enabled: boolean
  definition: {
    policy_id: string
    name: string
    description: string
    conditions: ResponsePolicyConditions
  }
  targets: InboxSLAPolicy
}
export interface ResponsePolicyPreview {
  case_id: string
  account_id: string
  existing_policy_version: string | null
  prospective_only: true
  default_policy: InboxSLAPolicy | null
  evaluations: {
    policy_id: string
    version_id: string
    name: string
    position: number
    enabled: boolean
    matched: boolean
    selected: boolean
    reason:
      | 'disabled'
      | 'conditions_not_matched'
      | 'earlier_policy_selected'
      | 'first_matching_policy'
    conditions: {
      field: string
      definition_id?: string
      label?: string
      matched: boolean
    }[]
  }[]
}
type Envelope<T> = { data: T }
type PolicyMetadata = Pick<
  ResponsePolicy,
  'id' | 'account_id' | 'version_id' | 'position' | 'revision' | 'enabled'
>
const root = (account: string) =>
  '/inbox/response-policies/' + encodeURIComponent(account)
const path = (account: string, id: string) =>
  root(account) + '/' + encodeURIComponent(id)
export const responsePolicyService = {
  list: (account: string) =>
    api.get<Envelope<{ policies: ResponsePolicy[]; maximum: number }>>(
      root(account),
    ),
  create: (account: string, input: ResponsePolicyInput) =>
    api.post<Envelope<{ policy: PolicyMetadata }>>(root(account), input),
  update: (account: string, id: string, input: ResponsePolicyInput) =>
    api.put<Envelope<{ policy: PolicyMetadata }>>(path(account, id), input),
  clone: (account: string, row: ResponsePolicy) =>
    api.post<Envelope<{ policy: PolicyMetadata }>>(
      path(account, row.id) + '/clone',
      { revision: row.revision },
    ),
  archive: (account: string, row: ResponsePolicy) =>
    api.delete(path(account, row.id), { data: { revision: row.revision } }),
  reorder: (account: string, rows: ResponsePolicy[]) =>
    api.put(root(account) + '/order', {
      order: rows.map(({ id, revision }) => ({ id, revision })),
    }),
  preview: (account: string, case_id: string) =>
    api.post<Envelope<ResponsePolicyPreview>>(root(account) + '/preview', {
      case_id,
    }),
}
