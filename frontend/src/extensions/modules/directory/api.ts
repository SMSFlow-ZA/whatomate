import { api } from '@/services/api'
export interface DirectoryEmployee { id: string; phone: string; zone: string; department: string; active: boolean; revoked?: boolean }
export interface DirectoryStatus { version: number; imported_at: string; fresh: boolean; total: number; active: number; revoked: number; freshness_seconds: number; employees: DirectoryEmployee[] }
export interface DirectoryPreview { source_sha256?: string; preview_id: string; digest: string; expected_version: number; employees: DirectoryEmployee[]; created_at: string; created_by: string; approved_at?: string; approved_by?: string }
export async function loadDirectory() { return (await api.get<{data: DirectoryStatus}>('/extensions/directory/roster')).data.data }
export async function createDirectoryPreview(employees: unknown, expected_version: number) { return (await api.post<{data: DirectoryPreview}>('/extensions/directory/preview', { employees, expected_version })).data.data }
export async function reviewDirectoryPreview(id: string) { return (await api.get<{data: DirectoryPreview}>(`/extensions/directory/preview/${encodeURIComponent(id)}`)).data.data }
export async function approveDirectoryPreview(preview: Pick<DirectoryPreview, 'preview_id' | 'digest' | 'expected_version'>) {
 return (await api.post('/extensions/directory/approve', { preview_id: preview.preview_id, digest: preview.digest, expected_version: preview.expected_version })).data.data
}
export async function revokeDirectoryEmployee(employee_id: string, expected_version: number) { return (await api.post('/extensions/directory/revoke', { employee_id, expected_version })).data.data }
export interface WorkbookMapping { employee_id: number; phone: number; active: number; zone: number; department: number }
export async function uploadDirectoryWorkbook(file: File, metadata: { expected_version: number; worksheet: string; mapping: WorkbookMapping; skip_header: boolean }) {
 const body = new FormData()
 body.append('metadata', JSON.stringify(metadata))
 body.append('file', file)
 return (await api.post<{data: DirectoryPreview}>('/extensions/directory/preview-file', body, { headers: { 'Content-Type': undefined } })).data.data
}
