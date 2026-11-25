// @vitest-environment happy-dom
import { createApp, h, nextTick, reactive, ref, type SetupContext } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
import AuditLogDetailView from './AuditLogDetailView.vue'
const mock = vi.hoisted(() => ({
  get: vi.fn(),
  route: undefined as any,
  viewer: undefined as any,
}))
vi.mock('@/services/api', () => ({ auditLogsService: { get: mock.get } }))
vi.mock('vue-router', () => ({ useRoute: () => mock.route }))
vi.mock('@/composables/useInboxViewerContext', () => ({
  useInboxViewerContext: () => mock.viewer,
}))
vi.mock('@/components/shared', () => ({
  PageHeader: {
    props: ['title'],
    setup: (props: { title: string }) => () => h('h1', props.title),
  },
}))
const cleanups: (() => void)[] = []
afterEach(() => {
  cleanups.splice(0).forEach((fn) => fn())
  vi.clearAllMocks()
})
const a = '00000000-0000-0000-0000-000000000001',
  b = '00000000-0000-0000-0000-000000000002'
const reply = (id: string, name: string) => ({
  data: {
    data: {
      id,
      user_name: name,
      resource_type: 'user',
      resource_id: a,
      action: 'event',
      changes: [],
      created_at: '2026-11-25T09:00:00Z',
      context_available: true,
      resource_available: false,
    },
  },
})
async function flush() {
  for (let i = 0; i < 4; i++) {
    await Promise.resolve()
    await nextTick()
  }
}
function mount() {
  mock.route = reactive({ params: { id: a } })
  mock.viewer = ref('viewer:org-one')
  const root = document.createElement('div'),
    app = createApp(AuditLogDetailView)
  app.component('RouterLink', {
    setup:
      (_: unknown, { slots }: SetupContext) =>
      () =>
        h('a', slots.default?.()),
  })
  document.body.append(root)
  app.mount(root)
  cleanups.push(() => {
    app.unmount()
    root.remove()
  })
  return root
}
it('tracks reused route parameters and discards a slow previous record and viewer', async () => {
  let complete!: (value: ReturnType<typeof reply>) => void
  mock.get
    .mockReturnValueOnce(
      new Promise((resolve) => {
        complete = resolve
      }),
    )
    .mockResolvedValueOnce(reply(b, 'Current synthetic actor'))
  const root = mount()
  mock.route.params.id = b
  await flush()
  expect(root.textContent).toContain('Current synthetic actor')
  complete(reply(a, 'Private previous actor'))
  await flush()
  expect(root.textContent).not.toContain('Private previous actor')
  mock.get.mockRejectedValueOnce({ response: { status: 403 } })
  mock.viewer.value = 'viewer:org-two'
  await flush()
  expect(root.textContent).not.toContain('Current synthetic actor')
  expect(root.textContent).toContain('access changed')
})
it('distinguishes unavailable storage from not-found and can recover', async () => {
  mock.get
    .mockRejectedValueOnce({ response: { status: 503 } })
    .mockResolvedValueOnce(reply(a, 'Recovered synthetic actor'))
  const root = mount()
  await flush()
  expect(root.textContent).toContain('temporarily unavailable')
  expect(root.textContent).not.toContain('No readable audit record')
  Array.from(root.querySelectorAll('button'))
    .find((button) => button.textContent?.includes('Retry record'))!
    .click()
  await flush()
  expect(root.textContent).toContain('Recovered synthetic actor')
  expect(root.querySelector('a')?.textContent).not.toBe('View resource')
})
