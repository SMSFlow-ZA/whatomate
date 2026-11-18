<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { toast } from 'vue-sonner'
import { isAxiosError } from 'axios'
import { inboxService, type CaseActivity, type CasePriority, type CaseStatus, type InboxCase } from '@/services/inbox'

const props = defineProps<{ accountId: string; contactId: string; canWrite: boolean; summary?: InboxCase; history?: boolean }>()
const emit = defineEmits<{ changed: [value: InboxCase] }>()
const { t, locale } = useI18n()
const current = ref<InboxCase | null>(null)
const activity = ref<CaseActivity[]>([])
const ready = ref(false)
const busy = ref(false)
const snoozing = ref(false)
const snoozeUntil = ref('')
let sequence = 0

const priorities: CasePriority[] = ['low', 'normal', 'high', 'urgent']
const activeStatuses: CaseStatus[] = ['open', 'pending', 'waiting_customer', 'snoozed', 'resolved']
const statuses = computed<CaseStatus[]>(() => current.value?.status === 'resolved' ? ['resolved', 'open'] : activeStatuses)
const format = (value: string) => new Intl.DateTimeFormat(locale.value, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value))

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
    const { data } = await inboxService.conversation(props.accountId, props.contactId)
    if (request !== sequence) return
    current.value = data.data.case
    activity.value = data.data.activity
    ready.value = true
  } catch {
    if (request === sequence) ready.value = false
  }
}

watch(() => [props.accountId, props.contactId], () => { void load(true) }, { immediate: true })
watch(() => [props.summary?.id, props.summary?.version], () => {
  if (props.summary?.version !== current.value?.version || props.summary?.id !== current.value?.id) void load()
})

async function update(input: { status?: CaseStatus; priority?: CasePriority; snoozed_until?: string }) {
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
  <div v-if="ready" class="flex flex-wrap items-center gap-2" :aria-label="t('inboxWorkflow.title')">
    <details v-if="history && activity.length" class="text-sm">
      <summary>{{ t('inboxWorkflow.activity') }}</summary>
      <ol>
        <li v-for="item in activity" :key="item.id">
          <time :datetime="item.created_at">{{ format(item.created_at) }}</time>
          {{ t('inboxWorkflow.event.' + item.kind) }}
          <span v-if="item.to_status && item.to_status !== item.from_status"> · {{ t('inboxWorkflow.status.' + item.to_status) }}</span>
          <span v-if="item.to_priority && item.to_priority !== item.from_priority"> · {{ t('inboxWorkflow.priority.' + item.to_priority) }}</span>
        </li>
      </ol>
    </details>
    <template v-else-if="current && !history">
      <template v-if="canWrite">
        <select
          class="case-control rounded-md border border-input bg-background px-2 py-1 text-sm"
          :aria-label="t('inboxWorkflow.caseStatus')"
          :value="current.status"
          :disabled="busy"
          @change="selectStatus"
        >
          <option v-for="status in statuses" :key="status" :value="status">
            {{ t(status === 'open' && current.status === 'resolved' ? 'inboxWorkflow.reopen' : 'inboxWorkflow.status.' + status) }}
          </option>
        </select>
        <select
          v-if="current.status !== 'resolved'"
          class="case-control rounded-md border border-input bg-background px-2 py-1 text-sm"
          :aria-label="t('inboxWorkflow.casePriority')"
          :value="current.priority"
          :disabled="busy"
          @change="selectPriority"
        >
          <option v-for="priority in priorities" :key="priority" :value="priority">
            {{ t('inboxWorkflow.priority.' + priority) }}
          </option>
        </select>
      </template>
      <span v-else class="rounded-md bg-muted px-2 py-1 text-sm">{{ t('inboxWorkflow.status.' + current.status) }}</span>
      <time class="text-sm text-muted-foreground" v-if="current.snoozed_until" :datetime="current.snoozed_until">
        {{ format(current.snoozed_until) }}
      </time>

    </template>
    <button v-else-if="canWrite" type="button" class="rounded-md border border-input bg-background px-2 py-1 text-sm" :disabled="busy" @click="update({ status: 'open' })">
      {{ t('inboxWorkflow.startCase') }}
    </button>
    <form v-if="snoozing" class="flex flex-wrap items-center gap-2" @submit.prevent="saveSnooze">
      <label class="flex flex-wrap items-center gap-2">
        {{ t('inboxWorkflow.snoozeUntil') }}
        <input class="rounded-md border border-input bg-background px-2 py-1 text-sm" v-model="snoozeUntil" type="datetime-local" required :disabled="busy" />
      </label>
      <button type="submit" class="rounded-md bg-primary px-3 py-1 text-sm text-primary-foreground" :disabled="busy">{{ t('common.save') }}</button>
      <button type="button" class="rounded-md px-3 py-1 text-sm hover:bg-accent" :disabled="busy" @click="snoozing = false">{{ t('common.cancel') }}</button>
    </form>
  </div>
</template>

<style scoped>
.case-control {
  width: auto;
  max-width: 100%;
}
</style>
