// @vitest-environment happy-dom
import { createApp, nextTick } from 'vue'
import { beforeEach, expect, it, vi } from 'vitest'
import ExtensionMFAView from './ExtensionMFAView.vue'
import { AuthenticationRefreshedError } from '@/services/api'
import { clearExtensionSession, extensionSession } from '@/extensions/session'
const { get, post } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('@/services/api', async importOriginal => ({ ...await importOriginal<typeof import('@/services/api')>(), api: { get, post } }))
beforeEach(() => { clearExtensionSession('org:user'); get.mockReset(); get.mockResolvedValue({data:{data:{enabled:false,verified:false,generation:0}}});post.mockReset() })
it('shows enrollment material only in the current screen and clears it after acknowledgement', async () => {
 post.mockResolvedValueOnce({data:{data:{id:'enrollment',secret:'ONE-TIME-SEED'}}})
 post.mockResolvedValueOnce({data:{data:{recovery_codes:['recovery-one'],generation:1}}})
 const host=document.createElement('div');document.body.append(host)
 const app=createApp(ExtensionMFAView);app.mount(host)
 await new Promise(resolve=>setTimeout(resolve,0));await nextTick()
 ;(host.querySelector('[data-action=enroll]') as HTMLButtonElement).click()
 await new Promise(resolve=>setTimeout(resolve,0));await nextTick()
 expect(host.textContent).toContain('ONE-TIME-SEED')
 const input=host.querySelector('[name=code]') as HTMLInputElement
 input.value='123456';input.dispatchEvent(new Event('input'));await nextTick()
 ;(host.querySelector('form') as HTMLFormElement).dispatchEvent(new Event('submit',{cancelable:true}))
 await new Promise(resolve=>setTimeout(resolve,0));await nextTick()
 expect(post).toHaveBeenLastCalledWith('/extensions/security/mfa/confirm',{enrollment_id:'enrollment',code:'123456'})
 expect(host.textContent).toContain('recovery-one');expect(host.textContent).not.toContain('ONE-TIME-SEED')
 ;(host.querySelector('[data-action=acknowledge]') as HTMLButtonElement).click();await nextTick()
 expect(host.textContent).not.toContain('recovery-one')
 app.unmount();host.remove()
})

it.each([false, true])('reconciles refreshed authentication without replay or claiming operation success (MFA %s)', async verified => {
 get.mockResolvedValueOnce({data:{data:{enabled:true,verified:false,generation:1}}})
 get.mockResolvedValueOnce({data:{data:{enabled:true,verified,generation:1}}})
 post.mockImplementationOnce(async () => {
   clearExtensionSession()
   throw new AuthenticationRefreshedError(extensionSession.identity, extensionSession.revision, true)
 })
 const host=document.createElement('div');document.body.append(host)
 const app=createApp(ExtensionMFAView);app.mount(host)
 await new Promise(resolve=>setTimeout(resolve,0));await nextTick()
 const input=host.querySelector('[name=code]') as HTMLInputElement
 input.value='123456';input.dispatchEvent(new Event('input'));await nextTick()
 ;(host.querySelector('form') as HTMLFormElement).dispatchEvent(new Event('submit',{cancelable:true}))
 await new Promise(resolve=>setTimeout(resolve,0));await nextTick()
 expect(post).toHaveBeenCalledTimes(1)
 expect(get).toHaveBeenCalledTimes(2)
 expect(input.value).toBe('')
 expect(host.textContent).toContain(verified ? 'Current session is MFA verified.' : 'Session refreshed. Enter a new authenticator code.')
 expect(host.textContent).not.toContain('MFA verified for this session.')
 expect(host.textContent).not.toContain('Save your one-time recovery codes')
 app.unmount();host.remove()
})

it('does not reconcile a refresh result after logout invalidates its session', async () => {
 get.mockResolvedValue({data:{data:{enabled:true,verified:false,generation:1}}})
 post.mockImplementationOnce(async () => {
   clearExtensionSession()
   const refreshed = new AuthenticationRefreshedError(extensionSession.identity, extensionSession.revision, true)
   clearExtensionSession('')
   throw refreshed
 })
 const host=document.createElement('div');document.body.append(host)
 const app=createApp(ExtensionMFAView);app.mount(host)
 await new Promise(resolve=>setTimeout(resolve,0));await nextTick()
 const input=host.querySelector('[name=code]') as HTMLInputElement
 input.value='123456';input.dispatchEvent(new Event('input'));await nextTick()
 ;(host.querySelector('form') as HTMLFormElement).dispatchEvent(new Event('submit',{cancelable:true}))
 await new Promise(resolve=>setTimeout(resolve,0));await nextTick()
 expect(get).toHaveBeenCalledTimes(1)
 expect(host.textContent).toContain('Security operation failed.')
 app.unmount();host.remove()
})

it('does not claim enrollment confirmation or invent recovery codes when refresh prevented submission', async () => {
 post.mockResolvedValueOnce({data:{data:{id:'enrollment',secret:'ONE-TIME-SEED'}}})
 post.mockImplementationOnce(async () => {
   clearExtensionSession()
   throw new AuthenticationRefreshedError(extensionSession.identity, extensionSession.revision, true)
 })
 const host=document.createElement('div');document.body.append(host)
 const app=createApp(ExtensionMFAView);app.mount(host)
 await new Promise(resolve=>setTimeout(resolve,0));await nextTick()
 ;(host.querySelector('[data-action=enroll]') as HTMLButtonElement).click()
 await new Promise(resolve=>setTimeout(resolve,0));await nextTick()
 const input=host.querySelector('[name=code]') as HTMLInputElement
 input.value='123456';input.dispatchEvent(new Event('input'));await nextTick()
 ;(host.querySelector('form') as HTMLFormElement).dispatchEvent(new Event('submit',{cancelable:true}))
 await new Promise(resolve=>setTimeout(resolve,0));await nextTick()
 expect(post).toHaveBeenCalledTimes(2)
 expect(host.textContent).toContain('Session refreshed. Enter a new authenticator code.')
 expect(host.textContent).not.toContain('Authenticator enabled.')
 expect(host.textContent).not.toContain('Save your one-time recovery codes')
 expect(input.value).toBe('')
 app.unmount();host.remove()
})
