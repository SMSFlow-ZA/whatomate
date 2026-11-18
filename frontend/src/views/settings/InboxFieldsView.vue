<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from '@/lib/toast'
import { useAuthStore } from '@/stores/auth'
import { inboxService, type FieldDefinition, type FieldKind } from '@/services/inbox'

const { t } = useI18n()
const auth = useAuthStore()
const canWrite = computed(() => auth.hasPermission('settings.general', 'write'))
const kinds: FieldKind[] = ['text', 'number', 'date', 'boolean', 'select']
const definitions = ref<FieldDefinition[]>([])
const enabled = ref(false)
const ready = ref(false)
const failed = ref(false)
const busy = ref(false)
const newField = () => ({ scope: 'case' as FieldDefinition['scope'], key: '', label: '', kind: 'text' as FieldKind, required_on_resolve: false })
const form = ref(newField())
const choices = ref('')
const archive = ref('')
async function load() {
  try {
    enabled.value = (await inboxService.settings()).data.data.enabled
    if (enabled.value) definitions.value = (await inboxService.fieldDefinitions()).data.data.definitions
    ready.value = true
    failed.value = false
  } catch { failed.value = true }
}
async function create() {
  busy.value = true
  try {
    const options = form.value.kind === 'select' ? choices.value.split('\n').map(s => s.trim()).filter(Boolean) : undefined
    await inboxService.createFieldDefinition({ ...form.value, options })
    form.value = newField()
    choices.value = ''
    await load()
  } catch { toast.error(t('inboxFields.saveError')) }
  finally { busy.value = false }
}
async function update(d: FieldDefinition, remove = false) {
  busy.value = true
  try {
    await inboxService.updateFieldDefinition(d.id, remove ? { archive: true } : { label: d.label, required_on_resolve: d.required_on_resolve })
    archive.value = ''
    await load()
  } catch { toast.error(t('inboxFields.saveError')) }
  finally { busy.value = false }
}
onMounted(load)
</script>
<template>
  <main class="mx-auto grid max-w-4xl gap-4 p-4">
    <h1 class="text-xl font-semibold">{{ t('inboxFields.title') }}</h1>
    <p v-if="failed" role="alert">{{ t('inboxFields.loadError') }}</p>
    <p v-else-if="ready && !enabled">{{ t('inboxFields.enableFirst') }}</p>
    <template v-else-if="ready">
      <form v-if="canWrite" class="grid gap-3 rounded-lg border border-border p-4" @submit.prevent="create">
        <h2 class="font-medium">{{ t('inboxFields.add') }}</h2>
        <div class="grid gap-2 sm:grid-cols-2">
          <label>{{ t('inboxFields.label') }} <input v-model.trim="form.label" maxlength="100" required /></label>
          <label>{{ t('inboxFields.key') }} <input v-model.trim="form.key" pattern="[a-z][a-z0-9_]*" maxlength="64" required /></label>
          <label>{{ t('inboxFields.appliesTo') }} <select v-model="form.scope"><option value="case">{{ t('inboxFields.scope.case') }}</option><option value="contact">{{ t('inboxFields.scope.contact') }}</option></select></label>
          <label>{{ t('inboxFields.type') }} <select v-model="form.kind"><option v-for="kind in kinds" :key="kind" :value="kind">{{ t('inboxFields.kind.' + kind) }}</option></select></label>
        </div>
        <label v-if="form.kind === 'select'">{{ t('inboxFields.choices') }} <textarea v-model="choices" rows="4" required /></label>
        <label><input v-model="form.required_on_resolve" type="checkbox" /> {{ t('inboxFields.required') }}</label>
        <button :disabled="busy" type="submit" class="justify-self-start">{{ t('common.save') }}</button>
      </form>
      <ul class="grid gap-2">
        <li v-for="d in definitions" :key="d.id" class="rounded-lg border border-border p-3">
          <form class="grid gap-2" @submit.prevent="update(d)">
            <p class="text-sm text-muted-foreground">{{ d.key }} · {{ t('inboxFields.scope.' + d.scope) }} · {{ t('inboxFields.kind.' + d.kind) }} <span v-if="d.archived_at"> · {{ t('inboxFields.archived') }}</span></p>
            <label>{{ t('inboxFields.label') }} <input v-model.trim="d.label" maxlength="100" required :disabled="!canWrite || !!d.archived_at" /></label>
            <label><input v-model="d.required_on_resolve" type="checkbox" :disabled="!canWrite || !!d.archived_at" /> {{ t('inboxFields.required') }}</label>
            <div v-if="canWrite && !d.archived_at" class="flex flex-wrap items-center gap-2">
              <button type="submit" :disabled="busy" class="outline small">{{ t('common.save') }}</button>
              <button v-if="archive !== d.id" type="button" @click="archive = d.id" class="ghost small">{{ t('inboxFields.archive') }}</button>
              <template v-else>
                <span class="text-sm">{{ t('inboxFields.archiveConfirm') }}</span>
                <button type="button" :disabled="busy" @click="update(d, true)" class="outline small">{{ t('inboxFields.archive') }}</button>
                <button type="button" @click="archive = ''" class="ghost small">{{ t('common.cancel') }}</button>
              </template>
            </div>
          </form>
        </li>
      </ul>
    </template>
  </main>
</template>

<style scoped>
input:not([type="checkbox"]), select, textarea { @apply block w-full rounded-md border border-input bg-background px-2 py-1; }
</style>
