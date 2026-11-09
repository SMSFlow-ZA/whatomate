<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import axios from 'axios'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { api } from '@/services/api'

const base = '/chatbot/ai-connections'
const organizationDefault = '00000000-0000-0000-0000-000000000000'

interface Profile {
  id: string
  name: string
  endpoint_id: string
  model: string
  enabled: boolean
  ready: boolean
}
interface Assignment {
  account_id: string
  connection_id: string
  include_history: boolean
  include_context: boolean
}
interface Endpoint {
  id: string
  label: string
  kind: 'foundry' | 'compatible'
  host: string
  local_only: boolean
}
interface Settings {
  enabled: boolean
  can_write: boolean
  connections: Profile[]
  assignments: Assignment[]
  endpoints: Endpoint[]
  accounts: { id: string; name: string }[]
}

const props = defineProps<{ aiEnabled: boolean }>()
const emit = defineEmits<{ changed: [] }>()
const settings = ref<Settings>()
const busy = ref(false)
const status = ref('')
const failure = ref('')
const draft = ref({ id: '', name: '', endpoint_id: '', model: '', credential: '' })
const account = ref(organizationDefault)
const selected = ref(organizationDefault)
const history = ref(false)
const context = ref(false)
const acknowledged = ref(false)
const editing = computed(() => draft.value.id !== '')
const endpoint = computed(() => settings.value?.endpoints.find((item) => item.id === draft.value.endpoint_id))
const selectedProfile = computed(() => settings.value?.connections.find((item) => item.id === selected.value))
const assigned = (id: string) => settings.value?.assignments.some((item) => item.connection_id === id) ?? false
const selectable = computed(() => settings.value?.connections.filter((item) => item.ready || item.id === selected.value) ?? [])

function loadAssignment() {
  const assignment = settings.value?.assignments.find((item) => item.account_id === account.value)
  selected.value = assignment?.connection_id ?? organizationDefault
  history.value = assignment?.include_history ?? false
  context.value = assignment?.include_context ?? false
  acknowledged.value = false
}
watch(account, loadAssignment)
function resetDisclosure() {
  history.value = false
  context.value = false
  acknowledged.value = false
}

async function reload() {
  const response = await api.get<{ data: Settings }>(base)
  settings.value = response.data.data
  loadAssignment()
}
onMounted(async () => {
  try { await reload() } catch {
    // Older deployments and users without AI settings access have no panel.
  }
})

function clearDraft() {
  draft.value = { id: '', name: '', endpoint_id: '', model: '', credential: '' }
}

function edit(profile: Profile) {
  draft.value = { id: profile.id, name: profile.name, endpoint_id: profile.endpoint_id, model: profile.model, credential: '' }
}

function showError(error: unknown) {
  const envelope = axios.isAxiosError(error) ? error.response?.data : undefined
  const message = typeof envelope?.message === 'string' ? envelope.message : 'The operation could not be completed.'
  const result = envelope?.data
  failure.value = typeof result?.latency_ms === 'number' && result.latency_ms > 0
    ? `${message} (${result.latency_ms} ms)`
    : message
}

async function perform(action: () => Promise<string>) {
  if (busy.value) return
  busy.value = true
  failure.value = ''
  status.value = ''
  try {
    status.value = await action()
    await reload()
    emit('changed')
  } catch (error) {
    showError(error)
  } finally {
    busy.value = false
  }
}

async function saveProfile() {
  await perform(async () => {
    const { id, name, endpoint_id, model, credential } = draft.value
    try {
      const payload = { name, endpoint_id, model, ...(credential ? { credential } : {}) }
      if (id) await api.put(`${base}/${id}`, payload)
      else await api.post(base, payload)
      clearDraft()
      return 'Connection saved. Test it before assigning it.'
    } finally {
      draft.value.credential = ''
    }
  })
}

function testProfile(id: string) {
  return perform(async () => {
    const response = await api.post<{ data: { latency_ms: number } }>(`${base}/${id}/test`, {}, { timeout: 35_000 })
    return `Connection test passed in ${response.data.data.latency_ms} ms.`
  })
}

function setEnabled(profile: Profile) {
  return perform(async () => {
    if (profile.enabled) await api.post(`${base}/${profile.id}/disable`)
    else await api.put(`${base}/${profile.id}`, { enabled: true })
    return profile.enabled ? 'Connection disabled.' : 'Connection enabled. Test it to restore use.'
  })
}

function remove(profile: Profile) {
  if (!window.confirm(`Delete the AI connection “${profile.name}”?`)) return
  return perform(async () => {
    await api.delete(`${base}/${profile.id}`)
    if (draft.value.id === profile.id) clearDraft()
    return 'Connection deleted.'
  })
}

function saveAssignment() {
  return perform(async () => {
    await api.put(`${base}/assignment`, {
      account_id: account.value,
      connection_id: selected.value,
      include_history: history.value,
      include_context: context.value,
    })
    return 'AI assignment saved.'
  })
}
</script>

<template>
  <section v-if="settings?.enabled" class="mt-4 space-y-4 rounded-lg border bg-card p-5" aria-labelledby="custom-ai-heading">
    <div>
      <h3 id="custom-ai-heading" class="text-lg font-semibold">Your AI connections</h3>
      <p class="mt-1 text-sm text-muted-foreground">Use an approved Microsoft Foundry deployment or a self-hosted model for chatbot replies and AI Response flow steps.</p>
      <p v-if="!props.aiEnabled" class="mt-1 text-sm text-muted-foreground">Enable and save AI Responses above to use these assignments in chatbots.</p>
    </div>
    <p v-if="status" role="status" class="text-sm">{{ status }}</p>
    <p v-if="failure" role="alert" class="text-sm text-destructive">{{ failure }}</p>

    <ul v-if="settings.connections.length" class="divide-y rounded-md border">
      <li v-for="profile in settings.connections" :key="profile.id" class="flex flex-wrap items-center justify-between gap-3 p-3">
        <div class="min-w-0">
          <p class="break-words text-sm font-medium">{{ profile.name }}</p>
          <p class="break-words text-xs text-muted-foreground">
            {{ profile.model }} · {{ profile.ready ? 'Ready' : profile.enabled ? 'Needs a connection test' : 'Disabled' }}
            <span v-if="assigned(profile.id)"> · Assigned</span>
          </p>
        </div>
        <div v-if="settings.can_write" class="flex flex-wrap gap-2">
          <Button type="button" size="sm" variant="outline" :disabled="busy || !profile.enabled" @click="testProfile(profile.id)">Test connection</Button>
          <Button type="button" size="sm" variant="outline" :disabled="busy || assigned(profile.id)" @click="edit(profile)">Edit</Button>
          <Button type="button" size="sm" variant="outline" :disabled="busy" @click="setEnabled(profile)">{{ profile.enabled ? 'Disable' : 'Enable' }}</Button>
          <Button type="button" size="sm" variant="ghost" :disabled="busy || assigned(profile.id)" @click="remove(profile)">Delete</Button>
        </div>
      </li>
    </ul>
    <p v-else class="text-sm text-muted-foreground">There are no connections for this organization yet.</p>

    <form v-if="settings.can_write" class="space-y-3 border-t pt-4" @submit.prevent="saveProfile">
      <h4 class="font-medium">{{ editing ? 'Edit connection' : 'Add a connection' }}</h4>
      <p v-if="settings.endpoints.length === 0" class="text-sm text-muted-foreground">Ask your workspace operator to approve an endpoint before adding a connection.</p>
      <div class="grid gap-3 sm:grid-cols-2">
        <label class="space-y-1 text-sm"><span>Connection name</span><Input v-model="draft.name" required maxlength="100" :disabled="busy" /></label>
        <label class="space-y-1 text-sm"><span>Approved endpoint</span>
          <select v-model="draft.endpoint_id" class="h-9 w-full rounded-md border bg-background px-3" required :disabled="busy">
            <option value="" disabled>Choose an endpoint</option>
            <option v-for="item in settings.endpoints" :key="item.id" :value="item.id">
              {{ item.label || item.host }} · {{ item.local_only ? 'Local only' : item.kind === 'foundry' ? 'Microsoft Foundry' : 'OpenAI-compatible' }}
            </option>
          </select>
        </label>
        <label class="space-y-1 text-sm"><span>{{ endpoint?.kind === 'foundry' ? 'Deployment name' : 'Model name' }}</span><Input v-model="draft.model" required maxlength="150" :disabled="busy" /></label>
        <label class="space-y-1 text-sm"><span>{{ endpoint?.kind === 'foundry' ? 'API key' : 'Gateway token' }}</span>
          <Input v-model="draft.credential" type="password" autocomplete="new-password" :required="!editing" :disabled="busy" maxlength="4096"
            :placeholder="editing ? 'Leave blank to keep existing credential' : undefined" />
        </label>
      </div>
      <p v-if="endpoint" class="break-all text-xs text-muted-foreground">Approved host: {{ endpoint.host }}</p>
      <p class="text-xs text-muted-foreground">Testing uses a synthetic prompt. To rotate a credential in use, add and test a replacement connection before switching.</p>
      <div class="flex gap-2">
        <Button type="submit" :disabled="busy || !draft.endpoint_id">Save connection</Button>
        <Button v-if="editing" type="button" variant="ghost" :disabled="busy" @click="clearDraft">Cancel edit</Button>
      </div>
    </form>

    <form class="space-y-3 border-t pt-4" @submit.prevent="saveAssignment">
      <h4 class="font-medium">Chatbot assignment</h4>
      <div class="grid gap-3 sm:grid-cols-2">
        <label class="space-y-1 text-sm"><span>Applies to</span>
          <select v-model="account" class="h-9 w-full rounded-md border bg-background px-3" :disabled="busy">
            <option :value="organizationDefault">Organization default</option>
            <option v-for="item in settings.accounts" :key="item.id" :value="item.id">{{ item.name }}</option>
          </select>
        </label>
        <label class="space-y-1 text-sm"><span>Connection</span>
          <select v-model="selected" @change="resetDisclosure" class="h-9 w-full rounded-md border bg-background px-3" :disabled="busy || !settings.can_write">
            <option :value="organizationDefault">{{ account === organizationDefault ? 'Use built-in AI settings' : 'Inherit organization default' }}</option>
            <option v-for="profile in selectable" :key="profile.id" :value="profile.id" :disabled="!profile.ready">{{ profile.name }}{{ profile.ready ? '' : ' · unavailable' }}</option>
          </select>
        </label>
      </div>
      <template v-if="selectedProfile">
        <p class="text-sm text-muted-foreground">This connection receives the current chatbot prompt and configured system instructions. Include extra context only when needed.</p>
        <label class="flex items-center gap-2 text-sm"><input v-model="history" type="checkbox" :disabled="busy || !settings.can_write" /> Include recent conversation history</label>
        <label class="flex items-center gap-2 text-sm"><input v-model="context" type="checkbox" :disabled="busy || !settings.can_write" /> Include existing AI context, including configured API results</label>
        <label v-if="settings.can_write" class="flex items-center gap-2 text-sm"><input v-model="acknowledged" type="checkbox" :disabled="busy" /> I approve sending this data to the selected connection</label>
      </template>
      <Button v-if="settings.can_write" type="submit" :disabled="busy || (selected !== organizationDefault && (!selectedProfile?.ready || !acknowledged))">Save assignment</Button>
    </form>
  </section>
</template>
