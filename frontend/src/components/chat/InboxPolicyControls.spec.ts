// @vitest-environment happy-dom
import { createApp, h, nextTick, reactive } from 'vue'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en.json'
import InboxConditionBuilder from './InboxConditionBuilder.vue'
import InboxSLAIndicators from './InboxSLAIndicators.vue'
const mock = vi.hoisted(() => ({ fieldDefinitions: vi.fn() }))
vi.mock('@/services/inbox', () => ({ inboxService: mock }))
const mounts: { app: ReturnType<typeof createApp>; root: HTMLElement }[] = []
async function settle() {
  for (let i = 0; i < 10; i++) {
    await Promise.resolve()
    await nextTick()
  }
}
async function mount(component: any, props: any) {
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp({ render: () => h(component, props) })
  app.use(createI18n({ legacy: false, locale: 'en', messages: { en } }))
  app.mount(root)
  mounts.push({ app, root })
  await settle()
  return root
}
beforeEach(() => {
  vi.resetAllMocks()
  mock.fieldDefinitions.mockResolvedValue({
    data: { data: { definitions: [] } },
  })
})
afterEach(() => {
  for (const { app, root } of mounts.splice(0)) {
    app.unmount()
    root.remove()
  }
})
it('preserves legacy status conditions while defaulting policy conditions to permitted priority', async () => {
  const legacy = reactive<any>({ modelValue: { predicates: [] } })
  const root = await mount(InboxConditionBuilder, legacy)
  root
    .querySelectorAll('button')
    .item(root.querySelectorAll('button').length - 1)
    .click()
  await settle()
  expect(legacy.modelValue.predicates).toEqual([
    { field: 'status', operator: 'eq', value: 'open' },
  ])
  const scoped = reactive<any>({
    modelValue: { predicates: [] },
    allowedFields: ['priority', 'custom_field'],
  })
  const scopedRoot = await mount(InboxConditionBuilder, scoped)
  scopedRoot.querySelector('button')!.click()
  await settle()
  expect(scoped.modelValue.predicates).toEqual([
    { field: 'priority', operator: 'eq', value: 'normal' },
  ])
  expect(
    [...scopedRoot.querySelectorAll('select option')].some(
      (option) => (option as HTMLOptionElement).value === 'status',
    ),
  ).toBe(false)
})
it('cannot add a disallowed fallback field when only custom fields are supported and none exist', async () => {
  const props = reactive<any>({
    modelValue: { predicates: [] },
    allowedFields: ['custom_field'],
  })
  const root = await mount(InboxConditionBuilder, props)
  expect(root.querySelector('button')!.disabled).toBe(true)
  expect(props.modelValue.predicates).toEqual([])
})
it('retains an unavailable field, operator and false value during metadata failure and recovery', async () => {
  mock.fieldDefinitions.mockRejectedValueOnce(new Error('metadata lookup'))
  const condition = {
    field: 'custom_field',
    definition_id: 'retained-field',
    operator: 'eq',
    value: false,
  }
  const validity = vi.fn()
  const props = reactive<any>({
    modelValue: { predicates: [condition] },
    onValidity: validity,
  })
  const root = await mount(InboxConditionBuilder, props)
  expect(root.textContent).toContain('retained-field')
  expect(validity).toHaveBeenLastCalledWith(false)
  const selected = [...root.querySelectorAll('select')].find(
    (el) => el.value === 'custom:retained-field',
  )!
  selected.dispatchEvent(new Event('change', { bubbles: true }))
  expect(props.modelValue.predicates).toEqual([condition])
  expect(root.querySelector('input')).toBeNull()
  mock.fieldDefinitions.mockResolvedValueOnce({
    data: {
      data: {
        definitions: [
          {
            id: 'retained-field',
            label: 'Recovered consent',
            kind: 'boolean',
            scope: 'case',
          },
        ],
      },
    },
  })
  const retry = [...root.querySelectorAll('button')].find(
    (el) => el.textContent?.trim() === en.common.retry,
  )!
  retry.click()
  await settle()
  expect(root.textContent).toContain('Recovered consent')
  expect(validity).toHaveBeenLastCalledWith(true)
  expect(props.modelValue.predicates).toEqual([condition])
})
it('does not expose a generic value editor while custom-field metadata is loading or absent', async () => {
  let finish!: (value: unknown) => void
  mock.fieldDefinitions.mockReturnValueOnce(
    new Promise((resolve) => {
      finish = resolve
    }),
  )
  const condition = {
    field: 'custom_field',
    definition_id: 'missing-field',
    operator: 'gte',
    value: 0,
  }
  const props = reactive<any>({ modelValue: { predicates: [condition] } })
  const root = await mount(InboxConditionBuilder, props)
  expect(root.textContent).toContain(
    'Loading details for recorded field missing-field',
  )
  finish({ data: { data: { definitions: [] } } })
  await settle()
  expect(root.textContent).toContain(
    'Details for recorded field missing-field are unavailable',
  )
  expect(
    [...root.querySelectorAll('select')].find((el) => el.value === 'gte')
      ?.disabled,
  ).toBe(true)
  expect(root.querySelector('input')).toBeNull()
  expect(props.modelValue.predicates).toEqual([condition])
})
it('starts a boolean condition with false and ignores archived fields for new conditions', async () => {
  mock.fieldDefinitions.mockResolvedValueOnce({
    data: {
      data: {
        definitions: [
          { id: 'old', kind: 'text', archived_at: '2026-01-01' },
          {
            id: 'current',
            kind: 'boolean',
            label: 'Synthetic consent',
            archived_at: null,
          },
        ],
      },
    },
  })
  const props = reactive<any>({
    modelValue: { predicates: [] },
    allowedFields: ['custom_field'],
  })
  const root = await mount(InboxConditionBuilder, props)
  root.querySelector('button')!.click()
  await settle()
  expect(props.modelValue.predicates).toEqual([
    {
      field: 'custom_field',
      definition_id: 'current',
      operator: 'eq',
      value: false,
    },
  ])
})
it('renders assigned policy names as literal text and keeps compact queue indicators uncluttered', async () => {
  const props = reactive<any>({
    sla: {
      policy_name: '<img src=x onerror=alert(1)>',
      first_reply: {
        status: 'on_track',
        remaining_seconds: 600,
        timezone: 'UTC',
        deadline: '2027-01-01T10:00:00Z',
      },
    },
  })
  const root = await mount(InboxSLAIndicators, props)
  expect(root.textContent).toContain('<img src=x onerror=alert(1)>')
  expect(root.querySelector('img')).toBeNull()
  props.compact = true
  await settle()
  expect(root.textContent).not.toContain('<img')
})
