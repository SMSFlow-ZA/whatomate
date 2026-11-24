<script setup lang="ts">
import {
  computed,
  getCurrentInstance,
  onUnmounted,
  ref,
  shallowRef,
  watch,
} from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import { useAuthStore } from '@/stores/auth'
import { useInboxViewerContext } from '@/composables/useInboxViewerContext'
import { inboxService } from '@/services/inbox'
import {
  inboxIntegrations,
  type IntegrationExecution,
  type IntegrationRecoveryInput,
} from '@/services/inboxIntegrations'
import {
  investigatedSchema,
  parseInvestigatedResults,
  validInvestigationReference,
  type InvestigatedField,
} from '@/lib/inboxIntegrationResults'

const props = defineProps<{
  execution: IntegrationExecution
  action: 'confirm_succeeded' | 'confirm_not_applied'
  disabled: boolean
}>()
const emit = defineEmits<{
  change: [input: IntegrationRecoveryInput | undefined, dirty: boolean]
  review: []
}>()
const { t } = useI18n(),
  auth = useAuthStore(),
  viewer = useInboxViewerContext()
const reference = ref(''),
  include = ref(false),
  loading = ref(false),
  failed = ref(false)
const schema = shallowRef<InvestigatedField[]>()
const values = ref<Record<string, string | number>>(Object.create(null))
const touched = ref(false)
const prefix = 'investigated-result-' + getCurrentInstance()!.uid
let generation = 0
const operator = computed(() =>
  ['settings.general', 'chat', 'custom_actions'].every((resource) =>
    auth.hasPermission(resource, 'write'),
  ),
)
const context = computed(() =>
  JSON.stringify([
    viewer.value,
    props.execution.id,
    props.execution.integration_id,
    props.action,
    operator.value,
  ]),
)
const parsed = computed(() =>
  parseInvestigatedResults(schema.value || [], values.value),
)
const referenceValid = computed(() =>
  validInvestigationReference(reference.value),
)
const input = computed<IntegrationRecoveryInput | undefined>(() => {
  if (!operator.value || !referenceValid.value) return undefined
  if (!include.value)
    return { action: props.action, evidence_reference: reference.value.trim() }
  if (
    props.action !== 'confirm_succeeded' ||
    loading.value ||
    failed.value ||
    !schema.value?.length ||
    !parsed.value.result
  )
    return undefined
  return {
    action: props.action,
    evidence_reference: reference.value.trim(),
    result: parsed.value.result,
  }
})
watch(
  [input, reference, include, values],
  () => emit('change', input.value, !!reference.value || include.value),
  { deep: true, immediate: true, flush: 'sync' },
)
async function loadSchema() {
  const current = ++generation,
    scope = context.value
  schema.value = undefined
  values.value = Object.create(null)
  loading.value = true
  failed.value = false
  try {
    if (!operator.value || props.action !== 'confirm_succeeded')
      throw new Error('Current investigation access required')
    const [definitions, fields] = await Promise.all([
      inboxIntegrations.list(),
      inboxService.fieldDefinitions(),
    ])
    if (current !== generation || scope !== context.value || !include.value)
      return
    const definition = definitions.data.data.integrations.find(
      (item) => item.id === props.execution.integration_id,
    )
    if (!definition) throw new Error('Current definition unavailable')
    const next = investigatedSchema(definition, fields.data.data.definitions)
    if (!next?.length) throw new Error('Declared result schema unavailable')
    schema.value = next
  } catch {
    if (current === generation && scope === context.value) failed.value = true
  } finally {
    if (current === generation) loading.value = false
  }
}
watch(
  include,
  (enabled) => {
    generation++
    schema.value = undefined
    values.value = Object.create(null)
    touched.value = false
    loading.value = false
    failed.value = false
    if (enabled) void loadSchema()
  },
  { flush: 'sync' },
)
watch(
  context,
  () => {
    generation++
    reference.value = ''
    include.value = false
    schema.value = undefined
    values.value = Object.create(null)
    loading.value = false
    failed.value = false
    touched.value = false
  },
  { flush: 'sync' },
)
onUnmounted(() => {
  generation++
})
function review() {
  touched.value = true
  if (input.value && !props.disabled) emit('review')
}
function label(entry: InvestigatedField) {
  return entry.field?.label || t('inboxInvestigation.resultReference')
}
function invalid(name: string) {
  return (
    (touched.value || values.value[name] !== undefined) &&
    !!parsed.value.errors[name]
  )
}
</script>
<template>
  <div class="grid min-w-0 gap-3">
    <p class="inbox-notice">{{ t('inboxInvestigation.attestationHelp') }}</p>
    <label class="inbox-label" :for="prefix + '-reference'"
      >{{ t('inboxInvestigation.reference') }} ({{
        t('common.required')
      }})</label
    >
    <input
      :id="prefix + '-reference'"
      v-model="reference"
      class="inbox-input"
      type="text"
      required
      aria-required="true"
      :disabled="disabled"
      :aria-invalid="!!reference && !referenceValid"
      :aria-describedby="
        prefix +
        '-reference-help' +
        (reference && !referenceValid ? ' ' + prefix + '-reference-error' : '')
      "
    />
    <p :id="prefix + '-reference-help'" class="inbox-caption">
      {{ t('inboxInvestigation.referenceHelp') }}
    </p>
    <p
      v-if="reference && !referenceValid"
      :id="prefix + '-reference-error'"
      role="alert"
    >
      {{ t('inboxInvestigation.referenceError') }}
    </p>
    <label v-if="action === 'confirm_succeeded'" class="inbox-check"
      ><input v-model="include" type="checkbox" :disabled="disabled" />{{
        t('inboxInvestigation.includeResults')
      }}</label
    >
    <p v-if="!include" class="inbox-caption">
      {{ t('inboxInvestigation.factOnly') }}
    </p>
    <template v-else>
      <p class="inbox-caption">{{ t('inboxInvestigation.schemaHelp') }}</p>
      <p v-if="loading" role="status">{{ t('common.loading') }}</p>
      <div v-if="failed" role="alert" class="grid gap-2">
        <p>{{ t('inboxInvestigation.schemaError') }}</p>
        <Button
          type="button"
          variant="outline"
          class="justify-self-start"
          :disabled="disabled || loading"
          @click="loadSchema"
          >{{ t('common.retry') }}</Button
        >
      </div>
      <div
        v-for="(entry, index) in schema"
        :key="entry.name"
        class="grid min-w-0 gap-1"
      >
        <label class="inbox-label" :for="prefix + '-value-' + index"
          >{{ label(entry) }} ({{ t('common.required') }})</label
        >
        <p :id="prefix + '-help-' + index" class="inbox-caption break-words">
          {{
            entry.field
              ? t('inboxFields.scope.' + entry.field.scope)
              : t('inboxInvestigation.resultReference')
          }}
          · {{ entry.name
          }}<span v-if="entry.field?.description">
            · {{ entry.field.description }}</span
          >
        </p>
        <select
          v-if="
            entry.field?.kind === 'boolean' || entry.field?.kind === 'select'
          "
          :id="prefix + '-value-' + index"
          v-model="values[entry.name]"
          class="inbox-input"
          required
          aria-required="true"
          :disabled="disabled"
          :aria-invalid="invalid(entry.name)"
          :aria-describedby="
            prefix +
            '-help-' +
            index +
            (invalid(entry.name) ? ' ' + prefix + '-error-' + index : '')
          "
        >
          <option value="">{{ t('inboxInvestigation.chooseValue') }}</option>
          <template v-if="entry.field.kind === 'boolean'"
            ><option value="true">{{ t('common.yes') }}</option>
            <option value="false">{{ t('common.no') }}</option></template
          >
          <option
            v-for="option in entry.field.options"
            v-else
            :key="option"
            :value="option"
          >
            {{ option }}
          </option>
        </select>
        <input
          v-else-if="
            entry.field?.kind === 'number' || entry.field?.kind === 'date'
          "
          :id="prefix + '-value-' + index"
          v-model="values[entry.name]"
          class="inbox-input"
          :type="entry.field.kind === 'date' ? 'date' : 'number'"
          step="any"
          required
          aria-required="true"
          :disabled="disabled"
          :aria-invalid="invalid(entry.name)"
          :aria-describedby="
            prefix +
            '-help-' +
            index +
            (invalid(entry.name) ? ' ' + prefix + '-error-' + index : '')
          "
        />
        <textarea
          v-else
          :id="prefix + '-value-' + index"
          v-model="values[entry.name]"
          class="inbox-input"
          rows="2"
          required
          aria-required="true"
          :disabled="disabled"
          :aria-invalid="invalid(entry.name)"
          :aria-describedby="
            prefix +
            '-help-' +
            index +
            (invalid(entry.name) ? ' ' + prefix + '-error-' + index : '')
          "
        />
        <p
          v-if="invalid(entry.name)"
          :id="prefix + '-error-' + index"
          role="alert"
        >
          {{ t('inboxInvestigation.valueError.' + parsed.errors[entry.name]) }}
        </p>
      </div>
      <p v-if="parsed.tooLarge" role="alert">
        {{ t('inboxInvestigation.tooLarge') }}
      </p>
    </template>
    <Button
      type="button"
      variant="outline"
      class="justify-self-start"
      :disabled="disabled || !input"
      @click="review"
      >{{ t('inboxInvestigation.review') }}</Button
    >
  </div>
</template>
