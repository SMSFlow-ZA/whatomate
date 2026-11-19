<script setup lang="ts">
import Modal from '@/components/oat/Modal.vue'
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from '@/lib/toast'
import { PageHeader } from '@/components/shared'
import InboxTabs from '@/components/shared/InboxTabs.vue'
import {
  Plus,
  Search,
  Pencil,
  Archive,
  Filter as ListFilter,
  ArrowLeft,
} from '@/lib/icons'
import { useAuthStore } from '@/stores/auth'
import {
  inboxService,
  type FieldDefinition,
  type FieldKind,
} from '@/services/inbox'

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
  kind: 'text' as FieldKind,
  required_on_resolve: false,
})
const form = ref(newField())
const choices = ref('')
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
  selected.value = d?.id ?? ''
  form.value = d
    ? {
        scope: d.scope,
        key: d.key,
        label: d.label,
        kind: d.kind,
        required_on_resolve: d.required_on_resolve,
      }
    : newField()
  choices.value = d?.options?.join('\n') ?? ''
  editing.value = true
}
async function save() {
  if (busy.value) return
  busy.value = true
  try {
    if (selected.value)
      await inboxService.updateFieldDefinition(selected.value, {
        label: form.value.label,
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
    toast.error(t('inboxFields.saveError'))
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
    <PageHeader
      :title="t('inboxFields.title')"
      :description="t('inboxUX.fieldsDescription')"
      ><template #actions
        ><button
          v-if="editing"
          form="inbox-field-editor"
          type="submit"
          :disabled="busy"
          class=""
        >
          {{ t('common.save') }}</button
        ><button
          v-if="canWrite && ready && enabled && !editing"
          @click="begin()"
          class=""
          type="button"
        >
          <Plus class="mr-2 h-4 w-4" aria-hidden="true" />{{
            t('inboxUX.newField')
          }}
        </button></template
      ></PageHeader
    >
    <div class="inbox-page-scroll">
      <div class="inbox-container">
        <div v-if="failed" role="alert" class="inbox-notice">
          {{ t('inboxFields.loadError')
          }}<button @click="load" class="outline small" type="button">
            {{ t('common.retry') }}
          </button>
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
                  <option value="">{{ t('inboxRules.any') }}</option>
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
                    <button
                      @click="begin(d)"
                      class="outline small"
                      type="button"
                    >
                      <Pencil
                        class="inbox-icon-gap inbox-icon-height-small inbox-icon-width-small"
                        aria-hidden="true"
                      />{{ t('common.edit') }}</button
                    ><button
                      :aria-label="t('inboxFields.archive') + ' ' + d.label"
                      @click="archive = d"
                      class="ghost icon"
                      type="button"
                    >
                      <Archive class="h-4 w-4" aria-hidden="true" />
                    </button>
                  </div>
                </li>
              </ul>
              <div v-else class="inbox-empty">
                <ListFilter class="h-8 w-8" aria-hidden="true" />
                <h2>
                  {{
                    t(
                      query || scope || tab === 'archived'
                        ? 'inboxUX.noMatches'
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
                <button
                  v-if="canWrite && tab === 'active'"
                  @click="begin()"
                  class="outline"
                  type="button"
                >
                  {{ t('inboxUX.newField') }}
                </button>
              </div>
            </div>
            <p class="inbox-caption mt-4">{{ t('inboxUX.fieldHelp') }}</p>
          </template>
          <template v-else
            ><button
              class="ghost small mb-4 -ml-2"
              @click="editing = false"
              type="button"
            >
              <ArrowLeft class="mr-2 h-4 w-4" aria-hidden="true" />{{
                t('inboxUX.backToFields')
              }}
            </button>
            <form
              id="inbox-field-editor"
              class="inbox-form"
              @submit.prevent="save"
            >
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
                  ><label class="inbox-label"
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
                <button type="submit" :disabled="busy" class="">
                  {{ t('common.save') }}</button
                ><button
                  type="button"
                  :disabled="busy"
                  @click="editing = false"
                  class="outline"
                >
                  {{ t('common.cancel') }}
                </button>
              </footer>
            </form></template
          >
        </template>
        <p v-else role="status" class="inbox-caption">
          {{ t('common.loading') }}
        </p>
      </div>
    </div>
    <Modal
      :open="!!archive"
      @update:open="
        (value) => {
          if (!value) archive = null
        }
      "
      :title="
        [String(t('inboxFields.archive')), String(archive?.label), '?'].join(
          ' ',
        )
      "
      alert
      ><p>{{ t('inboxUX.archiveHelp') }}</p>
      <template #footer
        ><button type="button" class="outline" @click="archive = null">
          {{ t('common.cancel') }}</button
        ><button type="button" :disabled="busy" @click="archiveField">
          {{ t('inboxFields.archive') }}
        </button></template
      ></Modal
    >
  </main>
</template>
