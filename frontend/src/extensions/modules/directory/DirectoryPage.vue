<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { uploadDirectoryWorkbook, approveDirectoryPreview, createDirectoryPreview, loadDirectory, reviewDirectoryPreview, revokeDirectoryEmployee, type DirectoryPreview, type DirectoryStatus } from './api'
const auth = useAuthStore()
const can = (action: string) => auth.hasExtensionPermission(`directory:${action}`)
const roster = ref<DirectoryStatus | null>(null)
const preview = ref<DirectoryPreview | null>(null)
const input = ref('')
const workbookFile = ref<File | null>(null)
const worksheet = ref('Sheet1')
const skipHeader = ref(true)
const columns = Array.from({ length: 64 }, (_, index) => {
 let remaining = index + 1
 let label = ''
 while (remaining > 0) { remaining--; label = String.fromCharCode(65 + remaining % 26) + label; remaining = Math.floor(remaining / 26) }
 return { index, label }
})
const mapping = ref({ employee_id: 0, phone: 1, active: 2, zone: 3, department: 4 })
function selectWorkbook(event: Event) { workbookFile.value = (event.target as HTMLInputElement).files?.[0] ?? null; preview.value = null; reviewed.value = false }
async function prepareWorkbook() { await run(async () => {
 if (!workbookFile.value) return
 if (workbookFile.value.size > 8 * 1024 * 1024) { error.value = 'The workbook exceeds the 8 MiB upload limit.'; return }
 if (!roster.value) await refresh()
 preview.value = await uploadDirectoryWorkbook(workbookFile.value, { expected_version: roster.value!.version, worksheet: worksheet.value, mapping: mapping.value, skip_header: skipHeader.value })
 reviewID.value = preview.value.preview_id; reviewed.value = false
}) }
const reviewID = ref('')
const reviewed = ref(false)
const busy = ref(false)
const error = ref('')
const notice = ref('')
const filter = ref('')
const employees = computed(() => (roster.value?.employees ?? []).filter(e => `${e.id} ${e.phone} ${e.zone} ${e.department}`.toLowerCase().includes(filter.value.toLowerCase())))
async function run(action: () => Promise<void>) {
 if (busy.value) return
 busy.value = true; error.value = ''; notice.value = ''
 try { await action() } catch (e: unknown) {
  const response = (e as { response?: { status?: number; data?: { error?: { code?: string } } } }).response
  if (response?.status === 409) { preview.value = null; reviewed.value = false; error.value = 'The directory changed or this approval was already used. Refresh the roster and create a new preview for review.' }
  else if (response?.status === 403) error.value = 'This action requires an explicit directory permission and a current verified session.'
  else if (response?.data?.error?.code === 'legacy_xls_unconfigured') error.value = 'Legacy XLS parsing is not configured by the host. Save the roster as XLSX and upload it again.'
  else if (response?.status === 400) error.value = 'The workbook or roster is invalid. Check the worksheet, column mapping, explicit active values, unique IDs and canonical phones. Formulas and macros are rejected.'
  else error.value = e instanceof SyntaxError ? 'Enter a valid JSON array of employee records.' : 'The directory request could not be completed. Refresh to check current state before retrying.'
 } finally { busy.value = false }
}
async function refresh() { roster.value = await loadDirectory() }
async function importFile(event: Event) {
 const file = (event.target as HTMLInputElement).files?.[0]
 if (!file) return
 await run(async () => { if (file.size > 900000) throw new SyntaxError('File too large'); input.value = await file.text(); JSON.parse(input.value); preview.value = null; reviewed.value = false })
}
async function prepare() { await run(async () => { if (!roster.value) await refresh(); preview.value = await createDirectoryPreview(JSON.parse(input.value), roster.value!.version); reviewID.value = preview.value.preview_id; reviewed.value = false }) }
async function review() { await run(async () => { preview.value = await reviewDirectoryPreview(reviewID.value.trim()); reviewed.value = false }) }
async function approve() { await run(async () => { if (!preview.value || !reviewed.value) return; await approveDirectoryPreview(preview.value); preview.value = null; reviewed.value = false; await refresh(); notice.value = 'Replacement approved and saved.' }) }
async function revoke(id: string) { await run(async () => { if (!roster.value) return; await revokeDirectoryEmployee(id, roster.value.version); await refresh(); notice.value = `Urgent revocation saved for ${id}.` }) }
onMounted(() => run(refresh))
</script>

<template>
 <main class="directory-page">
  <header><div><h1>Employee directory</h1><p>Review monthly roster replacements and immediately revoke eligibility.</p></div><button :disabled="busy" @click="run(refresh)">Refresh roster</button></header>
  <p v-if="error" role="alert" class="error">{{ error }}</p><p v-if="notice" role="status" class="notice">{{ notice }}</p><p v-if="busy" role="status">Saving or loading directory…</p>
  <section v-if="roster" aria-label="Directory status" class="status">
   <strong :class="roster.fresh ? 'notice' : 'error'">{{ roster.fresh ? 'Current roster' : 'Eligibility denied: roster missing, stale or future dated' }}</strong>
   <p>Version {{ roster.version }} · {{ roster.total }} records · {{ roster.active }} active, non-revoked · {{ roster.revoked }} revoked</p>
   <p>Last approved import: {{ roster.version ? new Date(roster.imported_at).toLocaleString() : 'None' }} · Maximum age: {{ roster.freshness_seconds / 3600 }} hours</p>
  </section>
  <section v-if="can('import')" class="panel">
   <h2>Import roster workbook</h2><p>Upload an XLSX workbook for server validation. Employee IDs and phones should be stored as text. Missing employees become inactive; urgent revocations remain in force. Maximum file size: 8 MiB. Legacy XLS requires a host-configured local worker.</p>
   <label>Roster workbook <input type="file" accept=".xlsx,.xls" :disabled="busy" @change="selectWorkbook" /></label>
   <label>Worksheet name <input v-model="worksheet" :disabled="busy" /></label>
   <p>Enter the worksheet name shown on the spreadsheet tab, then select the column for each field. Zone and department are optional.</p>
   <div class="mapping-fields">
    <label>Employee ID <select v-model.number="mapping.employee_id" :disabled="busy"><option v-for="column in columns" :key="column.index" :value="column.index">{{ column.label }}</option></select></label>
    <label>Phone <select v-model.number="mapping.phone" :disabled="busy"><option v-for="column in columns" :key="column.index" :value="column.index">{{ column.label }}</option></select></label>
    <label>Active <select v-model.number="mapping.active" :disabled="busy"><option v-for="column in columns" :key="column.index" :value="column.index">{{ column.label }}</option></select></label>
    <label>Zone <select v-model.number="mapping.zone" :disabled="busy"><option :value="-1">Not provided</option><option v-for="column in columns" :key="column.index" :value="column.index">{{ column.label }}</option></select></label>
    <label>Department <select v-model.number="mapping.department" :disabled="busy"><option :value="-1">Not provided</option><option v-for="column in columns" :key="column.index" :value="column.index">{{ column.label }}</option></select></label>
   </div>
   <label><input v-model="skipHeader" type="checkbox" :disabled="busy" /> First row is a header</label>
   <button :disabled="busy || !workbookFile || !roster || !worksheet.trim()" @click="prepareWorkbook">Upload, validate and save review</button>
   <details><summary>Alternative: canonical JSON roster</summary><p>Provide an array of records with id, phone, active, zone and department.</p>
   <label>Roster JSON file <input type="file" accept=".json,application/json" :disabled="busy" @change="importFile" /></label>
   <label>Employee records<textarea v-model="input" rows="8" spellcheck="false" :disabled="busy" aria-label="Employee roster JSON" /></label>
   <button :disabled="busy || !input.trim() || !roster" @click="prepare">Validate and save preview</button></details>
  </section>
  <section v-if="can('approve')" class="panel">
   <h2>Open saved review</h2><label>Preview ID <input v-model="reviewID" :disabled="busy" /></label><button :disabled="busy || !reviewID.trim()" @click="review">Load exact preview</button>
  </section>
  <section v-if="preview" class="panel">
   <h2>Review full replacement</h2><p>Preview {{ preview.preview_id }} · Replaces version {{ preview.expected_version }}</p><p class="digest">Canonical roster SHA-256 {{ preview.digest }}</p><p v-if="preview.source_sha256" class="digest">Uploaded workbook SHA-256 {{ preview.source_sha256 }}</p><p>Prepared by {{ preview.created_by }} at {{ new Date(preview.created_at).toLocaleString() }}</p>
   <div class="table-scroll"><table><thead><tr><th>Employee ID</th><th>Phone</th><th>Zone</th><th>Department</th><th>Active</th></tr></thead><tbody><tr v-for="employee in preview.employees" :key="employee.id"><td>{{ employee.id }}</td><td>{{ employee.phone }}</td><td>{{ employee.zone }}</td><td>{{ employee.department }}</td><td>{{ employee.active ? 'Yes' : 'No' }}</td></tr></tbody></table></div>
   <p v-if="preview.approved_at">Approved by {{ preview.approved_by }} at {{ new Date(preview.approved_at).toLocaleString() }}</p>
   <template v-else-if="can('approve')"><label class="confirm"><input v-model="reviewed" type="checkbox" :disabled="busy" />I reviewed all {{ preview.employees.length }} records and approve this full replacement, including deactivation of missing employees.</label><button :disabled="busy || !reviewed" @click="approve">Approve exact roster replacement</button></template>
  </section>
  <section v-if="roster" class="panel"><h2>Current roster</h2><label>Filter records <input v-model="filter" type="search" /></label>
   <div class="table-scroll"><table><thead><tr><th>Employee ID</th><th>Phone</th><th>Zone</th><th>Department</th><th>Status</th><th v-if="can('revoke')">Urgent action</th></tr></thead><tbody><tr v-for="employee in employees" :key="employee.id"><td>{{ employee.id }}</td><td>{{ employee.phone }}</td><td>{{ employee.zone }}</td><td>{{ employee.department }}</td><td>{{ employee.revoked ? 'Revoked' : !employee.active ? 'Inactive' : roster.fresh ? 'Eligible' : 'Denied: roster not current' }}</td><td v-if="can('revoke')"><button :disabled="busy || employee.revoked" @click="revoke(employee.id)">{{ employee.revoked ? 'Revocation saved' : 'Revoke immediately' }}</button></td></tr></tbody></table></div>
   <p v-if="!employees.length">No matching employee records.</p>
  </section>
 </main>
</template>
<style scoped>
.directory-page{max-width:1200px;margin:0 auto;padding:2rem;color:hsl(var(--foreground))}header{display:flex;justify-content:space-between;gap:1rem;align-items:start}h1{font-size:1.7rem;font-weight:650}h2{font-size:1.2rem;font-weight:600;margin-bottom:.75rem}p{margin:.5rem 0;color:hsl(var(--muted-foreground))}.panel,.status{margin-top:1.5rem;padding:1.25rem;border:1px solid hsl(var(--border));border-radius:.5rem}label{display:block;margin:.75rem 0}input,textarea,select{border:1px solid hsl(var(--border));border-radius:.25rem;padding:.5rem;background:hsl(var(--background));margin-left:.5rem}textarea{display:block;width:100%;margin:.5rem 0;font-family:monospace}button{border:1px solid hsl(var(--border));border-radius:.3rem;padding:.5rem .8rem;font-weight:550;background:hsl(var(--secondary));white-space:nowrap}button:disabled{opacity:.45;cursor:not-allowed}.error{color:#b91c1c}.notice{color:#15803d}.digest{font-family:monospace;overflow-wrap:anywhere}.table-scroll{overflow:auto;max-height:30rem;margin:1rem 0}table{width:100%;text-align:left;border-collapse:collapse}th,td{padding:.6rem;border-bottom:1px solid hsl(var(--border))}th{position:sticky;top:0;background:hsl(var(--background))}.confirm{display:flex;align-items:start;gap:.6rem}.confirm input{margin:0}.mapping-fields{display:flex;flex-wrap:wrap;gap:1rem}.mapping-fields select{min-width:5rem}details{margin-top:1.5rem}summary{cursor:pointer}@media(max-width:700px){.directory-page{padding:1rem}header{display:block}header button{margin-top:1rem}}
</style>
