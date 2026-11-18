<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from '@/lib/toast'
import Modal from '@/components/oat/Modal.vue'
import { inboxService, type InboxWorkflow, type InboxWorkflowPreview } from '@/services/inbox'
import InboxWorkflowPreviewPanel from './InboxWorkflowPreview.vue'

const props = defineProps<{ account: string; contact: string }>()
const emit = defineEmits<{ applied: [] }>()
const { t } = useI18n()
const open = ref(false)
const busy = ref(false)
const definitions = ref<InboxWorkflow[]>([])
const selected = ref('')
const caseId = ref('')
const preview = ref<InboxWorkflowPreview | null>(null)
const dispatch = ref(false)
const error = ref('')
let runId = ''
let generation = 0

watch(() => [props.account, props.contact], () => { open.value = false; generation++ })
async function show() {
  const current = ++generation
  open.value = true
  selected.value = ''
  preview.value = null
  caseId.value = ''
  busy.value = true
  error.value = ''
  try {
    const [caseReply, list, state] = await Promise.all([inboxService.conversation(props.account, props.contact), inboxService.workflowDefinitions('macro'), inboxService.automationState()])
    if (current !== generation) return
    const c = caseReply.data.data.case
    if (c?.status !== 'resolved') caseId.value = c?.id ?? ''
    definitions.value = list.data.data.workflows
    dispatch.value = state.data.data.dispatch_available
  } catch { if (current === generation) error.value = t('inboxRules.loadError') }
  finally { if (current === generation) busy.value = false }
}
async function prepare() {
  const current = generation
  busy.value = true
  error.value = ''
  preview.value = null
  try {
    const { data } = await inboxService.previewWorkflow(selected.value, caseId.value)
    if (current !== generation) return
    preview.value = data.data
    runId = crypto.randomUUID()
  } catch { if (current === generation) error.value = t('inboxRules.previewError') }
  finally { if (current === generation) busy.value = false }
}
async function apply() {
  if (!preview.value) return
  const current = generation
  busy.value = true
  error.value = ''
  try {
    const { data } = await inboxService.executeMacro(selected.value, caseId.value, runId, preview.value.confirmation)
    if (current !== generation) return
    toast.success(t('inboxRules.runState.' + data.data.run.state))
    open.value = false
    emit('applied')
  } catch { if (current === generation) error.value = t('inboxRules.applyError') }
  finally { if (current === generation) busy.value = false }
}
</script>
<template>
  <button type="button" @click="show" class="outline small mb-2">{{ t('inboxRules.macros') }}</button>
  <Modal v-model:open="open" :title="t('inboxRules.macros')" :description="t('inboxRules.macroHelp')">
      <p v-if="error" role="alert">{{ error }}</p>
      <p v-if="!busy && !caseId">{{ t('inboxRules.caseRequired') }}</p>
      <template v-else-if="caseId">
        <label>{{ t('inboxRules.chooseMacro') }}
          <select v-model="selected" class="block w-full rounded-md border border-input bg-background px-2 py-1" :disabled="busy" @change="preview = null">
            <option value="">—</option><option v-for="w in definitions" :key="w.id" :value="w.id">{{ w.name }}</option>
          </select>
        </label>
        <button type="button" :disabled="busy || !selected" @click="prepare" class="outline">{{ t('inboxRules.review') }}</button>
        <InboxWorkflowPreviewPanel v-if="preview" :preview="preview" />
        <p v-if="preview?.reply && !dispatch">{{ t('inboxRules.dispatchHeld') }}</p>
        <button v-if="preview" type="button" :disabled="busy || !preview.matches || preview.send_unavailable || (!!preview.reply && !dispatch)" @click="apply">
          {{ t(preview.reply ? 'inboxRules.confirmSend' : 'inboxRules.apply') }}
        </button>
      </template>
  </Modal>
</template>
