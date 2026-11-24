<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import UnsavedChangesDialog from '@/components/shared/UnsavedChangesDialog.vue'
import { useAuthStore } from '@/stores/auth'
import { useInboxViewerContext } from '@/composables/useInboxViewerContext'
import { useInboxDraftGuard } from '@/composables/useInboxDraftGuard'
import InboxNotificationHistory from './InboxNotificationHistory.vue'
import {
  inboxNoticeService,
  type InboxNoticeSettings,
  type InboxNoticeSettingsInput,
  type InboxNoticePerson,
} from '@/services/inboxNotices'

const props = defineProps<{ account: string }>()
const emit = defineEmits<{ busy: [value: boolean] }>()
const { t } = useI18n(),
  auth = useAuthStore(),
  viewer = useInboxViewerContext()
const context = computed(() => JSON.stringify([viewer.value, props.account]))
const canRead = computed(() =>
  ['chat', 'contacts', 'settings.general'].every((key) =>
    auth.hasPermission(key, 'read'),
  ),
)
const canWrite = computed(
  () => canRead.value && auth.hasPermission('settings.general', 'write'),
)
const canBrowse = computed(
  () => canRead.value && auth.hasPermission('users', 'read'),
)
const empty = (): InboxNoticeSettingsInput => ({
  revision: 0,
  enabled: false,
  at_risk: true,
  breach: true,
  recipient_ids: [],
})
const draft = ref(empty()),
  baseline = ref(''),
  saved = ref<InboxNoticeSettings>()
const loading = ref(false),
  ready = ref(false),
  failed = ref(false),
  busy = ref(false),
  blocked = ref(false),
  unconfirmed = ref(false),
  message = ref(''),
  savedMessage = ref(false)
const people = ref<InboxNoticePerson[]>([]),
  known = ref<Record<string, InboxNoticePerson>>({}),
  search = ref(''),
  appliedSearch = ref(''),
  nextCursor = ref(''),
  peopleBusy = ref(false),
  peopleFailed = ref(false),
  directoryDenied = ref(false)
const historyOpen = ref(false),
  status = ref<HTMLElement>()
let loadGeneration = 0,
  mutationGeneration = 0,
  peopleGeneration = 0
const locked = computed(
  () =>
    busy.value ||
    loading.value ||
    blocked.value ||
    unconfirmed.value ||
    !ready.value,
)
const invalid = computed(
  () =>
    draft.value.recipient_ids.length > 20 ||
    (draft.value.enabled &&
      (!draft.value.recipient_ids.length ||
        (!draft.value.at_risk && !draft.value.breach))),
)
const { discardOpen, confirmDiscard, cancelDiscard, requestDiscard } =
  useInboxDraftGuard(
    () => ready.value && JSON.stringify(draft.value) !== baseline.value,
  )
function confirmLeave(action: () => void) {
  if (!busy.value)
    requestDiscard(() => {
      baseline.value = JSON.stringify(draft.value)
      action()
    })
}
defineExpose({ confirmLeave })
function input(row: InboxNoticeSettings): InboxNoticeSettingsInput {
  return {
    revision: row.revision,
    enabled: row.enabled,
    at_risk: row.at_risk,
    breach: row.breach,
    recipient_ids: [...row.recipient_ids],
  }
}
function install(row: InboxNoticeSettings) {
  if (
    row.account_id !== props.account ||
    !Number.isSafeInteger(row.revision) ||
    !Array.isArray(row.recipient_ids)
  )
    throw new Error('Mismatched notification settings')
  saved.value = { ...row, recipient_ids: [...row.recipient_ids] }
  draft.value = input(row)
  baseline.value = JSON.stringify(draft.value)
}
async function load() {
  if (!canRead.value || blocked.value) return
  const key = context.value,
    generation = ++loadGeneration
  loading.value = true
  ready.value = false
  failed.value = false
  savedMessage.value = false
  try {
    const { data } = await inboxNoticeService.settings(props.account)
    if (key !== context.value || generation !== loadGeneration) return
    install(data.data.settings)
    ready.value = true
    unconfirmed.value = false
    message.value = ''
  } catch (cause) {
    if (key === context.value && generation === loadGeneration) {
      failed.value = true
      if (
        [401, 403].includes(
          (cause as { response?: { status?: number } })?.response?.status ?? 0,
        )
      ) {
        blocked.value = true
        draft.value = empty()
        saved.value = undefined
      }
    }
  } finally {
    if (key === context.value && generation === loadGeneration)
      loading.value = false
  }
}
async function findPeople(cursor = '') {
  if (!canBrowse.value || blocked.value) return
  const key = context.value,
    generation = ++peopleGeneration,
    query = cursor ? appliedSearch.value : search.value.trim()
  if (Array.from(query).length > 100) return
  peopleBusy.value = true
  peopleFailed.value = false
  if (!cursor) {
    people.value = []
    nextCursor.value = ''
    appliedSearch.value = query
  }
  try {
    const { data } = await inboxNoticeService.recipients(
      props.account,
      query,
      cursor || undefined,
    )
    if (key !== context.value || generation !== peopleGeneration) return
    people.value = data.data.recipients
    directoryDenied.value = false
    for (const person of people.value) known.value[person.id] = person
    nextCursor.value =
      data.data.has_more && data.data.next_cursor !== cursor
        ? data.data.next_cursor
        : ''
  } catch (cause) {
    if (key === context.value && generation === peopleGeneration) {
      people.value = []
      directoryDenied.value = [401, 403].includes(
        (cause as { response?: { status?: number } })?.response?.status ?? 0,
      )
      if (directoryDenied.value) {
        known.value = {}
        nextCursor.value = ''
      }
      peopleFailed.value = !directoryDenied.value
    }
  } finally {
    if (key === context.value && generation === peopleGeneration)
      peopleBusy.value = false
  }
}
function toggle(id: string, checked: boolean) {
  if (!canWrite.value || locked.value) return
  if (
    checked &&
    !draft.value.recipient_ids.includes(id) &&
    draft.value.recipient_ids.length < 20
  )
    draft.value.recipient_ids.push(id)
  else if (!checked)
    draft.value.recipient_ids = draft.value.recipient_ids.filter(
      (value) => value !== id,
    )
  savedMessage.value = false
}
function reset() {
  if (saved.value) {
    draft.value = input(saved.value)
    baseline.value = JSON.stringify(draft.value)
    message.value = ''
  }
}
async function save() {
  if (!canWrite.value || locked.value || invalid.value) return
  const key = context.value,
    generation = ++mutationGeneration,
    payload = { ...draft.value, recipient_ids: [...draft.value.recipient_ids] }
  busy.value = true
  emit('busy', true)
  message.value = ''
  savedMessage.value = false
  try {
    const { data } = await inboxNoticeService.save(props.account, payload)
    if (key !== context.value || generation !== mutationGeneration) return
    if (data.data.settings.revision !== payload.revision + 1)
      throw new Error('Unconfirmed revision')
    install(data.data.settings)
    savedMessage.value = true
  } catch (cause) {
    if (key === context.value && generation === mutationGeneration) {
      const code = (cause as { response?: { status?: number } })?.response
        ?.status
      if ([401, 403].includes(code ?? 0)) {
        blocked.value = true
        draft.value = empty()
        saved.value = undefined
        ready.value = false
      } else if (code === 400)
        message.value = t('inboxNotice.invalidRecipients')
      else {
        unconfirmed.value = true
        message.value = t(
          code === 409 ? 'inboxNotice.conflict' : 'inboxNotice.unknownSave',
        )
      }
    }
  } finally {
    if (key === context.value && generation === mutationGeneration) {
      busy.value = false
      emit('busy', false)
      void nextTick(() => status.value?.focus())
    }
  }
}
watch(
  [context, canRead, canWrite, canBrowse],
  () => {
    loadGeneration++
    mutationGeneration++
    peopleGeneration++
    draft.value = empty()
    baseline.value = ''
    saved.value = undefined
    ready.value = false
    loading.value = false
    failed.value = false
    blocked.value = false
    busy.value = false
    unconfirmed.value = false
    message.value = ''
    savedMessage.value = false
    people.value = []
    known.value = {}
    search.value = ''
    appliedSearch.value = ''
    nextCursor.value = ''
    peopleBusy.value = false
    peopleFailed.value = false
    directoryDenied.value = false
    historyOpen.value = false
    emit('busy', false)
    if (canRead.value) {
      void load()
      if (canBrowse.value) void findPeople()
    }
  },
  { immediate: true, flush: 'sync' },
)
onUnmounted(() => {
  loadGeneration++
  mutationGeneration++
  peopleGeneration++
  emit('busy', false)
})
</script>
<template>
  <section
    class="inbox-form mt-5"
    :aria-label="t('inboxNotice.settingsTitle')"
    :aria-busy="loading"
  >
    <UnsavedChangesDialog
      :open="discardOpen"
      @stay="cancelDiscard"
      @leave="confirmDiscard"
    />
    <h3 class="inbox-section-heading">{{ t('inboxNotice.settingsTitle') }}</h3>
    <p class="inbox-caption mt-2">{{ t('inboxNotice.settingsHelp') }}</p>
    <p v-if="!canRead || blocked" class="inbox-notice mt-4" role="alert">
      {{ t('inboxNotice.accessRequired') }}
    </p>
    <p v-else-if="loading" class="inbox-notice mt-4" role="status">
      {{ t('inboxNotice.loadingSettings') }}
    </p>
    <div v-else-if="failed" class="inbox-notice mt-4" role="alert">
      {{ t('inboxNotice.loadFailed') }}
      <Button variant="outline" @click="confirmLeave(load)">{{
        t('common.retry')
      }}</Button>
    </div>
    <form v-else-if="ready" @submit.prevent="save">
      <p v-if="!canWrite" class="inbox-notice mt-4">
        {{ t('inboxPolicy.readOnly') }}
      </p>
      <fieldset
        class="mt-5 space-y-4"
        :disabled="locked || !canWrite"
        @change="savedMessage = false"
      >
        <label class="inbox-check"
          ><input
            v-model="draft.enabled"
            type="checkbox"
            aria-describedby="internal-notice-enable-help"
          />{{ t('inboxNotice.enable') }}</label
        >
        <p id="internal-notice-enable-help" class="inbox-caption">
          {{ t('inboxNotice.activationHelp') }}
        </p>
        <fieldset class="flex flex-wrap gap-x-6 gap-y-3">
          <legend class="inbox-label mb-2">
            {{ t('inboxNotice.thresholds') }}
          </legend>
          <label class="inbox-check"
            ><input v-model="draft.at_risk" type="checkbox" />{{
              t('inboxNotice.at_risk')
            }}</label
          >
          <label class="inbox-check"
            ><input v-model="draft.breach" type="checkbox" />{{
              t('inboxNotice.breach')
            }}</label
          >
        </fieldset>
      </fieldset>
      <section
        class="mt-6 border-t pt-5"
        :aria-label="t('inboxNotice.recipients')"
      >
        <h4 class="inbox-section-heading">{{ t('inboxNotice.recipients') }}</h4>
        <p class="inbox-caption mt-2">{{ t('inboxNotice.recipientsHelp') }}</p>
        <p class="inbox-caption mt-3" role="status">
          {{ t('inboxNotice.selected', { count: draft.recipient_ids.length }) }}
        </p>
        <ul v-if="draft.recipient_ids.length" class="mt-3 divide-y">
          <li
            v-for="id in draft.recipient_ids"
            :key="id"
            class="flex flex-wrap items-center justify-between gap-2 py-2"
          >
            <div class="min-w-0">
              <p class="break-words text-sm font-medium">
                {{ known[id]?.name || t('inboxNotice.staffReference') }}
              </p>
              <p v-if="!known[id]" class="inbox-caption break-all">{{ id }}</p>
            </div>
            <Button
              v-if="canWrite"
              type="button"
              variant="ghost"
              :disabled="locked"
              :aria-label="
                t('inboxNotice.removePerson', { name: known[id]?.name || id })
              "
              @click="toggle(id, false)"
              >{{ t('common.remove') }}</Button
            >
          </li>
        </ul>
        <p
          v-if="!canBrowse || directoryDenied"
          class="inbox-notice mt-3"
          role="status"
        >
          {{ t('inboxNotice.directoryRequired') }}
          <Button
            v-if="canBrowse && directoryDenied"
            type="button"
            variant="outline"
            :disabled="peopleBusy || locked"
            @click="findPeople()"
            >{{
              peopleBusy ? t('inboxNotice.loadingPeople') : t('common.retry')
            }}</Button
          >
        </p>
        <template v-else>
          <div class="inbox-toolbar mt-4">
            <label class="inbox-label flex-1"
              >{{ t('inboxNotice.searchStaff')
              }}<input
                v-model="search"
                type="search"
                maxlength="100"
                :disabled="locked || !canWrite"
                @keydown.enter.prevent="findPeople()" /></label
            ><Button
              type="button"
              variant="outline"
              :disabled="locked || peopleBusy || !canWrite"
              @click="findPeople()"
              >{{ t('common.search') }}</Button
            >
          </div>
          <p v-if="peopleBusy" class="inbox-notice" role="status">
            {{ t('inboxNotice.loadingPeople') }}
          </p>
          <p v-else-if="peopleFailed" class="inbox-notice" role="alert">
            {{ t('inboxNotice.peopleFailed') }}
            <Button
              type="button"
              variant="outline"
              :disabled="locked"
              @click="findPeople()"
              >{{ t('common.retry') }}</Button
            >
          </p>
          <fieldset v-else :disabled="locked || !canWrite" class="space-y-1">
            <legend class="sr-only">
              {{ t('inboxNotice.searchResults') }}
            </legend>
            <label
              v-for="person in people"
              :key="person.id"
              class="inbox-check min-h-[44px]"
              ><input
                type="checkbox"
                :checked="draft.recipient_ids.includes(person.id)"
                :disabled="
                  draft.recipient_ids.length >= 20 &&
                  !draft.recipient_ids.includes(person.id)
                "
                @change="
                  toggle(person.id, ($event.target as HTMLInputElement).checked)
                "
              /><span class="min-w-0 break-words"
                >{{ person.name
                }}<span
                  v-if="!person.full_contact_read"
                  class="inbox-caption block"
                  >{{ t('inboxNotice.assignedOnly') }}</span
                ></span
              ></label
            >
            <p v-if="!people.length" class="inbox-caption">
              {{ t('inboxNotice.noPeople') }}
            </p>
          </fieldset>
          <Button
            v-if="nextCursor && !peopleBusy"
            type="button"
            variant="outline"
            class="mt-3"
            :disabled="locked || !canWrite"
            @click="findPeople(nextCursor)"
            >{{ t('inboxNotice.morePeople') }}</Button
          >
        </template>
      </section>
      <div ref="status" tabindex="-1" class="mt-4" aria-live="polite">
        <p v-if="invalid" class="inbox-notice">
          {{ t('inboxNotice.invalidRecipients') }}
        </p>
        <p v-if="message" class="inbox-notice text-destructive" role="alert">
          {{ message }}
        </p>
        <p v-if="savedMessage" class="inbox-notice">
          {{ t('inboxNotice.saved') }}
        </p>
      </div>
      <footer class="inbox-form-footer mt-5">
        <Button
          v-if="unconfirmed"
          type="button"
          variant="outline"
          @click="confirmLeave(load)"
          >{{ t('inboxPolicy.refreshSaved') }}</Button
        >
        <template v-else-if="canWrite"
          ><Button
            type="button"
            variant="outline"
            :disabled="locked"
            @click="confirmLeave(reset)"
            >{{ t('common.cancel') }}</Button
          ><Button type="submit" :disabled="locked || invalid">{{
            busy ? t('inboxPolicy.saving') : t('common.save')
          }}</Button></template
        >
      </footer>
    </form>
    <div v-if="ready && !blocked" class="mt-6 border-t pt-5">
      <Button
        type="button"
        variant="outline"
        :aria-expanded="historyOpen"
        @click="historyOpen = !historyOpen"
        >{{ t('inboxNotice.history') }}</Button
      ><InboxNotificationHistory
        v-if="historyOpen"
        :key="context"
        class="mt-4"
        :account="account"
      />
    </div>
  </section>
</template>
