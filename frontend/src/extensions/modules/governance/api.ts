import { api } from '@/services/api'
export interface InventoryRecord { ID: string; SubjectID: string; LineageID: string; Source: string; Reference: string; Generation: number; CreatedAt: string; RetainUntil: string }
export interface JournalEntry { ID: string; RecordID: string; State: string; Version: number; Generation: number; ConfirmationReference: string }
export async function inventory(unit: string) { return (await api.get<{ data: InventoryRecord[] }>('/extensions/governance/inventory', { params: { unit_id: unit } })).data.data ?? [] }
export async function journal(unit: string, request: string) { return (await api.get<{ data: JournalEntry[] }>('/extensions/governance/journal', { params: { unit_id: unit, request_id: request } })).data.data ?? [] }
export async function action<T = unknown>(name: string, payload: Record<string, unknown>) { return (await api.post<{ data: T }>(`/extensions/governance/${name}`, payload)).data.data }
export async function exportSubject(unit: string, subject: string) { return (await api.get<{ data: unknown }>('/extensions/governance/export', { params: { unit_id: unit, subject_id: subject } })).data.data }
