<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import { Plus, Trash2 } from 'lucide-vue-next'
import {
  inboxService,
  type InboxFilters,
  type InboxPredicate,
  type FieldDefinition,
} from '@/services/inbox'
import InboxFieldInput from './InboxFieldInput.vue'
const model = defineModel<InboxFilters>({ required: true })
const props = defineProps<{
  allowedFields?: InboxPredicate['field'][]
  help?: string
}>()
const emit = defineEmits<{ validity: [value: boolean] }>()
const allowed = (field: InboxPredicate['field']) =>
  !props.allowedFields || props.allowedFields.includes(field)
const { t } = useI18n(),
  definitions = ref<FieldDefinition[]>([]),
  failed = ref(false),
  loading = ref(false)
let generation = 0
const defaultField = computed(() => {
  const standard = (['status', 'priority', 'tag'] as const).find(allowed)
  if (standard)
    return {
      field: standard,
      operator: 'eq',
      value:
        standard === 'status'
          ? 'open'
          : standard === 'priority'
            ? 'normal'
            : '',
    }
  const definition =
    allowed('custom_field') && !loading.value && !failed.value
      ? definitions.value.find((field) => !field.archived_at)
      : undefined
  return definition
    ? {
        field: 'custom_field' as const,
        definition_id: definition.id,
        operator: 'eq',
        value: definition.kind === 'boolean' ? false : '',
      }
    : null
})
async function load() {
  const request = ++generation
  loading.value = true
  try {
    const { data } = await inboxService.fieldDefinitions()
    if (request !== generation) return
    definitions.value = data.data.definitions
    failed.value = false
  } catch {
    if (request === generation) failed.value = true
  } finally {
    if (request === generation) loading.value = false
  }
}
onMounted(load)
onUnmounted(() => generation++)
const definition = (condition: InboxPredicate) =>
  definitions.value.find((field) => field.id === condition.definition_id)
const fieldUnavailable = (condition: InboxPredicate) =>
  condition.field === 'custom_field' &&
  (loading.value || failed.value || !definition(condition))
watch(
  () => !(model.value.predicates ?? []).some(fieldUnavailable),
  (value) => emit('validity', value),
  { immediate: true, flush: 'sync' },
)
function field(condition: InboxPredicate) {
  return condition.field === 'custom_field'
    ? 'custom:' + condition.definition_id
    : condition.field
}
function selectField(condition: InboxPredicate, event: Event) {
  const value = (event.target as HTMLSelectElement).value
  if (value === field(condition)) return
  if (value.startsWith('custom:')) {
    condition.field = 'custom_field'
    condition.definition_id = value.slice(7)
  } else {
    condition.field = value as InboxPredicate['field']
    delete condition.definition_id
  }
  condition.operator = 'eq'
  condition.value = definition(condition)?.kind === 'boolean' ? false : ''
}
function operators(condition: InboxPredicate) {
  if (fieldUnavailable(condition)) return [condition.operator]
  const kind = definition(condition)?.kind
  return condition.field === 'custom_field'
    ? [
        'eq',
        'not_eq',
        ...(kind === 'text' ? ['contains'] : []),
        ...(['number', 'date'].includes(kind ?? '')
          ? ['gt', 'gte', 'lt', 'lte']
          : []),
        'is_empty',
        'not_empty',
      ]
    : ['eq', 'not_eq']
}
function add() {
  if (!defaultField.value) return
  model.value.predicates = [
    ...(model.value.predicates ?? []),
    { ...defaultField.value },
  ]
}
</script>
<template>
  <fieldset class="grid gap-3 border-t border-border pt-3">
    <legend class="text-sm font-medium">
      {{ t('inboxQuality.conditionsTitle') }}
    </legend>
    <p class="inbox-caption">{{ help || t('inboxQuality.conditionsHelp') }}</p>
    <p v-if="failed" role="alert" class="inbox-notice">
      {{ t('inboxFields.loadError') }}
      <Button type="button" size="sm" variant="outline" @click="load">{{
        t('common.retry')
      }}</Button>
    </p>
    <label v-if="model.predicates?.length" class="inbox-label"
      >{{ t('inboxQuality.match')
      }}<select v-model="model.match">
        <option value="all">{{ t('inboxQuality.matchAll') }}</option>
        <option value="any">{{ t('inboxQuality.matchAny') }}</option>
      </select></label
    >
    <div
      v-for="(condition, index) in model.predicates"
      :key="index"
      class="grid min-w-0 gap-2 rounded-lg border border-border p-3"
    >
      <label class="inbox-label"
        >{{ t('inboxQuality.conditionField')
        }}<select
          :value="field(condition)"
          @change="selectField(condition, $event)"
        >
          <option
            v-if="condition.field === 'custom_field' && !definition(condition)"
            :value="field(condition)"
          >
            {{
              t('inboxQuality.recordedField', { id: condition.definition_id })
            }}
          </option>
          <option v-if="allowed('status')" value="status">
            {{ t('inboxWorkflow.caseStatus') }}
          </option>
          <option v-if="allowed('priority')" value="priority">
            {{ t('inboxWorkflow.casePriority') }}
          </option>
          <option v-if="allowed('tag')" value="tag">
            {{ t('contacts.tags') }}
          </option>
          <option
            v-for="d in allowed('custom_field') ? definitions : []"
            :key="d.id"
            :value="'custom:' + d.id"
          >
            {{ d.label }} · {{ t('inboxFields.scope.' + d.scope)
            }}{{ d.archived_at ? ' · ' + t('inboxFields.archived') : '' }}
          </option>
        </select></label
      >
      <label class="inbox-label"
        >{{ t('inboxQuality.comparison')
        }}<select
          v-model="condition.operator"
          :disabled="fieldUnavailable(condition)"
        >
          <option v-for="op in operators(condition)" :key="op" :value="op">
            {{ t('inboxQuality.operator.' + op) }}
          </option>
        </select></label
      >
      <p
        v-if="fieldUnavailable(condition)"
        class="inbox-notice"
        :role="loading ? 'status' : 'alert'"
      >
        {{
          t(
            loading
              ? 'inboxQuality.fieldLoading'
              : 'inboxQuality.fieldUnavailable',
            { id: condition.definition_id },
          )
        }}
        <Button
          v-if="!loading && !failed"
          type="button"
          variant="outline"
          @click="load"
          >{{ t('common.retry') }}</Button
        >
      </p>
      <template
        v-else-if="!['is_empty', 'not_empty'].includes(condition.operator)"
      >
        <InboxFieldInput
          v-if="definition(condition)"
          :definition="definition(condition)!"
          :model-value="condition.value ?? null"
          @update:model-value="condition.value = $event"
        />
        <label
          v-else-if="
            condition.field === 'status' || condition.field === 'priority'
          "
          class="inbox-label"
        >
          {{ t('inboxQuality.conditionValue') }}
          <select v-model="condition.value">
            <option
              v-for="value in condition.field === 'status'
                ? ['open', 'pending', 'waiting_customer', 'snoozed', 'resolved']
                : ['low', 'normal', 'high', 'urgent']"
              :key="value"
              :value="value"
            >
              {{ t('inboxWorkflow.' + condition.field + '.' + value) }}
            </option>
          </select>
        </label>
        <label v-else class="inbox-label">
          {{ t('inboxQuality.conditionValue') }}
          <input v-model="condition.value" maxlength="100" />
        </label>
      </template>
      <p v-if="definition(condition)?.kind === 'text'" class="inbox-caption">
        {{ t('inboxQuality.textMatching') }}
      </p>
      <Button
        type="button"
        variant="ghost"
        size="sm"
        class="justify-self-end"
        :aria-label="t('inboxQuality.removeCondition', { number: index + 1 })"
        @click="model.predicates?.splice(index, 1)"
        ><Trash2 class="mr-2 h-4 w-4" aria-hidden="true" />{{
          t('common.remove')
        }}</Button
      >
    </div>
    <Button
      type="button"
      variant="outline"
      size="sm"
      :disabled="
        !defaultField ||
        (model.predicates?.length ?? 0) + (model.fields?.length ?? 0) >= 20
      "
      @click="add"
      ><Plus class="mr-2 h-4 w-4" aria-hidden="true" />{{
        t('inboxQuality.addCondition')
      }}</Button
    >
  </fieldset>
</template>
