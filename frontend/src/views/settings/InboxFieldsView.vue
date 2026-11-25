<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useInboxViewerContext } from '@/composables/useInboxViewerContext'
import InboxFieldRequirement from '@/components/settings/InboxFieldRequirement.vue'
import InboxIntakeMappings from '@/components/settings/InboxIntakeMappings.vue'
import { useInboxDraftGuard } from '@/composables/useInboxDraftGuard'
import UnsavedChangesDialog from '@/components/shared/UnsavedChangesDialog.vue'
import { useI18n } from 'vue-i18n'
import { isAxiosError } from 'axios'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { PageHeader } from '@/components/shared'
import InboxTabs from '@/components/shared/InboxTabs.vue'
import {
  Plus,
  Search,
  Pencil,
  Archive,
  ListFilter,
  ArrowLeft,
} from 'lucide-vue-next'
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogCancel,
  AlertDialogAction,
} from '@/components/ui/alert-dialog'
import { useAuthStore } from '@/stores/auth'
import {
  inboxService,
  type FieldDefinition,
  type FieldKind,
  type FieldRequirement,
} from '@/services/inbox'
import '@/assets/inbox.css'

const { t } = useI18n()
const auth = useAuthStore()
const viewer = useInboxViewerContext()
const mode = ref('fields')
const canManageIntake = computed(
  () =>
    ['settings.general', 'flows.chatbot', 'chat', 'contacts'].every(
      (resource) => auth.hasPermission(resource, 'write'),
    ) && auth.hasPermission('contacts', 'read'),
)
const intakeEditor = ref<InstanceType<typeof InboxIntakeMappings>>()
function selectMode(next: string) {
  if (mode.value === next || busy.value) return
  const change = () => {
    editing.value = false
    mode.value = next
  }
  if (mode.value === 'intake') intakeEditor.value?.confirmLeave(change)
  else requestDiscard(change)
}
const canWrite = computed(() => auth.hasPermission('settings.general', 'write'))
const kinds: FieldKind[] = [
  'text',
  'email',
  'url',
  'number',
  'date',
  'boolean',
  'select',
]
const definitions = ref<FieldDefinition[]>([])
const enabled = ref(false),
  ready = ref(false),
  failed = ref(false),
  busy = ref(false)
const query = ref(''),
  scope = ref(''),
  tab = ref('active'),
  editing = ref(false),
  selected = ref('')
const newField = () => ({
  scope: 'case' as FieldDefinition['scope'],
  key: '',
  label: '',
  description: '',
  kind: 'text' as FieldKind,
  required_on_resolve: false,
  required_when: null as FieldRequirement | null,
  revision: undefined as number | undefined,
})
const form = ref(newField())
const requirementMode = computed({
  get: () =>
    form.value.required_on_resolve
      ? 'always'
      : form.value.required_when
        ? 'conditional'
        : 'optional',
  set: (mode: string) => {
    form.value.required_on_resolve = mode === 'always'
    form.value.required_when =
      mode === 'conditional' ? { match: 'all', predicates: [] } : null
  },
})
const requirementValid = ref(true),
  conflict = ref(false)
const choices = ref('')
const advancedKey = ref(false),
  baseline = ref(''),
  saveError = ref('')
const { discardOpen, confirmDiscard, cancelDiscard, requestDiscard } =
  useInboxDraftGuard(
    () =>
      editing.value &&
      JSON.stringify([form.value, choices.value]) !== baseline.value,
  )
watch(
  () => form.value.label,
  (label) => {
    if (selected.value || advancedKey.value) return
    let key = label
      .normalize('NFKD')
      .replace(/[\u0300-\u036f]/g, '')
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, '_')
      .replace(/^_+|_+$/g, '')
    if (!/^[a-z]/.test(key)) key = 'field_' + key
    key = key.slice(0, 60) || 'field'
    const base = key
    let suffix = 2
    while (
      definitions.value.some(
        (d) => d.scope === form.value.scope && d.key === key,
      )
    )
      key = base + '_' + suffix++
    form.value.key = key
  },
)
const archive = ref<FieldDefinition | null>(null)
const filtered = computed(() =>
  definitions.value.filter(
    (d) =>
      (tab.value === 'archived' ? !!d.archived_at : !d.archived_at) &&
      (!scope.value || d.scope === scope.value) &&
      (d.label + ' ' + d.key)
        .toLocaleLowerCase()
        .includes(query.value.toLocaleLowerCase()),
  ),
)
let generation = 0,
  mutationGeneration = 0
async function load() {
  const request = ++generation,
    context = viewer.value
  try {
    const settings = await inboxService.settings()
    if (request !== generation || context !== viewer.value) return
    enabled.value = settings.data.data.enabled
    const response = enabled.value
      ? await inboxService.fieldDefinitions()
      : null
    if (request !== generation || context !== viewer.value) return
    definitions.value = response?.data.data.definitions ?? []
    ready.value = true
    failed.value = false
  } catch {
    if (request === generation && context === viewer.value) failed.value = true
  }
}
function begin(d?: FieldDefinition) {
  if (busy.value) return
  saveError.value = ''
  conflict.value = false
  requirementValid.value = true
  advancedKey.value = false
  selected.value = d?.id ?? ''
  form.value = d
    ? {
        scope: d.scope,
        key: d.key,
        label: d.label,
        description: d.description ?? '',
        kind: d.kind,
        required_on_resolve: d.required_on_resolve,
        required_when: d.required_when
          ? JSON.parse(JSON.stringify(d.required_when))
          : null,
        revision: d.revision,
      }
    : newField()
  choices.value = d?.options?.join('\n') ?? ''
  editing.value = true
  baseline.value = JSON.stringify([form.value, choices.value])
}
const choiceError = computed(() => {
  if (selected.value || form.value.kind !== 'select') return ''
  const lines = choices.value
    .split('\n')
    .map((line, index) => ({ text: line.trim(), line: index + 1 }))
    .filter((item) => item.text)
  if (!lines.length || lines.length > 100) return t('inboxReview.choiceCount')
  const seen = new Set<string>()
  for (const item of lines) {
    if (Array.from(item.text).length > 100)
      return t('inboxReview.choiceLength', { line: item.line })
    if (seen.has(item.text))
      return t('inboxReview.choiceDuplicate', { line: item.line })
    seen.add(item.text)
  }
  return ''
})
async function save() {
  if (
    busy.value ||
    conflict.value ||
    !canWrite.value ||
    (requirementMode.value === 'conditional' && !requirementValid.value)
  )
    return
  if (choiceError.value) {
    saveError.value = choiceError.value
    return
  }
  busy.value = true
  const request = ++mutationGeneration,
    context = viewer.value
  try {
    if (selected.value)
      await inboxService.updateFieldDefinition(selected.value, {
        label: form.value.label,
        description: form.value.description,
        required_on_resolve: form.value.required_on_resolve,
        required_when: form.value.required_when,
        revision: form.value.revision,
      })
    else
      await inboxService.createFieldDefinition({
        ...form.value,
        options:
          form.value.kind === 'select'
            ? choices.value
                .split('\n')
                .map((s) => s.trim())
                .filter(Boolean)
            : undefined,
      })
    if (request !== mutationGeneration || context !== viewer.value) return
    editing.value = false
    await load()
    if (request !== mutationGeneration || context !== viewer.value) return
    toast.success(t('settings.saved'))
  } catch (error) {
    if (request !== mutationGeneration || context !== viewer.value) return
    conflict.value = isAxiosError(error) && error.response?.status === 409
    const detail =
      isAxiosError(error) && error.response?.status === 400
        ? error.response.data?.message
        : undefined
    saveError.value = conflict.value
      ? t('inboxIntake.conflict')
      : typeof detail === 'string'
        ? detail
        : t('inboxFields.saveError')
    toast.error(saveError.value)
  } finally {
    if (request === mutationGeneration && context === viewer.value)
      busy.value = false
  }
}
function reloadDefinition() {
  requestDiscard(async () => {
    const id = selected.value
    await load()
    if (failed.value) return
    const current = definitions.value.find((d) => d.id === id && !d.archived_at)
    if (current) begin(current)
    else editing.value = false
  })
}
async function archiveField() {
  if (!archive.value || busy.value) return
  const request = ++mutationGeneration,
    context = viewer.value
  busy.value = true
  try {
    await inboxService.updateFieldDefinition(archive.value.id, {
      archive: true,
      revision: archive.value.revision,
    })
    if (request !== mutationGeneration || context !== viewer.value) return
    archive.value = null
    await load()
  } catch {
    if (request === mutationGeneration && context === viewer.value)
      toast.error(t('inboxFields.saveError'))
  } finally {
    if (request === mutationGeneration && context === viewer.value)
      busy.value = false
  }
}
onMounted(load)
watch(viewer, () => {
  cancelDiscard()
  generation++
  mutationGeneration++
  editing.value = false
  mode.value = 'fields'
  archive.value = null
  definitions.value = []
  form.value = newField()
  ready.value = false
  busy.value = false
  void load()
})
onUnmounted(() => {
  generation++
  mutationGeneration++
})
</script>
<template>
  <main class="inbox-ui inbox-page">
    <UnsavedChangesDialog
      :open="discardOpen"
      @leave="confirmDiscard"
      @stay="cancelDiscard"
    />
    <PageHeader
      :title="t('inboxFields.title')"
      :description="t('inboxUX.fieldsDescription')"
      ><template #actions
        ><Button
          v-if="editing && mode === 'fields'"
          form="inbox-field-editor"
          type="submit"
          :disabled="
            busy ||
            conflict ||
            (requirementMode === 'conditional' && !requirementValid)
          "
          >{{ t('common.save') }}</Button
        ><Button
          v-if="canWrite && ready && enabled && !editing && mode === 'fields'"
          @click="begin()"
          ><Plus class="mr-2 h-4 w-4" aria-hidden="true" />{{
            t('inboxUX.newField')
          }}</Button
        ></template
      ></PageHeader
    >
    <div class="inbox-page-scroll">
      <div class="inbox-container">
        <div v-if="failed" role="alert" class="inbox-notice">
          {{ t('inboxFields.loadError')
          }}<Button size="sm" variant="outline" @click="load">{{
            t('common.retry')
          }}</Button>
        </div>
        <p v-else-if="ready && !enabled">{{ t('inboxFields.enableFirst') }}</p>
        <template v-else-if="ready">
          <InboxTabs
            v-if="canManageIntake"
            :model-value="mode"
            :label="t('inboxFields.title')"
            :items="[
              { value: 'fields', label: t('inboxFields.title') },
              { value: 'intake', label: t('inboxIntake.mappings') },
            ]"
            @update:model-value="selectMode"
          />
          <InboxIntakeMappings
            v-if="mode === 'intake' && canManageIntake"
            ref="intakeEditor"
          />
          <template v-else>
            <template v-if="!editing">
              <InboxTabs
                v-model="tab"
                :label="t('inboxFields.title')"
                :items="[
                  { value: 'active', label: t('inboxUX.activeFields') },
                  { value: 'archived', label: t('inboxUX.archivedFields') },
                ]"
              />
              <div class="inbox-toolbar">
                <label class="inbox-search"
                  ><Search class="h-4 w-4 shrink-0" aria-hidden="true" /><input
                    v-model="query"
                    :aria-label="t('inboxUX.searchFields')"
                    :placeholder="t('inboxUX.searchFields')" /></label
                ><label class="inbox-label"
                  >{{ t('inboxFields.appliesTo')
                  }}<select v-model="scope">
                    <option value="">{{ t('inboxSla.any') }}</option>
                    <option value="case">
                      {{ t('inboxFields.scope.case') }}
                    </option>
                    <option value="contact">
                      {{ t('inboxFields.scope.contact') }}
                    </option>
                  </select></label
                >
              </div>
              <div class="inbox-panel">
                <ul v-if="filtered.length" class="inbox-list">
                  <li v-for="d in filtered" :key="d.id" class="inbox-list-row">
                    <div class="inbox-list-copy">
                      <p class="inbox-list-title">{{ d.label }}</p>
                      <p class="inbox-caption mt-1">{{ d.key }}</p>
                    </div>
                    <span class="inbox-badge">{{
                      t('inboxFields.scope.' + d.scope)
                    }}</span
                    ><span class="inbox-caption min-w-20">{{
                      t('inboxFields.kind.' + d.kind)
                    }}</span
                    ><span
                      v-if="d.required_on_resolve && !d.archived_at"
                      class="inbox-badge"
                      >{{ t('inboxFields.required') }}</span
                    >
                    <span
                      v-if="d.conditional_required && !d.archived_at"
                      class="inbox-badge"
                      >{{ t('inboxIntake.conditional') }}</span
                    >
                    <div v-if="canWrite && !d.archived_at" class="flex gap-1">
                      <Button variant="outline" size="sm" @click="begin(d)"
                        ><Pencil
                          class="mr-1.5 h-3.5 w-3.5"
                          aria-hidden="true"
                        />{{ t('common.edit') }}</Button
                      ><Button
                        variant="ghost"
                        size="icon"
                        :aria-label="t('inboxFields.archive') + ' ' + d.label"
                        @click="archive = d"
                        ><Archive class="h-4 w-4" aria-hidden="true"
                      /></Button>
                    </div>
                  </li>
                </ul>
                <div v-else class="inbox-empty">
                  <ListFilter class="h-8 w-8" aria-hidden="true" />
                  <h2>
                    {{
                      t(
                        query || scope
                          ? 'inboxUX.noMatches'
                          : tab === 'archived'
                            ? 'inboxQuality.noArchivedFields'
                            : 'inboxUX.noFields',
                      )
                    }}
                  </h2>
                  <p>
                    {{
                      t(
                        tab === 'archived'
                          ? 'inboxUX.archiveHelp'
                          : 'inboxUX.noFieldsHelp',
                      )
                    }}
                  </p>
                  <Button
                    v-if="canWrite && tab === 'active'"
                    variant="outline"
                    @click="begin()"
                    >{{ t('inboxUX.newField') }}</Button
                  >
                </div>
              </div>
              <p class="inbox-caption mt-4">{{ t('inboxUX.fieldHelp') }}</p>
            </template>
            <template v-else
              ><Button
                variant="ghost"
                size="sm"
                class="mb-4 -ml-2"
                :disabled="busy"
                @click="requestDiscard(() => (editing = false))"
                ><ArrowLeft class="mr-2 h-4 w-4" aria-hidden="true" />{{
                  t('inboxUX.backToFields')
                }}</Button
              >
              <form
                id="inbox-field-editor"
                class="inbox-form"
                :aria-busy="busy"
                @submit.prevent="save"
              >
                <p
                  v-if="saveError"
                  role="alert"
                  class="inbox-notice text-destructive"
                >
                  {{ saveError }}
                  <Button
                    v-if="conflict"
                    type="button"
                    variant="outline"
                    @click="reloadDefinition"
                    >{{ t('inboxIntake.reload') }}</Button
                  >
                </p>
                <fieldset :disabled="busy" class="contents">
                  <section class="inbox-form-section">
                    <h2>
                      {{
                        t(selected ? 'inboxUX.editField' : 'inboxFields.add')
                      }}
                    </h2>
                    <p>{{ t('inboxUX.fieldHelp') }}</p>
                    <div class="inbox-grid">
                      <label class="inbox-label"
                        >{{ t('inboxFields.label')
                        }}<input
                          v-model.trim="form.label"
                          maxlength="100"
                          required /></label
                      ><label v-if="selected || advancedKey" class="inbox-label"
                        >{{ t('inboxFields.key')
                        }}<input
                          v-model.trim="form.key"
                          :aria-label="t('inboxFields.key')"
                          aria-describedby="inbox-field-key-help"
                          pattern="[a-z][a-z0-9_]*"
                          maxlength="64"
                          required
                          :disabled="!!selected"
                        /><span
                          id="inbox-field-key-help"
                          class="inbox-caption"
                          >{{ t('inboxUX.fieldKeyHelp') }}</span
                        ></label
                      ><label class="inbox-label"
                        >{{ t('inboxFields.appliesTo')
                        }}<select v-model="form.scope" :disabled="!!selected">
                          <option value="case">
                            {{ t('inboxFields.scope.case') }}
                          </option>
                          <option value="contact">
                            {{ t('inboxFields.scope.contact') }}
                          </option>
                        </select></label
                      ><label class="inbox-label"
                        >{{ t('inboxFields.type')
                        }}<select v-model="form.kind" :disabled="!!selected">
                          <option
                            v-for="kind in kinds"
                            :key="kind"
                            :value="kind"
                          >
                            {{ t('inboxFields.kind.' + kind) }}
                          </option>
                        </select></label
                      >
                    </div>
                    <label v-if="!selected" class="inbox-check mt-4"
                      ><input v-model="advancedKey" type="checkbox" />{{
                        t('inboxQuality.advancedKey')
                      }}
                      <span v-if="!advancedKey" class="inbox-caption">{{
                        form.key
                      }}</span></label
                    >
                    <label class="inbox-label mt-4"
                      >{{ t('inboxQuality.fieldHelpText')
                      }}<textarea
                        v-model="form.description"
                        rows="2"
                        maxlength="1000"
                      />
                    </label>
                    <p class="inbox-caption mt-2">
                      {{
                        t(
                          form.kind === 'email'
                            ? 'inboxIntake.emailHelp'
                            : form.kind === 'url'
                              ? 'inboxIntake.urlHelp'
                              : 'inboxQuality.fieldValidation.' + form.kind,
                        )
                      }}
                    </p>
                    <p v-if="selected" class="inbox-caption mt-3">
                      {{ t('inboxUX.fieldTypeLocked') }}
                    </p>
                  </section>
                  <section
                    v-if="form.kind === 'select'"
                    class="inbox-form-section"
                  >
                    <label class="inbox-label"
                      >{{ t('inboxFields.choices')
                      }}<textarea
                        v-model="choices"
                        :aria-label="t('inboxFields.choices')"
                        :aria-invalid="!!choiceError"
                        aria-describedby="inbox-choice-help inbox-choice-error"
                        rows="5"
                        required
                        :disabled="!!selected"
                      /><span class="inbox-caption">{{
                        t('inboxUX.fieldOptionsHelp')
                      }}</span></label
                    >
                    <p id="inbox-choice-help" class="inbox-caption">
                      {{ t('inboxReview.choiceHelp') }}
                    </p>
                    <p
                      v-if="choiceError"
                      id="inbox-choice-error"
                      role="alert"
                      class="text-sm text-destructive"
                    >
                      {{ choiceError }}
                    </p>
                  </section>
                  <section class="inbox-form-section">
                    <h2>{{ t('inboxIntake.resolveTitle') }}</h2>
                    <p id="inbox-field-required-help">
                      {{ t('inboxIntake.resolveHelp') }}
                    </p>
                    <label class="inbox-label"
                      >{{ t('inboxIntake.requirement') }}
                      <select
                        v-model="requirementMode"
                        aria-describedby="inbox-field-required-help"
                        :disabled="busy"
                      >
                        <option value="optional">
                          {{ t('inboxIntake.optional') }}
                        </option>
                        <option value="always">
                          {{ t('inboxIntake.always') }}
                        </option>
                        <option value="conditional">
                          {{ t('inboxIntake.when') }}
                        </option>
                      </select>
                    </label>
                    <fieldset
                      :disabled="busy"
                      class="mt-4 min-w-0"
                      v-if="form.required_when"
                    >
                      <legend class="sr-only">
                        {{ t('inboxIntake.when') }}
                      </legend>
                      <InboxFieldRequirement
                        v-model="form.required_when"
                        @validity="requirementValid = $event"
                      />
                    </fieldset>
                  </section>
                  <footer class="inbox-form-footer">
                    <Button
                      type="submit"
                      :disabled="
                        busy ||
                        conflict ||
                        (requirementMode === 'conditional' && !requirementValid)
                      "
                      >{{ t('common.save') }}</Button
                    ><Button
                      type="button"
                      variant="outline"
                      :disabled="busy"
                      @click="requestDiscard(() => (editing = false))"
                      >{{ t('common.cancel') }}</Button
                    >
                  </footer>
                </fieldset>
              </form></template
            >
          </template>
        </template>
        <p v-else role="status" class="inbox-caption">
          {{ t('common.loading') }}
        </p>
      </div>
    </div>
    <AlertDialog
      :open="!!archive"
      @update:open="
        (value) => {
          if (!value) archive = null
        }
      "
      ><AlertDialogContent
        ><AlertDialogHeader
          ><AlertDialogTitle
            >{{ t('inboxFields.archive') }}
            {{ archive?.label }}?</AlertDialogTitle
          ><AlertDialogDescription>{{
            t('inboxUX.archiveHelp')
          }}</AlertDialogDescription></AlertDialogHeader
        ><AlertDialogFooter
          ><AlertDialogCancel>{{ t('common.cancel') }}</AlertDialogCancel
          ><AlertDialogAction :disabled="busy" @click="archiveField">{{
            t('inboxFields.archive')
          }}</AlertDialogAction></AlertDialogFooter
        ></AlertDialogContent
      ></AlertDialog
    >
  </main>
</template>
