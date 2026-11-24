<script setup lang="ts">
import '@/assets/inbox.css'
import {
  computed,
  getCurrentInstance,
  nextTick,
  onUnmounted,
  ref,
  watch,
} from 'vue'
import { useI18n } from 'vue-i18n'
import { ArrowLeft, Plus, RotateCw, Trash2, Plug } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import UnsavedChangesDialog from '@/components/shared/UnsavedChangesDialog.vue'
import { useInboxDraftGuard } from '@/composables/useInboxDraftGuard'
import { useInboxViewerContext } from '@/composables/useInboxViewerContext'
import { useAuthStore } from '@/stores/auth'
import { inboxService, type FieldDefinition } from '@/services/inbox'
import {
  inboxIntegrations,
  type IntegrationDefinition,
  type IntegrationDefinitionInput,
  type IntegrationEndpoint,
} from '@/services/inboxIntegrations'

const { t } = useI18n()
const auth = useAuthStore()
const context = useInboxViewerContext()
const allowed = computed(() => auth.hasPermission('settings.general', 'write'))
const items = ref<IntegrationDefinition[]>([]),
  endpoints = ref<IntegrationEndpoint[]>([]),
  fields = ref<FieldDefinition[]>([])
const state = ref({ enabled: false, dispatch_available: false })
const loading = ref(false),
  failed = ref(false),
  fieldsFailed = ref(false),
  saving = ref(false),
  settingBusy = ref(false)
const editing = ref(false),
  selectedId = ref(''),
  baseline = ref(''),
  error = ref(''),
  notice = ref(''),
  query = ref('')
const credential = ref(''),
  replaceCredential = ref(false),
  storedCredential = ref(false),
  originalAuth = ref('none')
const enableRequested = ref(false),
  removeRequested = ref(false)
const heading = ref<HTMLElement>(),
  newButton = ref<InstanceType<typeof Button>>()
let generation = 0,
  origin: HTMLElement | undefined
const defaults = (): IntegrationDefinitionInput => ({
  name: '',
  description: '',
  endpoint_key: '',
  auth_mode: 'bearer',
  enabled: false,
  inputs: [],
  outputs: [],
  reference_key: '',
})
const form = ref(defaults())
const snapshot = () =>
  JSON.stringify([form.value, credential.value, replaceCredential.value])
const dirty = () => editing.value && snapshot() !== baseline.value
const { discardOpen, confirmDiscard, cancelDiscard, requestDiscard } =
  useInboxDraftGuard(dirty)
defineExpose({
  confirmLeave: (action: () => void) => {
    if (!saving.value) requestDiscard(action)
  },
})
const visibleItems = computed(() =>
  items.value.filter((item) =>
    item.name.toLocaleLowerCase().includes(query.value.toLocaleLowerCase()),
  ),
)
const endpoint = computed(() =>
  endpoints.value.find((item) => item.key === form.value.endpoint_key),
)
const identifier = (name: string) => /^[a-z][a-z0-9_]{0,63}$/.test(name)
const referenceId = 'integration-reference-' + getCurrentInstance()?.uid
const referenceInvalid = computed(
  () => !!form.value.reference_key && !identifier(form.value.reference_key),
)
const schemaError = computed(() => {
  const inputNames = form.value.inputs
    .map((input) => input.name)
    .filter(Boolean)
  if (
    inputNames.some((name) => !identifier(name)) ||
    new Set(inputNames).size !== inputNames.length
  )
    return t('inboxIntegrations.inputSchemaError')
  const names = form.value.outputs.map((output) => output.name).filter(Boolean)
  const destinations = form.value.outputs
    .map((output) => output.definition_id)
    .filter(Boolean)
  if (
    names.some((name) => !identifier(name)) ||
    new Set(names).size !== names.length ||
    new Set(destinations).size !== destinations.length
  )
    return t('inboxIntegrations.outputSchemaError')
  return ''
})
const valid = computed(() => {
  const value = form.value
  const needsCredential =
    value.auth_mode !== 'none' &&
    (!storedCredential.value ||
      replaceCredential.value ||
      originalAuth.value !== value.auth_mode)
  return (
    !!value.name.trim() &&
    !!endpoint.value &&
    (!needsCredential ||
      (!!credential.value && !/[\r\n]/.test(credential.value))) &&
    value.inputs.every((input) => identifier(input.name)) &&
    new Set(value.inputs.map((input) => input.name)).size ===
      value.inputs.length &&
    value.outputs.every(
      (output) =>
        identifier(output.name) &&
        fields.value.some(
          (field) => field.id === output.definition_id && !field.archived_at,
        ),
    ) &&
    new Set(value.outputs.map((output) => output.name)).size ===
      value.outputs.length &&
    new Set(value.outputs.map((output) => output.definition_id)).size ===
      value.outputs.length &&
    (!value.reference_key || identifier(value.reference_key)) &&
    (!value.outputs.length || !fieldsFailed.value)
  )
})
async function loadFields(request: number) {
  try {
    const { data } = await inboxService.fieldDefinitions()
    if (request !== generation) return
    fields.value = data.data.definitions
    fieldsFailed.value = false
  } catch {
    if (request === generation) fieldsFailed.value = true
  }
}
async function load() {
  const request = ++generation
  loading.value = true
  failed.value = false
  if (!allowed.value) {
    items.value = []
    loading.value = false
    return
  }
  try {
    const [definitions, settings] = await Promise.all([
      inboxIntegrations.list(),
      inboxIntegrations.settings(),
    ])
    if (request !== generation) return
    items.value = definitions.data.data.integrations
    endpoints.value = definitions.data.data.endpoints
    state.value = settings.data.data
    await loadFields(request)
  } catch {
    if (request === generation) failed.value = true
  } finally {
    if (request === generation) loading.value = false
  }
}
function open(item?: IntegrationDefinition, event?: Event) {
  const trigger =
    event?.currentTarget instanceof HTMLElement
      ? event.currentTarget
      : undefined
  requestDiscard(() => {
    origin = trigger
    editing.value = true
    selectedId.value = item?.id ?? ''
    form.value = item
      ? {
          name: item.name,
          description: item.description,
          endpoint_key: item.endpoint_key,
          auth_mode: item.auth_mode,
          enabled: item.enabled,
          reference_key: item.reference_key,
          inputs: item.inputs.map((input) => ({
            name: input.name,
            kind: input.kind,
            required: input.required,
          })),
          outputs: item.outputs.map((output) => ({
            name: output.name,
            definition_id: output.definition_id,
          })),
        }
      : defaults()
    storedCredential.value = !!item?.has_credential
    originalAuth.value = form.value.auth_mode
    credential.value = ''
    replaceCredential.value = !storedCredential.value
    error.value = ''
    notice.value = ''
    removeRequested.value = false
    baseline.value = snapshot()
    void nextTick(() => heading.value?.focus())
  })
}
function close() {
  if (saving.value) return
  requestDiscard(() => {
    editing.value = false
    credential.value = ''
    baseline.value = ''
    form.value = defaults()
    error.value = ''
    notice.value = ''
    void nextTick(() => {
      if (origin?.isConnected) origin.focus()
      else newButton.value?.$el?.focus()
    })
  })
}
async function save() {
  if (!valid.value || saving.value || !allowed.value) return
  const request = ++generation,
    id = selectedId.value
  saving.value = true
  error.value = ''
  notice.value = ''
  const input: IntegrationDefinitionInput = {
    ...form.value,
    inputs: form.value.inputs.map((item) => ({ ...item })),
    outputs: form.value.outputs.map((item) => ({ ...item })),
  }
  if (input.auth_mode !== 'none' && credential.value)
    input.credential = credential.value
  try {
    const { data } = await (id
      ? inboxIntegrations.update(id, input)
      : inboxIntegrations.create(input))
    if (request !== generation) return
    const saved = data.data.integration
    items.value = [
      ...items.value.filter((item) => item.id !== saved.id),
      saved,
    ].sort((a, b) => a.name.localeCompare(b.name))
    selectedId.value = saved.id
    form.value = {
      name: saved.name,
      description: saved.description,
      endpoint_key: saved.endpoint_key,
      auth_mode: saved.auth_mode,
      enabled: saved.enabled,
      inputs: saved.inputs.map((item) => ({ ...item })),
      outputs: saved.outputs.map((item) => ({ ...item })),
      reference_key: saved.reference_key,
    }
    storedCredential.value = saved.has_credential
    originalAuth.value = saved.auth_mode
    credential.value = ''
    replaceCredential.value = false
    baseline.value = snapshot()
    notice.value = t(
      id ? 'inboxIntegrations.saved' : 'inboxIntegrations.createdDisabled',
    )
  } catch {
    if (request === generation) error.value = t('inboxIntegrations.saveError')
  } finally {
    if (request === generation) saving.value = false
  }
}
async function remove() {
  if (!selectedId.value || saving.value) return
  const request = ++generation,
    id = selectedId.value
  saving.value = true
  error.value = ''
  try {
    await inboxIntegrations.remove(id)
    if (request !== generation) return
    items.value = items.value.filter((item) => item.id !== id)
    baseline.value = snapshot()
    saving.value = false
    close()
  } catch {
    if (request === generation) error.value = t('inboxIntegrations.removeError')
  } finally {
    if (request === generation) saving.value = false
  }
}
async function setEnabled(enabled: boolean) {
  if (settingBusy.value || !allowed.value) return
  const requestedContext = context.value
  settingBusy.value = true
  error.value = ''
  try {
    const { data } = await inboxIntegrations.setEnabled(enabled)
    if (requestedContext !== context.value) return
    state.value.enabled = data.data.enabled
    enableRequested.value = false
  } catch {
    if (requestedContext === context.value)
      error.value = t('inboxIntegrations.settingError')
  } finally {
    if (requestedContext === context.value) settingBusy.value = false
  }
}
watch(
  [context, allowed],
  () => {
    generation++
    editing.value = false
    saving.value = false
    settingBusy.value = false
    credential.value = ''
    form.value = defaults()
    baseline.value = ''
    items.value = []
    fields.value = []
    endpoints.value = []
    state.value = { enabled: false, dispatch_available: false }
    storedCredential.value = false
    replaceCredential.value = false
    originalAuth.value = 'none'
    query.value = ''
    origin = undefined
    error.value = ''
    notice.value = ''
    enableRequested.value = false
    removeRequested.value = false
    void load()
  },
  { immediate: true, flush: 'sync' },
)
onUnmounted(() => {
  generation++
  credential.value = ''
  baseline.value = ''
  origin = undefined
})
</script>

<template>
  <section class="inbox-ui" aria-labelledby="integration-title">
    <UnsavedChangesDialog
      :open="discardOpen"
      @leave="confirmDiscard"
      @stay="cancelDiscard"
    />
    <p v-if="!allowed" role="status">{{ t('inboxIntegrations.permission') }}</p>
    <template v-else>
      <div class="inbox-toolbar">
        <div>
          <h2
            id="integration-title"
            ref="heading"
            tabindex="-1"
            class="inbox-section-heading"
          >
            {{
              t(
                editing
                  ? 'inboxIntegrations.editTitle'
                  : 'inboxIntegrations.title',
              )
            }}
          </h2>
          <p class="inbox-caption mt-1">
            {{ t('inboxIntegrations.description') }}
          </p>
        </div>
        <Button
          v-if="!editing"
          variant="outline"
          :disabled="loading"
          @click="load"
          ><RotateCw class="mr-2 h-4 w-4" aria-hidden="true" />{{
            t('inboxQuality.refresh')
          }}</Button
        >
        <Button
          v-if="!editing"
          ref="newButton"
          :disabled="loading || failed || !endpoints.length"
          @click="open(undefined, $event)"
          ><Plus class="mr-2 h-4 w-4" aria-hidden="true" />{{
            t('inboxIntegrations.new')
          }}</Button
        >
        <Button v-else variant="outline" :disabled="saving" @click="close"
          ><ArrowLeft class="mr-2 h-4 w-4" aria-hidden="true" />{{
            t('inboxIntegrations.back')
          }}</Button
        >
      </div>
      <p v-if="error" role="alert" class="inbox-notice">{{ error }}</p>
      <p v-if="notice" role="status" class="inbox-notice">{{ notice }}</p>
      <div v-if="failed" role="alert" class="inbox-notice">
        {{ t('inboxIntegrations.loadError')
        }}<Button variant="outline" @click="load">{{
          t('common.retry')
        }}</Button>
      </div>
      <div
        v-if="loading"
        aria-busy="true"
        :aria-label="t('common.loading')"
        class="grid gap-3 py-4"
      >
        <div v-for="line in 3" :key="line" class="h-12 rounded bg-muted" />
      </div>
      <template v-if="!loading && !failed && !editing">
        <div class="inbox-notice">
          <Plug class="h-5 w-5" aria-hidden="true" />
          <div class="flex-1">
            <strong>{{
              t(
                state.enabled && state.dispatch_available
                  ? 'inboxIntegrations.dispatchReady'
                  : 'inboxIntegrations.dispatchHeld',
              )
            }}</strong>
            <p class="inbox-caption mt-1">
              {{ t('inboxIntegrations.holdHelp') }}
            </p>
            <p v-if="!state.dispatch_available" class="inbox-caption mt-2">
              {{ t('inboxIntegrations.hostHeld') }}
            </p>
          </div>
          <Button
            v-if="state.enabled"
            variant="outline"
            :disabled="settingBusy"
            @click="setEnabled(false)"
            >{{ t('inboxIntegrations.hold') }}</Button
          ><Button
            v-else
            variant="outline"
            :disabled="settingBusy || !state.dispatch_available"
            @click="enableRequested = true"
            >{{ t('inboxIntegrations.allow') }}</Button
          >
        </div>
        <div v-if="enableRequested" class="inbox-form-section">
          <p>{{ t('inboxIntegrations.enableReview') }}</p>
          <div class="flex flex-wrap gap-2">
            <Button :disabled="settingBusy" @click="setEnabled(true)">{{
              t('inboxIntegrations.confirmEnable')
            }}</Button
            ><Button
              variant="outline"
              :disabled="settingBusy"
              @click="enableRequested = false"
              >{{ t('common.cancel') }}</Button
            >
          </div>
        </div>
        <p v-if="!endpoints.length" class="inbox-notice">
          {{ t('inboxIntegrations.noEndpoints') }}
        </p>
        <label v-if="items.length" class="inbox-label mb-4 max-w-md"
          >{{ t('inboxIntegrations.search')
          }}<input v-model="query" type="search"
        /></label>
        <div v-if="!visibleItems.length" class="inbox-empty">
          <h3>
            {{
              t(
                items.length
                  ? 'inboxIntegrations.noMatches'
                  : 'inboxIntegrations.empty',
              )
            }}
          </h3>
          <p>{{ t('inboxIntegrations.emptyHelp') }}</p>
        </div>
        <ul v-else class="inbox-list inbox-panel">
          <li
            v-for="item in visibleItems"
            :key="item.id"
            class="inbox-list-row"
          >
            <div class="inbox-list-copy">
              <h3 class="inbox-list-title">{{ item.name }}</h3>
              <p v-if="item.description" class="inbox-caption mt-1">
                {{ item.description }}
              </p>
              <p class="inbox-caption mt-1">
                {{
                  endpoints.find((value) => value.key === item.endpoint_key)
                    ?.name || t('inboxIntegrations.endpointUnavailable')
                }}
              </p>
            </div>
            <span class="inbox-badge">{{
              t(
                item.enabled
                  ? 'inboxIntegrations.enabled'
                  : 'inboxIntegrations.disabled',
              )
            }}</span
            ><Button variant="outline" @click="open(item, $event)"
              >{{ t('common.edit')
              }}<span class="sr-only"> {{ item.name }}</span></Button
            >
          </li>
        </ul>
      </template>
      <form v-if="editing" class="inbox-form" @submit.prevent="save">
        <fieldset :disabled="saving" class="min-w-0">
          <section class="inbox-form-section">
            <h3 class="inbox-section-heading">
              {{ t('inboxIntegrations.connection') }}
            </h3>
            <p>{{ t('inboxIntegrations.connectionHelp') }}</p>
            <div class="inbox-grid">
              <label class="inbox-label"
                >{{ t('common.name')
                }}<input v-model="form.name" maxlength="100" required /></label
              ><label class="inbox-label"
                >{{ t('inboxIntegrations.endpoint')
                }}<select
                  v-model="form.endpoint_key"
                  :aria-label="t('inboxIntegrations.endpoint')"
                  required
                >
                  <option value="">
                    {{ t('inboxIntegrations.chooseEndpoint') }}
                  </option>
                  <option
                    v-for="item in endpoints"
                    :key="item.key"
                    :value="item.key"
                  >
                    {{ item.name }}
                  </option>
                  <option
                    v-if="form.endpoint_key && !endpoint"
                    :value="form.endpoint_key"
                    disabled
                  >
                    {{ t('inboxIntegrations.endpointUnavailable') }}
                  </option>
                </select></label
              >
            </div>
            <p v-if="endpoint" class="inbox-caption mt-3 break-words">
              {{ endpoint.method }} · {{ endpoint.destination }}
            </p>
            <label class="inbox-label mt-4"
              >{{ t('common.description')
              }}<textarea
                v-model="form.description"
                maxlength="1000"
                rows="2"
              /></label
            ><label v-if="selectedId" class="inbox-check mt-4"
              ><input v-model="form.enabled" type="checkbox" />{{
                t('inboxIntegrations.enableDefinition')
              }}</label
            >
            <p v-else class="inbox-caption mt-4">
              {{ t('inboxIntegrations.newDisabled') }}
            </p>
          </section>
          <section class="inbox-form-section">
            <h3 class="inbox-section-heading">
              {{ t('inboxIntegrations.authentication') }}
            </h3>
            <p>{{ t('inboxIntegrations.secretHelp') }}</p>
            <label class="inbox-label max-w-md"
              >{{ t('inboxIntegrations.authMode')
              }}<select
                v-model="form.auth_mode"
                :aria-label="t('inboxIntegrations.authMode')"
              >
                <option value="bearer">
                  {{ t('inboxIntegrations.bearer') }}
                </option>
                <option value="api_key">
                  {{ t('inboxIntegrations.apiKey') }}
                </option>
                <option value="none">
                  {{ t('inboxIntegrations.noAuth') }}
                </option>
              </select></label
            ><template v-if="form.auth_mode !== 'none'"
              ><p class="inbox-caption mt-3">
                {{
                  t(
                    storedCredential
                      ? 'inboxIntegrations.secretStored'
                      : 'inboxIntegrations.secretMissing',
                  )
                }}
              </p>
              <label
                v-if="storedCredential && originalAuth === form.auth_mode"
                class="inbox-check mt-3"
                ><input
                  v-model="replaceCredential"
                  type="checkbox"
                  @change="credential = ''"
                />{{ t('inboxIntegrations.replaceSecret') }}</label
              ><label
                v-if="
                  !storedCredential ||
                  replaceCredential ||
                  originalAuth !== form.auth_mode
                "
                class="inbox-label mt-3 max-w-lg"
                >{{ t('inboxIntegrations.credential')
                }}<input
                  v-model="credential"
                  type="password"
                  autocomplete="new-password"
                  maxlength="8192"
                  required /></label
            ></template>
          </section>
          <section class="inbox-form-section">
            <div class="inbox-toolbar">
              <h3 class="inbox-section-heading">
                {{ t('inboxIntegrations.inputs') }}
              </h3>
              <Button
                type="button"
                variant="outline"
                :disabled="form.inputs.length >= 32"
                @click="
                  form.inputs.push({ name: '', kind: 'text', required: true })
                "
                ><Plus class="mr-2 h-4 w-4" aria-hidden="true" />{{
                  t('inboxIntegrations.addInput')
                }}</Button
              >
            </div>
            <p>{{ t('inboxIntegrations.inputHelp') }}</p>
            <p v-if="!form.inputs.length" class="inbox-caption">
              {{ t('inboxIntegrations.noInputs') }}
            </p>
            <div
              v-for="(input, index) in form.inputs"
              :key="index"
              class="inbox-integration-schema-row"
            >
              <label class="inbox-label"
                >{{ t('inboxIntegrations.inputName', { number: index + 1 })
                }}<input
                  v-model="input.name"
                  pattern="[a-z][a-z0-9_]{0,63}"
                  maxlength="64"
                  required /></label
              ><label class="inbox-label"
                >{{ t('inboxIntegrations.kind')
                }}<select
                  v-model="input.kind"
                  :aria-label="
                    t('inboxIntegrations.kindFor', { number: index + 1 })
                  "
                >
                  <option
                    v-for="kind in ['text', 'number', 'date', 'boolean']"
                    :key="kind"
                    :value="kind"
                  >
                    {{ t('inboxFields.kind.' + kind) }}
                  </option>
                </select></label
              ><label class="inbox-check self-end py-3"
                ><input v-model="input.required" type="checkbox" />{{
                  t('inboxIntegrations.required')
                }}</label
              ><Button
                type="button"
                variant="ghost"
                size="icon"
                :aria-label="
                  t('inboxIntegrations.removeInput', { number: index + 1 })
                "
                @click="form.inputs.splice(index, 1)"
                ><Trash2 class="h-4 w-4" aria-hidden="true"
              /></Button>
            </div>
          </section>
          <section class="inbox-form-section">
            <div class="inbox-toolbar">
              <h3 class="inbox-section-heading">
                {{ t('inboxIntegrations.outputs') }}
              </h3>
              <Button
                type="button"
                variant="outline"
                :disabled="fieldsFailed || form.outputs.length >= 16"
                @click="form.outputs.push({ name: '', definition_id: '' })"
                ><Plus class="mr-2 h-4 w-4" aria-hidden="true" />{{
                  t('inboxIntegrations.addOutput')
                }}</Button
              >
            </div>
            <p>{{ t('inboxIntegrations.outputHelp') }}</p>
            <p v-if="fieldsFailed" role="alert" class="inbox-notice">
              {{ t('inboxIntegrations.fieldError')
              }}<Button
                type="button"
                variant="outline"
                @click="loadFields(generation)"
                >{{ t('common.retry') }}</Button
              >
            </p>
            <p v-if="!form.outputs.length" class="inbox-caption">
              {{ t('inboxIntegrations.noOutputs') }}
            </p>
            <div
              v-for="(output, index) in form.outputs"
              :key="index"
              class="inbox-integration-output-row"
            >
              <label class="inbox-label"
                >{{ t('inboxIntegrations.outputName', { number: index + 1 })
                }}<input
                  v-model="output.name"
                  pattern="[a-z][a-z0-9_]{0,63}"
                  maxlength="64"
                  required /></label
              ><label class="inbox-label"
                >{{ t('inboxIntegrations.targetField')
                }}<select
                  v-model="output.definition_id"
                  :aria-label="
                    t('inboxIntegrations.targetFieldFor', { number: index + 1 })
                  "
                  required
                >
                  <option value="">
                    {{ t('inboxIntegrations.chooseField') }}
                  </option>
                  <option
                    v-for="field in fields"
                    :key="field.id"
                    :value="field.id"
                    :disabled="!!field.archived_at"
                  >
                    {{ field.label }} ·
                    {{ t('inboxIntegrations.scope.' + field.scope) }} ·
                    {{ t('inboxFields.kind.' + field.kind)
                    }}{{
                      field.archived_at
                        ? ' (' + t('inboxFields.archived') + ')'
                        : ''
                    }}
                  </option>
                  <option
                    v-if="
                      output.definition_id &&
                      !fields.some((field) => field.id === output.definition_id)
                    "
                    :value="output.definition_id"
                    disabled
                  >
                    {{ t('inboxIntegrations.fieldUnavailable') }}
                  </option>
                </select></label
              ><Button
                type="button"
                variant="ghost"
                size="icon"
                :aria-label="
                  t('inboxIntegrations.removeOutput', { number: index + 1 })
                "
                @click="form.outputs.splice(index, 1)"
                ><Trash2 class="h-4 w-4" aria-hidden="true"
              /></Button>
            </div>
            <label class="inbox-label mt-4 max-w-md"
              ><span :id="referenceId + '-label'">{{
                t('inboxIntegrations.referenceKey')
              }}</span
              ><input
                v-model="form.reference_key"
                :aria-labelledby="referenceId + '-label'"
                :aria-invalid="referenceInvalid ? true : undefined"
                :aria-describedby="
                  referenceId +
                  '-help' +
                  (referenceInvalid ? ' ' + referenceId + '-error' : '')
                "
                pattern="[a-z][a-z0-9_]{0,63}"
                maxlength="64"
              /><span :id="referenceId + '-help'" class="inbox-caption">{{
                t('inboxIntegrations.referenceHelp')
              }}</span
              ><span
                v-if="referenceInvalid"
                :id="referenceId + '-error'"
                role="alert"
                class="text-sm"
                >{{ t('inboxIntegrations.referenceError') }}</span
              ></label
            >
          </section>
          <div class="inbox-form-footer">
            <p v-if="schemaError" role="alert" class="inbox-notice w-full">
              {{ schemaError }}
            </p>
            <Button type="submit" :disabled="!valid || saving">{{
              t(saving ? 'common.saving' : 'common.save')
            }}</Button
            ><Button
              type="button"
              variant="outline"
              :disabled="saving"
              @click="close"
              >{{ t('common.cancel') }}</Button
            ><Button
              v-if="selectedId"
              type="button"
              variant="outline"
              class="sm:ml-auto"
              @click="removeRequested = true"
              >{{ t('common.delete') }}</Button
            >
          </div>
          <div v-if="removeRequested" class="inbox-form-section">
            <p>{{ t('inboxIntegrations.removeReview') }}</p>
            <div class="flex flex-wrap gap-2">
              <Button type="button" variant="destructive" @click="remove">{{
                t('inboxIntegrations.confirmRemove')
              }}</Button
              ><Button
                type="button"
                variant="outline"
                @click="removeRequested = false"
                >{{ t('common.cancel') }}</Button
              >
            </div>
          </div>
        </fieldset>
      </form>
    </template>
  </section>
</template>

<style scoped>
input[aria-invalid='true'] {
  border-color: hsl(var(--destructive));
}
.inbox-integration-schema-row {
  display: grid;
  grid-template-columns: minmax(0, 1.3fr) minmax(0, 1fr) auto auto;
  align-items: end;
  gap: 0.75rem;
  padding-block: 1rem;
  border-bottom: 1px solid hsl(var(--border));
}
.inbox-integration-output-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1.5fr) auto;
  align-items: end;
  gap: 0.75rem;
  padding-block: 1rem;
  border-bottom: 1px solid hsl(var(--border));
}
@media (max-width: 640px) {
  .inbox-integration-schema-row,
  .inbox-integration-output-row {
    grid-template-columns: minmax(0, 1fr) auto;
  }
  .inbox-integration-schema-row > label:first-child,
  .inbox-integration-output-row > label:first-child {
    grid-column: 1 / -1;
  }
}
</style>
