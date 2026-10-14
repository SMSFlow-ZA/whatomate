import { ref } from 'vue'
import { defineStore } from 'pinia'
import { api } from '@/services/api'
import { brandPalette, hsl, validateBranding } from '@/utils/branding'
export interface Branding { display_name: string; accent_color: string; logo_url: string; favicon_url: string; custom: boolean }
const base = ((window as any).__BASE_PATH__ ?? '').replace(/\/$/, '')
export const defaultBranding: Branding = { display_name: 'SMSFlow', accent_color: '#00AEEF', logo_url: base + '/branding/smsflow-logo.png', favicon_url: base + '/branding/smsflow-favicon.svg', custom: false }
export const useBrandingStore = defineStore('branding', () => {
  const current = ref<Branding>({ ...defaultBranding })
  const loadError = ref(false)
  function apply(value: Branding) {
    current.value = validateBranding(value.display_name, value.accent_color) ? value : { ...defaultBranding }
    const p = brandPalette(current.value.accent_color), style = document.documentElement.style
    style.setProperty('--primary', hsl(p.accent)); style.setProperty('--primary-foreground', hsl(p.foreground))
    style.setProperty('--brand-light-ring', hsl(p.lightText)); style.setProperty('--brand-dark-ring', hsl(p.darkText))
    style.setProperty('--ring', 'var(--brand-ring)'); style.setProperty('--brand-accent', p.accent); style.setProperty('--brand-hover', p.hover)
    style.setProperty('--brand-foreground', p.foreground); style.setProperty('--brand-light-text', p.lightText); style.setProperty('--brand-dark-text', p.darkText)
    document.title = current.value.display_name
    const icon = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
    if (icon) { icon.href = current.value.favicon_url; icon.type = current.value.favicon_url.endsWith('.svg') ? 'image/svg+xml' : 'image/png' }
    const description = document.querySelector<HTMLMetaElement>('meta[name="description"]')
    if (description) description.content = current.value.display_name + ' · WhatsApp Business workspace'
  }
  async function load() { try { const r = await api.get('/branding', { timeout: 5000 }); apply(r.data.data); loadError.value = false } catch { loadError.value = true; apply({ ...defaultBranding }) } }
  async function save(form: FormData) { const r = await api.put('/branding', form, { headers: { 'Content-Type': 'multipart/form-data' } }); apply(r.data.data) }
  async function reset() { const r = await api.delete('/branding'); apply(r.data.data) }
  return { current, loadError, load, save, reset, apply }
})
