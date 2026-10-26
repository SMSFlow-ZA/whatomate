// @vitest-environment happy-dom
import { createApp, nextTick } from 'vue'
import { beforeEach, expect, it } from 'vitest'
import AuthenticationPolicyNotice from './AuthenticationPolicyNotice.vue'
import { clearExtensionSession, refreshExtensionSession } from '@/extensions/session'
const catalog={modules:[],permissions:[],grants:[],mfa_verified:false,authentication_policy:'primary-session-demo',primary_session_authorized:true}
beforeEach(()=>clearExtensionSession('org:user'))
it('shows the truthful shared notice only during current authenticated demo access', async()=>{
 const host=document.createElement('div');document.body.append(host)
 const app=createApp(AuthenticationPolicyNotice);app.mount(host)
 expect(host.textContent).toBe('')
 await refreshExtensionSession(async()=>catalog);await nextTick()
 expect(host.textContent).toContain('Demo access: password sign-in; MFA temporarily not required.')
 expect(host.querySelector('[role=status]')).not.toBe(null)
 expect(host.textContent).not.toContain('MFA verified')
 await refreshExtensionSession(async()=>({...catalog,mfa_verified:true,authentication_policy:'mfa',primary_session_authorized:false}));await nextTick()
 expect(host.textContent).toBe('')
 await refreshExtensionSession(async()=>catalog);await nextTick()
 clearExtensionSession('');await nextTick()
 expect(host.textContent).toBe('')
 app.unmount();host.remove()
})
