// Local layout fixture only: synthetic authority and intercepted API data are
// deliberately not authentication, backend or protected-access evidence.
import { createApp, h, ref } from 'vue'
import { createPinia } from 'pinia'
import { createMemoryHistory, createRouter, RouterView } from 'vue-router'
import AppLayout from '../../src/components/layout/AppLayout.vue'
import { useAuthStore } from '../../src/stores/auth'
import { defaultBranding, useBrandingStore } from '../../src/stores/branding'
import { wsService } from '../../src/services/websocket'
import { i18n } from '../../src/i18n'
import { Dialog, DialogContent, DialogTitle, DialogDescription } from '../../src/components/ui/dialog'
import { AlertDialog, AlertDialogContent, AlertDialogTitle, AlertDialogDescription } from '../../src/components/ui/alert-dialog'
import Knowledge from '../../src/extensions/modules/knowledge/KnowledgePage.vue'
import Directory from '../../src/extensions/modules/directory/DirectoryPage.vue'
import Cases from '../../src/extensions/modules/cases/CasesPage.vue'
import AI from '../../src/extensions/modules/ai/AIPage.vue'
import Conversation from '../../src/extensions/modules/conversation/ConversationPage.vue'
import Reports from '../../src/extensions/modules/reports/ReportsPage.vue'
import Governance from '../../src/extensions/modules/governance/GovernancePage.vue'
import '../../src/assets/index.css'

const modules = { knowledge: Knowledge, directory: Directory, cases: Cases, ai: AI, conversation: Conversation, reports: Reports, governance: Governance }
const selected = new URLSearchParams(location.search).get('module') || 'knowledge'
const dialogs = { setup() {
  const ordinary = ref(false), alert = ref(false)
  const long = () => Array.from({ length: 35 }, (_, i) => h('p', { class: 'py-2' }, `Synthetic dialog line ${i}`))
  return () => h('section', [
    h('button', { onClick: () => { ordinary.value = true } }, 'Open dialog'),
    h('button', { onClick: () => { alert.value = true } }, 'Open alert'),
    h(Dialog, { open: ordinary.value, 'onUpdate:open': (v: boolean) => { ordinary.value = v } }, () => h(DialogContent, {}, () => [h(DialogTitle, {}, () => 'Long dialog'), h(DialogDescription, {}, () => 'Synthetic scroll fixture'), ...long(), h('button', { onClick: () => { ordinary.value = false } }, 'Finish dialog')])),
    h(AlertDialog, { open: alert.value, 'onUpdate:open': (v: boolean) => { alert.value = v } }, () => h(AlertDialogContent, {}, () => [h(AlertDialogTitle, {}, () => 'Long alert'), h(AlertDialogDescription, {}, () => 'Synthetic scroll fixture'), ...long(), h('button', { onClick: () => { alert.value = false } }, 'Finish alert')]))
  ])
} }
const component = selected === 'dialogs' ? dialogs : modules[selected as keyof typeof modules]
const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: AppLayout, children: [{ path: '', component, meta: { extensionModule: selected, permission: `${selected}:read` } }] }] })
const app = createApp({ render: () => h(RouterView) })
const pinia = createPinia(); app.use(pinia); app.use(router); app.use(i18n)
useBrandingStore(pinia).apply({ ...defaultBranding, display_name: 'Example Employee Relations — demonstration', accent_color: '#b00020' })
const auth = useAuthStore(pinia)
auth.user = { id: 'layout-fixture', organization_id: 'fixture', full_name: 'Layout fixture', email: 'layout@example.invalid' }
auth.hasExtensionPermission = () => true
auth.refreshUserData = async () => true
wsService.connect = () => {}
await router.isReady(); app.mount('#app')
