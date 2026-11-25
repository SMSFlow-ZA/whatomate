import { api } from './api'

export interface IntakeInput {
  input: string
  definition_id: string
  required: boolean
}
export interface IntakeMapping {
  id: string
  revision: number
  name: string
  description: string
  consent_key: string
  fields: IntakeInput[]
  enabled: boolean
  unavailable_fields?: string[]
}
export type IntakeMappingInput = Omit<
  IntakeMapping,
  'id' | 'unavailable_fields'
>
export interface IntakeSummary {
  id: string
  state: string
  issued_at: string
  contact_fields_hidden: boolean
  outcomes: {
    definition_id: string
    label: string
    scope: 'case' | 'contact'
    status: string
  }[]
}
export const inboxIntake = {
  disable: (id: string, revision: number) =>
    api.post('/inbox/intake-mappings/' + encodeURIComponent(id) + '/disable', {
      revision,
    }),
  summary: (account: string, contact: string, caseId: string) =>
    api.get<{
      data: { case_id: string; summaries: IntakeSummary[]; has_more: boolean }
    }>(
      '/inbox/conversations/' +
        encodeURIComponent(account) +
        '/' +
        encodeURIComponent(contact) +
        '/intake',
      { params: { case_id: caseId } },
    ),
  list: () =>
    api.get<{ data: { mappings: IntakeMapping[]; has_more: boolean } }>(
      '/inbox/intake-mappings',
    ),
  create: (input: IntakeMappingInput) =>
    api.post('/inbox/intake-mappings', input),
  update: (id: string, input: IntakeMappingInput) =>
    api.put('/inbox/intake-mappings/' + encodeURIComponent(id), input),
}
