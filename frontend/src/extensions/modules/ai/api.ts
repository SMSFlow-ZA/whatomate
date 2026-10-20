import { api } from '@/services/api'
export interface Source { Reference: string; DocumentID: string; Version: string; ContentHash: string; ManifestHash: string; Page: { Number: number; Text: string } }
export interface Assistance { id: string; case_id: string; audience: string; kind: string; version: number; status: string; correction?: string; result: { Text: string; Citations: Source[] | null; Handoff: boolean }; classification?: { Label: string; Rationale: string } }
export async function requestAssistance(caseID: string, audience: string, question: string, kind: 'answers' | 'suggestions') { return (await api.post<{data: Assistance}>(`/extensions/ai/${kind}`, {case_id: caseID, audience, question}, {timeout: 120000})).data.data }
export async function readAssistance(id: string) { return (await api.get<{data: Assistance}>(`/extensions/ai/records/${encodeURIComponent(id)}`)).data.data }
export async function reviewAssistance(record: Assistance, action: 'correct' | 'handoff', reason: string) { return (await api.post<{data: Assistance}>(`/extensions/ai/records/${encodeURIComponent(record.id)}/${action}`, {expected_version: record.version, reason})).data.data }
