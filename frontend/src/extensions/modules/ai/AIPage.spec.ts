// @vitest-environment happy-dom
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
const mocks=vi.hoisted(()=>({requestAssistance:vi.fn(),readAssistance:vi.fn(),reviewAssistance:vi.fn()}))
vi.mock('./api',()=>mocks)
vi.mock('@/stores/auth',()=>({useAuthStore:()=>({hasExtensionPermission:()=>true})}))
import AIPage from './AIPage.vue'
import { clearExtensionSession, refreshExtensionSession } from '../../session'
const notice='This deployment is configured for application data and RAG storage in South Africa. Azure OpenAI Global Standard inference, including embedding generation, may process submitted content internationally.'
const catalog=(ai_processing?:unknown)=>({mfa_verified:true,modules:[{id:'ai',api_version:1}],permissions:['ai:assist'],grants:[],...(ai_processing!==undefined?{ai_processing}:{})})
let app:App|undefined,host:HTMLElement
beforeEach(()=>{vi.resetAllMocks();clearExtensionSession('synthetic:operator');host=document.createElement('div');document.body.append(host)})
afterEach(()=>{app?.unmount();app=undefined;host.remove();clearExtensionSession()})

it('shows international inference including embeddings before the submission form',async()=>{
 await refreshExtensionSession(async()=>catalog({scope:'global_standard',stored_data_country:'ZA',inference_may_be_international:true,embeddings_may_be_international:true,notice}))
 app=createApp(AIPage);app.mount(host);await nextTick()
 const disclosure=host.querySelector('[aria-label="AI processing location"]')!
 expect(disclosure?.textContent).toContain(notice)
 expect(disclosure.compareDocumentPosition(host.querySelector('form')!) & Node.DOCUMENT_POSITION_FOLLOWING).not.toBe(0)
 expect(host.textContent).not.toContain('Local inference')
})

it('blocks new answer and category submissions on malformed disclosure but retains saved-record controls',async()=>{
 await refreshExtensionSession(async()=>catalog({scope:'global_standard'}))
 app=createApp(AIPage);app.mount(host);await nextTick()
 expect(host.textContent).toContain('AI processing information is unavailable')
 const form=host.querySelector('form')!
 expect(Array.from(form.querySelectorAll('button')).every(b=>b.disabled)).toBe(true)
 form.dispatchEvent(new Event('submit',{bubbles:true,cancelable:true}));await nextTick()
 expect(mocks.requestAssistance).not.toHaveBeenCalled()
 expect(Array.from(host.querySelectorAll('button')).find(b=>b.textContent==='Reload current record')?.disabled).toBe(false)
})
