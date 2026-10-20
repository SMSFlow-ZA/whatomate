import DirectoryPage from './modules/directory/DirectoryPage.vue'
import CasesPage from './modules/cases/CasesPage.vue'
import KnowledgePage from './modules/knowledge/KnowledgePage.vue'
import AIPage from './modules/ai/AIPage.vue'
import ConversationPage from './modules/conversation/ConversationPage.vue'
import ReportsPage from './modules/reports/ReportsPage.vue'
import GovernancePage from './modules/governance/GovernancePage.vue'
import { createExtensionRegistry, type ExtensionManifest, type CompiledComponents } from './registry'
const page = (id:string, title:string, permission:string, component:string, anyOf:readonly string[]=[]):ExtensionManifest => ({
 id, apiVersion:1, version:'0.1.0', requires:[],
 routes:[{id:'workspace',path:'workspace',title,permission,component,...(anyOf.length?{anyOf}: {})}],
 navigation:[{id:'workspace',label:title,route:'workspace',permission,...(anyOf.length?{anyOf}: {})}], panels:[]
})
export const compiledExtensionManifests: readonly ExtensionManifest[] = [
 page('directory','Employee directory','directory:read','directory/Page'),
 page('cases','ER cases','cases:read.own','cases/Page',['cases:read.triage','cases:work.assigned','cases:read']),
 page('knowledge','Knowledge publishing','knowledge:read','knowledge/Page',['knowledge:upload','knowledge:review','knowledge:publish','knowledge:retire']),
 page('ai','Knowledge assistant','ai:assist','ai/Page'),
 page('conversation','Synthetic conversation','conversation:simulate','conversation/Page'),
 page('governance','Privacy lifecycle','governance:journal.read','governance/Page',['governance:inventory.write','governance:request.access','governance:request.correct','governance:request.export','governance:request.delete','governance:hold.manage','governance:policy.manage','governance:restore.reconcile']),
 {...page('reports','Operational reports','reports:operational','reports/Page'),
 routes:[{id:'workspace',path:'workspace',title:'Operational reports',permission:'reports:operational',component:'reports/Page'},
 {id:'board',path:'board',title:'Leadership reports',permission:'reports:aggregate',component:'reports/Page'}],
 navigation:[{id:'workspace',label:'Operational reports',route:'workspace',permission:'reports:operational'},
 {id:'board',label:'Leadership reports',route:'board',permission:'reports:aggregate'}]}
]
export const compiledExtensionComponents:CompiledComponents = Object.freeze({'directory/Page':DirectoryPage,'cases/Page':CasesPage,'knowledge/Page':KnowledgePage,'ai/Page':AIPage,'conversation/Page':ConversationPage,'reports/Page':ReportsPage,'governance/Page':GovernancePage})
export function createCompiledCatalog(enabled:string) { return createExtensionRegistry(compiledExtensionManifests,compiledExtensionComponents,enabled.split(',').map(s=>s.trim()).filter(Boolean)) }
export const extensionRegistry = createCompiledCatalog(import.meta.env.VITE_EXTENSION_MODULES || '')
