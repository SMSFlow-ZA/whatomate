import { expect, it, vi } from 'vitest'
vi.mock('@/services/api',()=>({api:{}}))
import { createPinia, setActivePinia } from 'pinia'
import { useAuthStore } from '@/stores/auth'
import { refreshExtensionSession, clearExtensionSession } from './session'
import { createCompiledCatalog } from './compiledCatalog'
it('omits every page by default and statically enables only declared build modules',()=>{
 expect(createCompiledCatalog('').discover(()=>true).routes).toHaveLength(0)
 const visible=createCompiledCatalog('cases,directory').discover(p=>p==='cases:read')
 expect(visible.routes.map(r=>r.path)).toEqual(['/extensions/cases/workspace'])
 expect(createCompiledCatalog('directory,cases,knowledge,ai,conversation,reports').discover(()=>true).routes).toHaveLength(7)
 expect(()=>createCompiledCatalog('runtime-url')).toThrow(/unknown/)
})

it('admits own-case employees, triage operators and content reviewers through their concrete current grants',async()=>{
 vi.stubGlobal('localStorage',{setItem:vi.fn(),getItem:vi.fn(),removeItem:vi.fn()})
 for(const permission of ['cases:read.own','cases:read.triage','cases:work.assigned','knowledge:upload','knowledge:review','knowledge:publish','knowledge:retire']) {
  setActivePinia(createPinia())
  const auth=useAuthStore(), [resource,action]=permission.split(':') as [string,string]
  auth.setAuth({user:{id:'u',email:'u@example.test',full_name:'User',organization_id:'org',is_super_admin:true,role:{id:'role',name:'admin',is_system:true,permissions:[{id:'p',resource,action}]}}})
  const registry=createCompiledCatalog('cases,knowledge')
  const discover=()=>registry.discover(p=>auth.hasExtensionPermission(p))
  expect(discover().routes).toHaveLength(0)
  await refreshExtensionSession(async()=>({mfa_verified:true,modules:[{id:resource,api_version:1}],permissions:[permission],grants:[]}))
  const visible=discover()
  expect(visible.routes).toHaveLength(1)
  expect(visible.routes[0].meta?.permission).toBe(permission)
  expect(visible.navigation[0].permission).toBe(permission)
  const enter=visible.routes[0].beforeEnter as ()=>boolean
  expect(enter()).toBe(true)
  clearExtensionSession()
  expect(enter()).toBe(false)
  expect(discover().navigation).toHaveLength(0)
 }
})
it('does not treat a route alternative as a newly granted permission',()=>{
 expect(createCompiledCatalog('cases,knowledge').discover(()=>false).routes).toHaveLength(0)
 expect(createCompiledCatalog('cases,knowledge').discover(p=>p==='cases:admin').routes).toHaveLength(0)
})
