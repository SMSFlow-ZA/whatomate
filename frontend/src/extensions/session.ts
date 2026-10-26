import { reactive, readonly } from 'vue'
import { parseExtensionPermission } from './permissions'
import { parseAIProcessingDisclosure, type AIProcessingDisclosure } from './processingDisclosure'

export interface ExtensionGrant { permission: string; unit_id?: string; audience?: string }
export type ExtensionAuthenticationPolicy = 'mfa' | 'primary-session-demo'
interface Catalog { modules: {id:string;api_version:number}[]; permissions:string[]; grants:ExtensionGrant[]; mfa_verified:boolean; authentication_policy?:ExtensionAuthenticationPolicy; primary_session_authorized?:boolean; generic_host_access?:boolean; ai_processing?:unknown }
const state = reactive({ identity:'', ready:false, revision:0, modules:[] as string[], permissions:[] as string[], grants:[] as ExtensionGrant[], authenticationPolicy:null as ExtensionAuthenticationPolicy|null, mfaVerified:false, genericHostAccess:null as boolean|null, aiProcessing:null as AIProcessingDisclosure|null, aiProcessingUnavailable:false })
export const extensionSession = readonly(state)
let flight: Promise<boolean> | undefined
let flightEpoch = -1
export function clearExtensionSession(identity = state.identity) {
  state.identity = identity; state.ready = false; state.modules = []; state.permissions = []; state.grants = []; state.revision++
  state.aiProcessing=null; state.aiProcessingUnavailable=false
  state.authenticationPolicy=null; state.mfaVerified=false
  state.genericHostAccess=null
}
export function extensionIdentity(identity: string) { if (identity !== state.identity) clearExtensionSession(identity) }
export function extensionPermission(permission:string):boolean {
  const parsed = parseExtensionPermission(permission)
  return !!parsed && state.ready && state.modules.includes(parsed.resource) && state.permissions.includes(permission)
}
/** Choices come only from the authenticated catalog; they do not grant authority. */
export function extensionScopes(permission:string):ReadonlyArray<Readonly<ExtensionGrant>> {
  if (!extensionPermission(permission)) return []
  return state.grants.filter(g => g.permission === permission).map(g => Object.freeze({...g}))
}
function validate(raw: unknown): Catalog {
  if (!raw || typeof raw !== 'object') throw new Error('Invalid extension catalog')
  const c = raw as Catalog
  if (c.generic_host_access !== undefined && typeof c.generic_host_access !== 'boolean') throw new Error('Invalid host access policy')
  const legacyMFA = c.authentication_policy === undefined && c.primary_session_authorized === undefined && c.mfa_verified === true
  const explicitMFA = c.authentication_policy === 'mfa' && c.primary_session_authorized === false && c.mfa_verified === true
  const primaryDemo = c.authentication_policy === 'primary-session-demo' && c.primary_session_authorized === true && typeof c.mfa_verified === 'boolean'
  if (!(legacyMFA || explicitMFA || primaryDemo) || !Array.isArray(c.modules) || !Array.isArray(c.permissions) || !Array.isArray(c.grants) || c.modules.length > 64 || c.permissions.length > 512 || c.grants.length > 4096) throw new Error('Invalid extension catalog')
  const ids = new Set<string>()
  for (const m of c.modules) {
    if (!m || !/^[a-z][a-z0-9-]{0,63}$/.test(m.id) || m.api_version !== 1 || ids.has(m.id)) throw new Error('Invalid module')
    ids.add(m.id)
  }
  if (!c.permissions.every(p => typeof p === 'string' && parseExtensionPermission(p))) throw new Error('Invalid permission')
  for (const g of c.grants) {
    if (!g || !c.permissions.includes(g.permission) || !parseExtensionPermission(g.permission)) throw new Error('Invalid grant')
    for (const scope of [g.unit_id, g.audience]) if (scope !== undefined && (typeof scope !== 'string' || !scope || scope.length > 256 || /[\x00-\x1f*]/.test(scope))) throw new Error('Invalid scope')
  }
  return c
}
export function refreshExtensionSession(load:()=>Promise<unknown>):Promise<boolean> {
  if (!state.identity) return Promise.resolve(false)
  const epoch = state.revision
  if (flight && flightEpoch === epoch) return flight
  flightEpoch = epoch
  const identity = state.identity
  const promise = (async () => {
    try {
      const c = validate(await load())
      if (state.revision !== epoch || state.identity !== identity) return false
      const modules = c.modules.map(m => m.id).sort()
      const authenticationPolicy = c.authentication_policy ?? 'mfa'
      const genericHostAccess = c.generic_host_access ?? null
      const permissions = [...new Set(c.permissions)].sort()
      const grants = [...new Map(c.grants.map(g => {
        const grant = {permission:g.permission, ...(g.unit_id ? {unit_id:g.unit_id} : {}), ...(g.audience ? {audience:g.audience} : {})}
        return [JSON.stringify(grant), grant] as const
      })).entries()].sort(([left], [right]) => left < right ? -1 : left > right ? 1 : 0).map(([, grant]) => grant)
      // Changes invalidate pending data requests and remount protected views.
      const changed = !state.ready || JSON.stringify([state.modules,state.permissions,state.grants,state.authenticationPolicy,state.mfaVerified,state.genericHostAccess]) !== JSON.stringify([modules,permissions,grants,authenticationPolicy,c.mfa_verified,genericHostAccess])
      state.modules = modules; state.permissions = permissions; state.grants = grants; state.ready = true
      state.authenticationPolicy=authenticationPolicy; state.mfaVerified=c.mfa_verified
      state.genericHostAccess=genericHostAccess
      // Metadata is independent from authority: malformed optional disclosure
      // blocks new AI submissions, without changing MFA/permissions/navigation.
      const hasProcessing=Object.prototype.hasOwnProperty.call(c,'ai_processing')
      state.aiProcessing=hasProcessing?parseAIProcessingDisclosure(c.ai_processing):null
      state.aiProcessingUnavailable=hasProcessing && state.aiProcessing===null
      if (changed) state.revision++
      return true
    } catch { if (state.revision === epoch && state.identity === identity) clearExtensionSession(); return false }
    finally { if (flightEpoch === epoch) flight = undefined }
  })()
  flight = promise
  return promise
}
