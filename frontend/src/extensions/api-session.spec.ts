import { beforeEach, expect, it, vi } from 'vitest'
import { AxiosError } from 'axios'
import { clearExtensionSession, extensionSession, refreshExtensionSession } from './session'
let api:typeof import('@/services/api').api
const catalog=()=>({mfa_verified:true,modules:[{id:'cases',api_version:1}],permissions:['cases:read'],grants:[]})
beforeEach(async()=>{
 vi.stubGlobal('window',{__BASE_PATH__:'',dispatchEvent:vi.fn(),location:{href:''}})
 vi.stubGlobal('document',{cookie:''})
 vi.stubGlobal('localStorage',{getItem:()=>null,removeItem:vi.fn()})
 vi.stubGlobal('navigator',{})
 api=(await import('@/services/api')).api
 clearExtensionSession('org:user')
 await refreshExtensionSession(async()=>catalog())
})
it.each(['/extensions/cases','/extensions/security/mfa'])('drops a protected response which completes after session invalidation: %s',async url=>{
 let finish!:()=>void
 api.defaults.adapter=config=>new Promise(resolve=>{finish=()=>resolve({data:{marker:'protected'},status:200,statusText:'OK',headers:{},config})})
 const request=api.get(url)
 await Promise.resolve();await Promise.resolve()
 clearExtensionSession('other:user')
 finish()
 await expect(request).rejects.toThrow('Extension session changed')
})
it('does not clear the session for unknown or forbidden case IDs represented as 404',async()=>{
 api.defaults.adapter=config=>Promise.reject(new AxiosError('not found','ERR_BAD_REQUEST',config,undefined,{status:404,statusText:'Not Found',headers:{},data:{},config}))
 await expect(api.get('/extensions/cases/unknown')).rejects.toThrow()
 expect(extensionSession.ready).toBe(true)
})
it('clears visible extension authority on 403 without logging out the ordinary host',async()=>{
 api.defaults.adapter=config=>Promise.reject(new AxiosError('denied','ERR_BAD_REQUEST',config,undefined,{status:403,statusText:'Forbidden',headers:{},data:{},config}))
 await expect(api.get('/extensions/cases')).rejects.toThrow()
 expect(extensionSession.ready).toBe(false)
 expect(window.location.href).toBe('')
})

it('does not restamp a retried request after the account generation changes',async()=>{
 const previous=extensionSession.revision
 clearExtensionSession('other:user')
 api.defaults.adapter=config=>Promise.resolve({data:{marker:'protected'},status:200,statusText:'OK',headers:{},config})
 const request={_extensionRevision:previous} as Parameters<typeof api.get>[1]
 await expect(api.get('/extensions/cases',request)).rejects.toThrow('Extension session changed')
})
