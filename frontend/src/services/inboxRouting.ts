import { api } from './api'

export type RoutingStrategy =
  | 'inherit'
  | 'manual'
  | 'round_robin'
  | 'load_balanced'
export interface RoutingPolicyInput {
  revision: number
  name: string
  description: string
  strategy: RoutingStrategy
  max_active: number | null
  max_assignments: number
  window_seconds: number
}
export interface RoutingPolicy extends RoutingPolicyInput {
  id: string
  archived_at?: string
}
export interface RoutingConfiguration {
  account_id: string
  team_id: string
  revision: number
  policy_id: string
  policy_revision: number
  name: string
  strategy: RoutingStrategy
  applied_at: string
}
export interface RoutingApplication {
  account_id: string
  team_id: string
  agent_ids: string[]
  revision: number
  expires_at?: string
  confirmation?: string
}
export interface RoutingChange {
  agent_id: string
  name: string
  before_active: number
  after_active: number
  before_assignments: number
  after_assignments: number
  before_window_seconds: number
  after_window_seconds: number
}
export interface RoutingReview {
  account_id: string
  account_name: string
  team_id: string
  team_name: string
  policy: RoutingPolicy
  configuration: RoutingConfiguration | null
  changes: RoutingChange[]
  expires_at: string
  confirmation: string
  limits_shared_across_teams: boolean
  before_strategy: RoutingStrategy | null
  effective_strategy: RoutingStrategy
}
interface Envelope<T> {
  data: T
}
const policyPath = (id: string) =>
  '/inbox/routing-policies/' + encodeURIComponent(id)
export const inboxRouting = {
  list: () =>
    api.get<
      Envelope<{
        policies: RoutingPolicy[]
        has_more: boolean
        can_write: boolean
      }>
    >('/inbox/routing-policies'),
  configuration: (team: string, account: string) =>
    api.get<
      Envelope<{
        configuration: RoutingConfiguration | null
        account_name: string
        team_name: string
        team_strategy: RoutingStrategy
        effective_strategy: RoutingStrategy
        can_write: boolean
      }>
    >(
      '/inbox/teams/' +
        encodeURIComponent(team) +
        '/routing/' +
        encodeURIComponent(account),
    ),
  create: (input: RoutingPolicyInput) =>
    api.post<Envelope<{ policy: RoutingPolicy }>>(
      '/inbox/routing-policies',
      input,
    ),
  update: (id: string, input: RoutingPolicyInput) =>
    api.put<Envelope<{ policy: RoutingPolicy }>>(policyPath(id), input),
  archive: (id: string, revision: number) =>
    api.put(policyPath(id), { revision, archive: true }),
  preview: (id: string, input: RoutingApplication) =>
    api.post<Envelope<RoutingReview>>(policyPath(id) + '/preview', input),
  apply: (id: string, input: RoutingApplication) =>
    api.post<
      Envelope<{
        applied: boolean
        already_applied: boolean
        refresh_current_limits: boolean
        configuration: RoutingConfiguration
      }>
    >(policyPath(id) + '/apply', input),
}
