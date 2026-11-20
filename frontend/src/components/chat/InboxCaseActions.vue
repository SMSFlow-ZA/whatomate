<script setup lang="ts">
import { inboxTextEntry } from '@/lib/inboxInteraction'
import { Button } from '@/components/ui/button'
import {
  Check,
  Flag,
  Clock3,
  History,
  RotateCcw,
  HelpCircle,
} from 'lucide-vue-next'
import '@/assets/inbox.css'
import InboxFieldPanel from './InboxFieldPanel.vue'
import InboxResolutionDialog from './InboxResolutionDialog.vue'
import InboxSLAIndicators from './InboxSLAIndicators.vue'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import { computed, ref, watch, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
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
const route = useRoute()
const activityGroups = computed(() => {
  const groups = new Map<string, CaseActivity[]>()
  for (const item of activity.value) {
    groups.set(item.case_id, [...(groups.get(item.case_id) ?? []), item])
  }
  return [...groups.entries()]
})
const current = ref<InboxCase | null>(null)
const activity = ref<CaseActivity[]>([])
const ready = ref(false)
const loading = ref(false)
const failed = ref(false)
const busy = ref(false)
const snoozing = ref(false)
const resolving = ref(false)
const snoozeUntil = ref('')
const snoozeOptions = computed(() => {
  if (current.value?.sla?.snooze_options?.length)
    return current.value.sla.snooze_options
  const now = new Date(),
    tomorrow = new Date(now)
  tomorrow.setDate(tomorrow.getDate() + 1)
  tomorrow.setHours(9, 0, 0, 0)
  const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone
  return [
    {
      key: 'later',
      until: new Date(now.getTime() + 7200000).toISOString(),
      timezone,
    },
    { key: 'tomorrow', until: tomorrow.toISOString(), timezone },
  ]
})

let sequence = 0
let targetTimer: ReturnType<typeof setInterval> | undefined
function refreshTargets() {
  if (
    !props.history &&
    document.visibilityState === 'visible' &&
    !loading.value &&
    !busy.value &&
    !snoozing.value &&
    !resolving.value
  )
    void load()
}

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
  loading.value = true
  failed.value = false
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
      typeof route.query.case_id === 'string' ? route.query.case_id : undefined,
    )
    if (request !== sequence) return
    current.value = data.data.case
    activity.value = data.data.activity ?? []
    ready.value = true
  } catch {
    if (request === sequence) failed.value = true
  } finally {
    if (request === sequence) loading.value = false
  }
}

watch(
  () => [props.accountId, props.contactId, route.query.case_id],
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

function chooseSnooze(until: string) {
  const date = new Date(until)
  const pad = (value: number) => String(value).padStart(2, '0')
  snoozeUntil.value = `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}
async function update(input: {
  status?: CaseStatus
  priority?: CasePriority
  snoozed_until?: string
}) {
  if (input.status === 'resolved') {
    resolving.value = true
    return
  }
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
function shortcut(event: KeyboardEvent) {
  if (
    props.history ||
    !props.canWrite ||
    busy.value ||
    loading.value ||
    event.repeat ||
    event.isComposing ||
    !event.ctrlKey ||
    !event.altKey
  )
    return
  if (
    inboxTextEntry(event) ||
    document.querySelector('[role="dialog"][data-state="open"]')
  )
    return
  const actions: Record<string, CaseStatus> = {
    p: 'pending',
    w: 'waiting_customer',
    s: 'snoozed',
    o: 'open',
    Enter: current.value?.status === 'resolved' ? 'open' : 'resolved',
  }
  const status = actions[event.key]
  if (
    !status ||
    (!current.value && status !== 'open') ||
    (current.value?.status === 'resolved' && status !== 'open')
  )
    return
  event.preventDefault()
  if (status === 'snoozed') snoozing.value = true
  else void update({ status })
}
onMounted(() => {
  window.addEventListener('keydown', shortcut)
  document.addEventListener('visibilitychange', refreshTargets)
  targetTimer = setInterval(refreshTargets, 30000)
})
onUnmounted(() => {
  sequence++
  window.removeEventListener('keydown', shortcut)
  document.removeEventListener('visibilitychange', refreshTargets)
  clearInterval(targetTimer)
})
function resolvedCase(value: InboxCase) {
  emit('changed', value)
  void load()
}
</script>

<template>
  <div v-if="failed" role="alert" class="inbox-ui inbox-notice">
    {{ t('inboxQuality.caseLoadError') }}
    <Button type="button" size="sm" variant="outline" @click="load()">{{
      t('common.retry')
    }}</Button>
  </div>
  <p v-else-if="loading && !ready" role="status" class="inbox-caption">
    {{ t('common.loading') }}
  </p>
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
        <div v-if="activity.length">
          <section
            v-for="[caseId, entries] in activityGroups"
            :key="caseId"
            :id="'case-' + caseId"
          >
            <RouterLink
              class="inbox-caption mt-3 block underline"
              :to="{ query: { ...route.query, case_id: caseId } }"
              >{{ t('inboxQuality.caseLabel') }}
              {{ caseId.slice(0, 8) }}</RouterLink
            >
            <ol>
              <li v-for="item in entries" :key="item.id">
                <time :datetime="item.created_at" class="inbox-caption">{{
                  format(item.created_at)
                }}</time>
                <p>
                  {{
                    item.actor_name ||
                    t(
                      item.actor_id
                        ? 'inboxQuality.formerUser'
                        : 'inboxQuality.systemActor',
                    )
                  }}
                  · {{ t('inboxWorkflow.event.' + item.kind)
                  }}<span v-if="item.detail"> / {{ item.detail }}</span
                  ><span
                    v-if="item.to_status && item.to_status !== item.from_status"
                  >
                    /
                    {{
                      item.from_status
                        ? t('inboxWorkflow.status.' + item.from_status) + ' → '
                        : ''
                    }}{{ t('inboxWorkflow.status.' + item.to_status) }}</span
                  ><span
                    v-if="
                      item.to_priority &&
                      item.to_priority !== item.from_priority
                    "
                  >
                    /
                    {{
                      item.from_priority
                        ? t('inboxWorkflow.priority.' + item.from_priority) +
                          ' → '
                        : ''
                    }}{{
                      t('inboxWorkflow.priority.' + item.to_priority)
                    }}</span
                  >
                  <span v-if="item.assignee_name || item.team_name">
                    / {{ item.team_name
                    }}{{ item.team_name && item.assignee_name ? ' · ' : ''
                    }}{{ item.assignee_name }}</span
                  >
                </p>
              </li>
            </ol>
          </section>
        </div>
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
          aria-keyshortcuts="Control+Alt+Enter"
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
      <div class="mt-1 flex items-start gap-2">
        <details v-if="current.sla" class="min-w-0 flex-1">
          <summary class="inbox-caption min-h-8 cursor-pointer py-1">
            {{ t('inboxQuality.responseDetails') }}
          </summary>
          <InboxSLAIndicators v-if="current.sla" :sla="current.sla" />
        </details>
        <Popover
          ><PopoverTrigger as-child
            ><Button
              type="button"
              variant="ghost"
              size="icon"
              :aria-label="t('inboxQuality.statusHelpTitle')"
              ><HelpCircle
                class="h-4 w-4"
                aria-hidden="true" /></Button></PopoverTrigger
          ><PopoverContent
            class="inbox-ui max-h-[75dvh] w-80 max-w-[calc(100vw-2rem)] overflow-auto"
            align="end"
            ><h3 class="font-medium">
              {{ t('inboxQuality.statusHelpTitle') }}
            </h3>
            <dl class="inbox-caption grid gap-2 py-2">
              <template
                v-for="state in ['pending', 'waiting_customer', 'snoozed']"
                :key="state"
                ><dt class="font-medium">
                  {{ t('inboxWorkflow.status.' + state) }}
                </dt>
                <dd>{{ t('inboxQuality.statusHelp.' + state) }}</dd></template
              >
            </dl>
            <p class="inbox-caption py-2">{{ t('inboxQuality.shortcuts') }}</p>
          </PopoverContent></Popover
        >
      </div>
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
      <div class="flex flex-wrap gap-2">
        <Button
          v-for="option in snoozeOptions"
          :key="option.key"
          type="button"
          variant="outline"
          size="sm"
          :disabled="busy"
          @click="chooseSnooze(option.until)"
          >{{ t('inboxReview.snooze.' + option.key) }}</Button
        >
      </div>
      <p class="inbox-caption">{{ t('inboxReview.snoozeHelp') }}</p>
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
    <InboxResolutionDialog
      v-if="!history"
      v-model="resolving"
      :account="accountId"
      :contact="contactId"
      @resolved="resolvedCase($event)"
    />
  </section>
</template>
