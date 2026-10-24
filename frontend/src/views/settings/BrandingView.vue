<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { useBrandingStore } from '@/stores/branding'
import { brandPalette, validateBranding } from '@/utils/branding'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { toast } from 'vue-sonner'
const auth = useAuthStore(), branding = useBrandingStore()
const name = ref(branding.current.display_name), accent = ref(branding.current.accent_color)
const logo = ref<File>(), favicon = ref<File>(), logoPreview = ref(''), faviconPreview = ref('')
const removeLogo = ref(false), removeFavicon = ref(false), busy = ref(false), resetOpen = ref(false)
const loading = ref(true)
const canWrite = computed(() => !loading.value && !branding.loadError && auth.hasPermission('settings.branding', 'write'))
const palette = computed(() => brandPalette(accent.value)), valid = computed(() => validateBranding(name.value, accent.value))
function release() { for (const url of [logoPreview.value, faviconPreview.value]) if (url) URL.revokeObjectURL(url) }
function reload() {
  release(); logo.value = undefined; favicon.value = undefined; logoPreview.value = ''; faviconPreview.value = ''
  removeLogo.value = false; removeFavicon.value = false; name.value = branding.current.display_name; accent.value = branding.current.accent_color
}
function selectImage(event: Event, kind: 'logo' | 'favicon') {
  const input = event.target as HTMLInputElement, file = input.files?.[0]
  if (!file) return
  if (!['image/png', 'image/jpeg'].includes(file.type) || file.size > (kind === 'logo' ? 2 * 1024 * 1024 : 512 * 1024)) { toast.error('Choose PNG/JPEG: logo up to 2 MiB, favicon up to 512 KiB.'); input.value = ''; return }
  if (kind === 'logo') { if (logoPreview.value) URL.revokeObjectURL(logoPreview.value); logo.value = file; logoPreview.value = URL.createObjectURL(file); removeLogo.value = false }
  else { if (faviconPreview.value) URL.revokeObjectURL(faviconPreview.value); favicon.value = file; faviconPreview.value = URL.createObjectURL(file); removeFavicon.value = false }
}
async function save() {
  if (!canWrite.value || !valid.value) return
  busy.value = true
  const form = new FormData(); form.set('display_name', name.value.trim()); form.set('accent_color', accent.value)
  if (removeLogo.value) form.set('remove_logo', 'true'); else if (logo.value) form.set('logo', logo.value)
  if (removeFavicon.value) form.set('remove_favicon', 'true'); else if (favicon.value) form.set('favicon', favicon.value)
  try { await branding.save(form); reload(); toast.success('Branding saved. Other sessions receive it on their next page load.') }
  catch (e: any) { toast.error(e.response?.data?.message || 'Branding was not saved. Your previous settings remain in place.') } finally { busy.value = false }
}
async function reset() {
  busy.value = true
  try { await branding.reset(); reload(); resetOpen.value = false; toast.success('Default branding restored.') }
  catch { toast.error('Branding could not be reset. Please try again.') } finally { busy.value = false }
}
onUnmounted(release)
onMounted(async () => { await branding.load(); reload(); loading.value = false })
const defaultLogo = computed(() => branding.current.default_logo_url || (((window as any).__BASE_PATH__ ?? '').replace(/\/$/, '') + '/branding/whatomate-logo.svg'))
</script>
<template>
  <div class="h-full overflow-y-auto p-4 md:p-6"><div class="mx-auto max-w-5xl space-y-6 pb-12">
    <header><h1 class="text-2xl font-semibold text-foreground">Branding</h1><p class="mt-1 text-sm text-muted-foreground">Make this workspace feel like your business. Included on every plan and trial.</p></header>
    <p v-if="branding.loadError" role="alert" class="text-destructive">Existing branding could not be loaded. Reload this page before editing.</p>
    <div class="grid gap-6 lg:grid-cols-2">
      <form class="space-y-5 rounded-xl border border-border bg-card p-5" @submit.prevent="save">
        <div class="space-y-2"><Label for="brand-name">Display name</Label><Input id="brand-name" v-model="name" :disabled="!canWrite || busy" /><p class="text-xs text-muted-foreground">1–80 characters. This does not rename your organization or WhatsApp business profile.</p></div>
        <div class="space-y-2"><Label for="brand-colour">Primary accent colour</Label><div class="flex gap-3"><input id="brand-colour" v-model="accent" type="color" class="h-9 w-12 rounded border border-border" :disabled="!canWrite || busy" /><Input v-model="accent" aria-label="Accent colour hex value" :disabled="!canWrite || busy" class="font-mono" /></div><p class="text-xs text-muted-foreground">Text adjusts for readable contrast. Status colours keep their meaning.</p></div>
        <div class="space-y-2"><Label for="brand-logo">Logo</Label><input id="brand-logo" type="file" accept="image/png,image/jpeg" :disabled="!canWrite || busy" class="block w-full text-sm text-foreground" @change="selectImage($event, 'logo')" /><p class="text-xs text-muted-foreground">PNG/JPEG, up to 2 MiB and 2048×2048 pixels. Transparent PNG works best.</p><label class="flex items-center gap-2 text-sm text-muted-foreground"><input v-model="removeLogo" type="checkbox" :disabled="!canWrite || busy" />Use the default logo</label></div>
        <div class="space-y-2"><Label for="brand-favicon">Browser icon (favicon)</Label><input id="brand-favicon" type="file" accept="image/png,image/jpeg" :disabled="!canWrite || busy" class="block w-full text-sm text-foreground" @change="selectImage($event, 'favicon')" /><p class="text-xs text-muted-foreground">PNG/JPEG, up to 512 KiB and 2048×2048 pixels. A square icon works best.</p><label class="flex items-center gap-2 text-sm text-muted-foreground"><input v-model="removeFavicon" type="checkbox" :disabled="!canWrite || busy" />Use the default favicon</label></div>
        <p class="text-xs text-muted-foreground">Images use your storage allowance. The purchase portal and invoices stay SMSFlow branded.</p>
        <p v-if="!canWrite" class="text-sm text-muted-foreground">An administrator can change these settings.</p>
        <div v-else class="flex flex-wrap gap-3"><Button type="submit" :disabled="busy || !valid">{{ busy ? 'Saving…' : 'Save branding' }}</Button><Button type="button" variant="outline" :disabled="busy" @click="resetOpen = !resetOpen">Reset to {{ branding.current.default_display_name || 'Whatomate' }} defaults</Button></div>
        <div v-if="resetOpen && canWrite" class="rounded-lg border border-border p-3"><p class="mb-3 text-sm text-foreground">Restore the default name, colours and images? Current uploaded branding images will be removed.</p><Button type="button" :disabled="busy" @click="reset">Confirm reset</Button><Button type="button" variant="ghost" @click="resetOpen = false">Keep my branding</Button></div>
      </form>
      <section class="space-y-4" aria-label="Branding previews"><h2 class="text-sm font-semibold text-foreground">Preview before saving</h2>
        <div v-for="mode in ['light', 'dark']" :key="mode" class="overflow-hidden rounded-xl border border-border" :style="{ background: mode === 'light' ? '#f0f3f6' : '#0a0a0b', color: mode === 'light' ? '#073756' : '#fafafa' }">
          <div class="flex items-center gap-3 border-b p-4" :style="{ borderColor: mode === 'light' ? '#dbe4ea' : '#263343' }"><img :src="removeLogo ? defaultLogo : logoPreview || branding.current.logo_url" :alt="name || 'Workspace logo'" class="h-9 max-w-[120px] rounded bg-white p-1 object-contain" /><span class="min-w-0 truncate font-semibold">{{ name || 'Workspace name' }}</span><img v-if="!removeFavicon" :src="faviconPreview || branding.current.favicon_url" alt="Favicon preview" class="ml-auto h-5 w-5 rounded bg-white object-contain" /></div>
          <div class="space-y-4 p-5"><p class="text-xs uppercase tracking-widest" :style="{ color: mode === 'light' ? palette.lightText : palette.darkText }">{{ mode }} mode</p><h3 class="text-lg font-semibold">Welcome to {{ name || 'your workspace' }}</h3><p class="text-sm opacity-75">Connect with customers and manage your team.</p><div class="flex items-center gap-4"><span class="rounded-lg px-4 py-2 text-sm font-semibold" :style="{ background: palette.accent, color: palette.foreground }">Continue</span><span class="text-sm underline" :style="{ color: mode === 'light' ? palette.lightText : palette.darkText }">View conversations</span></div></div>
        </div><p class="text-xs text-muted-foreground">Images are previewed locally until saved. Light/dark preferences and layout stay the same.</p>
      </section>
    </div>
  </div></div>
</template>
