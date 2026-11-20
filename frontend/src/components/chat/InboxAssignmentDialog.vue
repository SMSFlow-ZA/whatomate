<script setup lang="ts">
import { useInboxDialogFocus } from '@/composables/useInboxDialogFocus'
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogDescription,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import {
  inboxService,
  type InboxCase,
  type InboxCapacity,
} from '@/services/inbox'
const props = defineProps<{ account: string; contact: string }>(),
  open = defineModel<boolean>({ required: true }),
  emit = defineEmits<{ assigned: [value: InboxCase] }>()
const { t } = useI18n(),
  rows = ref<InboxCapacity[]>([]),
  current = ref<InboxCase>(),
  canOverride = ref(false),
  reason = ref(''),
  query = ref(''),
  busy = ref(false),
  loading = ref(false),
  error = ref(''),
  updated = ref('')
let generation = 0,
  timer: ReturnType<typeof setInterval> | undefined
const visible = computed(() =>
  rows.value.filter((row) =>
    row.name.toLocaleLowerCase().includes(query.value.toLocaleLowerCase()),
  ),
)
async function load() {
  const request = ++generation
  loading.value = true
  try {
    const { data } = await inboxService.assignmentOptions(
      props.account,
      props.contact,
    )
    if (request !== generation) return
    rows.value = data.data.agents
    current.value = data.data.case
    canOverride.value = data.data.can_override
    updated.value = new Date().toLocaleTimeString()
    error.value = ''
  } catch {
    if (request === generation) error.value = t('inboxCapacity.loadError')
  } finally {
    if (request === generation) loading.value = false
  }
}
watch(
  () => [open.value, props.account, props.contact],
  () => {
    generation++
    clearInterval(timer)
    if (open.value) {
      rows.value = []
      current.value = undefined
      reason.value = ''
      void load()
      timer = setInterval(() => {
        if (!busy.value && document.visibilityState === 'visible') void load()
      }, 30000)
    }
  },
)
onUnmounted(() => {
  generation++
  clearInterval(timer)
})
async function assign(id: string | null) {
  if (!current.value || busy.value) return
  const request = generation,
    account = props.account,
    contact = props.contact
  busy.value = true
  error.value = ''
  try {
    const { data } = await inboxService.assignConversation(
      account,
      contact,
      current.value.id,
      current.value.version,
      id,
      reason.value.trim() || undefined,
    )
    if (request !== generation) return
    emit('assigned', data.data.case)
    open.value = false
  } catch {
    if (request === generation)
      error.value = t('inboxQuality.assignmentChanged')
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
      ><DialogHeader
        ><DialogTitle>{{ t('inboxQuality.assignCase') }}</DialogTitle
        ><DialogDescription>{{
          t('inboxQuality.assignCaseHelp')
        }}</DialogDescription></DialogHeader
      >
      <div v-if="error" role="alert" class="inbox-notice">
        {{ error
        }}<Button variant="outline" size="sm" :disabled="busy" @click="load">{{
          t('common.retry')
        }}</Button>
      </div>
      <p v-if="loading && !rows.length" role="status">
        {{ t('common.loading') }}
      </p>
      <div class="flex items-end gap-2">
        <label class="inbox-label grow"
          >{{ t('chat.searchUsers') }}<input v-model="query" /></label
        ><Button
          size="sm"
          variant="outline"
          :disabled="loading || busy"
          @click="load"
          >{{ t('inboxQuality.refresh') }}</Button
        >
      </div>
      <p v-if="updated" class="inbox-caption" role="status">
        {{ t('inboxQuality.lastUpdated', { time: updated }) }}
      </p>
      <p v-if="current?.status === 'resolved'" class="inbox-notice">
        {{ t('inboxQuality.assignResolved') }}
      </p>
      <label
        v-if="canOverride && current?.status !== 'resolved'"
        class="inbox-label"
        >{{ t('inboxCapacity.override')
        }}<input v-model="reason" maxlength="500" /><span
          class="inbox-caption"
          >{{ t('inboxQuality.overrideHelp') }}</span
        ></label
      >
      <ul class="inbox-list">
        <li v-for="row in visible" :key="row.agent_id">
          <Button
            variant="ghost"
            class="h-auto min-h-12 w-full justify-between gap-3 py-3"
            :disabled="
              busy ||
              current?.status === 'resolved' ||
              (row.max_active > 0 &&
                row.active >= row.max_active &&
                (!canOverride || !reason.trim()))
            "
            @click="assign(row.agent_id)"
            ><span class="text-left"
              ><span class="block">{{ row.name }}</span
              ><span class="inbox-caption"
                >{{
                  t(
                    row.available
                      ? 'inboxCapacity.available'
                      : 'inboxCapacity.unavailable',
                  )
                }}
                · {{ row.active }} /
                {{ row.max_active || t('inboxCapacity.unlimited') }}</span
              ><span
                v-if="row.max_active > 0 && row.active >= row.max_active"
                class="inbox-caption block"
                >{{ t('inboxQuality.atCapacity') }}</span
              ></span
            ><span
              v-if="current?.assignee_id === row.agent_id"
              class="inbox-badge"
              >{{ t('inboxQuality.currentOwner') }}</span
            ></Button
          >
        </li>
      </ul>
      <p v-if="!loading && !visible.length" class="inbox-caption">
        {{ t('chat.noUsersFound') }}
      </p>
      <Button
        v-if="current?.assignee_id && current.status !== 'resolved'"
        variant="outline"
        :disabled="busy"
        @click="assign(null)"
        >{{ t('chat.unassignContact') }}</Button
      >
    </DialogContent></Dialog
  >
</template>
