// @vitest-environment happy-dom
import { expect, it } from 'vitest'
import { cloneInboxPolicy, inboxTextEntry } from './inboxInteraction'
it('isolates saved policy scalars and nested shifts from discarded editor changes', () => {
  const saved = {
    first_reply_minutes: 30,
    shifts: [{ Day: 1, StartMinute: 480 }],
  }
  const editor = cloneInboxPolicy(saved)
  editor.first_reply_minutes = 99
  editor.shifts[0]!.StartMinute = 900
  expect(saved).toEqual({
    first_reply_minutes: 30,
    shifts: [{ Day: 1, StartMinute: 480 }],
  })
})
it('does not treat text entry as an inbox shortcut surface', () => {
  for (const tag of ['input', 'textarea', 'select']) {
    const target = document.createElement(tag)
    expect(inboxTextEntry({ target } as unknown as KeyboardEvent)).toBe(true)
  }
  const target = document.createElement('div')
  target.contentEditable = 'true'
  expect(inboxTextEntry({ target } as unknown as KeyboardEvent)).toBe(true)
  expect(
    inboxTextEntry({
      target: document.createElement('button'),
    } as unknown as KeyboardEvent),
  ).toBe(false)
})
