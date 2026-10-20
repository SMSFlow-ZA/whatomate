import { describe, expect, it, vi } from 'vitest'
const post = vi.fn()
vi.mock('@/services/api', () => ({ api: { post, get: vi.fn() } }))
describe('directory approval client', () => {
 it('binds approval to the reviewed digest and original expected version', async () => {
  const { approveDirectoryPreview } = await import('./api')
  post.mockResolvedValueOnce({ data: { data: { version: 8 } } })
  await approveDirectoryPreview({ preview_id: 'p', digest: 'exact', expected_version: 7 })
  expect(post).toHaveBeenCalledWith('/extensions/directory/approve', { preview_id: 'p', digest: 'exact', expected_version: 7 })
 })
})
it('uploads the original workbook with explicit mapping and version', async () => {
 const { uploadDirectoryWorkbook } = await import('./api')
 post.mockResolvedValueOnce({ data: { data: { preview_id: 'upload' } } })
 const file = new File(['bytes'], 'roster.xlsx')
 const metadata = { expected_version: 3, worksheet: 'xl/worksheets/sheet1.xml', mapping: { employee_id: 0, phone: 1, active: 2, zone: -1, department: -1 }, skip_header: true }
 await uploadDirectoryWorkbook(file, metadata)
 const args = post.mock.calls.at(-1)!
 expect(args[0]).toBe('/extensions/directory/preview-file')
 expect((args[1] as FormData).get('metadata')).toBe(JSON.stringify(metadata))
 expect((args[1] as FormData).get('file')).toBeInstanceOf(File)
 expect(args[2]).toEqual({ headers: { 'Content-Type': undefined } })
})
