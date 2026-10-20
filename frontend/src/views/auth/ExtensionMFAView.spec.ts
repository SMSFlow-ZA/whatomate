// @vitest-environment happy-dom
import { createApp, nextTick } from 'vue'
import { beforeEach, expect, it, vi } from 'vitest'
import ExtensionMFAView from './ExtensionMFAView.vue'
const { get, post } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('@/services/api', () => ({ api: { get, post } }))
beforeEach(() => { get.mockResolvedValue({data:{data:{enabled:false,verified:false,generation:0}}});post.mockReset() })
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
