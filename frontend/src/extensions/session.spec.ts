import { beforeEach, expect, it } from 'vitest'
import { clearExtensionSession, extensionSession, extensionPermission, extensionScopes, refreshExtensionSession } from './session'
const catalog = () => ({mfa_verified:true,modules:[{id:'cases',api_version:1}],permissions:['cases:read'],grants:[{permission:'cases:read',unit_id:'east'}]})
beforeEach(()=>clearExtensionSession('org:user'))
it('requires a current MFA catalog and exact module permission; never wildcard or admin inference',async()=>{
 expect(extensionPermission('cases:read')).toBe(false)
 expect(await refreshExtensionSession(async()=>catalog())).toBe(true)
 expect(extensionPermission('cases:read')).toBe(true)
 expect(extensionPermission('cases:write')).toBe(false)
 expect(extensionPermission('cases:*')).toBe(false)
 const scopes=extensionScopes('cases:read')
 expect(scopes).toEqual([{permission:'cases:read',unit_id:'east'}])
 expect(Object.isFrozen(scopes[0])).toBe(true)
 clearExtensionSession()
 expect(extensionPermission('cases:read')).toBe(false)
 expect(extensionScopes('cases:read')).toEqual([])
})
it('rejects omitted backend modules, bad MFA and untrusted scopes',async()=>{
 for(const value of [{...catalog(),modules:[]},{...catalog(),mfa_verified:false},{...catalog(),grants:[{permission:'cases:read',unit_id:'*'}]}]) {
  await refreshExtensionSession(async()=>value)
  expect(extensionPermission('cases:read')).toBe(false)
 }
})
it('cannot resurrect a logged out or switched account through an old response',async()=>{
 let finish!:(value:unknown)=>void
 const pending=refreshExtensionSession(()=>new Promise(resolve=>finish=resolve))
 clearExtensionSession('other:user')
 finish(catalog())
 expect(await pending).toBe(false)
 expect(extensionSession.ready).toBe(false)
 expect(extensionSession.identity).toBe('other:user')
})
it('deduplicates same-generation checks but allows a new account request without waiting',async()=>{
 let finish!:(value:unknown)=>void
 const pending=refreshExtensionSession(()=>new Promise(resolve=>finish=resolve))
 expect(refreshExtensionSession(async()=>catalog())).toBe(pending)
 clearExtensionSession('other:user')
 await refreshExtensionSession(async()=>({...catalog(),permissions:[],grants:[]}))
 finish(catalog());await pending
 expect(extensionPermission('cases:read')).toBe(false)
 expect(extensionSession.ready).toBe(true)
})
it('revocation or refresh failure immediately clears all scopes',async()=>{
 await refreshExtensionSession(async()=>catalog())
 await refreshExtensionSession(async()=>{throw new Error('revoked')})
 expect(extensionSession.ready).toBe(false)
 expect(extensionSession.grants).toEqual([])
})

it('does not invalidate a pending authorized view when the same catalog arrives in a different order', async () => {
 const first = { mfa_verified: true, modules: [{ id: 'cases', api_version: 1 }, { id: 'directory', api_version: 1 }], permissions: ['cases:read', 'directory:read'], grants: [{ permission: 'cases:read', unit_id: 'east' }, { permission: 'cases:read', unit_id: 'west' }] }
 await refreshExtensionSession(async () => first)
 const revision = extensionSession.revision
 await refreshExtensionSession(async () => ({ ...first, modules: [...first.modules].reverse(), permissions: [...first.permissions].reverse(), grants: [...first.grants].reverse() }))
 expect(extensionSession.revision).toBe(revision)
 expect(extensionPermission('cases:read')).toBe(true)
 expect(extensionScopes('cases:read')).toHaveLength(2)
})

it('accepts only explicit authenticated primary demo access without inventing MFA proof', async () => {
 expect(await refreshExtensionSession(async()=>({...catalog(),mfa_verified:false,authentication_policy:'primary-session-demo',primary_session_authorized:true}))).toBe(true)
 expect(extensionPermission('cases:read')).toBe(true)
 expect(extensionSession.authenticationPolicy).toBe('primary-session-demo')
 expect(extensionSession.mfaVerified).toBe(false)
})

it.each([
 {mfa_verified:false},
 {mfa_verified:false,primary_session_authorized:true},
 {mfa_verified:true,primary_session_authorized:true},
 {mfa_verified:false,authentication_policy:'primary-session-demo'},
 {mfa_verified:false,authentication_policy:'primary-session-demo',primary_session_authorized:false},
 {mfa_verified:false,authentication_policy:'primary-session-demo',primary_session_authorized:'true'},
 {mfa_verified:'false',authentication_policy:'primary-session-demo',primary_session_authorized:true},
 {mfa_verified:true,authentication_policy:'unknown',primary_session_authorized:true},
 {mfa_verified:false,authentication_policy:'mfa',primary_session_authorized:false},
 {mfa_verified:true,authentication_policy:'mfa'},
 {mfa_verified:true,authentication_policy:'mfa',primary_session_authorized:true},
])('rejects incomplete or contradictory authentication policy %j', async authentication => {
 expect(await refreshExtensionSession(async()=>({...catalog(),...authentication}))).toBe(false)
 expect(extensionSession.ready).toBe(false)
 expect(extensionSession.authenticationPolicy).toBe(null)
 expect(extensionSession.mfaVerified).toBe(false)
})

it('normalizes legacy MFA and explicit MFA policies and invalidates equal grants on policy changes', async () => {
 await refreshExtensionSession(async()=>catalog())
 expect(extensionSession.authenticationPolicy).toBe('mfa')
 expect(extensionSession.mfaVerified).toBe(true)
 const previous=extensionSession.revision
 await refreshExtensionSession(async()=>({...catalog(),authentication_policy:'mfa',primary_session_authorized:false}))
 expect(extensionSession.revision).toBe(previous)
 await refreshExtensionSession(async()=>({...catalog(),mfa_verified:false,authentication_policy:'primary-session-demo',primary_session_authorized:true}))
 expect(extensionSession.revision).toBeGreaterThan(previous)
 const demoRevision=extensionSession.revision
 await refreshExtensionSession(async()=>({...catalog(),mfa_verified:true,authentication_policy:'primary-session-demo',primary_session_authorized:true}))
 expect(extensionSession.revision).toBeGreaterThan(demoRevision)
 expect(extensionSession.mfaVerified).toBe(true)
 clearExtensionSession('other:user')
 expect(extensionSession.authenticationPolicy).toBe(null)
 expect(extensionSession.mfaVerified).toBe(false)
})

it('clears demo policy on revocation and refuses a late demo response after identity switch', async()=>{
 const demo={...catalog(),mfa_verified:false,authentication_policy:'primary-session-demo',primary_session_authorized:true}
 await refreshExtensionSession(async()=>demo)
 await refreshExtensionSession(async()=>{throw new Error('revoked')})
 expect(extensionSession.authenticationPolicy).toBe(null)
 expect(extensionSession.mfaVerified).toBe(false)
 let finish!:(value:unknown)=>void
 const pending=refreshExtensionSession(()=>new Promise(resolve=>finish=resolve))
 clearExtensionSession('other:user')
 finish(demo)
 expect(await pending).toBe(false)
 expect(extensionSession.authenticationPolicy).toBe(null)
 expect(extensionSession.ready).toBe(false)
})
