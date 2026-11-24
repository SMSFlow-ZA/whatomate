// @vitest-environment happy-dom
import { createApp, h } from 'vue'
import { expect, it, vi } from 'vitest'
import InboxWorkflowPreview from './InboxWorkflowPreview.vue'
vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: { number: number }) =>
      params ? key + ':' + params.number : key,
  }),
}))

it('announces original action numbers in a remaining-sequence review', () => {
  const root = document.createElement('div')
  const app = createApp({
    render: () =>
      h(InboxWorkflowPreview, {
        purpose: 'sequence',
        startIndex: 5,
        preview: {
          matches: true,
          confirmation: 'synthetic',
          actions: [
            { type: 'note', value: 'First remaining action' },
            { type: 'note', value: 'Next remaining action' },
          ],
          send_unavailable: false,
          recipient: { name: 'Synthetic customer', phone: '+12025550123' },
          account: 'Synthetic account',
        },
      }),
  })
  app.mount(root)
  try {
    expect(root.querySelector('ol')?.getAttribute('start')).toBe('6')
    expect(
      [...root.querySelectorAll('ol li .sr-only')].map((item) =>
        item.textContent?.trim(),
      ),
    ).toEqual([
      'inboxIntegrationHistory.actionNumber:6:',
      'inboxIntegrationHistory.actionNumber:7:',
    ])
  } finally {
    app.unmount()
  }
})

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

it('shows typed integration intent and deferred values without exposing an internal definition ID', () => {
  const root = document.createElement('div')
  const id = '2d594d54-c4c4-4709-9d08-bb1a9a57f5e3'
  const app = createApp({
    render: () =>
      h(InboxWorkflowPreview, {
        preview: {
          matches: true,
          confirmation: 'synthetic',
          send_unavailable: false,
          actions: [{ type: 'integration', value: id }],
          recipient: { name: 'Synthetic customer', phone: '+12025550123' },
          account: 'Support',
          integrations: {
            0: {
              id,
              name: 'Create service request',
              method: 'POST',
              destination: 'https://service.example.invalid',
              available: true,
              inputs: {
                count: 0,
                consent: false,
                customer: '<img src=x onerror=alert(1)>',
              },
              deferred_inputs: { ticket: ['case.field.ticket'] },
              outputs: [
                {
                  id: 'field',
                  key: 'case.field.ticket',
                  label: 'Ticket reference',
                  scope: 'case',
                },
              ],
            },
          },
        },
      }),
  })
  app.mount(root)
  try {
    expect(root.textContent).toContain('Create service request')
    expect(root.textContent).toContain('POST · https://service.example.invalid')
    expect(root.textContent).toContain('0')
    expect(root.textContent).toContain('false')
    expect(root.textContent).toContain('case.field.ticket')
    expect(root.textContent).toContain('inboxIntegrations.deferredHelp')
    expect(root.textContent).toContain('Ticket reference')
    expect(root.textContent).not.toContain(id)
    expect(root.querySelector('img,script,a')).toBeNull()
  } finally {
    app.unmount()
  }
})

it('labels a partial reply instead of presenting pending values as approved message content', () => {
  const root = document.createElement('div')
  const app = createApp({
    render: () =>
      h(InboxWorkflowPreview, {
        preview: {
          matches: true,
          confirmation: 'synthetic',
          send_unavailable: false,
          actions: [],
          recipient: { name: 'Synthetic customer', phone: '+12025550123' },
          account: 'Support',
          reply: {
            text: 'Ticket …',
            deferred_variables: ['case.field.ticket'],
          },
        },
      }),
  })
  app.mount(root)
  try {
    expect(root.textContent).toContain('inboxIntegrations.deferredReply')
    expect(root.textContent).toContain('case.field.ticket')
    expect(root.textContent).toContain('Ticket …')
  } finally {
    app.unmount()
  }
})
