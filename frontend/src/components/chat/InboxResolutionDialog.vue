<script setup lang="ts">
import { useInboxDialogFocus } from '@/composables/useInboxDialogFocus'
import { ref, watch, computed, nextTick } from 'vue'
import { isAxiosError } from 'axios'
import { useI18n } from 'vue-i18n'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
  DialogFooter,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import {
  inboxService,
  type MissingInboxField,
  type FieldValue,
  type InboxCase,
} from '@/services/inbox'
import InboxFieldInput from './InboxFieldInput.vue'
const props = defineProps<{ account: string; contact: string }>()
const open = defineModel<boolean>({ required: true })
const emit = defineEmits<{ resolved: [value: InboxCase] }>()
const { t } = useI18n()
const missing = ref<MissingInboxField[]>([]),
  values = ref<Record<string, FieldValue>>({})
const caseId = ref(''),
  version = ref(0),
  loading = ref(false),
  busy = ref(false),
  error = ref(''),
  loadFailed = ref(false)
const context = ref({ contact: '', account: '' }),
  fieldErrors = ref<Record<string, string>>({}),
  failure = ref(''),
  completed = ref(false)
let generation = 0
const blocked = computed(() => missing.value.some((field) => !field.can_edit))
async function load(keepDraft = false) {
  const previous = keepDraft ? { ...values.value } : {}
  const request = ++generation
  loading.value = true
  error.value = ''
  failure.value = ''
  fieldErrors.value = {}
  completed.value = false
  missing.value = []
  values.value = previous
  loadFailed.value = false
  try {
    const { data } = await inboxService.resolutionRequirements(
      props.account,
      props.contact,
    )
    if (request !== generation) return
    missing.value = data.data.missing_fields
    caseId.value = data.data.case_id
    version.value = data.data.version
    context.value = {
      contact: data.data.contact_name ?? '',
      account: data.data.account_name ?? '',
    }
    completed.value = data.data.status === 'resolved'
    if (completed.value) error.value = t('inboxFollowup.alreadyResolved')
  } catch {
    if (request === generation) {
      loadFailed.value = true
      error.value = t('inboxFields.loadError')
    }
  } finally {
    if (request === generation) loading.value = false
  }
}
watch(
  () => [open.value, props.account, props.contact],
  () => {
    generation++
    if (open.value) void load()
  },
  { immediate: true },
)
function updateField(id: string, value: FieldValue) {
  values.value[id] = value
  delete fieldErrors.value[id]
  if (
    !loadFailed.value &&
    !completed.value &&
    !['permission', 'conflict', 'unavailable'].includes(failure.value) &&
    !Object.keys(fieldErrors.value).length
  )
    error.value = ''
}

async function resolve() {
  if (
    busy.value ||
    blocked.value ||
    loading.value ||
    loadFailed.value ||
    completed.value ||
    ['permission', 'conflict', 'unavailable'].includes(failure.value)
  )
    return
  const request = generation,
    account = props.account,
    contact = props.contact
  let focusField = ''
  busy.value = true
  error.value = ''
  fieldErrors.value = {}
  failure.value = ''
  try {
    const { data } = await inboxService.completeAndResolve(
      account,
      contact,
      caseId.value,
      version.value,
      missing.value
        .filter((field) => field.can_edit)
        .map((field) => ({
          definition_id: field.id,
          value: values.value[field.id] ?? null,
        })),
    )
    if (request !== generation) return
    emit('resolved', data.data.case)
    open.value = false
  } catch (problem) {
    if (request !== generation) return
    const status = isAxiosError(problem) ? problem.response?.status : undefined
    const data = isAxiosError(problem)
      ? problem.response?.data?.data
      : undefined
    if (status === 422 || status === 400) {
      failure.value = 'validation'
      error.value = t('inboxFollowup.validationError')
      if (Array.isArray(data?.missing_fields)) {
        missing.value = data.missing_fields
        for (const field of missing.value) {
          const value = values.value[field.id]
          if (value == null || (typeof value === 'string' && !value.trim()))
            fieldErrors.value[field.id] = t('inboxFields.required')
        }
      }
      for (const field of data?.field_errors ?? []) {
        fieldErrors.value[field.definition_id] = t('inboxFollowup.invalidValue')
      }
      focusField =
        missing.value.find((field) => fieldErrors.value[field.id])?.id ?? ''
    } else if (status === 409) {
      failure.value = 'conflict'
      error.value = t('inboxFollowup.conflictError')
    } else if (status === 401 || status === 403) {
      failure.value = 'permission'
      error.value = t('inboxFollowup.permissionError')
    } else if (status === 404) {
      failure.value = 'unavailable'
      error.value = t('inboxFollowup.caseUnavailable')
    } else {
      failure.value = 'network'
      error.value = t('inboxFollowup.networkError')
    }
  } finally {
    busy.value = false
    if (focusField) {
      await nextTick()
      if (request === generation)
        document.getElementById('resolve-' + focusField)?.focus()
    }
  }
}

const restoreDialogFocus = useInboxDialogFocus(open)
</script>
<template>
  <Dialog
    :open="open"
    @update:open="
      (value) => {
        if (!busy) open = value
      }
    "
    ><DialogContent
      @close-auto-focus="restoreDialogFocus"
      class="inbox-ui max-h-[85dvh] overflow-auto sm:max-w-lg"
      @interact-outside="busy && $event.preventDefault()"
    >
      <DialogHeader
        ><DialogTitle>{{
          t(
            missing.length
              ? 'inboxQuality.completeDetails'
              : 'inboxFollowup.confirmResolve',
          )
        }}</DialogTitle
        ><DialogDescription>{{
          t(
            missing.length
              ? 'inboxQuality.completeDetailsHelp'
              : 'inboxFollowup.confirmResolveHelp',
          )
        }}</DialogDescription></DialogHeader
      >
      <p v-if="!loading && caseId" class="inbox-caption">
        {{
          [
            context.contact,
            context.account,
            t('inboxQuality.caseLabel') + ' ' + caseId.slice(0, 8),
          ]
            .filter(Boolean)
            .join(' · ')
        }}
      </p>
      <p v-if="loading" role="status">{{ t('common.loading') }}</p>
      <div v-if="error" role="alert" class="inbox-notice">
        <p>{{ error }}</p>
        <Button
          variant="outline"
          size="sm"
          :disabled="busy"
          @click="failure === 'network' ? resolve() : load(true)"
          >{{
            t(failure === 'network' ? 'common.retry' : 'common.refresh')
          }}</Button
        >
      </div>
      <form
        v-if="!loading"
        id="inbox-resolution-form"
        class="grid gap-4"
        @submit.prevent="resolve"
      >
        <div v-for="field in missing" :key="field.id" class="grid gap-2">
          <label :for="'resolve-' + field.id" class="inbox-label"
            >{{ field.label }}
            <span class="inbox-caption">{{
              t('inboxFields.scope.' + field.scope)
            }}</span></label
          >
          <p :id="'resolve-help-' + field.id" class="inbox-caption">
            {{ field.description }} {{ t('inboxFields.required') }}
          </p>
          <InboxFieldInput
            v-if="field.can_edit"
            :id="'resolve-' + field.id"
            :aria-describedby="
              'resolve-help-' +
              field.id +
              (fieldErrors[field.id] ? ' resolve-error-' + field.id : '')
            "
            :aria-invalid="!!fieldErrors[field.id]"
            :definition="field"
            required
            :model-value="values[field.id] ?? null"
            :disabled="busy"
            @update:model-value="updateField(field.id, $event)"
          />
          <p
            v-if="fieldErrors[field.id]"
            :id="'resolve-error-' + field.id"
            class="text-sm text-destructive"
            role="alert"
          >
            {{ fieldErrors[field.id] }}
          </p>
          <div v-if="!field.can_edit" class="inbox-notice">
            <p>{{ field.handoff }}</p>
            <RouterLink
              v-if="field.editor_path"
              :to="field.editor_path"
              class="underline"
              >{{ t('inboxQuality.openContact') }}</RouterLink
            >
          </div>
        </div>
      </form>
      <DialogFooter
        ><Button variant="outline" :disabled="busy" @click="open = false">{{
          t('common.cancel')
        }}</Button
        ><Button
          form="inbox-resolution-form"
          type="submit"
          :disabled="
            loading ||
            busy ||
            blocked ||
            loadFailed ||
            completed ||
            failure === 'permission' ||
            failure === 'conflict' ||
            failure === 'unavailable'
          "
          >{{
            t(
              missing.length
                ? 'inboxQuality.saveResolve'
                : 'inboxQueue.resolve',
            )
          }}</Button
        ></DialogFooter
      >
    </DialogContent></Dialog
  >
</template>
