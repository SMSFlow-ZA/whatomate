import { describe, expect, it, vi } from 'vitest'
const {post, put, get} = vi.hoisted(() => ({post:vi.fn(),put:vi.fn(),get:vi.fn()}))
vi.mock('@/services/api', () => ({api:{post,put,get}}))
import { caseAction, createCase, listCases } from './api'
describe('protected cases client', () => {
 it('uses current aggregate version for state changes',async()=>{
  post.mockResolvedValueOnce({data:{data:{id:'case',version:8,state:'Resolved'}}})
  await caseAction({id:'case',version:7,state:'InProgress'},'state',{state:'Resolved',expected_version:99})
  expect(post).toHaveBeenCalledWith('/extensions/cases/case/state',{state:'Resolved',expected_version:7})
 })
 it('uses PUT for drafts and encodes opaque IDs',async()=>{
  put.mockResolvedValueOnce({data:{data:{}}})
  await caseAction({id:'case/id',version:3,state:'Draft'},'draft',{narrative:'synthetic'})
  expect(put).toHaveBeenCalledWith('/extensions/cases/case%2Fid/draft',{narrative:'synthetic',expected_version:3})
 })
 it('keeps the provided intake key and rejects uncertain writes',async()=>{
  const input={key:'unchanged',unit_id:'u',narrative:'synthetic',snapshot:{zone:'z',department:'d',case_type:'c'},draft:false}
  post.mockRejectedValueOnce(new Error('commit unknown'))
  await expect(createCase(input)).rejects.toThrow('commit unknown')
  post.mockResolvedValueOnce({data:{data:{id:'durable'}}})
  expect(await createCase(input)).toEqual({id:'durable'})
  expect(post.mock.calls.at(-1)?.[1]).toEqual(input)
  expect(post.mock.calls.at(-1)?.[0]).toBe('/extensions/cases/')
 })
 it('uses server-side filtered search',async()=>{
  get.mockResolvedValueOnce({data:{data:[]}});await listCases('Submitted')
  expect(get).toHaveBeenCalledWith('/extensions/cases/',{params:{q:'Submitted'}})
 })
})
