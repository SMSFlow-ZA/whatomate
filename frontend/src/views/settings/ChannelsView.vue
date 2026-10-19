<script setup lang="ts">
import { onMounted, ref, computed } from 'vue'
import api from '@/services/api'
import { type ChannelConnection } from '@/lib/channels'
import { useAuthStore } from '@/stores/auth'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { toast } from 'vue-sonner'
const auth = useAuthStore(), connections = ref<ChannelConnection[]>([]), busy = ref(false), error = ref('')
const name = ref('SMSFlow'), clientID = ref(''), clientSecret = ref(''), maxSegments = ref(3), editing = ref<ChannelConnection | null>(null)
const balanceThreshold = ref('100')
const encrypted = ref(false), botFlowID = ref(''), botConsent = ref(false), flows = ref<{id:string;name:string;channel:string;enabled:boolean}[]>([])
const canWrite = computed(() => auth.hasPermission('settings.channels', 'write'))
async function reload() {
  try {
    const [r, p] = await Promise.all([api.get('/channel-connections'), api.get('/channels/providers')])
    try { const f = await api.get('/chatbot/flows?limit=200'); flows.value = ((f.data.data || f.data).flows || []).filter((f:any) => f.channel === 'sms' && f.enabled) } catch { flows.value = [] }
    connections.value = (r.data.data || r.data).connections || []; encrypted.value = (p.data.data || p.data).credentials_configured
  } catch { error.value = 'Channel settings could not be loaded.' }
}
function edit(c: ChannelConnection) { editing.value = c; balanceThreshold.value = c.balance_threshold || '100'; botFlowID.value = c.bot_flow_id || ''; botConsent.value = c.bot_consent_confirmed || false; name.value = c.name; maxSegments.value = c.max_segments; clientID.value = ''; clientSecret.value = ''; error.value = '' }
function reset() { editing.value = null; balanceThreshold.value = '100'; botFlowID.value = ''; botConsent.value = false; name.value = 'SMSFlow'; maxSegments.value = 3; clientID.value = ''; clientSecret.value = '' }
async function save() {
  if (busy.value) return
  busy.value = true; error.value = ''
  try {
    const credentials = clientID.value || clientSecret.value ? { client_id: clientID.value, client_secret: clientSecret.value } : undefined
    if (!editing.value && !credentials) throw new Error('credentials')
    const body = { name: name.value, provider: 'smsflow', max_segments: maxSegments.value, credentials }
    if (editing.value) await api.put(`/channel-connections/${editing.value.id}`, { ...body, enabled: editing.value.enabled, balance_threshold: balanceThreshold.value, revalidate: editing.value.error_code === 'validation_required', bot_flow_id: botFlowID.value, bot_consent_confirmed: botConsent.value })
    else await api.post('/channel-connections', body)
    reset(); await reload(); toast.success('SMSFlow connection saved.')
  } catch { await reload(); error.value = 'Connection could not be saved. Check the credentials, compatible SMSFlow API and reply-capable route. Existing settings are preserved.' }
  finally { clientID.value = ''; clientSecret.value = ''; busy.value = false }
}
async function toggle(c: ChannelConnection) {
  busy.value = true
  try { await api.put(`/channel-connections/${c.id}`, { name: c.name, max_segments: c.max_segments, enabled: !c.enabled }); await reload() }
  catch { error.value = 'The connection changed or could not be updated. Reload and try again.' }
  finally { busy.value = false }
}
onMounted(reload)
</script>
<template>
  <main class="h-full overflow-y-auto p-4 md:p-6"><div class="mx-auto max-w-4xl space-y-6 pb-12">
    <div><h1 class="text-2xl font-semibold">Channels</h1><p class="mt-2 text-muted-foreground">Connect your SMSFlow account for SMS conversations and optional WhatsApp fallback.</p></div>
    <p class="rounded-lg border p-4 text-sm">SMS usage is charged to your SMSFlow account. Connecting an account sends no SMS. Fallback stays off until you enable it in a sending workflow.</p>
    <p v-if="!encrypted" class="text-sm text-destructive">Connection creation requires host-managed credential encryption.</p>
    <p v-if="error" role="alert" class="text-sm text-destructive">{{ error }}</p>
    <section v-for="c in connections" :key="c.id" class="space-y-2 rounded-lg border p-4">
      <div class="flex flex-wrap items-center justify-between gap-3"><h2 class="font-semibold">{{ c.name }}</h2><span class="text-sm">{{ c.enabled ? 'Enabled' : 'Dispatch disabled' }}</span></div>
      <p class="text-sm text-muted-foreground">{{ c.provider }} · {{ c.max_segments }} segments maximum · {{ c.capabilities.inbound ? 'Replies enabled' : 'No inbound route' }}</p>
      <p v-if="c.balance?.amount" class="text-sm" :class="c.low_balance ? 'text-destructive' : 'text-muted-foreground'">Balance: {{ c.balance.amount }} {{ c.balance.unit === 'currency' ? c.balance.currency : c.balance.unit }}<span v-if="c.low_balance"> · Low balance</span></p>
      <p v-if="c.error_code || c.balance_error_code || c.poll_failures" class="text-sm text-destructive">Action needed: {{ c.error_code || c.balance_error_code || 'event polling failed' }}. Already-submitted messages are retained for reconciliation.</p>
      <div v-if="canWrite" class="flex gap-2"><Button variant="outline" :disabled="busy" @click="edit(c)">Edit / rotate credentials</Button><Button variant="outline" :disabled="busy" @click="toggle(c)">{{ c.enabled ? 'Stop pending SMS dispatch' : 'Enable dispatch' }}</Button></div>
    </section>
    <form v-if="canWrite" class="space-y-4 rounded-lg border p-5" @submit.prevent="save">
      <h2 class="font-semibold">{{ editing ? 'Edit connection' : 'Connect SMSFlow' }}</h2>
      <label class="block text-sm">Connection name<Input v-model="name" class="mt-1" maxlength="100" required /></label>
      <label class="block text-sm">Client ID<Input v-model="clientID" class="mt-1" autocomplete="off" :required="!editing" /></label>
      <label class="block text-sm">Client secret<Input v-model="clientSecret" class="mt-1" type="password" autocomplete="new-password" :required="!editing" /></label>
      <p v-if="editing" class="text-xs text-muted-foreground">Leave both credential fields empty to keep them. Rotation must retain the same registered account and connection.</p>
      <label v-if="editing" class="block text-sm">Low-balance alert threshold<Input v-model="balanceThreshold" inputmode="decimal" class="mt-1" required /><span class="text-xs text-muted-foreground">Uses the account’s reported credits or currency, without conversion.</span></label>
      <div v-if="editing" class="space-y-2"><label class="block text-sm">SMS reply bot<select v-model="botFlowID" class="mt-1 block w-full rounded-md border bg-background p-2"><option value="">No automatic replies</option><option v-for="f in flows" :key="f.id" :value="f.id">{{ f.name }}</option></select></label><label v-if="botFlowID" class="flex items-start gap-2 text-sm"><input v-model="botConsent" type="checkbox" class="mt-1" />I confirm consent for automated SMS replies. STOP replies suppress further SMS.</label><p class="text-xs text-muted-foreground">Create an SMS flow under Chatbot → Flows first. SMS bots support text, prompts, text choices, conditions, timing and variables.</p></div>
      <label class="block text-sm">Maximum segments per message<Input v-model.number="maxSegments" class="mt-1 max-w-24" type="number" min="1" max="10" required /></label>
      <div class="flex gap-2"><Button :disabled="busy || !encrypted" type="submit">{{ busy ? 'Validating…' : 'Save connection' }}</Button><Button v-if="editing" type="button" variant="outline" @click="reset">Cancel</Button></div>
    </form>
  </div></main>
</template>
