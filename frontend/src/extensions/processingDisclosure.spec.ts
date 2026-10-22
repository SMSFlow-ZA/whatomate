import { beforeEach, expect, it } from 'vitest'
import { clearExtensionSession, extensionPermission, extensionSession, refreshExtensionSession } from './session'

const notice = 'This deployment is configured for application data and RAG storage in South Africa. Azure OpenAI Global Standard inference, including embedding generation, may process submitted content internationally.'
const metadata = () => ({ scope:'global_standard', stored_data_country:'ZA', inference_may_be_international:true, embeddings_may_be_international:true, notice })
const catalog = () => ({ mfa_verified:true, modules:[{id:'ai',api_version:1}], permissions:['ai:assist'], grants:[] })
beforeEach(() => clearExtensionSession('synthetic:operator'))

it('stores valid disclosure without changing current authorization or navigation revision', async () => {
 await refreshExtensionSession(async () => catalog())
 const revision=extensionSession.revision
 await refreshExtensionSession(async () => ({...catalog(),ai_processing:metadata()}))
 expect(extensionSession.aiProcessing?.notice).toBe(notice)
 expect(extensionSession.aiProcessingUnavailable).toBe(false)
 expect(extensionPermission('ai:assist')).toBe(true)
 expect(extensionSession.revision).toBe(revision)
 clearExtensionSession()
 expect(extensionSession.aiProcessing).toBeNull()
 expect(extensionSession.aiProcessingUnavailable).toBe(false)
})

it.each([null,{}, {...metadata(),notice:'All processing stays in ZA'}, {...metadata(),embeddings_may_be_international:false}, {...metadata(),approval_reference:'private-approval'}, {...metadata(),notice:'<script>bad</script>'}])('rejects unsupported optional metadata without invalidating authority (%j)',async value => {
 await refreshExtensionSession(async () => catalog())
 const revision=extensionSession.revision
 expect(await refreshExtensionSession(async () => ({...catalog(),ai_processing:value}))).toBe(true)
 expect(extensionSession.aiProcessing).toBeNull()
 expect(extensionSession.aiProcessingUnavailable).toBe(true)
 expect(extensionPermission('ai:assist')).toBe(true)
 expect(extensionSession.revision).toBe(revision)
})

it('keeps catalogs without the optional field compatible and clears removed metadata',async()=>{
 await refreshExtensionSession(async()=>({...catalog(),ai_processing:metadata()}))
 await refreshExtensionSession(async()=>catalog())
 expect(extensionSession.aiProcessing).toBeNull()
 expect(extensionSession.aiProcessingUnavailable).toBe(false)
 expect(extensionPermission('ai:assist')).toBe(true)
})

it('cannot resurrect disclosure through a response from an old logged-out session',async()=>{
 let finish!:(value:unknown)=>void
 const pending=refreshExtensionSession(()=>new Promise(resolve=>finish=resolve))
 clearExtensionSession('different:operator')
 finish({...catalog(),ai_processing:metadata()})
 expect(await pending).toBe(false)
 expect(extensionSession.aiProcessing).toBeNull()
 expect(extensionSession.aiProcessingUnavailable).toBe(false)
})
