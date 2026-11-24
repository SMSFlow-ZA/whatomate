// @vitest-environment happy-dom
import { createApp, h } from 'vue'
import { expect, it, vi } from 'vitest'
import InboxWorkflowPreview from './InboxWorkflowPreview.vue'
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

it('shows the selected agent name instead of an internal identifier in a review', () => {
  const id = '73cb156b-dc74-499c-a537-9a535aef2724'
  const root = document.createElement('div')
  const app = createApp({
    render: () =>
      h(InboxWorkflowPreview, {
        users: [{ id, full_name: 'Example agent' }],
        preview: {
          matches: true,
          confirmation: 'synthetic-confirmation',
          actions: [{ type: 'assign', value: id }],
          send_unavailable: false,
          recipient: { name: 'Example customer', phone: '+12025550190' },
          account: 'Example inbox',
        },
      }),
  })
  app.mount(root)
  try {
    expect(root.textContent).toContain('Example agent')
    expect(root.textContent).not.toContain(id)
  } finally {
    app.unmount()
  }
})

it('shows rich saved-reply intent even when blocked, without following links', () => {
  const root = document.createElement('div')
  const app = createApp({
    render: () =>
      h(InboxWorkflowPreview, {
        purpose: 'reply',
        preview: {
          matches: true,
          confirmation: '',
          actions: [],
          send_unavailable: true,
          recipient: { name: 'Example customer', phone: '+12025550190' },
          account: 'Support',
          reply: {
            text: 'Hi Example',
            canned_name: 'Saved greeting',
            interactive: {
              type: 'cta_url',
              body: 'Hi Example',
              button_text: 'Open case',
              url: 'https://example.invalid/case',
            },
          },
        },
      }),
  })
  app.mount(root)
  try {
    expect(root.textContent).toContain('Saved greeting')
    expect(root.textContent).toContain('Open case')
    expect(root.textContent).toContain('https://example.invalid/case')
    expect(root.querySelector('a')).toBeNull()
  } finally {
    app.unmount()
  }
})
