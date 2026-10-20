import { api } from '@/services/api'
export type CaseState = 'Draft' | 'Submitted' | 'Assigned' | 'InProgress' | 'AwaitingInfo' | 'Escalated' | 'Resolved' | 'Closed'
export interface CaseRecord { id: string; state: CaseState; version: number; unit_id?: string; narrative?: string; category?: string; snapshot?: { zone: string; department: string; case_type: string }; assignments?: { id: string; role: string }[]; participants?: { id: string; role: string }[]; events?: { version: number; kind: string; actor_id: string; reason: string; from: CaseState; to: CaseState; category_from?: string; category_to?: string; occurred_at: string; recorded_at: string }[] }
export interface Catalog { permissions: string[]; grants: {permission: string; unit_id?: string}[] }
export async function loadCaseAccess() { return (await api.get<{data: Catalog}>('/extensions/catalog')).data.data }
export async function listCases(q = '') { return (await api.get<{data: CaseRecord[]}>('/extensions/cases/', { params: { q } })).data.data }
export async function readCase(id: string) { return (await api.get<{data: CaseRecord}>(`/extensions/cases/${encodeURIComponent(id)}`)).data.data }
export async function createCase(input: {key: string; unit_id: string; narrative: string; snapshot?: {zone: string; department: string; case_type: string}; draft: boolean}) { return (await api.post<{data: {id: string}}>('/extensions/cases/', input)).data.data }
export async function caseAction(c: CaseRecord, action: 'draft' | 'state' | 'assignment' | 'category' | 'participants', input: Record<string, unknown>) {
 const url = `/extensions/cases/${encodeURIComponent(c.id)}/${action}`
 const payload = { ...input, expected_version: c.version }
 return (await (action === 'draft' ? api.put<{data: CaseRecord}>(url,payload) : api.post<{data: CaseRecord}>(url,payload))).data.data
}
