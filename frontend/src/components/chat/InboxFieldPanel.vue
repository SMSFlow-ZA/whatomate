<script setup lang="ts">
import '@/assets/inbox.css'
import { computed, ref, watch, onMounted, onUnmounted } from 'vue'
import InboxLiveStatus from '@/components/shared/InboxLiveStatus.vue'
import { useInboxLive } from '@/composables/useInboxLive'
import { isInboxAccessFailure } from '@/lib/inboxAccess'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import {
  inboxService,
  type FieldValue,
  type FieldDefinition,
} from '@/services/inbox'
import InboxFieldInput from './InboxFieldInput.vue'
import { useInboxDraftGuard } from '@/composables/useInboxDraftGuard'
import UnsavedChangesDialog from '@/components/shared/UnsavedChangesDialog.vue'

const props = defineProps<{
  contact: string
  account?: string
  caseId?: string
  canWrite?: boolean
}>()
const { t } = useI18n()
const definitions = ref<FieldDefinition[]>([])
const values = ref<Record<string, FieldValue>>({})
const stored = ref<Record<string, FieldValue>>({})
const subjectId = ref('')
const busy = ref('')
const ready = ref(false)
const failed = ref(false)
const errors = ref<Record<string, string>>({})
const dirty = computed(() =>
  Object.keys(values.value).some(
    (id) => (values.value[id] ?? null) !== (stored.value[id] ?? null),
  ),
)
const { discardOpen, confirmDiscard, cancelDiscard, requestDiscard } =
  useInboxDraftGuard(() => dirty.value)
let request = 0
const visible = computed(() =>
  definitions.value.filter(
    (d) =>
      !d.archived_at ||
      Object.prototype.hasOwnProperty.call(stored.value, d.id),
  ),
)

async function load(background = false) {
  const sequence = ++request
  const authored = JSON.stringify(values.value)
  if (!background) ready.value = false
  failed.value = false
  const { account, contact } = props
  if (account && !props.caseId) return
  try {
    if (
      !(await inboxService.settings()).data.data.enabled ||
      sequence !== request
    )
      return
    const [rules, reply] = await Promise.all([
      inboxService.fieldDefinitions(account ? 'case' : 'contact'),
      inboxService.fieldValues(
        contact,
        account,
        account ? props.caseId : undefined,
      ),
    ])
    if (sequence !== request) return
    if (
      background &&
      (dirty.value || busy.value || authored !== JSON.stringify(values.value))
    )
      return false
    if (account && props.caseId !== reply.data.data.subject_id)
      throw new Error('Case context changed')
    definitions.value = rules.data.data.definitions
    subjectId.value = reply.data.data.subject_id
    stored.value = Object.fromEntries(
      reply.data.data.values.map((entry) => [entry.definition_id, entry.value]),
    )
    values.value = { ...stored.value }
    ready.value = true
  } catch (error) {
    if (sequence === request) {
      if (isInboxAccessFailure(error)) clearPrivateFields()
      else failed.value = true
    }
    return false
  }
}
watch(
  () => [props.contact, props.account, props.caseId],
  () => load(),
  {
    immediate: true,
  },
)
function update(id: string, value: FieldValue) {
  values.value[id] = value
  delete errors.value[id]
}
async function save(d: FieldDefinition) {
  const sequence = request
  const value = values.value[d.id] ?? null
  busy.value = d.id
  try {
    await inboxService.setFieldValue(
      props.contact,
      props.account,
      d.id,
      subjectId.value,
      value,
    )
    if (sequence === request) {
      stored.value[d.id] = value
      toast.success(t('settings.saved'))
    }
  } catch (error) {
    if (sequence === request) {
      if (isInboxAccessFailure(error)) {
        clearPrivateFields()
        return
      }
      errors.value[d.id] = t('inboxFields.saveError')
      toast.error(errors.value[d.id])
    }
  } finally {
    busy.value = ''
  }
}
function cancelField(id: string) {
  values.value[id] = stored.value[id] ?? null
  delete errors.value[id]
}
function clearPrivateFields() {
  request++
  definitions.value = []
  values.value = {}
  stored.value = {}
  subjectId.value = ''
  ready.value = false
  failed.value = true
  busy.value = ''
  cancelDiscard()
}
const live = useInboxLive(
  () =>
    props.account
      ? props.caseId
        ? { kind: 'case', case_id: props.caseId }
        : null
      : { kind: 'contact', contact_id: props.contact },
  {
    context: () => JSON.stringify([props.contact, props.account, props.caseId]),
    blocked: () => !!busy.value || dirty.value || discardOpen.value,
    refresh: () => load(true),
    denied: clearPrivateFields,
    clear: clearPrivateFields,
  },
)
function retryLoad() {
  if (ready.value) live.requestRefresh()
  else void load()
}
let livePoll: ReturnType<typeof setInterval> | undefined
function pollVisible() {
  if (
    document.visibilityState === 'visible' &&
    !busy.value &&
    !dirty.value &&
    !discardOpen.value
  )
    live.requestRefresh()
}
onMounted(() => {
  livePoll = setInterval(pollVisible, 30000)
  document.addEventListener('visibilitychange', pollVisible)
})
onUnmounted(() => {
  request++
  clearInterval(livePoll)
  document.removeEventListener('visibilitychange', pollVisible)
})
defineExpose({ requestLeave: requestDiscard, hasDraft: () => dirty.value })
</script>
<template>
  <UnsavedChangesDialog
    :open="discardOpen"
    @leave="confirmDiscard"
    @stay="cancelDiscard"
  />
  <section
    v-if="failed || (ready && visible.length)"
    class="inbox-ui inbox-fields-panel"
  >
    <h3 class="mb-2 font-medium">
      {{ t(account ? 'inboxUX.caseFieldTitle' : 'inboxUX.contactFieldTitle') }}
    </h3>
    <InboxLiveStatus
      :state="live.state.value"
      :pending="live.pending.value"
      class="mb-2"
    />
    <div v-if="failed" role="alert">
      {{ t(ready ? 'inboxLive.fieldRefreshError' : 'inboxFields.loadError') }}
      <p v-if="ready && dirty" class="inbox-caption">
        {{ t('inboxLive.finishFieldEdit') }}
      </p>
      <Button size="sm" variant="outline" @click="retryLoad">{{
        t('common.retry')
      }}</Button>
    </div>
    <ul v-if="ready" class="grid gap-2">
      <li v-for="d in visible" :key="d.id" class="grid gap-1">
        <label
          >{{ d.label }}
          <small v-if="d.required_on_resolve && !d.archived_at">
            · {{ t('inboxFields.required') }}</small
          ><small v-if="d.archived_at">
            · {{ t('inboxFields.archived') }}</small
          ></label
        >
        <p :id="'field-help-' + d.id" class="inbox-caption">
          {{ d.description }}
          <span v-if="d.conditional_required && !d.archived_at">{{
            t('inboxIntake.conditional')
          }}</span>
          <span v-if="d.required_on_resolve && !d.archived_at">{{
            t('inboxFields.required')
          }}</span
          ><span v-if="d.archived_at">{{ t('inboxFields.archived') }}</span>
        </p>
        <form class="flex items-center gap-2" @submit.prevent="save(d)">
          <InboxFieldInput
            :definition="d"
            :aria-describedby="'field-help-' + d.id"
            :model-value="values[d.id] ?? null"
            :disabled="!!d.archived_at || !canWrite || !!busy"
            @update:model-value="(value) => update(d.id, value)"
          />
          <Button
            v-if="canWrite && !d.archived_at"
            size="sm"
            variant="outline"
            type="submit"
            :disabled="
              !!busy || (stored[d.id] ?? null) === (values[d.id] ?? null)
            "
            >{{ t('common.save') }}</Button
          >
          <Button
            v-if="canWrite && !d.archived_at"
            type="button"
            variant="ghost"
            size="sm"
            :disabled="!!busy"
            @click="cancelField(d.id)"
            >{{ t('common.cancel') }}</Button
          >
        </form>
        <p v-if="errors[d.id]" role="alert" class="text-sm text-destructive">
          {{ errors[d.id] }}
        </p>
      </li>
    </ul>
  </section>
</template>
