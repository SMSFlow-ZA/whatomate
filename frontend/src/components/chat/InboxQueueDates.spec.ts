// @vitest-environment happy-dom
import { createApp, h, nextTick, ref } from 'vue'
import { afterEach, expect, it, vi } from 'vitest'
import InboxQueueDates from './InboxQueueDates.vue'
import type { InboxQueueDiscovery } from '@/services/inbox'
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const cleanups: (() => void)[] = []
afterEach(() => {
  cleanups.splice(0).forEach((fn) => fn())
})
function setup(
  value: InboxQueueDiscovery = {
    order: 'oldest',
    timezone: 'UTC',
    opened_from: '2026-11-01T00:00:00Z',
  },
) {
  const model = ref(value),
    disabled = ref(false),
    updates = vi.fn((next: InboxQueueDiscovery) => {
      model.value = next
    })
  const root = document.createElement('div')
  document.body.append(root)
  const app = createApp({
    render: () =>
      h(InboxQueueDates, {
        modelValue: model.value,
        disabled: disabled.value,
        'onUpdate:modelValue': updates,
      }),
  })
  app.mount(root)
  cleanups.push(() => {
    app.unmount()
    root.remove()
  })
  const dates = () => [
    ...root.querySelectorAll<HTMLInputElement>('input[type=date]'),
  ]
  const button = (key: string) =>
    [...root.querySelectorAll<HTMLButtonElement>('button')].find(
      (b) => b.textContent?.trim() === key,
    )!
  return { model, disabled, updates, root, dates, button }
}
it('isolates drafts, cancels edits and applies local dates only on submission', async () => {
  const f = setup()
  f.dates()[0].value = '2026-11-02'
  f.dates()[0].dispatchEvent(new Event('input'))
  await nextTick()
  expect(f.updates).not.toHaveBeenCalled()
  expect(f.model.value.opened_from).toBe('2026-11-01T00:00:00Z')
  f.button('common.cancel').click()
  await nextTick()
  expect(f.dates()[0].value).toBe('2026-11-01')
  f.dates()[1].value = '2026-11-02'
  f.dates()[1].dispatchEvent(new Event('input'))
  await nextTick()
  f.root
    .querySelector('form')!
    .dispatchEvent(new Event('submit', { cancelable: true }))
  await nextTick()
  expect(f.model.value.opened_before).toBe('2026-11-03T00:00:00.000Z')
  expect(f.model.value.order).toBe('oldest')
})
it('preserves exact API instants until an explicit replacement or reset', async () => {
  const original = {
    order: 'oldest' as const,
    timezone: 'UTC',
    opened_from: '2026-11-01T12:30:00.000001Z',
  }
  const f = setup(original)
  expect(f.root.textContent).toContain(original.opened_from)
  expect(f.dates()).toHaveLength(0)
  expect(f.updates).not.toHaveBeenCalled()
  f.button('inboxDiscovery.replaceDates').click()
  await nextTick()
  expect(f.dates()[0].value).toBe('')
  expect(f.updates).not.toHaveBeenCalled()
  expect(document.activeElement).toBe(f.dates()[0])
  f.button('common.cancel').click()
  await nextTick()
  expect(f.dates()).toHaveLength(0)
  expect(f.model.value).toEqual(original)
  f.button('inboxDiscovery.clearDates').click()
  await nextTick()
  expect(f.model.value).toEqual({ order: 'oldest' })
  expect(document.activeElement).toBe(f.dates()[0])
})
it('shows a focusable associated range error and recovers without losing other draft values', async () => {
  const f = setup()
  f.dates()[0].value = '2026-11-03'
  f.dates()[0].dispatchEvent(new Event('input'))
  f.dates()[1].value = '2026-11-02'
  f.dates()[1].dispatchEvent(new Event('input'))
  await nextTick()
  const submit = () =>
    f.root
      .querySelector('form')!
      .dispatchEvent(new Event('submit', { cancelable: true }))
  submit()
  await nextTick()
  await nextTick()
  const error = f.root.querySelector<HTMLElement>('[role=alert]')!
  expect(error.textContent).toContain('inboxDiscovery.error.range')
  expect(document.activeElement).toBe(error)
  expect(f.dates()[0].getAttribute('aria-describedby')).toBe(error.id)
  expect(f.updates).not.toHaveBeenCalled()
  f.dates()[1].value = '2026-11-03'
  f.dates()[1].dispatchEvent(new Event('input'))
  await nextTick()
  submit()
  await nextTick()
  expect(f.root.querySelector('[role=alert]')).toBeNull()
  expect(f.model.value.opened_before).toBe('2026-11-04T00:00:00.000Z')
})
it('does not emit changes when the editor is disabled', async () => {
  const f = setup()
  f.disabled.value = true
  await nextTick()
  expect(f.root.querySelectorAll('fieldset[disabled]')).toHaveLength(2)
  expect(f.button('inboxDiscovery.applyDates').disabled).toBe(true)
  expect(f.updates).not.toHaveBeenCalled()
})
