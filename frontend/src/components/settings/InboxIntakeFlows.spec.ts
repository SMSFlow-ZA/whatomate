// @vitest-environment happy-dom
import { createApp, h, nextTick, reactive, ref } from 'vue'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import InboxIntakeMappings from './InboxIntakeMappings.vue'
import ChatIntakeSelect from '@/components/chatbot/ChatIntakeSelect.vue'
import InboxIntakeSummary from '@/components/chat/InboxIntakeSummary.vue'

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  create: vi.fn(),
  update: vi.fn(),
  disable: vi.fn(),
  summary: vi.fn(),
  fields: vi.fn(),
}))
const viewer = ref('viewer-a')
vi.mock('@/composables/useInboxViewerContext', () => ({
  useInboxViewerContext: () => viewer,
}))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ hasPermission: () => true }),
}))
vi.mock('@/services/inboxIntake', () => ({ inboxIntake: mocks }))
vi.mock('@/services/inbox', () => ({
  inboxService: { fieldDefinitions: mocks.fields },
}))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key,
    te: () => true,
    locale: ref('en'),
  }),
}))
vi.mock('vue-router', () => ({
  onBeforeRouteLeave: vi.fn(),
  onBeforeRouteUpdate: vi.fn(),
}))

const field = {
  id: 'reference',
  scope: 'case',
  key: 'reference',
  label: 'Reference',
  kind: 'text',
  required_on_resolve: false,
}
const mapping = {
  id: 'mapping',
  revision: 3,
  name: 'Synthetic intake',
  description: '',
  enabled: true,
  consent_key: 'consent',
  fields: [{ input: 'reference', definition_id: field.id, required: false }],
  unavailable_fields: [],
}
const list = (mappings: unknown[]) => ({
  data: { data: { mappings, has_more: false } },
})
const summary = (caseId: string, label: string) => ({
  data: {
    data: {
      case_id: caseId,
      has_more: false,
      summaries: [
        {
          id: caseId,
          state: 'applied',
          issued_at: '2026-11-25T04:00:00Z',
          contact_fields_hidden: false,
          outcomes: [
            {
              definition_id: 'reference',
              label,
              scope: 'case',
              status: 'saved',
            },
          ],
        },
      ],
    },
  },
})
const deferred = () => {
  let resolve!: (value: unknown) => void
  const promise = new Promise((r) => {
    resolve = r
  })
  return { resolve, promise }
}
const settle = async () => {
  for (let i = 0; i < 16; i++) {
    await Promise.resolve()
    await nextTick()
  }
}
let app: ReturnType<typeof createApp> | undefined, root: HTMLElement
function mount(component: any, props: Record<string, unknown> = {}) {
  root = document.createElement('div')
  document.body.append(root)
  app = createApp({ render: () => h(component, props) })
  app.component('RouterLink', { template: '<a><slot /></a>' })
  app.config.globalProperties.$t = (key: string) => key
  const vm = app.mount(root)
  return (
    vm.$.subTree.component as unknown as { setupState: Record<string, any> }
  ).setupState
}
beforeEach(() => {
  vi.clearAllMocks()
  viewer.value = 'viewer-a'
  mocks.list.mockResolvedValue(list([mapping]))
  mocks.fields.mockResolvedValue({ data: { data: { definitions: [field] } } })
  mocks.create.mockResolvedValue({})
  mocks.update.mockResolvedValue({})
  mocks.disable.mockResolvedValue({})
  mocks.summary.mockResolvedValue(summary('case', 'Synthetic saved field'))
})
afterEach(() => {
  app?.unmount()
  app = undefined
  root?.remove()
})

it('creates mappings disabled and clears busy state after the authoritative reload', async () => {
  const state = mount(InboxIntakeMappings)
  await settle()
  state.begin()
  await settle()
  state.form.name = 'New intake'
  state.form.fields = [
    { input: 'answer', definition_id: field.id, required: true },
  ]
  state.form.enabled = true
  expect(state.valid).toBe(true)
  await state.save()
  await settle()
  expect(mocks.create).toHaveBeenCalledWith(
    expect.objectContaining({
      name: 'New intake',
      enabled: false,
      consent_key: 'consent',
    }),
  )
  expect(state.busy).toBe(false)
  expect(state.editing).toBe(false)
  expect(document.activeElement?.id).toBe('intake-new-mapping')
})

it('returns focus to the remounted mapping trigger on cancel and after save', async () => {
  const state = mount(InboxIntakeMappings)
  await settle()
  const original = document.getElementById('intake-edit-' + mapping.id)
  original?.focus()
  state.begin(mapping)
  await settle()
  expect(original?.isConnected).toBe(false)
  state.close()
  await settle()
  expect(document.activeElement?.id).toBe('intake-edit-' + mapping.id)
  expect(document.activeElement).not.toBe(original)
  state.begin(mapping)
  await settle()
  state.form.description = 'Reviewed description'
  await state.save()
  await settle()
  expect(state.busy).toBe(false)
  expect(document.activeElement?.id).toBe('intake-edit-' + mapping.id)
})

it('falls back to the new mapping action when the edited row is no longer visible', async () => {
  const state = mount(InboxIntakeMappings)
  await settle()
  state.begin(mapping)
  await settle()
  state.query = 'a different mapping'
  state.close()
  await settle()
  expect(document.getElementById('intake-edit-' + mapping.id)).toBeNull()
  expect(document.activeElement?.id).toBe('intake-new-mapping')
})

it('blocks invalid and duplicate field mappings and preserves a stale editor draft', async () => {
  const state = mount(InboxIntakeMappings)
  await settle()
  state.begin(mapping)
  await settle()
  state.form.fields.push({ ...state.form.fields[0] })
  expect(state.valid).toBe(false)
  await state.save()
  expect(mocks.update).not.toHaveBeenCalled()
  state.form.fields.pop()
  state.form.name = 'Preserved stale draft'
  mocks.update.mockRejectedValue({
    isAxiosError: true,
    response: { status: 409 },
  })
  await state.save()
  await settle()
  expect(state.form.name).toBe('Preserved stale draft')
  expect(state.conflict).toBe(true)
  await state.save()
  expect(mocks.update).toHaveBeenCalledTimes(1)
  expect(root.textContent).toContain('inboxIntake.reload')
})

it('can disable an enabled mapping even when its target was archived', async () => {
  mocks.list.mockResolvedValue(
    list([{ ...mapping, unavailable_fields: [field.id] }]),
  )
  mocks.fields.mockResolvedValue({
    data: {
      data: {
        definitions: [{ ...field, archived_at: '2026-11-25T00:00:00Z' }],
      },
    },
  })
  const state = mount(InboxIntakeMappings)
  await settle()
  expect(root.textContent).toContain('inboxIntake.mappingFieldsUnavailable')
  await state.disable(mapping)
  expect(mocks.disable).toHaveBeenCalledWith(mapping.id, mapping.revision)
  expect(mocks.update).not.toHaveBeenCalled()
  expect(state.busy).toBe(false)
})

it('invalidates pending discard actions and late results on a viewer change', async () => {
  const state = mount(InboxIntakeMappings)
  await settle()
  state.begin(mapping)
  await settle()
  state.form.name = 'Private draft'
  const action = vi.fn()
  state.confirmLeave(action)
  expect(state.discardOpen).toBe(true)
  viewer.value = 'viewer-b'
  await settle()
  state.confirmDiscard()
  await settle()
  expect(action).not.toHaveBeenCalled()
  expect(state.form.name).toBe('')
  expect(state.editing).toBe(false)
})

it('shows only enabled, available and step-compatible mappings without clearing an unavailable selection', async () => {
  mocks.list.mockResolvedValue(
    list([
      mapping,
      {
        ...mapping,
        id: 'answer',
        fields: [{ input: 'answer', definition_id: field.id, required: false }],
      },
      { ...mapping, id: 'disabled', enabled: false },
    ]),
  )
  const props = reactive({ value: 'disabled', stepType: 'prompt' })
  const state = mount(ChatIntakeSelect, props)
  await settle()
  expect(state.available.map((item: any) => item.id)).toEqual(['answer'])
  expect((root.querySelector('select') as HTMLSelectElement).value).toBe(
    'disabled',
  )
  expect(root.textContent).toContain('inboxIntake.mappingUnavailable')
  props.stepType = 'whatsapp_flow'
  await settle()
  expect(state.available.map((item: any) => item.id)).toEqual([
    'mapping',
    'answer',
  ])
})

it('ignores reversed summary responses and clears private rows while the case changes', async () => {
  const old = deferred(),
    current = deferred()
  mocks.summary
    .mockReturnValueOnce(old.promise)
    .mockReturnValueOnce(current.promise)
  const props = reactive({ account: 'a', contact: 'contact', caseId: 'first' })
  const state = mount(InboxIntakeSummary, props)
  await settle()
  props.account = 'b'
  props.caseId = 'second'
  await settle()
  expect(state.rows).toEqual([])
  current.resolve(summary('second', 'Current result'))
  await settle()
  old.resolve(summary('first', 'Stale private result'))
  await settle()
  expect(root.textContent).toContain('Current result')
  expect(root.textContent).not.toContain('Stale private result')
  expect(mocks.summary).toHaveBeenLastCalledWith('b', 'contact', 'second')
})

it('rejects mismatched case summaries and retries only the selected case', async () => {
  mocks.summary
    .mockResolvedValueOnce(summary('wrong-case', 'Never shown'))
    .mockRejectedValueOnce(new Error('offline'))
    .mockResolvedValue(summary('selected', 'Verified result'))
  const state = mount(InboxIntakeSummary, {
    account: 'account',
    contact: 'contact',
    caseId: 'selected',
  })
  await settle()
  expect(state.failed).toBe(true)
  expect(root.textContent).not.toContain('Never shown')
  await state.load()
  expect(state.failed).toBe(true)
  await state.load()
  await settle()
  expect(state.failed).toBe(false)
  expect(root.textContent).toContain('Verified result')
  expect(mocks.summary).toHaveBeenCalledTimes(3)
  expect(
    mocks.summary.mock.calls.every(
      (call) =>
        JSON.stringify(call) ===
        JSON.stringify(['account', 'contact', 'selected']),
    ),
  ).toBe(true)
})
