<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useTeamsStore } from '@/stores/teams'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { accountsService, tagsService, templatesService, usersService } from '@/services/api'
import { inboxService, type InboxAction, type InboxWorkflow, type InboxWorkflowInput, type InboxWorkflowPreview, type InboxWorkflowRun, type QueueCase } from '@/services/inbox'
import InboxFieldMatchers from '@/components/chat/InboxFieldMatchers.vue'
import InboxWorkflowPreviewPanel from '@/components/chat/InboxWorkflowPreview.vue'

const { t } = useI18n()
const teamsStore = useTeamsStore()
const mode = ref<'macro' | 'rule'>('rule')
const list = ref<InboxWorkflow[]>([])
const runs = ref<InboxWorkflowRun[]>([])
const id = ref('')
const enabled = ref(false)
const switchState = ref({ enabled: false, dispatch_available: false })
const busy = ref(false)
const ready = ref(false)
const failed = ref(false)
const lookupFailed = ref(false)
const caseId = ref('')
const cases = ref<QueueCase[]>([])
const dryRunId = ref('')
const preview = ref<InboxWorkflowPreview | null>(null)
const accounts = ref<{ id: string; name: string }[]>([])
const teams = ref<{ id: string; name: string }[]>([])
const users = ref<{ id: string; full_name: string }[]>([])
const tags = ref<{ name: string }[]>([])
const templates = ref<{ id: string; name: string; language: string; whats_app_account?: string }[]>([])
const actions: InboxAction['type'][] = ['assign', 'tag', 'priority', 'status', 'note', 'send_text', 'send_template']
const statuses = ['open', 'pending', 'waiting_customer', 'resolved']
const priorities = ['low', 'normal', 'high', 'urgent']
const triggers = ['case_opened', 'incoming_message', 'status_changed']
const fresh = (): InboxWorkflowInput => ({
  kind: mode.value, name: '', trigger: mode.value === 'rule' ? 'case_opened' : '', position: 0,
  actions: [{ type: 'note', value: '' }], conditions: {},
})
const form = ref(fresh())
const deleting = ref(false)
const readyToSave = computed(() => !!form.value.name.trim() && form.value.actions.every(action => action.value.trim() && (action.type !== 'send_template' || action.consent_confirmed)) &&
  form.value.actions.filter(action => action.type === 'send_text' || action.type === 'send_template').length <= 1)
async function load() {
  busy.value = true
  try {
    enabled.value = (await inboxService.settings()).data.data.enabled
    if (enabled.value) {
      const [w, r, state] = await Promise.all([inboxService.workflowDefinitions(mode.value), inboxService.workflowRuns(), inboxService.automationState()])
      list.value = w.data.data.workflows
      runs.value = r.data.data.runs
      switchState.value = state.data.data
    }
    ready.value = true
    failed.value = false
  } catch { failed.value = true }
  finally { busy.value = false }
}
function select(w?: InboxWorkflow) {
  id.value = w?.id ?? ''
  deleting.value = false
  if (w) {
    form.value = { kind: w.kind, name: w.name, trigger: w.trigger ?? '', position: w.position, actions: w.actions.map(action => ({ ...action })),
      conditions: { ...w.conditions, tags: [...(w.conditions.tags ?? [])], fields: [...(w.conditions.fields ?? [])] }, enabled: !!w.enabled_at }
  } else form.value = fresh()
}
async function changeMode(next: typeof mode.value) {
  mode.value = next
  select()
  dryRunId.value = ''
  preview.value = null
  await load()
}
async function save(remove = false) {
  const target = id.value
  busy.value = true
  try {
    if (remove) await inboxService.deleteWorkflow(target)
    else if (target) await inboxService.updateWorkflow(target, form.value)
    else await inboxService.createWorkflow(form.value)
    select()
    await load()
    toast.success(t('settings.saved'))
  } catch { toast.error(t('inboxRules.saveError')) }
  finally { busy.value = false }
}
async function stopOrStart() {
  busy.value = true
  try {
    const { data } = await inboxService.setAutomation(!switchState.value.enabled)
    switchState.value = data.data
    await load()
  } catch { toast.error(t('inboxRules.saveError')) }
  finally { busy.value = false }
}
async function prepare(w: InboxWorkflow) {
  dryRunId.value = w.id
  preview.value = null
  caseId.value = ''
  try { cases.value = (await inboxService.queue({})).data.data.cases }
  catch { toast.error(t('inboxRules.previewError')) }
}
async function runPreview() {
  preview.value = null
  try { preview.value = (await inboxService.previewWorkflow(dryRunId.value, caseId.value)).data.data }
  catch { toast.error(t('inboxRules.previewError')) }
}
function move(index: number, delta: number) {
  const items = form.value.actions
  const other = index + delta
  if (other < 0 || other >= items.length) return
  const [item] = items.splice(index, 1)
  items.splice(other, 0, item)
}
function loadLookups() {
  lookupFailed.value = false
  void accountsService.list().then(r => { accounts.value = r.data.data?.accounts ?? [] }).catch(() => { lookupFailed.value = true })
  void teamsStore.fetchTeams().then(result => { teams.value = result.teams }).catch(() => { lookupFailed.value = true })
  void usersService.list().then(r => { users.value = (r.data as any).data?.users ?? (r.data as any).users ?? [] }).catch(() => { lookupFailed.value = true })
  void tagsService.list().then(r => { tags.value = (r.data as any).data?.tags ?? (r.data as any).tags ?? [] }).catch(() => { lookupFailed.value = true })
  void templatesService.list({ status: 'APPROVED', category: 'UTILITY', limit: 100 }).then(r => { templates.value = (r.data as any).data?.templates ?? (r.data as any).templates ?? [] }).catch(() => { lookupFailed.value = true })
}
onMounted(async () => {
  await load()
  if (!enabled.value) return
  loadLookups()
})
</script>

<template>
  <p v-if="lookupFailed" role="alert" class="p-4 text-destructive">{{ t('inboxQueue.loadFiltersError') }}
    <button type="button" class="underline" @click="loadLookups">{{ t('common.retry') }}</button>
  </p>
  <main class="mx-auto grid max-w-5xl gap-4 p-4">
    <h1 class="text-xl font-semibold">{{ t('inboxRules.title') }}</h1>
    <p v-if="failed" role="alert">{{ t('inboxRules.loadError') }}</p>
    <p v-else-if="ready && !enabled"><RouterLink to="/settings" class="underline">{{ t('inboxRules.enableFirst') }}</RouterLink></p>
    <template v-else-if="ready">
      <div class="flex flex-wrap items-center gap-2 rounded-lg border border-border p-3">
        <p class="mr-auto">{{ t(!switchState.dispatch_available ? 'inboxRules.dispatchHeld' : switchState.enabled ? 'inboxRules.sendsEnabled' : 'inboxRules.sendsHeld') }}</p>
        <Button type="button" variant="outline" :disabled="busy || !switchState.dispatch_available" @click="stopOrStart">{{ t(switchState.enabled ? 'inboxRules.stopSends' : 'inboxRules.allowSends') }}</Button>
      </div>
      <div class="flex gap-2">
        <Button type="button" variant="outline" :aria-pressed="mode === 'rule'" :disabled="busy" @click="changeMode('rule')">{{ t('inboxRules.rules') }}</Button>
        <Button type="button" variant="outline" :aria-pressed="mode === 'macro'" :disabled="busy" @click="changeMode('macro')">{{ t('inboxRules.macros') }}</Button>
      </div>
      <ul class="divide-y divide-border">
        <li v-for="w in list" :key="w.id" class="flex flex-wrap items-center gap-2 py-2">
          <span class="mr-auto">{{ w.name }} <span v-if="w.kind === 'rule'" class="text-sm text-muted-foreground"> Â· {{ w.position }} Â· {{ t(w.enabled_at ? 'inboxRules.enabled' : 'inboxRules.disabled') }}</span></span>
          <Button type="button" variant="outline" size="sm" @click="select(w)">{{ t('common.edit') }}</Button>
          <Button type="button" variant="ghost" size="sm" @click="prepare(w)">{{ t('inboxRules.dryRun') }}</Button>
        </li>
      </ul>
      <section v-if="dryRunId" class="grid gap-2 rounded-lg border border-border p-4">
        <h2 class="font-medium">{{ t('inboxRules.dryRun') }}</h2>
        <p class="text-sm">{{ t('inboxRules.dryRunHelp') }}</p>
        <label>{{ t('inboxRules.chooseCase') }}
          <select v-model="caseId" @change="preview = null"><option value="">â€”</option><option v-for="c in cases" :key="c.id" :value="c.id">{{ c.contact_name || t('inboxQueue.unnamed') }} Â· {{ c.account_name }}</option></select>
        </label>
        <Button type="button" variant="outline" :disabled="!caseId" class="justify-self-start" @click="runPreview">{{ t('inboxRules.review') }}</Button>
        <InboxWorkflowPreviewPanel v-if="preview" :preview="preview" />
      </section>
      <form class="grid gap-3 rounded-lg border border-border p-4" @submit.prevent="save()">
        <h2 class="font-medium">{{ t(id ? 'inboxRules.edit' : 'inboxRules.add') }}</h2>
        <label>{{ t('inboxRules.name') }} <input v-model.trim="form.name" maxlength="100" required /></label>
        <div v-if="mode === 'rule'" class="grid gap-2 sm:grid-cols-2">
          <label>{{ t('inboxRules.trigger') }} <select v-model="form.trigger"><option v-for="trigger in triggers" :key="trigger" :value="trigger">{{ t('inboxRules.event.' + trigger) }}</option></select></label>
          <label>{{ t('inboxRules.position') }} <input v-model.number="form.position" type="number" min="-10000" max="10000" required /></label>
        </div>
        <label v-if="mode === 'rule' && id"><input v-model="form.enabled" type="checkbox" /> {{ t('inboxRules.enableRule') }}</label>
        <details class="rounded-md border border-border p-3">
          <summary>{{ t('inboxRules.conditions') }}</summary>
          <div class="mt-2 grid gap-2 sm:grid-cols-2">
            <label>{{ t('inboxQueue.account') }} <select v-model="form.conditions.account_id"><option :value="undefined">{{ t('inboxSla.any') }}</option><option v-for="a in accounts" :key="a.id" :value="a.id">{{ a.name }}</option></select></label>
            <label>{{ t('inboxQueue.team') }} <select v-model="form.conditions.team_id"><option :value="undefined">{{ t('inboxSla.any') }}</option><option v-for="item in teams" :key="item.id" :value="item.id">{{ item.name }}</option></select></label>
            <label>{{ t('inboxQueue.assignee') }} <select v-model="form.conditions.assignee_id"><option :value="undefined">{{ t('inboxSla.any') }}</option><option v-for="u in users" :key="u.id" :value="u.id">{{ u.full_name }}</option></select></label>
            <label>{{ t('inboxWorkflow.caseStatus') }} <select v-model="form.conditions.status"><option :value="undefined">{{ t('inboxSla.any') }}</option><option v-for="s in [...statuses, 'snoozed']" :key="s" :value="s">{{ t('inboxWorkflow.status.' + s) }}</option></select></label>
            <label>{{ t('inboxWorkflow.casePriority') }} <select v-model="form.conditions.priority"><option :value="undefined">{{ t('inboxSla.any') }}</option><option v-for="p in priorities" :key="p" :value="p">{{ t('inboxWorkflow.priority.' + p) }}</option></select></label>
            <label v-if="tags.length">{{ t('contacts.tags') }} <select :value="form.conditions.tags ?? []" multiple @change="form.conditions.tags = Array.from(($event.target as HTMLSelectElement).selectedOptions, item => item.value)"><option v-for="tag in tags" :key="tag.name" :value="tag.name">{{ tag.name }}</option></select></label>
            <InboxFieldMatchers class="sm:col-span-2" :model-value="form.conditions.fields ?? []" @update:model-value="form.conditions.fields = $event" />
          </div>
        </details>
        <fieldset class="grid gap-2">
          <legend>{{ t('inboxRules.actions') }}</legend>
          <div v-for="(action, i) in form.actions" :key="i" class="grid gap-2 rounded-md border border-border p-2">
            <label>{{ t('inboxRules.actionType') }}
              <select v-model="action.type" @change="action.value = ''; action.consent_confirmed = false">
                <option v-for="type in actions" :key="type" :value="type">{{ t('inboxRules.action.' + type) }}</option>
              </select>
            </label>
            <select v-if="action.type === 'priority' || action.type === 'status'" v-model="action.value" :aria-label="t('inboxRules.actionValue')">
              <option value="">â€”</option><option v-for="value in action.type === 'priority' ? priorities : statuses" :key="value" :value="value">{{ t('inboxWorkflow.' + action.type + '.' + value) }}</option>
            </select>
            <select v-else-if="action.type === 'assign'" v-model="action.value" :aria-label="t('inboxQueue.assignee')"><option value="">â€”</option><option v-for="user in users" :key="user.id" :value="user.id">{{ user.full_name }}</option></select>
            <select v-else-if="action.type === 'tag'" v-model="action.value" :aria-label="t('contacts.tags')"><option value="">â€”</option><option v-for="tag in tags" :key="tag.name" :value="tag.name">{{ tag.name }}</option></select>
            <template v-else-if="action.type === 'send_template'">
              <select v-model="action.value" :aria-label="t('inboxRules.action.send_template')"><option value="">â€”</option><option v-for="item in templates" :key="item.id" :value="item.id">{{ item.name }} Â· {{ item.language }}</option></select>
              <label><input v-model="action.consent_confirmed" type="checkbox" /> {{ t('inboxRules.consent') }}</label>
            </template>
            <textarea v-else v-model="action.value" rows="2" maxlength="4096" :aria-label="t('inboxRules.actionValue')" />
            <div class="flex flex-wrap gap-1">
              <Button size="sm" variant="ghost" type="button" :disabled="!i" @click="move(i, -1)">{{ t('inboxRules.moveUp') }}</Button>
              <Button size="sm" variant="ghost" type="button" :disabled="i === form.actions.length - 1" @click="move(i, 1)">{{ t('inboxRules.moveDown') }}</Button>
              <Button size="sm" variant="ghost" type="button" :disabled="form.actions.length <= 1" @click="form.actions.splice(i, 1)">{{ t('common.remove') }}</Button>
            </div>
          </div>
        </fieldset>
        <div class="flex flex-wrap gap-2">
          <Button type="button" variant="outline" :disabled="form.actions.length >= 10" @click="form.actions.push({ type: 'note', value: '' })">{{ t('inboxRules.addAction') }}</Button>
          <Button type="submit" :disabled="busy || !readyToSave">{{ t('common.save') }}</Button>
          <Button v-if="id" type="button" variant="ghost" @click="select()">{{ t('common.cancel') }}</Button>
          <Button v-if="id" type="button" variant="ghost" :disabled="busy" @click="deleting ? save(true) : deleting = true">{{ t(deleting ? 'inboxRules.confirmDelete' : 'common.delete') }}</Button>
        </div>
      </form>
      <section class="rounded-lg border border-border p-4">
        <div class="mb-2 flex items-center justify-between gap-2"><h2 class="font-medium">{{ t('inboxRules.history') }}</h2><Button type="button" size="sm" variant="outline" :disabled="busy" @click="load">{{ t('inboxQueue.retry') }}</Button></div>
        <p v-if="!runs.length">{{ t('inboxRules.noRuns') }}</p>
        <ul v-else class="divide-y divide-border text-sm">
          <li v-for="run in runs" :key="run.id" class="py-2">{{ run.workflow_name }} Â· {{ t('inboxRules.runState.' + run.state) }} <span v-if="run.outcome"> Â· {{ t('inboxRules.outcome.' + run.outcome) }}</span> Â· {{ new Date(run.created_at).toLocaleString() }}</li>
        </ul>
      </section>
    </template>
  </main>
</template>

<style scoped>
input:not([type="checkbox"]), select, textarea { @apply block w-full rounded-md border border-input bg-background px-2 py-1; }
</style>
