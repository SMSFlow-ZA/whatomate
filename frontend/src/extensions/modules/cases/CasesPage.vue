<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { caseAction, createCase, listCases, loadCaseAccess, readCase, type CaseRecord, type CaseState, type Catalog } from './api'
const records = ref<CaseRecord[]>([]), selected = ref<CaseRecord | null>(null)
const access = ref<Catalog>({permissions: [], grants: []})
const query = ref(''), busy = ref(false), error = ref(''), notice = ref('')
const narrative = ref(''), category = ref(''), reason = ref(''), candidate = ref(''), target = ref<CaseState>('InProgress')
const participant = ref(''), participantRole = ref('subject'), backups = ref('')
 const intake = ref({ unit_id: '', narrative: '', draft: false })
// Retained across retries: an uncertain commit must replay exactly this key and payload.
const intakeKey = ref(crypto.randomUUID()), pendingIntake = ref<string | null>(null)
const can = (permission: string, unit?: string) => access.value.permissions.includes(permission) && !!unit && access.value.grants.some(g => g.permission === permission && g.unit_id === unit)
const intakeUnits = computed(() => [...new Set(access.value.grants.filter(g => g.permission === 'cases:submit.own' && access.value.permissions.includes(g.permission) && g.unit_id && g.unit_id !== '*').map(g => g.unit_id!))])
const operator = computed(() => !!selected.value?.snapshot)
const canWork = computed(() => !!selected.value && operator.value && (can('cases:work.assigned',selected.value.unit_id) || can('cases:assign.triage',selected.value.unit_id)))
const states: Record<CaseState, CaseState[]> = {Draft:['Submitted'],Submitted:[],Assigned:['InProgress','Escalated'],InProgress:['AwaitingInfo','Escalated','Resolved'],AwaitingInfo:['InProgress','Escalated','Resolved'],Escalated:['InProgress','AwaitingInfo','Resolved'],Resolved:['Closed','InProgress'],Closed:['InProgress']}
const availableStates = computed(() => selected.value ? states[selected.value.state] : [])
async function run(fn: () => Promise<void>) {
 if (busy.value) return
 busy.value = true; error.value = ''; notice.value = ''
 try { await fn() } catch (e: unknown) {
  const response = (e as {response?: {status?: number; data?: {error?: {code?: string}}}}).response
  if (response?.status === 409) error.value = 'This case changed. Refresh the case before trying again.'
  else if (response?.status === 404) { selected.value = null; error.value = 'This case is unavailable or your current access changed.' }
  else if (response?.status === 403) error.value = 'Your current verified session does not allow this action in this unit.'
  else if (response?.status === 400) { pendingIntake.value = null; error.value = 'Check the fields, assignment eligibility and allowed state transition.' }
  else error.value = pendingIntake.value ? 'The save could not be confirmed. Retry the unchanged intake below to recover its result.' : 'The request could not be confirmed. Refresh current case state before retrying.'
 } finally { busy.value = false }
}
async function refresh() { access.value = await loadCaseAccess(); records.value = await listCases(query.value); if (selected.value) { if (records.value.some(c => c.id === selected.value!.id)) await select(selected.value.id); else selected.value = null } }
async function select(id: string) { selected.value = await readCase(id); narrative.value = selected.value.narrative ?? ''; category.value = selected.value.category ?? ''; target.value = states[selected.value.state][0] ?? 'InProgress'; reason.value = ''; candidate.value = '' }
async function mutate(action: 'draft' | 'state' | 'assignment' | 'category' | 'participants') {
 await run(async () => {
  if (!selected.value) return
  const payload = action === 'draft' ? {narrative: narrative.value} : action === 'state' ? {state:target.value,reason:reason.value} : action === 'category' ? {category:category.value,reason:reason.value} : action === 'participants' ? {candidate_id:participant.value,participant_role:participantRole.value,reason:reason.value} : {candidate_id:candidate.value,backup_ids:backups.value.split(',').map(s=>s.trim()).filter(Boolean),role:'handler',reason:reason.value}
  await caseAction(selected.value,action,payload); await refresh(); notice.value = 'Case change saved.'
 })
}
async function submitIntake() {
 await run(async () => {
  if (!pendingIntake.value) pendingIntake.value = JSON.stringify({...intake.value,key:intakeKey.value})
  const result = await createCase(JSON.parse(pendingIntake.value)); pendingIntake.value = null; intakeKey.value = crypto.randomUUID(); intake.value.narrative = ''; await refresh(); if (records.value.some(c => c.id === result.id)) await select(result.id); notice.value = "Case saved. Reference: " + result.id
 })
}
onMounted(() => run(refresh))
</script>
<template>
 <main class="cases-page">
  <header><div><h1>Employee Relations cases</h1><p>Review your current inbox and record case decisions.</p></div><button :disabled="busy" @click="run(refresh)">Refresh cases</button></header>
  <p v-if="error" role="alert" class="error">{{ error }}</p><p v-if="notice" role="status" class="notice">{{ notice }}</p><p v-if="busy" role="status">Loading or saving cases…</p>
  <div class="workspace">
   <section class="panel inbox" aria-label="Case inbox"><h2>Inbox</h2><form @submit.prevent="run(refresh)"><label>Find by reference, status or category<input v-model="query" type="search" /></label><button :disabled="busy">Search</button></form><p v-if="!records.length && !busy">No cases are available with your current access.</p><ul><li v-for="c in records" :key="c.id"><button :disabled="busy" :aria-pressed="selected?.id === c.id" @click="run(() => select(c.id))"><strong>{{ c.category || 'Case' }}</strong><span>{{ c.state }} · v{{ c.version }}</span><small>{{ c.id }}</small></button></li></ul></section>
   <section class="panel detail" aria-label="Case detail"><template v-if="selected"><h2>Case {{ selected.id }}</h2><p>{{ selected.state }} · Version {{ selected.version }}</p>
    <template v-if="selected.snapshot"><dl><div><dt>Zone at intake</dt><dd>{{ selected.snapshot.zone }}</dd></div><div><dt>Department at intake</dt><dd>{{ selected.snapshot.department }}</dd></div><div><dt>Type at intake</dt><dd>{{ selected.snapshot.case_type }}</dd></div></dl><p>Current category: {{ selected.category }}</p><h3>Narrative</h3><p class="narrative">{{ selected.narrative }}</p><p>Assigned: {{ selected.assignments?.map(a => a.id).join(', ') || 'Awaiting triage' }}</p></template>
    <p v-else>Your case status is shown here. Internal handling notes and identities are protected.</p>
    <section v-if="canWork || selected.state === 'Draft'" class="actions"><h3>Case actions</h3><template v-if="selected.state === 'Draft'"><label>Draft narrative<textarea v-model="narrative" rows="6" /></label><button :disabled="busy || !narrative.trim()" @click="mutate('draft')">Save draft</button></template>
     <label>Reason for this decision<textarea v-model="reason" rows="2" /></label>
     <template v-if="availableStates.length"><label>Next state<select v-model="target"><option v-for="state in availableStates" :key="state" :value="state">{{ state }}</option></select></label><button :disabled="busy || ((selected.state === 'Resolved' || selected.state === 'Closed') && !reason.trim())" @click="mutate('state')">Update state</button></template>
     <template v-if="canWork"><label>Correct category<input v-model="category" /></label><button :disabled="busy || !category.trim() || !reason.trim()" @click="mutate('category')">Save human correction</button><label>Handler user ID<input v-model="candidate" autocomplete="off" /></label><label>Backup user IDs, separated by commas<input v-model="backups" autocomplete="off" /></label><button :disabled="busy || !candidate.trim() || !reason.trim() || selected.state === 'Draft' || selected.state === 'Closed' || selected.state === 'Resolved'" @click="mutate('assignment')">{{ selected.assignments?.length ? 'Reassign handler' : 'Assign handler' }}</button><h3>Verified conflict facts</h3><p v-for="link in selected.participants" :key="link.id + link.role">{{ link.role }}: {{ link.id }}</p><label>Participant user ID<input v-model="participant" autocomplete="off" /></label><label>Relationship<select v-model="participantRole"><option value="subject">Subject</option><option value="supervisor">Participant supervisor</option></select></label><button :disabled="busy || !participant.trim() || !reason.trim()" @click="mutate('participants')">Record verified participant</button></template>
    </section>
    <section v-if="selected.events?.length" aria-label="Case audit history"><h3>Decision history</h3><ol><li v-for="event in selected.events" :key="event.version"><strong>v{{ event.version }} · {{ event.kind || 'Case event' }}</strong><p>{{ event.from }} → {{ event.to }} · {{ new Date(event.occurred_at).toLocaleString() }}</p><p v-if="event.category_to">Category: {{ event.category_from }} → {{ event.category_to }}</p><p>{{ event.reason }}</p><small>Recorded {{ new Date(event.recorded_at).toLocaleString() }} by {{ event.actor_id }}</small></li></ol></section>
   </template><p v-else>Select a case to review its current details.</p></section>
  </div>
  <section v-if="intakeUnits.length" class="panel intake" aria-label="Case intake"><h2>New case</h2><p>Save a draft or submit a new case. A retry after an uncertain save keeps the same intake reference.</p><form @submit.prevent="submitIntake"><fieldset :disabled="busy || !!pendingIntake"><label>Unit<select v-model="intake.unit_id" required><option disabled value="">Select an authorized unit</option><option v-for="unit in intakeUnits" :key="unit" :value="unit">{{ unit }}</option></select></label><label>Narrative<textarea v-model="intake.narrative" rows="5" maxlength="65536" required /></label><label class="checkbox"><input v-model="intake.draft" type="checkbox" />Save as draft</label></fieldset><button :disabled="busy">{{ pendingIntake ? 'Retry unchanged intake' : 'Save case' }}</button></form></section>
 </main>
</template>
<style scoped>
.cases-page{max-width:1400px;margin:auto;padding:2rem;color:hsl(var(--foreground));}header{display:flex;justify-content:space-between;gap:1rem;align-items:center;margin-bottom:1.5rem}h1{font-size:1.7rem;font-weight:650}h2{font-size:1.2rem;font-weight:600;margin-bottom:1rem}h3{font-weight:600;margin:1rem 0 .5rem}.workspace{display:grid;grid-template-columns:minmax(250px,1fr) 2fr;gap:1rem}.panel{border:1px solid hsl(var(--border));border-radius:.7rem;padding:1.25rem;background:hsl(var(--card));margin-bottom:1rem}label{display:grid;gap:.35rem;margin:.75rem 0}input,textarea,select{border:1px solid hsl(var(--border));border-radius:.35rem;padding:.6rem;background:hsl(var(--background));width:100%}button{border:1px solid hsl(var(--border));border-radius:.35rem;padding:.55rem .85rem;background:hsl(var(--secondary));cursor:pointer}button:disabled{opacity:.5;cursor:default}button[aria-pressed=true]{border-color:hsl(var(--primary))}ul{list-style:none;padding:0}li{margin:.6rem 0}li button{width:100%;text-align:left;display:grid;gap:.2rem}small{overflow-wrap:anywhere}dl{display:flex;gap:1.5rem;flex-wrap:wrap}dt{font-size:.8rem;opacity:.7}.narrative{white-space:pre-wrap;overflow-wrap:anywhere}.actions{border-top:1px solid hsl(var(--border));margin-top:1rem;padding-top:.5rem}.error{color:#b42318;margin:1rem 0}.notice{color:#087443;margin:1rem 0}.intake{max-width:900px}.checkbox{display:flex;align-items:center}.checkbox input{width:auto}fieldset{border:0;padding:0}@media(max-width:800px){.workspace{grid-template-columns:1fr}.cases-page{padding:1rem}header{align-items:flex-start}}
</style>
