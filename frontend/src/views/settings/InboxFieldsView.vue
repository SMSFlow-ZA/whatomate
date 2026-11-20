<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useInboxDraftGuard } from '@/composables/useInboxDraftGuard'
import UnsavedChangesDialog from '@/components/shared/UnsavedChangesDialog.vue'
import { useI18n } from 'vue-i18n'
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
} from '@/services/inbox'
import '@/assets/inbox.css'

const { t } = useI18n()
const auth = useAuthStore()
const canWrite = computed(() => auth.hasPermission('settings.general', 'write'))
const kinds: FieldKind[] = ['text', 'number', 'date', 'boolean', 'select']
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
})
const form = ref(newField())
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
async function load() {
  try {
    enabled.value = (await inboxService.settings()).data.data.enabled
    if (enabled.value)
      definitions.value = (
        await inboxService.fieldDefinitions()
      ).data.data.definitions
    ready.value = true
    failed.value = false
  } catch {
    failed.value = true
  }
}
function begin(d?: FieldDefinition) {
  saveError.value = ''
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
      }
    : newField()
  choices.value = d?.options?.join('\n') ?? ''
  editing.value = true
  baseline.value = JSON.stringify([form.value, choices.value])
}
async function save() {
  if (busy.value) return
  busy.value = true
  try {
    if (selected.value)
      await inboxService.updateFieldDefinition(selected.value, {
        label: form.value.label,
        description: form.value.description,
        required_on_resolve: form.value.required_on_resolve,
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
    editing.value = false
    await load()
    toast.success(t('settings.saved'))
  } catch {
    saveError.value = t('inboxFields.saveError')
    toast.error(saveError.value)
  } finally {
    busy.value = false
  }
}
async function archiveField() {
  if (!archive.value || busy.value) return
  busy.value = true
  try {
    await inboxService.updateFieldDefinition(archive.value.id, {
      archive: true,
    })
    archive.value = null
    await load()
  } catch {
    toast.error(t('inboxFields.saveError'))
  } finally {
    busy.value = false
  }
}
onMounted(load)
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
          v-if="editing"
          form="inbox-field-editor"
          type="submit"
          :disabled="busy"
          >{{ t('common.save') }}</Button
        ><Button
          v-if="canWrite && ready && enabled && !editing"
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
              @click="requestDiscard(() => (editing = false))"
              ><ArrowLeft class="mr-2 h-4 w-4" aria-hidden="true" />{{
                t('inboxUX.backToFields')
              }}</Button
            >
            <form
              id="inbox-field-editor"
              class="inbox-form"
              @submit.prevent="save"
            >
              <p
                v-if="saveError"
                role="alert"
                class="inbox-notice text-destructive"
              >
                {{ saveError }}
              </p>
              <section class="inbox-form-section">
                <h2>
                  {{ t(selected ? 'inboxUX.editField' : 'inboxFields.add') }}
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
                    /><span id="inbox-field-key-help" class="inbox-caption">{{
                      t('inboxUX.fieldKeyHelp')
                    }}</span></label
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
                      <option v-for="kind in kinds" :key="kind" :value="kind">
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
                  {{ t('inboxQuality.fieldValidation.' + form.kind) }}
                </p>
                <p v-if="selected" class="inbox-caption mt-3">
                  {{ t('inboxUX.fieldTypeLocked') }}
                </p>
              </section>
              <section v-if="form.kind === 'select'" class="inbox-form-section">
                <label class="inbox-label"
                  >{{ t('inboxFields.choices')
                  }}<textarea
                    v-model="choices"
                    rows="5"
                    required
                    :disabled="!!selected"
                  /><span class="inbox-caption">{{
                    t('inboxUX.fieldOptionsHelp')
                  }}</span></label
                >
              </section>
              <section class="inbox-form-section">
                <label class="inbox-check"
                  ><input
                    v-model="form.required_on_resolve"
                    :aria-label="t('inboxFields.required')"
                    aria-describedby="inbox-field-required-help"
                    type="checkbox"
                  /><span
                    >{{ t('inboxFields.required')
                    }}<span
                      id="inbox-field-required-help"
                      class="inbox-caption mt-1 block"
                      >{{ t('inboxUX.fieldRequiredHelp') }}</span
                    ></span
                  ></label
                >
              </section>
              <footer class="inbox-form-footer">
                <Button type="submit" :disabled="busy">{{
                  t('common.save')
                }}</Button
                ><Button
                  type="button"
                  variant="outline"
                  :disabled="busy"
                  @click="requestDiscard(() => (editing = false))"
                  >{{ t('common.cancel') }}</Button
                >
              </footer>
            </form></template
          >
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
