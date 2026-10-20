import { api } from '@/services/api'
export interface Metadata { document_id: string; version: string; filename: string; media_type: string; audiences: string[]; effective_at: string; withdraw_at?: string }
export interface Page { Number: number; Text: string; Method: string; ExtractorRevision: string }
export interface KnowledgeJob { id: string; actor_id: string; status: string; version: number; metadata: Metadata; manifest_hash?: string; pages?: Page[]; failure?: string }
export interface Source { Reference: string; DocumentID: string; Version: string; ContentHash: string; ManifestHash: string; Page: Page }
export async function loadJobs() { return (await api.get<{data: KnowledgeJob[]}>('/extensions/knowledge/jobs')).data.data }
export async function loadJob(id: string) { return (await api.get<{data: KnowledgeJob}>(`/extensions/knowledge/jobs/${encodeURIComponent(id)}`)).data.data }
export async function uploadKnowledge(file: File, metadata: Metadata) { const body = new FormData(); body.append('metadata', JSON.stringify(metadata)); body.append('file', file); return (await api.post<{data: KnowledgeJob}>('/extensions/knowledge/uploads', body, { headers: { 'Content-Type': undefined } })).data.data }
export async function transition(job: KnowledgeJob, action: 'approve' | 'publish' | 'retire') { return (await api.post<{data: KnowledgeJob}>(`/extensions/knowledge/jobs/${encodeURIComponent(job.id)}/${action}`, { expected_version: job.version })).data.data }
export async function searchKnowledge(audience: string, q: string) { return (await api.get<{data: Source[]}>('/extensions/knowledge/search', { params: { audience, q } })).data.data }
export async function downloadSource(source: Source, audience: string) { const response = await api.get<Blob>('/extensions/knowledge/download', { params: { audience, document_id: source.DocumentID, version: source.Version }, responseType: 'blob' }); const url = URL.createObjectURL(response.data); const a = document.createElement('a'); a.href = url; a.download = 'source-document'; a.click(); URL.revokeObjectURL(url) }
