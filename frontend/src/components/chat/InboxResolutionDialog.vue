<script setup lang="ts">
import { useInboxDialogFocus } from '@/composables/useInboxDialogFocus'
import { ref, watch, computed } from 'vue'
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
let generation = 0
const blocked = computed(() => missing.value.some((field) => !field.can_edit))
async function load(keepDraft = false) {
  const previous = keepDraft ? { ...values.value } : {}
  const request = ++generation
  loading.value = true
  error.value = ''
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
watch(
  values,
  () => {
    if (!loadFailed.value) error.value = ''
  },
  { deep: true },
)
async function resolve() {
  if (busy.value || blocked.value) return
  const request = generation,
    account = props.account,
    contact = props.contact
  busy.value = true
  error.value = ''
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
  } catch {
    if (request === generation)
      error.value = t('inboxQuality.resolutionChanged')
  } finally {
    busy.value = false
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
        ><DialogTitle>{{ t('inboxQuality.completeDetails') }}</DialogTitle
        ><DialogDescription>{{
          t('inboxQuality.completeDetailsHelp')
        }}</DialogDescription></DialogHeader
      >
      <p v-if="loading" role="status">{{ t('common.loading') }}</p>
      <div v-if="error" role="alert" class="inbox-notice">
        <p>{{ error }}</p>
        <Button
          variant="outline"
          size="sm"
          :disabled="busy"
          @click="load(true)"
          >{{ t('common.refresh') }}</Button
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
            :aria-describedby="'resolve-help-' + field.id"
            :definition="field"
            required
            :model-value="values[field.id] ?? null"
            :disabled="busy"
            @update:model-value="values[field.id] = $event"
          />
          <div v-else class="inbox-notice">
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
          :disabled="loading || busy || blocked || loadFailed"
          >{{ t('inboxQuality.saveResolve') }}</Button
        ></DialogFooter
      >
    </DialogContent></Dialog
  >
</template>
