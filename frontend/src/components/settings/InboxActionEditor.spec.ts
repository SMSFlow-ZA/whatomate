// @vitest-environment happy-dom
import { createApp, h, nextTick, ref } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
import InboxActionEditor from './InboxActionEditor.vue'
import InboxMessageText from './InboxMessageText.vue'
import type { InboxAction } from '@/services/inbox'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/components/chat/InboxMessageVariables.vue', () => ({
  default: {
    template:
      '<button type="button" @click="$emit(\'insert\', \'{{contact_name}}\')">Insert variable</button>',
  },
}))

const cleanups: (() => void)[] = []
afterEach(() => {
  cleanups.splice(0).forEach((cleanup) => cleanup())
})
const settle = async () => {
  for (let i = 0; i < 6; i++) {
    await Promise.resolve()
    await nextTick()
  }
}

it('preserves saved template mappings when template lookups arrive later', async () => {
  const action = ref<InboxAction>({
    type: 'send_template',
    value: 'template-one',
    personalise: true,
    body_parameters: { '1': '{{contact_name}}' },
    header_parameters: { '1': 'CASE' },
  })
  const templates = ref<
    {
      id: string
      name: string
      language: string
      body_content: string
      header_content: string
    }[]
  >([])
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp({
    render: () =>
      h(InboxActionEditor, {
        modelValue: action.value,
        'onUpdate:modelValue': (value) => {
          action.value = value
        },
        users: [],
        tags: [],
        templates: templates.value,
      }),
  })
  app.mount(root)
  cleanups.push(() => {
    app.unmount()
    root.remove()
  })
  await settle()
  expect(action.value.body_parameters?.['1']).toBe('{{contact_name}}')
  templates.value = [
    {
      id: 'template-one',
      name: 'update',
      language: 'en',
      body_content: 'Hello {{1}}',
      header_content: 'Case {{1}}',
    },
  ]
  await settle()
  const inputs = [...root.querySelectorAll('textarea')]
  expect(inputs.map((input) => input.value)).toEqual([
    'CASE',
    '{{contact_name}}',
  ])
  inputs[1]!.value = 'Updated body'
  inputs[1]!.dispatchEvent(new Event('input', { bubbles: true }))
  await settle()
  expect(action.value.body_parameters?.['1']).toBe('Updated body')
  expect(action.value.header_parameters?.['1']).toBe('CASE')
})

it('inserts a variable at the selection, restores caret focus and opts in explicitly', async () => {
  const text = ref('Hello selected friend')
  const personalised = vi.fn()
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp({
    render: () =>
      h(InboxMessageText, {
        modelValue: text.value,
        'onUpdate:modelValue': (value) => {
          text.value = value
        },
        label: 'Reply',
        variables: true,
        onPersonalised: personalised,
      }),
  })
  app.mount(root)
  cleanups.push(() => {
    app.unmount()
    root.remove()
  })
  const input = root.querySelector('textarea')!
  input.focus()
  input.setSelectionRange(6, 14)
  root.querySelector('button')!.click()
  await settle()
  expect(text.value).toBe('Hello {{contact_name}} friend')
  expect(personalised).toHaveBeenCalledTimes(1)
  expect(document.activeElement).toBe(input)
  expect(input.selectionStart).toBe(22)
})

it('keeps legacy reply text literal until the editor opts in', async () => {
  const action = ref<InboxAction>({
    type: 'send_text',
    value: 'Literal {{contact_name}}',
  })
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp({
    render: () =>
      h(InboxActionEditor, {
        modelValue: action.value,
        'onUpdate:modelValue': (value) => {
          action.value = value
        },
        users: [],
        tags: [],
        templates: [],
      }),
  })
  app.mount(root)
  cleanups.push(() => {
    app.unmount()
    root.remove()
  })
  await settle()
  expect(action.value.personalise).toBeUndefined()
  const check = root.querySelector<HTMLInputElement>('input[type="checkbox"]')!
  check.checked = true
  check.dispatchEvent(new Event('change', { bubbles: true }))
  await settle()
  expect(action.value.personalise).toBe(true)
  expect(action.value.value).toBe('Literal {{contact_name}}')
})
