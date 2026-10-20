// @vitest-environment happy-dom
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
const { get } = vi.hoisted(()=>({get:vi.fn()}))
vi.mock('@/services/api',()=>({api:{get,post:vi.fn()}}))
import { useAuthStore, type User } from '@/stores/auth'
import { clearExtensionSession, extensionSession } from './session'
const user=(id='operator'):User=>({id,email:`${id}@synthetic.invalid`,full_name:id,organization_id:'org'})
const catalog={modules:[{id:'reports',api_version:1}],permissions:['reports:operational'],grants:[],mfa_verified:true}
describe('catalog bootstrap after access-cookie refresh',()=>{
 beforeEach(()=>{setActivePinia(createPinia());localStorage.clear();get.mockReset();clearExtensionSession('')})
 it('requests a fresh catalog when interceptor invalidation discarded the retried response',async()=>{
  const auth=useAuthStore();auth.setAuth({user:user()})
  get.mockImplementationOnce(async()=>{clearExtensionSession();return {data:{data:catalog}}}).mockResolvedValueOnce({data:{data:catalog}})
  expect(await auth.refreshExtensionAccess()).toBe(true)
  expect(get).toHaveBeenCalledTimes(2)
  expect(extensionSession.ready).toBe(true)
 })
 it('never retries under a changed operator identity',async()=>{
  const auth=useAuthStore();auth.setAuth({user:user()})
  get.mockImplementationOnce(async()=>{auth.setAuth({user:user('other')});return {data:{data:catalog}}})
  expect(await auth.refreshExtensionAccess()).toBe(false)
  expect(get).toHaveBeenCalledTimes(1)
  expect(extensionSession.ready).toBe(false)
 })
 it('bounds failed revalidation and leaves protected data unavailable',async()=>{
  const auth=useAuthStore();auth.setAuth({user:user()})
  get.mockImplementation(async()=>{clearExtensionSession();throw new Error('Access denied')})
  expect(await auth.refreshExtensionAccess()).toBe(false)
  expect(get.mock.calls.length).toBeLessThanOrEqual(2)
  expect(extensionSession.ready).toBe(false)
 })
})
