<script setup lang="ts">
import { useInboxDialogFocus } from '@/composables/useInboxDialogFocus'
import { computed, ref, watch, onMounted, nextTick, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { Zap, Search } from 'lucide-vue-next'
import '@/assets/inbox.css'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  inboxService,
  type InboxWorkflow,
  type InboxWorkflowPreview,
} from '@/services/inbox'
import InboxWorkflowPreviewPanel from './InboxWorkflowPreview.vue'

const props = defineProps<{
  account: string
  contact: string
  users?: { id: string; full_name: string }[]
}>()
const emit = defineEmits<{ applied: [] }>()
const { t } = useI18n()
const open = ref(false)
const busy = ref(false)
const applying = ref(false)
const definitions = ref<InboxWorkflow[]>([])
const selected = ref('')
const query = ref('')
const searchInput = ref<HTMLInputElement>()
const filtered = computed(() =>
  definitions.value.filter((w) =>
    w.name.toLocaleLowerCase().includes(query.value.toLocaleLowerCase()),
  ),
)
const selectedDefinition = computed(() =>
  definitions.value.find((w) => w.id === selected.value),
)
const caseId = ref('')
const preview = ref<InboxWorkflowPreview | null>(null)
const dispatch = ref(false)
const error = ref('')
let runId = ''
let generation = 0

watch(
  () => [props.account, props.contact],
  () => {
    open.value = false
    generation++
  },
)
async function show() {
  const current = ++generation
  open.value = true
  selected.value = ''
  query.value = ''
  preview.value = null
  caseId.value = ''
  busy.value = true
  error.value = ''
  try {
    const [caseReply, list, state] = await Promise.all([
      inboxService.conversation(props.account, props.contact),
      inboxService.workflowDefinitions('macro'),
      inboxService.automationState(),
    ])
    if (current !== generation) return
    const c = caseReply.data.data.case
    if (c?.status !== 'resolved') caseId.value = c?.id ?? ''
    definitions.value = list.data.data.workflows
    dispatch.value = state.data.data.dispatch_available
    await nextTick()
    if (current === generation) searchInput.value?.focus()
  } catch {
    if (current === generation) error.value = t('inboxRules.loadError')
  } finally {
    if (current === generation) busy.value = false
  }
}
async function prepare() {
  const current = ++generation
  const target = selected.value
  busy.value = true
  error.value = ''
  preview.value = null
  try {
    const { data } = await inboxService.previewWorkflow(
      selected.value,
      caseId.value,
    )
    if (current !== generation || selected.value !== target) return
    preview.value = data.data
    runId = crypto.randomUUID()
  } catch {
    if (current === generation) error.value = t('inboxRules.previewError')
  } finally {
    if (current === generation) busy.value = false
  }
}
function selectMacro(id: string) {
  selected.value = id
  void prepare()
}
async function apply() {
  if (!preview.value) return
  const current = generation
  busy.value = true
  applying.value = true
  error.value = ''
  try {
    const { data } = await inboxService.executeMacro(
      selected.value,
      caseId.value,
      runId,
      preview.value.confirmation,
    )
    if (current !== generation) return
    toast.success(t('inboxRules.runState.' + data.data.run.state))
    open.value = false
    emit('applied')
  } catch {
    if (current === generation) error.value = t('inboxRules.applyError')
  } finally {
    applying.value = false
    if (current === generation) busy.value = false
  }
}
function changeOpen(value: boolean) {
  if (!applying.value) open.value = value
}
watch(open, (value) => {
  if (!value) {
    generation++
    preview.value = null
    busy.value = false
  }
})
function shortcut(event: KeyboardEvent) {
  if (
    event.ctrlKey &&
    event.altKey &&
    event.key.toLowerCase() === 'm' &&
    !event.isComposing &&
    !event.repeat &&
    !open.value &&
    !document.querySelector('[role="dialog"][data-state="open"]')
  ) {
    event.preventDefault()
    void show()
  }
}
onMounted(() => window.addEventListener('keydown', shortcut))
onUnmounted(() => {
  generation++
  window.removeEventListener('keydown', shortcut)
})

const restoreDialogFocus = useInboxDialogFocus(open)
</script>
<template>
  <Button
    type="button"
    variant="ghost"
    size="sm"
    class="mb-2 gap-1.5"
    aria-keyshortcuts="Control+Alt+M"
    @click="show"
    ><Zap class="h-4 w-4" aria-hidden="true" />{{
      t('inboxRules.macros')
    }}</Button
  >
  <Dialog :open="open" @update:open="changeOpen"
    ><DialogContent
      @close-auto-focus="restoreDialogFocus"
      class="inbox-ui max-h-[88dvh] overflow-auto sm:max-w-3xl"
      ><DialogHeader
        ><DialogTitle>{{ t('inboxRules.macros') }}</DialogTitle
        ><DialogDescription>{{
          t('inboxRules.macroHelp')
        }}</DialogDescription></DialogHeader
      >
      <p v-if="error" role="alert" class="inbox-notice text-destructive">
        {{ error }}
        <Button size="sm" variant="outline" @click="show">{{
          t('common.retry')
        }}</Button>
      </p>
      <p v-if="busy && !caseId" role="status" class="inbox-caption">
        {{ t('common.loading') }}
      </p>
      <p v-else-if="!caseId" class="inbox-notice">
        {{ t('inboxQuality.macroCaseRequired') }}
      </p>
      <div v-else class="inbox-macro-layout">
        <div class="inbox-macro-list">
          <label class="inbox-search mb-3"
            ><Search class="h-4 w-4 shrink-0" aria-hidden="true" /><input
              ref="searchInput"
              v-model="query"
              :aria-label="t('inboxUX.searchMacros')"
              :placeholder="t('inboxUX.searchMacros')"
          /></label>
          <ul
            v-if="filtered.length"
            class="inbox-list"
            :aria-label="t('inboxRules.chooseMacro')"
          >
            <li v-for="w in filtered" :key="w.id">
              <button
                type="button"
                class="inbox-macro-option"
                :aria-pressed="selected === w.id"
                :aria-label="w.name"
                :disabled="busy"
                @click="selectMacro(w.id)"
              >
                <span class="font-medium">{{ w.name }}</span
                ><span class="inbox-caption mt-1 block">{{
                  w.actions
                    .map((a) => t('inboxRules.action.' + a.type))
                    .join(', ')
                }}</span>
              </button>
            </li>
          </ul>
          <p v-else class="inbox-caption p-3">{{ t('inboxUX.noMatches') }}</p>
        </div>
        <div class="inbox-macro-review">
          <h3 v-if="selectedDefinition" class="mb-3 font-semibold">
            {{ selectedDefinition.name }}
          </h3>
          <p v-if="!preview" class="inbox-caption">
            {{ t(busy ? 'common.loading' : 'inboxUX.chooseMacroHelp') }}
          </p>
          <InboxWorkflowPreviewPanel
            v-else
            :preview="preview"
            :users="props.users"
          />
          <p v-if="preview?.reply && !dispatch" class="inbox-notice mt-3">
            {{ t('inboxRules.dispatchHeld') }}
          </p>
          <Button
            v-if="preview"
            type="button"
            class="mt-4"
            :disabled="
              busy ||
              !preview.matches ||
              preview.actions_eligible === false ||
              preview.send_unavailable ||
              (!!preview.reply && !dispatch)
            "
            @click="apply"
            >{{
              t(preview.reply ? 'inboxRules.confirmSend' : 'inboxRules.apply')
            }}</Button
          >
        </div>
      </div>
    </DialogContent></Dialog
  >
</template>
