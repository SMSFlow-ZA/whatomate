<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import api from '@/services/api'
import { type ChannelConnection, type ChannelOptions, initialChannelOptions } from '@/lib/channels'
const model = defineModel<ChannelOptions>({ required: true })
const draft = reactive<ChannelOptions>({ ...initialChannelOptions(), ...model.value })
watch(() => model.value, value => Object.assign(draft,value), { deep: true })
watch(draft, value => { model.value = { ...value } }, { deep: true })
const props = withDefaults(defineProps<{ text?: string; rich?: boolean; allowDirectSms?: boolean; disabled?: boolean }>(), { text: '', rich: false, allowDirectSms: false, disabled: false })
const connections = ref<ChannelConnection[]>([]), loaded = ref(false), preview = ref(''), previewError = ref('')
const active = computed(() => draft.channel === 'sms' || draft.fallback_enabled)
let previewGeneration = 0
let timer: ReturnType<typeof setTimeout> | undefined
watch(() => [props.text, props.rich, draft.channel, draft.sms_text, draft.connection_id, active.value], () => {
  const generation = ++previewGeneration
  clearTimeout(timer); preview.value = ''; previewError.value = ''
  const text = draft.channel === 'sms' ? props.text : (draft.sms_text || (props.rich ? '' : props.text))
  if (!active.value || !text) return
  timer = setTimeout(async () => {
    const selected = connections.value.find(c => c.id === draft.connection_id)
    try {
      const r = await api.post('/channels/preview', { text, max_segments: selected?.max_segments ?? 3 })
      const p = r.data.data || r.data
      if (generation !== previewGeneration) return
      preview.value = `${p.segments} SMS segment${p.segments === 1 ? '' : 's'} · ${p.encoding}`
    } catch { if (generation !== previewGeneration) return; previewError.value = 'This SMS exceeds the connection limit or cannot be sent. Edit the text before sending.' }
  }, 300)
})
onMounted(async () => {
  try { const r = await api.get('/channels'); connections.value = (r.data.data || r.data).connections || [] }
  catch { connections.value = [] }
  finally { loaded.value = true }
})
onUnmounted(() => clearTimeout(timer))
</script>
<template>
  <fieldset v-if="connections.length || active" :disabled="disabled" class="mb-3 space-y-3 rounded-lg border p-3 text-sm">
    <legend class="px-1 font-medium">Delivery channel</legend>
    <div class="flex flex-wrap items-center gap-3">
      <label class="flex items-center gap-2">Send using
        <select v-model="draft.channel" class="rounded border bg-background p-2">
          <option value="whatsapp">WhatsApp</option><option v-if="!rich || allowDirectSms" value="sms">SMS</option>
        </select>
      </label>
      <label v-if="draft.channel === 'whatsapp'" class="flex items-center gap-2"><input v-model="draft.fallback_enabled" type="checkbox" />Use SMS if WhatsApp cannot deliver</label>
    </div>
    <template v-if="active">
      <label class="block">SMS connection
        <select v-model="draft.connection_id" class="mt-1 block w-full rounded border bg-background p-2" required>
          <option disabled value="">Select a connection</option><option v-for="c in connections" :key="c.id" :value="c.id">{{ c.name }} · maximum {{ c.max_segments }} segments</option>
        </select>
      </label>
      <label v-if="draft.fallback_enabled && draft.channel === 'whatsapp'" class="block">SMS version <span v-if="rich">(required)</span>
        <textarea v-model="draft.sms_text" :required="rich" class="mt-1 block w-full rounded border bg-background p-2" rows="2" :placeholder="rich ? 'Write a plain-text alternative' : 'Leave empty to reuse the plain-text message'" />
      </label>
      <template v-if="draft.channel === 'whatsapp' && draft.fallback_enabled">
        <label class="flex items-center gap-2"><input v-model="draft.timeout_enabled" type="checkbox" />Also fall back if WhatsApp remains undelivered</label>
        <label v-if="draft.timeout_enabled" class="flex items-center gap-2">Wait <input v-model.number="draft.timeout_minutes" type="number" min="1" max="1440" class="w-20 rounded border bg-background p-2" /> minutes</label>
        <p class="text-xs text-muted-foreground">Delivered-but-unread messages never trigger SMS. Both messages may arrive if WhatsApp delivers after SMS is submitted.</p>
      </template>
      <label class="flex items-start gap-2"><input v-model="draft.consent_confirmed" type="checkbox" class="mt-1" />I confirm these recipients may receive this SMS. SMSFlow charges apply to my connected account.</label>
      <p v-if="preview" class="text-xs text-muted-foreground" aria-live="polite">{{ preview }}</p>
      <p v-if="previewError" class="text-xs text-destructive" role="alert">{{ previewError }}</p>
    </template>
  </fieldset>
  <p v-else-if="loaded" class="mb-2 text-xs text-muted-foreground">SMS is available after an administrator connects SMSFlow in Settings → Channels.</p>
</template>
