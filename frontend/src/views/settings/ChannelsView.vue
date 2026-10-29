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
const dispatchState = ref<'loading' | 'paused' | 'ready' | 'unavailable'>('loading')
const settingsLoaded = ref(false)
// Use the latest server capabilities, rather than the object captured by Edit.
const currentEditing = computed(() => settingsLoaded.value ? connections.value.find(c => c.id === editing.value?.id) : undefined)
const repliesAvailable = computed(() => currentEditing.value?.capabilities.inbound === true)
async function reload() {
  dispatchState.value = 'loading'; settingsLoaded.value = false; encrypted.value = false
  try {
    const [r, p, health] = await Promise.allSettled([api.get('/channel-connections'), api.get('/channels/providers'), api.get('/channels/health')])
    const stopped = health.status === 'fulfilled' ? (health.value.data?.data || health.value.data)?.dispatch_stopped : undefined
    dispatchState.value = typeof stopped === 'boolean' ? (stopped ? 'paused' : 'ready') : 'unavailable'
    if (r.status !== 'fulfilled' || p.status !== 'fulfilled') throw new Error('settings')
    connections.value = (r.value.data.data || r.value.data).connections || []; encrypted.value = (p.value.data.data || p.value.data).credentials_configured === true
    settingsLoaded.value = true
    try { const f = await api.get('/chatbot/flows?limit=200'); flows.value = ((f.data.data || f.data).flows || []).filter((f:any) => f.channel === 'sms' && f.enabled) } catch { flows.value = [] }
  } catch { connections.value = []; flows.value = []; dispatchState.value = 'unavailable'; error.value = 'Channel settings could not be loaded. Refresh before changing this connection.' }
}
function edit(c: ChannelConnection) { editing.value = c; balanceThreshold.value = c.balance_threshold || '100'; botFlowID.value = c.bot_flow_id || ''; botConsent.value = c.bot_consent_confirmed || false; name.value = c.name; maxSegments.value = c.max_segments; clientID.value = ''; clientSecret.value = ''; error.value = '' }
function reset() { editing.value = null; balanceThreshold.value = '100'; botFlowID.value = ''; botConsent.value = false; name.value = 'SMSFlow'; maxSegments.value = 3; clientID.value = ''; clientSecret.value = '' }
async function save() {
  if (busy.value || !canWrite.value || !settingsLoaded.value || !encrypted.value || (editing.value && !currentEditing.value)) return
  busy.value = true; error.value = ''
  try {
    const credentials = clientID.value || clientSecret.value ? { client_id: clientID.value, client_secret: clientSecret.value } : undefined
    if (!editing.value && !credentials) throw new Error('credentials')
    const body = { name: name.value, provider: 'smsflow', max_segments: maxSegments.value, credentials }
    if (currentEditing.value) await api.put(`/channel-connections/${currentEditing.value.id}`, { ...body, enabled: currentEditing.value.enabled, balance_threshold: balanceThreshold.value, revalidate: currentEditing.value.error_code === 'validation_required', bot_flow_id: repliesAvailable.value ? botFlowID.value : '', bot_consent_confirmed: repliesAvailable.value ? botConsent.value : false })
    else await api.post('/channel-connections', body)
    reset(); await reload(); toast.success('SMSFlow connection saved.')
  } catch { await reload(); error.value = 'Connection could not be saved. Check the credentials, SMPP integration access and connection settings. Existing settings are preserved.' }
  finally { clientID.value = ''; clientSecret.value = ''; busy.value = false }
}
async function toggle(c: ChannelConnection) {
  if (busy.value || !canWrite.value || !settingsLoaded.value || (!c.enabled && dispatchState.value !== 'ready')) return
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
    <p class="text-sm text-muted-foreground">Use a dedicated SMSFlow API key with SMPP integration access. Messages use the existing SMPP integration service. Uncertain submissions wait for delivery reports and are never automatically sent again.</p>
    <div role="status" class="space-y-2 rounded-lg border p-4 text-sm">
      <p v-if="dispatchState === 'paused'">SMS sending is paused by the host. Connections can be configured or stopped; direct SMS and fallback will not dispatch while this hold is active.</p>
      <p v-else-if="dispatchState === 'unavailable'">Sending status is unavailable. Enablement is blocked until status can be refreshed. You can still edit loaded settings or stop an enabled connection.</p>
      <p v-else-if="dispatchState === 'loading'">Checking SMS sending status…</p>
      <p v-else>The host permits SMS dispatch. Each connection and sending workflow must also be enabled, with recipient consent.</p>
      <Button type="button" variant="outline" :disabled="busy || dispatchState === 'loading'" @click="reload">Refresh status</Button>
    </div>
    <p v-if="!encrypted" class="text-sm text-destructive">Connection creation requires host-managed credential encryption.</p>
    <p v-if="error" role="alert" class="text-sm text-destructive">{{ error }}</p>
    <section v-for="c in connections" :key="c.id" class="space-y-2 rounded-lg border p-4">
      <div class="flex flex-wrap items-center justify-between gap-3"><h2 class="min-w-0 max-w-full break-words font-semibold">{{ c.name }}</h2><span class="text-sm">{{ c.enabled ? 'Enabled' : 'Dispatch disabled' }}</span></div>
      <p class="text-sm text-muted-foreground">{{ c.provider }} · {{ c.max_segments }} segments maximum · {{ c.capabilities.inbound ? 'Reply-capable route' : 'One-way SMS · No inbound route' }}</p>
      <p v-if="c.balance?.amount" class="text-sm" :class="c.low_balance ? 'text-destructive' : 'text-muted-foreground'">Balance: {{ c.balance.amount }} {{ c.balance.unit === 'currency' ? c.balance.currency : c.balance.unit }}<span v-if="c.low_balance"> · Low balance</span></p>
      <p v-if="c.error_code || c.balance_error_code || c.poll_failures" class="text-sm text-destructive">Action needed: {{ c.error_code || c.balance_error_code || 'event polling failed' }}. Already-submitted messages are retained for reconciliation.</p>
      <div v-if="canWrite" class="flex flex-col gap-2 sm:flex-row sm:flex-wrap"><Button variant="outline" :disabled="busy" @click="edit(c)">Edit / rotate credentials</Button><Button variant="outline" :disabled="busy || (!c.enabled && dispatchState !== 'ready')" @click="toggle(c)">{{ c.enabled ? 'Stop pending SMS dispatch' : 'Enable dispatch' }}</Button></div>
    </section>
    <form v-if="canWrite" class="space-y-4 rounded-lg border p-5" @submit.prevent="save">
      <h2 class="font-semibold">{{ editing ? 'Edit connection' : 'Connect SMSFlow' }}</h2>
      <label class="block text-sm">Connection name<Input v-model="name" class="mt-1" maxlength="100" required /></label>
      <label class="block text-sm">Client ID<Input v-model="clientID" class="mt-1" autocomplete="off" :required="!editing" /></label>
      <label class="block text-sm">Client secret<Input v-model="clientSecret" class="mt-1" type="password" autocomplete="new-password" :required="!editing" /></label>
      <p v-if="editing" class="text-xs text-muted-foreground">Leave both credential fields empty to keep them. Rotation must retain the same registered account and connection.</p>
      <label v-if="editing" class="block text-sm">Low-balance alert threshold<Input v-model="balanceThreshold" inputmode="decimal" class="mt-1" required /><span class="text-xs text-muted-foreground">Uses the account’s reported credits or currency, without conversion.</span></label>
      <div v-if="editing && repliesAvailable" class="space-y-2"><label class="block text-sm">SMS reply bot<select v-model="botFlowID" class="mt-1 block w-full rounded-md border bg-background p-2"><option value="">No automatic replies</option><option v-for="f in flows" :key="f.id" :value="f.id">{{ f.name }}</option></select></label><label v-if="botFlowID" class="flex items-start gap-2 text-sm"><input v-model="botConsent" type="checkbox" class="mt-1" />I confirm consent for automated SMS replies. STOP replies suppress further SMS.</label><p class="text-xs text-muted-foreground">Create an SMS flow under Chatbot → Flows first. SMS bots support text, prompts, text choices, conditions, timing and variables.</p></div>
      <p v-else-if="currentEditing" class="text-sm text-muted-foreground">One-way SMS supports outbound messages and delivery reports. This connection has no inbound route, so SMS reply bots are unavailable. Saving removes any stale reply-bot binding.</p>
      <label class="block text-sm">Maximum segments per message<Input v-model.number="maxSegments" class="mt-1 max-w-24" type="number" min="1" max="10" required /></label>
      <div class="flex gap-2"><Button :disabled="busy || !encrypted || !settingsLoaded || (!!editing && !currentEditing)" type="submit">{{ busy ? 'Validating…' : 'Save connection' }}</Button><Button v-if="editing" type="button" variant="outline" @click="reset">Cancel</Button></div>
    </form>
  </div></main>
</template>
