// @vitest-environment happy-dom
import { createApp, h, nextTick } from 'vue'
import { createI18n } from 'vue-i18n'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import en from '@/i18n/locales/en.json'
import ChatbotFlowsView from './ChatbotFlowsView.vue'
import { chatbotService } from '@/services/api'
import { toast } from 'vue-sonner'

const state = vi.hoisted(() => ({ read: true, write: true }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ hasPermission: (_resource: string, action: string) => action === 'read' ? state.read : state.write }) }))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }), RouterLink: { template: '<a><slot /></a>' } }))
vi.mock('@/services/api', () => ({ chatbotService: { listFlows: vi.fn(), duplicateFlow: vi.fn(), updateFlow: vi.fn() } }))
vi.mock('vue-sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
const source = { id: 'source', name: 'Welcome', enabled: true, description: 'Test', trigger_keywords: ['start'], steps_count: 1 }
let root: HTMLDivElement, app: ReturnType<typeof createApp>
async function flush() { for (let i = 0; i < 12; i++) { await Promise.resolve(); await nextTick() } }
async function mount() {
  root = document.createElement('div'); document.body.append(root)
  app = createApp({ render: () => h(TooltipProvider, {}, { default: () => h(ChatbotFlowsView) }) })
  app.component('RouterLink', { template: '<a><slot /></a>' })
  app.use(createI18n({ legacy: false, locale: 'en', messages: { en } })); app.mount(root); await flush()
}
function copies() { return Array.from(root.querySelectorAll<HTMLButtonElement>('button[aria-label="Duplicate flow"]')) }
beforeEach(() => {
  state.read = true; state.write = true; vi.clearAllMocks()
  vi.mocked(chatbotService.listFlows).mockResolvedValue({ data: { flows: [{ ...source }], total: 1 } } as any)
  vi.mocked(chatbotService.duplicateFlow).mockResolvedValue({ data: { id: 'copy', enabled: false } } as any)
})
afterEach(() => { app?.unmount(); root?.remove() })
describe('chatbot duplication', () => {
  it('copies through the server endpoint and refreshes the list without enabling either flow', async () => {
    await mount(); expect(copies()).toHaveLength(1)
    vi.mocked(chatbotService.listFlows).mockResolvedValueOnce({ data: { flows: [{ ...source }, { ...source, id: 'copy', name: 'Welcome (Copy)', enabled: false }], total: 2 } } as any)
    copies()[0]!.click(); await flush()
    expect(chatbotService.duplicateFlow).toHaveBeenCalledExactlyOnceWith('source')
    expect(chatbotService.updateFlow).not.toHaveBeenCalled()
    expect(root.textContent).toContain('Welcome (Copy)'); expect(root.textContent).toContain('Inactive')
    expect(toast.success).toHaveBeenCalledWith('Flow duplicated successfully')
  })
  it('blocks repeated clicks during a pending request and restores controls after failure', async () => {
    let reject!: (reason: Error) => void
    vi.mocked(chatbotService.duplicateFlow).mockImplementationOnce(() => new Promise((_resolve, fail) => { reject = fail }))
    await mount(); const button = copies()[0]!; button.click(); button.click(); await flush()
    expect(chatbotService.duplicateFlow).toHaveBeenCalledTimes(1); expect(copies()[0]!.disabled).toBe(true)
    reject(Error('offline')); await flush()
    expect(toast.error).toHaveBeenCalled(); expect(copies()[0]!.disabled).toBe(false)
    expect(root.textContent).not.toContain('Welcome (Copy)')
    copies()[0]!.click(); await flush(); expect(chatbotService.duplicateFlow).toHaveBeenCalledTimes(2)
  })
  it.each([[false, true], [true, false], [false, false]])('requires both read=%s and write=%s', async (read, write) => {
    state.read = read; state.write = write; await mount(); expect(copies()).toHaveLength(0)
    expect(chatbotService.duplicateFlow).not.toHaveBeenCalled()
  })
})
