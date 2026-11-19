// @vitest-environment happy-dom
import { createApp, h, nextTick, ref } from 'vue'
import { expect, it } from 'vitest'
import InboxTabs from './InboxTabs.vue'

it('moves selection and focus together using arrows and Home/End', async () => {
  const model = ref('rules')
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp({
    render: () =>
      h(InboxTabs, {
        modelValue: model.value,
        'onUpdate:modelValue': (value: string) => {
          model.value = value
        },
        label: 'Inbox tools',
        items: [
          { value: 'rules', label: 'Rules' },
          { value: 'macros', label: 'Macros' },
          { value: 'history', label: 'History' },
        ],
      }),
  })
  app.mount(root)
  try {
    const tabs = root.querySelectorAll<HTMLButtonElement>('[role=tab]')
    tabs[0].focus()
    tabs[0].dispatchEvent(
      new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }),
    )
    await nextTick()
    expect(model.value).toBe('macros')
    expect(document.activeElement).toBe(tabs[1])
    expect(tabs[1].getAttribute('aria-selected')).toBe('true')
    expect(tabs[0].tabIndex).toBe(-1)
    tabs[1].dispatchEvent(
      new KeyboardEvent('keydown', { key: 'End', bubbles: true }),
    )
    await nextTick()
    expect(model.value).toBe('history')
    expect(document.activeElement).toBe(tabs[2])
    tabs[2].dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Home', bubbles: true }),
    )
    await nextTick()
    expect(model.value).toBe('rules')
    tabs[0].dispatchEvent(
      new KeyboardEvent('keydown', { key: 'ArrowLeft', bubbles: true }),
    )
    await nextTick()
    expect(model.value).toBe('history')
  } finally {
    app.unmount()
    root.remove()
  }
})
