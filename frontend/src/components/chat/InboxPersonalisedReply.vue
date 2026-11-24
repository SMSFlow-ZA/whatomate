<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useInboxDialogFocus } from '@/composables/useInboxDialogFocus'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { inboxService, type InboxWorkflowPreview } from '@/services/inbox'
import type { messagesService } from '@/services/api'
import InboxWorkflowPreviewPanel from './InboxWorkflowPreview.vue'

const props = defineProps<{
  account: string
  contact: string
  text: string
  sending: boolean
  sendError?: string
  viewer?: string
}>()
export type PersonalisedReply = {
  body: string
  client_message_id: string
  personalisation: NonNullable<
    Parameters<typeof messagesService.send>[1]['personalisation']
  >
}
const emit = defineEmits<{ confirm: [reply: PersonalisedReply] }>()
const { t } = useI18n()
const open = ref(false),
  busy = ref(false),
  error = ref('')
const confirming = ref(false)
const preview = ref<InboxWorkflowPreview>()
let generation = 0,
  operationId = ''
const context = computed(() =>
  [props.viewer, props.account, props.contact, props.text].join('\u0000'),
)
watch(context, () => {
  generation++
  open.value = false
  preview.value = undefined
  operationId = ''
})
watch(open, (value) => {
  if (!value) {
    generation++
    busy.value = false
  }
})
watch(
  () => props.sending,
  (value) => {
    if (!value) confirming.value = false
  },
)
onUnmounted(() => {
  generation++
})
async function review() {
  if (props.sending) return
  const current = ++generation,
    captured = context.value
  open.value = true
  busy.value = true
  preview.value = undefined
  confirming.value = false
  error.value = ''
  try {
    const thread = await inboxService.conversation(props.account, props.contact)
    if (current !== generation || captured !== context.value || !open.value)
      return
    const c = thread.data.data.case
    if (!c || c.status === 'resolved') {
      error.value = t('inboxPersonalisation.startCase')
      return
    }
    const { data } = await inboxService.previewMessage(c.id, props.text)
    if (current !== generation || captured !== context.value || !open.value)
      return
    preview.value = data.data
    if (!operationId) operationId = crypto.randomUUID()
  } catch {
    if (current === generation)
      error.value = t('inboxPersonalisation.previewError')
  } finally {
    if (current === generation) busy.value = false
  }
}
function confirm() {
  const result = preview.value
  if (
    busy.value ||
    props.sending ||
    confirming.value ||
    !result?.case_id ||
    result.case_version === undefined ||
    !result.confirmation ||
    !result.reply ||
    result.send_unavailable
  )
    return
  confirming.value = true
  emit('confirm', {
    body: result.reply.text,
    client_message_id: operationId,
    personalisation: {
      case_id: result.case_id,
      version: result.case_version,
      authored_text: props.text,
      confirmation: result.confirmation,
    },
  })
}
function changeOpen(value: boolean) {
  if (!props.sending) open.value = value
}
function close() {
  open.value = false
  operationId = ''
}
const restoreFocus = useInboxDialogFocus(open)
function focusTitle(event: Event) {
  event.preventDefault()
  void nextTick(() =>
    document
      .querySelector<HTMLElement>('[data-inbox-personalised-title]')
      ?.focus(),
  )
}
defineExpose({ review, close })
</script>
<template>
  <Dialog :open="open" @update:open="changeOpen">
    <DialogContent
      class="inbox-ui max-h-[90dvh] overflow-y-auto [&>button:last-child]:min-h-[44px] [&>button:last-child]:min-w-[44px] [&>button:last-child]:right-2 [&>button:last-child]:top-2 [&>button:last-child]:flex [&>button:last-child]:items-center [&>button:last-child]:justify-center"
      @open-auto-focus="focusTitle"
      @close-auto-focus="restoreFocus"
      @escape-key-down="props.sending && $event.preventDefault()"
      @interact-outside="props.sending && $event.preventDefault()"
    >
      <DialogHeader>
        <DialogTitle
          data-inbox-personalised-title
          tabindex="-1"
          class="pr-8 focus:outline-none"
          >{{ t('inboxPersonalisation.review') }}</DialogTitle
        >
        <DialogDescription>{{
          t('inboxPersonalisation.reviewHelp')
        }}</DialogDescription>
      </DialogHeader>
      <p v-if="busy" role="status">{{ t('common.loading') }}</p>
      <p v-if="sendError" role="alert" class="inbox-notice">
        {{ sendError }} {{ t('inboxPersonalisation.uncertain') }}
      </p>
      <template v-if="error"
        ><p role="alert" class="inbox-notice">{{ error }}</p>
        <Button
          type="button"
          variant="outline"
          class="min-h-[44px]"
          :disabled="busy || sending"
          @click="review"
          >{{ t('common.retry') }}</Button
        ></template
      >
      <InboxWorkflowPreviewPanel
        v-if="preview"
        :preview="preview"
        purpose="reply"
      />
      <div class="flex flex-wrap justify-end gap-2">
        <Button
          id="inbox-personalised-reply-cancel"
          class="min-h-[44px]"
          type="button"
          variant="outline"
          :disabled="sending"
          @click="changeOpen(false)"
          >{{ t('common.cancel') }}</Button
        >
        <Button
          type="button"
          class="min-h-[44px]"
          :disabled="
            busy ||
            sending ||
            confirming ||
            !preview?.confirmation ||
            preview?.send_unavailable
          "
          @click="confirm"
          >{{ t('inboxPersonalisation.sendReviewed') }}</Button
        >
      </div>
    </DialogContent>
  </Dialog>
</template>
