<script setup lang="ts">
import { Button } from '@/components/ui/button'
import { Check, Flag, Clock3, History, RotateCcw } from 'lucide-vue-next'
import '@/assets/inbox.css'
import InboxFieldPanel from './InboxFieldPanel.vue'
import InboxSLAIndicators from './InboxSLAIndicators.vue'
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { isAxiosError } from 'axios'
import {
  inboxService,
  type CaseActivity,
  type CasePriority,
  type CaseStatus,
  type InboxCase,
} from '@/services/inbox'

const props = defineProps<{
  accountId: string
  contactId: string
  canWrite: boolean
  summary?: InboxCase
  history?: boolean
}>()
const emit = defineEmits<{ changed: [value: InboxCase]; 'needs-fields': [] }>()
const { t, locale } = useI18n()
const current = ref<InboxCase | null>(null)
const activity = ref<CaseActivity[]>([])
const ready = ref(false)
const busy = ref(false)
const snoozing = ref(false)
const snoozeUntil = ref('')
let sequence = 0

const priorities: CasePriority[] = ['low', 'normal', 'high', 'urgent']
const activeStatuses: CaseStatus[] = [
  'open',
  'pending',
  'waiting_customer',
  'snoozed',
  'resolved',
]
const statuses = computed<CaseStatus[]>(() =>
  current.value?.status === 'resolved' ? ['resolved', 'open'] : activeStatuses,
)
const format = (value: string) =>
  new Intl.DateTimeFormat(locale.value, {
    dateStyle: 'medium',
    timeStyle: 'short',
  }).format(new Date(value))

async function load(reset = false) {
  const request = ++sequence
  if (reset) {
    current.value = null
    activity.value = []
    ready.value = false
    snoozing.value = false
    snoozeUntil.value = ''
  }
  try {
    const { data } = await inboxService.conversation(
      props.accountId,
      props.contactId,
    )
    if (request !== sequence) return
    current.value = data.data.case
    activity.value = data.data.activity
    ready.value = true
  } catch {
    if (request === sequence) ready.value = false
  }
}

watch(
  () => [props.accountId, props.contactId],
  () => {
    void load(true)
  },
  { immediate: true },
)
watch(
  () => [props.summary?.id, props.summary?.version],
  () => {
    if (
      props.summary?.version !== current.value?.version ||
      props.summary?.id !== current.value?.id
    )
      void load()
  },
)

async function update(input: {
  status?: CaseStatus
  priority?: CasePriority
  snoozed_until?: string
}) {
  const accountId = props.accountId
  const contactId = props.contactId
  busy.value = true
  try {
    const { data } = await inboxService.update(accountId, contactId, {
      case_id: current.value?.id,
      version: current.value?.version,
      ...input,
    })
    if (props.accountId !== accountId || props.contactId !== contactId) return
    emit('changed', data.data.case)
    current.value = data.data.case
    snoozing.value = false
    await load()
  } catch (error) {
    if (props.accountId !== accountId || props.contactId !== contactId) return
    if (isAxiosError(error) && error.response?.status === 409) {
      toast.error(t('inboxWorkflow.conflict'))
      await load()
    } else if (isAxiosError(error) && error.response?.status === 422) {
      toast.error(t('inboxFields.missing'))
      emit('needs-fields')
    } else {
      toast.error(t('inboxWorkflow.updateError'))
    }
  } finally {
    busy.value = false
  }
}

function selectStatus(event: Event) {
  const control = event.target as HTMLSelectElement
  const status = control.value as CaseStatus
  control.value = current.value?.status ?? 'open'
  if (status === 'snoozed') {
    snoozing.value = true
    return
  }
  void update({ status })
}

function selectPriority(event: Event) {
  const control = event.target as HTMLSelectElement
  const priority = control.value as CasePriority
  control.value = current.value?.priority ?? 'normal'
  void update({ priority })
}

function saveSnooze() {
  const deadline = new Date(snoozeUntil.value)
  if (Number.isNaN(deadline.valueOf()) || deadline.valueOf() <= Date.now()) {
    toast.error(t('inboxWorkflow.futureTime'))
    return
  }
  void update({ status: 'snoozed', snoozed_until: deadline.toISOString() })
}
</script>

<template>
  <section
    v-if="ready"
    class="inbox-ui"
    :class="history ? 'inbox-case-details' : 'inbox-case-toolbar'"
    :aria-label="t('inboxWorkflow.title')"
  >
    <template v-if="history">
      <details class="inbox-history">
        <summary>
          <History class="h-4 w-4" aria-hidden="true" />{{
            t('inboxWorkflow.activity')
          }}
        </summary>
        <ol v-if="activity.length">
          <li v-for="item in activity" :key="item.id">
            <time :datetime="item.created_at" class="inbox-caption">{{
              format(item.created_at)
            }}</time>
            <p>
              {{ t('inboxWorkflow.event.' + item.kind)
              }}<span v-if="item.detail"> / {{ item.detail }}</span
              ><span
                v-if="item.to_status && item.to_status !== item.from_status"
              >
                / {{ t('inboxWorkflow.status.' + item.to_status) }}</span
              ><span
                v-if="
                  item.to_priority && item.to_priority !== item.from_priority
                "
              >
                / {{ t('inboxWorkflow.priority.' + item.to_priority) }}</span
              >
            </p>
          </li>
        </ol>
        <p v-else class="inbox-caption py-3">
          {{ t('inboxUX.caseHistoryEmpty') }}
        </p>
      </details>
      <InboxFieldPanel
        v-if="current"
        :contact="contactId"
        :account="accountId"
        :case-id="current.id"
        :can-write="canWrite"
      />
    </template>
    <template v-else-if="current">
      <div class="inbox-case-controls">
        <template v-if="canWrite"
          ><label class="inbox-case-select"
            ><Clock3 class="h-3.5 w-3.5" aria-hidden="true" /><select
              :aria-label="t('inboxWorkflow.caseStatus')"
              :value="current.status"
              :disabled="busy"
              @change="selectStatus"
            >
              <option v-for="status in statuses" :key="status" :value="status">
                {{
                  t(
                    status === 'open' && current.status === 'resolved'
                      ? 'inboxWorkflow.reopen'
                      : 'inboxWorkflow.status.' + status,
                  )
                }}
              </option>
            </select></label
          ><label v-if="current.status !== 'resolved'" class="inbox-case-select"
            ><Flag class="h-3.5 w-3.5" aria-hidden="true" /><select
              :aria-label="t('inboxWorkflow.casePriority')"
              :value="current.priority"
              :disabled="busy"
              @change="selectPriority"
            >
              <option
                v-for="priority in priorities"
                :key="priority"
                :value="priority"
              >
                {{ t('inboxWorkflow.priority.' + priority) }}
              </option>
            </select></label
          ></template
        >
        <span v-else class="inbox-badge">{{
          t('inboxWorkflow.status.' + current.status)
        }}</span>
        <Button
          v-if="canWrite"
          type="button"
          size="sm"
          class="ml-auto"
          :variant="current.status === 'resolved' ? 'outline' : 'default'"
          :disabled="busy"
          @click="
            update({
              status: current.status === 'resolved' ? 'open' : 'resolved',
            })
          "
          ><RotateCcw
            v-if="current.status === 'resolved'"
            class="mr-1.5 h-3.5 w-3.5"
            aria-hidden="true"
          /><Check v-else class="mr-1.5 h-3.5 w-3.5" aria-hidden="true" />{{
            t(
              current.status === 'resolved'
                ? 'inboxWorkflow.reopen'
                : 'inboxQueue.resolve',
            )
          }}</Button
        >
      </div>
      <InboxSLAIndicators v-if="current.sla" class="mt-2" :sla="current.sla" />
      <time
        v-if="current.snoozed_until"
        class="inbox-caption mt-2 block"
        :datetime="current.snoozed_until"
        >{{ t('inboxWorkflow.snoozeUntil') }}
        {{ format(current.snoozed_until) }}</time
      >
    </template>
    <Button
      v-else-if="canWrite"
      variant="outline"
      size="sm"
      :disabled="busy"
      @click="update({ status: 'open' })"
      >{{ t('inboxWorkflow.startCase') }}</Button
    >
    <form v-if="snoozing" class="mt-3 grid gap-3" @submit.prevent="saveSnooze">
      <label class="inbox-label"
        >{{ t('inboxWorkflow.snoozeUntil')
        }}<input
          v-model="snoozeUntil"
          type="datetime-local"
          required
          :disabled="busy"
      /></label>
      <div class="flex gap-2">
        <Button type="submit" size="sm" :disabled="busy">{{
          t('common.save')
        }}</Button
        ><Button
          type="button"
          size="sm"
          variant="outline"
          :disabled="busy"
          @click="snoozing = false"
          >{{ t('common.cancel') }}</Button
        >
      </div>
    </form>
  </section>
</template>
