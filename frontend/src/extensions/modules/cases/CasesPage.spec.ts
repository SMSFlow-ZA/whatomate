// @vitest-environment happy-dom
import {afterEach,beforeEach,expect,it,vi} from 'vitest'
import {createApp,type App} from 'vue'
const mocks=vi.hoisted(()=>({createCase:vi.fn(),listCases:vi.fn(),loadCaseAccess:vi.fn()}))
vi.mock('./api',()=>({...mocks,caseAction:vi.fn(),readCase:vi.fn()}))
import CasesPage from './CasesPage.vue'
let app:App,host:HTMLElement
beforeEach(()=>{vi.clearAllMocks();mocks.listCases.mockResolvedValue([]);mocks.loadCaseAccess.mockResolvedValue({permissions:['cases:submit.own'],grants:[{permission:'cases:submit.own',unit_id:'demo-unit'}]});mocks.createCase.mockResolvedValue({id:'durable'});host=document.createElement('div');document.body.append(host)})
afterEach(()=>{app.unmount();host.remove()})
it('lets an employee submit a concern without inventing server-owned roster and classification facts',async()=>{
 app=createApp(CasesPage);app.mount(host);await vi.waitFor(()=>expect(host.querySelector('.intake')).not.toBeNull())
 const form=host.querySelector('.intake form')!
 const labels=Array.from(form.querySelectorAll('label')).map(l=>l.textContent)
 expect(labels.some(l=>l?.trim()==='Department')).toBe(false)
 expect(labels.some(l=>l?.trim()==='Zone')).toBe(false)
 expect(labels.some(l=>l?.trim()==='Case type')).toBe(false)
 const unit=form.querySelector('select') as HTMLSelectElement;unit.value='demo-unit';unit.dispatchEvent(new Event('change'))
 const narrative=form.querySelector('textarea') as HTMLTextAreaElement;narrative.value='Synthetic concern';narrative.dispatchEvent(new Event('input'))
 form.dispatchEvent(new Event('submit',{bubbles:true,cancelable:true}))
 await vi.waitFor(()=>expect(mocks.createCase).toHaveBeenCalled())
 expect(mocks.createCase.mock.calls[0]?.[0]).toEqual({key:expect.any(String),unit_id:'demo-unit',narrative:'Synthetic concern',draft:false})
})
