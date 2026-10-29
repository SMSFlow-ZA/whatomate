// @vitest-environment happy-dom
import { createApp, h, nextTick, ref } from 'vue'
import { describe, it, expect, vi } from 'vitest'
import ChannelOptions from './ChannelOptions.vue'
import { initialChannelOptions } from '@/lib/channels'
vi.mock('@/services/api', () => ({ default: { get: vi.fn().mockResolvedValue({ data: { connections: [] } }), post: vi.fn().mockResolvedValue({ data: { segments: 1, encoding: 'GSM-7' } }) } }))

describe('workflow timeout control', () => {
  it('saves the administrator-selected timeout as a number', async () => {
    const value = ref({ ...initialChannelOptions(), fallback_enabled: true, timeout_enabled: true })
    const root = document.createElement('div'); document.body.append(root)
    const app = createApp({ render: () => h(ChannelOptions, { modelValue: value.value, 'onUpdate:modelValue': v => { value.value = v } }) })
    app.mount(root)
    try {
      await nextTick()
      const input = root.querySelector<HTMLInputElement>('input[type=number]')!
      input.value = '27'; input.dispatchEvent(new Event('input', { bubbles: true }))
      await nextTick(); await nextTick()
      expect(value.value.timeout_minutes).toBe(27)
    } finally { app.unmount(); root.remove() }
  })
})
