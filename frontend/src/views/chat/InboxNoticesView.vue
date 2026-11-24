<script setup lang="ts">
import '@/assets/inbox.css'
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ArrowLeft, RotateCw } from 'lucide-vue-next'
import { Button } from '@/components/ui/button'
import { useAuthStore } from '@/stores/auth'
import { useInboxViewerContext } from '@/composables/useInboxViewerContext'
import { useInboxCursorPage } from '@/composables/useInboxCursorPage'
import {
  inboxNoticeService,
  inboxNoticesChanged,
  type InboxNotice,
} from '@/services/inboxNotices'
const { t } = useI18n(),
  auth = useAuthStore(),
  viewer = useInboxViewerContext()
const canRead = computed(() => auth.hasPermission('chat', 'read'))
const unread = ref(true),
  denied = ref(false),
  featureDisabled = ref(false),
  unavailable = ref(false),
  acknowledgeBusy = ref(''),
  acknowledgeError = ref(''),
  updateAvailable = ref(false)
const acknowledged = ref(new Set<string>()),
  acknowledgedMessage = ref(false),
  acknowledgementStatus = ref<HTMLElement>()
const scope = computed(() =>
  JSON.stringify([viewer.value, canRead.value, unread.value]),
)
const date = (value: string) =>
  new Date(value).toLocaleString(undefined, { timeZoneName: 'short' })
const page = useInboxCursorPage(
  () => scope.value,
  async (cursor) => {
    if (!canRead.value)
      return { items: [] as InboxNotice[], hasMore: false, nextCursor: '' }
    const key = scope.value
    try {
      const { data } = await inboxNoticeService.list(
        undefined,
        unread.value,
        cursor,
      )
      if (key === scope.value) {
        denied.value = false
        featureDisabled.value = false
        unavailable.value = false
        updateAvailable.value = false
        acknowledged.value = new Set()
        acknowledgeError.value = ''
      }
      return {
        items: data.data.notices,
        hasMore: data.data.has_more,
        nextCursor: data.data.next_cursor,
      }
    } catch (cause) {
      if (
        (cause as { response?: { status?: number } })?.response?.status === 404
      ) {
        if (key === scope.value) unavailable.value = true
        // Confirm the ordinary workspace setting; a missing route alone is
        // not proof that inbox workflows were disabled.
        let availability
        try {
          availability = await inboxNoticeService.availability()
        } catch (checkFailure) {
          if (
            key === scope.value &&
            [401, 403].includes(
              (checkFailure as { response?: { status?: number } })?.response
                ?.status ?? 0,
            )
          )
            denied.value = true
          throw checkFailure
        }
        if (key === scope.value && !availability.data.data.enabled) {
          featureDisabled.value = true
          return { items: [] as InboxNotice[], hasMore: false, nextCursor: '' }
        }
      }
      if (
        key === scope.value &&
        [401, 403].includes(
          (cause as { response?: { status?: number } })?.response?.status ?? 0,
        )
      )
        denied.value = true
      throw cause
    }
  },
)
const { items, busy, failed, ready, updatedAt, position, hasOlder } = page
let mutationGeneration = 0,
  refreshTimer: ReturnType<typeof setTimeout> | undefined,
  periodic: ReturnType<typeof setInterval> | undefined
const visible = computed(() =>
  canRead.value && !denied.value && !featureDisabled.value && !unavailable.value
    ? items.value
    : [],
)
function refresh() {
  if (
    !canRead.value ||
    busy.value ||
    acknowledgeBusy.value ||
    document.visibilityState === 'hidden'
  )
    return
  return page.refresh()
}
function invalidate() {
  if (!canRead.value || denied.value) return
  updateAvailable.value = true
  clearTimeout(refreshTimer)
  refreshTimer = setTimeout(() => {
    if (!position.value) void refresh()
  }, 300)
}
async function acknowledge(notice: InboxNotice) {
  if (
    !canRead.value ||
    denied.value ||
    acknowledgeBusy.value ||
    !!acknowledgeError.value ||
    busy.value ||
    notice.read_at ||
    acknowledged.value.has(notice.id)
  )
    return
  const key = scope.value,
    generation = ++mutationGeneration
  acknowledgeBusy.value = notice.id
  acknowledgeError.value = ''
  acknowledgedMessage.value = false
  try {
    const { data } = await inboxNoticeService.read(notice.id)
    if (key !== scope.value || generation !== mutationGeneration) return
    if (data.data.id !== notice.id || data.data.read !== true)
      throw new Error('Unconfirmed acknowledgement')
    if (unread.value)
      items.value = items.value.filter((item) => item.id !== notice.id)
    else acknowledged.value = new Set([...acknowledged.value, notice.id])
    acknowledgedMessage.value = true
    updateAvailable.value = true
    void nextTick(() => acknowledgementStatus.value?.focus())
  } catch (cause) {
    if (key === scope.value && generation === mutationGeneration) {
      if (
        [401, 403].includes(
          (cause as { response?: { status?: number } })?.response?.status ?? 0,
        )
      )
        denied.value = true
      acknowledgeError.value = t('inboxNotice.acknowledgeFailed')
    }
  } finally {
    if (key === scope.value && generation === mutationGeneration)
      acknowledgeBusy.value = ''
  }
}
watch(
  scope,
  () => {
    mutationGeneration++
    acknowledgeBusy.value = ''
    acknowledgeError.value = ''
    acknowledged.value = new Set()
    acknowledgedMessage.value = false
    denied.value = false
    featureDisabled.value = false
    unavailable.value = false
    updateAvailable.value = false
    clearTimeout(refreshTimer)
  },
  { flush: 'sync' },
)
onMounted(() => {
  window.addEventListener(inboxNoticesChanged, invalidate)
  window.addEventListener('focus', invalidate)
  document.addEventListener('visibilitychange', invalidate)
  periodic = setInterval(() => {
    if (!position.value && !denied.value) void refresh()
  }, 30000)
})
onUnmounted(() => {
  mutationGeneration++
  clearTimeout(refreshTimer)
  clearInterval(periodic)
  window.removeEventListener(inboxNoticesChanged, invalidate)
  window.removeEventListener('focus', invalidate)
  document.removeEventListener('visibilitychange', invalidate)
})
</script>
<template>
  <div class="inbox-ui h-full overflow-y-auto">
    <header class="border-b px-4 py-5 md:px-6">
      <div class="mx-auto max-w-4xl">
        <RouterLink
          :to="{ name: 'chat-conversation' }"
          class="inline-flex min-h-[44px] items-center gap-2 text-sm hover:underline"
          ><ArrowLeft class="size-4" aria-hidden="true" />{{
            t('inboxNotice.backToInbox')
          }}</RouterLink
        >
        <h1 class="mt-2 text-xl font-semibold">{{ t('inboxNotice.title') }}</h1>
        <p class="inbox-caption mt-2 max-w-prose">
          {{ t('inboxNotice.listHelp') }}
        </p>
      </div>
    </header>
    <section
      class="mx-auto max-w-4xl p-4 md:p-6"
      :aria-label="t('inboxNotice.title')"
      :aria-busy="busy"
    >
      <p v-if="!canRead || denied" class="inbox-notice" role="alert">
        {{ t('inboxNotice.chatRequired') }}
      </p>
      <template v-else>
        <div class="inbox-toolbar">
          <label class="inbox-check"
            ><input
              v-model="unread"
              type="checkbox"
              :disabled="!!acknowledgeBusy"
            />{{ t('inboxNotice.unreadOnly') }}</label
          ><Button
            type="button"
            variant="outline"
            :disabled="busy || !!acknowledgeBusy"
            @click="refresh"
            ><RotateCw class="mr-2 size-4" aria-hidden="true" />{{
              t('inboxQuality.refresh')
            }}</Button
          >
        </div>
        <p v-if="updatedAt" class="inbox-caption mt-2">
          {{ t('inboxNotice.updated', { at: date(updatedAt) }) }}
        </p>
        <p
          v-if="updateAvailable && position"
          class="inbox-notice mt-3"
          role="status"
        >
          {{ t('inboxNotice.newActivity') }}
        </p>
        <p v-if="failed" class="inbox-notice mt-3" role="alert">
          {{ t('inboxNotice.listFailed') }}
          <Button
            type="button"
            variant="outline"
            :disabled="busy || !!acknowledgeBusy"
            @click="refresh"
            >{{ t('common.retry') }}</Button
          >
        </p>
        <p v-if="acknowledgeError" class="inbox-notice mt-3" role="alert">
          {{ acknowledgeError }}
        </p>
        <p v-if="featureDisabled" class="inbox-notice mt-4" role="status">
          {{ t('inboxNotice.featureDisabled') }}
        </p>
        <p
          ref="acknowledgementStatus"
          tabindex="-1"
          class="inbox-caption mt-3"
          aria-live="polite"
        >
          {{ acknowledgedMessage ? t('inboxNotice.acknowledgeSuccess') : '' }}
        </p>
        <p v-if="busy" class="inbox-caption mt-4" role="status">
          {{ t('inboxNotice.loadingList') }}
        </p>
        <p
          v-else-if="ready && !visible.length && !failed && !featureDisabled"
          class="inbox-empty mt-5"
        >
          {{ t(unread ? 'inboxNotice.unreadEmpty' : 'inboxNotice.listEmpty') }}
        </p>
        <ol
          v-if="visible.length"
          class="mt-4 divide-y"
          :aria-label="t('inboxNotice.title')"
        >
          <li v-for="notice in visible" :key="notice.id" class="py-5">
            <div class="flex flex-wrap items-start justify-between gap-3">
              <div class="min-w-0 flex-1 basis-56">
                <p class="text-sm font-medium">
                  {{ t('inboxNotice.' + notice.threshold) }} ·
                  {{ t('inboxNotice.kind.' + notice.kind) }}
                </p>
                <RouterLink
                  class="mt-1 block min-h-[44px] break-words py-2.5 text-base font-semibold underline-offset-4 hover:underline sm:min-h-0 sm:py-0"
                  :to="{
                    name: 'chat-conversation',
                    params: { contactId: notice.contact_id },
                    query: {
                      inbox_account: notice.account_name,
                      inbox_account_id: notice.account_id,
                      case_id: notice.case_id,
                    },
                  }"
                  >{{
                    notice.contact_name || t('inboxQueue.unnamed')
                  }}</RouterLink
                >
                <p class="inbox-caption mt-1 break-words">
                  {{ notice.account_name }}
                </p>
              </div>
              <span class="inbox-badge">{{
                t('inboxNotice.state.' + notice.target_state)
              }}</span>
            </div>
            <dl class="mt-3 grid gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
              <div>
                <dt class="inbox-caption">{{ t('inboxNotice.due') }}</dt>
                <dd>{{ date(notice.due_at) }}</dd>
              </div>
              <div>
                <dt class="inbox-caption">{{ t('inboxNotice.recorded') }}</dt>
                <dd>{{ date(notice.observed_at) }}</dd>
              </div>
            </dl>
            <details class="mt-2 text-sm">
              <summary class="min-h-[44px] cursor-pointer py-2 sm:min-h-0">
                {{ notice.policy_name || t('inboxPolicy.default') }}
              </summary>
              <p class="inbox-caption break-all">
                {{ t('inboxNotice.policyVersion') }}:
                {{ notice.policy_version_id }}
              </p>
              <p class="inbox-caption mt-1 break-all">
                {{ t('inboxNotice.caseReference') }}: {{ notice.case_id }}
              </p>
            </details>
            <Button
              v-if="!notice.read_at && !acknowledged.has(notice.id)"
              type="button"
              variant="outline"
              :disabled="busy || !!acknowledgeBusy || !!acknowledgeError"
              @click="acknowledge(notice)"
              >{{
                acknowledgeBusy === notice.id
                  ? t('inboxNotice.acknowledging')
                  : t('inboxNotice.acknowledge')
              }}</Button
            >
            <p v-else class="inbox-caption">
              {{ t('inboxNotice.acknowledged') }}
            </p>
          </li>
        </ol>
        <nav
          v-if="position || hasOlder"
          class="mt-4 flex gap-2"
          :aria-label="t('inboxNotice.noticePages')"
        >
          <Button
            type="button"
            variant="outline"
            :disabled="busy || !!acknowledgeBusy || !position"
            @click="page.newer"
            >{{ t('inboxNotice.newer') }}</Button
          ><Button
            type="button"
            variant="outline"
            :disabled="busy || !!acknowledgeBusy || !hasOlder"
            @click="page.older"
            >{{ t('inboxNotice.older') }}</Button
          >
        </nav>
      </template>
    </section>
  </div>
</template>
