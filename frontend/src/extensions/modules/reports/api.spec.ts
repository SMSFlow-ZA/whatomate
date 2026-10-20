import { describe, expect, it, vi } from 'vitest'
const {get}=vi.hoisted(()=>({get:vi.fn()}))
vi.mock('@/services/api',()=>({api:{get}}))
import { loadReport, downloadReport, monthWindow, loadTrend, downloadTrend } from './api'
describe('reporting client',()=>{
 it('sends filters only for operational reports',async()=>{
  get.mockResolvedValue({data:{data:{report:{},metadata:{}}}})
  await loadReport('board',{start:'2026-09-01',end:'2026-10-01',zone:'sensitive'})
  expect(get).toHaveBeenLastCalledWith('/extensions/reports/board',{params:{start:'2026-09-01',end:'2026-10-01'}})
  await loadReport('operational',{start:'2026-09-01',end:'2026-10-01',zone:'scope'})
  expect(get).toHaveBeenLastCalledWith('/extensions/reports/operational',{params:{start:'2026-09-01',end:'2026-10-01',zone:'scope'}})
 })
 it('downloads only through authenticated controlled export',async()=>{
  const blob=new Blob(['synthetic']);get.mockResolvedValueOnce({data:blob})
  expect(await downloadReport('board',{start:'2026-09-01',end:'2026-10-01',department:'tiny'})).toBe(blob)
  expect(get).toHaveBeenLastCalledWith('/extensions/reports/export',{params:{mode:'board',start:'2026-09-01',end:'2026-10-01'},responseType:'blob'})
 })
 it('uses exact UTC month boundaries across year rollover',()=>{
  expect(monthWindow('2025-12')).toEqual({start:'2025-12-01',end:'2026-01-01'})
  expect(()=>monthWindow('2026-13')).toThrow()
 })
})

describe('operational trend client',()=>{
 it('loads actual period series with exact dimensions and bounded length',async()=>{
  get.mockResolvedValueOnce({data:{data:{periods:[]}}})
  await loadTrend({end_month:'2026-10',months:6,dimension:'zone',department:'Operations'})
  expect(get).toHaveBeenLastCalledWith('/extensions/reports/trend',{params:{end_month:'2026-10',months:6,dimension:'zone',department:'Operations'}})
 })
 it('exports series through the current authenticated export route',async()=>{
  const blob=new Blob(['trend']);get.mockResolvedValueOnce({data:blob})
  expect(await downloadTrend({end_month:'2026-10',months:6,dimension:'department'})).toBe(blob)
  expect(get).toHaveBeenLastCalledWith('/extensions/reports/export',{params:{mode:'trend',end_month:'2026-10',months:6,dimension:'department'},responseType:'blob'})
 })
})
