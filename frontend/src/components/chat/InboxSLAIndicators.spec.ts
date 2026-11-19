// @vitest-environment happy-dom
import { createApp, h } from 'vue'
import { expect, it, vi } from 'vitest'
import InboxSLAIndicators from './InboxSLAIndicators.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key, locale: { value: 'en' } }),
}))

it('advances active deadlines without changing completed or paused states', () => {
  const expired = new Date(Date.now() - 60000).toISOString()
  const root = document.createElement('div')
  const app = createApp({
    render: () =>
      h(InboxSLAIndicators, {
        sla: {
          first_reply: { status: 'met', deadline: expired, remaining_seconds: -60 },
          next_reply: { status: 'on_track', deadline: expired, remaining_seconds: -60 },
          resolution: { status: 'paused', deadline: expired, remaining_seconds: -60 },
        },
      }),
  })
  app.mount(root)
  try {
    expect(
      [...root.querySelectorAll('li')].map((node) => node.dataset.state),
    ).toEqual(['met', 'breached', 'paused'])
  } finally {
    app.unmount()
  }
})
