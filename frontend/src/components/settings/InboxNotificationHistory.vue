<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import { useInboxViewerContext } from '@/composables/useInboxViewerContext'
import { useInboxCursorPage } from '@/composables/useInboxCursorPage'
import { inboxNoticeService } from '@/services/inboxNotices'
const props = defineProps<{ account: string }>()
const { t } = useI18n(),
  viewer = useInboxViewerContext(),
  denied = ref(false)
const scope = computed(() => JSON.stringify([viewer.value, props.account]))
const page = useInboxCursorPage(
  () => scope.value,
  async (cursor) => {
    denied.value = false
    const requested = scope.value
    try {
      const { data } = await inboxNoticeService.history(props.account, cursor)
      if (data.data.events.some((event) => event.account_id !== props.account))
        throw new Error('Mismatched observation account')
      return {
        items: data.data.events,
        hasMore: data.data.has_more,
        nextCursor: data.data.next_cursor,
      }
    } catch (cause) {
      if (
        requested === scope.value &&
        [401, 403].includes(
          (cause as { response?: { status?: number } })?.response?.status ?? 0,
        )
      )
        denied.value = true
      throw cause
    }
  },
)
const { items, busy, failed, ready, position, hasOlder } = page
const date = (value: string) =>
  new Date(value).toLocaleString(undefined, { timeZoneName: 'short' })
</script>
<template>
  <section :aria-label="t('inboxNotice.history')" :aria-busy="busy">
    <h4 class="inbox-section-heading mb-2">{{ t('inboxNotice.history') }}</h4>
    <p class="inbox-caption">{{ t('inboxNotice.historyHelp') }}</p>
    <p v-if="denied" class="inbox-notice mt-3" role="alert">
      {{ t('inboxNotice.accessRequired') }}
    </p>
    <template v-else>
      <div class="mt-3 flex flex-wrap items-center gap-3">
        <Button
          type="button"
          variant="outline"
          :disabled="busy"
          @click="page.refresh"
          >{{ t('inboxQuality.refresh') }}</Button
        ><span v-if="page.updatedAt.value" class="inbox-caption">{{
          t('inboxNotice.updated', { at: date(page.updatedAt.value) })
        }}</span>
      </div>
      <p v-if="failed" class="inbox-notice mt-3" role="alert">
        {{ t('inboxNotice.historyFailed') }}
        <Button
          type="button"
          variant="outline"
          :disabled="busy"
          @click="page.refresh"
          >{{ t('common.retry') }}</Button
        >
      </p>
      <p v-if="busy" class="inbox-caption mt-3" role="status">
        {{ t('inboxNotice.loadingHistory') }}
      </p>
      <p v-else-if="ready && !items.length && !failed" class="inbox-empty mt-3">
        {{ t('inboxNotice.historyEmpty') }}
      </p>
      <ol
        v-if="items.length"
        class="mt-3 divide-y"
        :aria-label="t('inboxNotice.history')"
      >
        <li v-for="event in items" :key="event.id" class="py-4">
          <p class="text-sm font-medium">
            {{ t('inboxNotice.' + event.threshold) }} ·
            {{ t('inboxNotice.kind.' + event.kind) }}
          </p>
          <p class="inbox-caption mt-1">
            {{ t('inboxNotice.observed', { at: date(event.observed_at) }) }}
          </p>
          <p class="inbox-caption mt-1">
            {{
              t('inboxNotice.availableSummary', {
                available: event.available_count,
                suppressed: event.suppressed_count,
              })
            }}
          </p>
          <details class="mt-2 text-sm">
            <summary class="min-h-[44px] cursor-pointer py-2 sm:min-h-0">
              {{ t('inboxNotice.observationDetails') }}
            </summary>
            <dl class="inbox-caption space-y-2 break-all">
              <div>
                <dt class="font-medium">
                  {{ t('inboxNotice.caseReference') }}
                </dt>
                <dd>{{ event.case_id }}</dd>
              </div>
              <div>
                <dt class="font-medium">
                  {{ t('inboxNotice.policyVersion') }}
                </dt>
                <dd>{{ event.policy_version_id }}</dd>
              </div>
              <div>
                <dt class="font-medium">{{ t('inboxNotice.due') }}</dt>
                <dd>{{ date(event.due_at) }}</dd>
              </div>
              <div>
                <dt class="font-medium">
                  {{ t('inboxNotice.routingRevision') }}
                </dt>
                <dd>{{ event.settings_revision }}</dd>
              </div>
            </dl>
            <ul class="mt-3 space-y-3">
              <li v-for="receipt in event.recipients" :key="receipt.id">
                <p class="text-sm">
                  {{ t('inboxNotice.outcome.' + receipt.outcome) }}
                </p>
                <p class="inbox-caption break-all">
                  {{ receipt.recipient_id }}
                </p>
              </li>
            </ul>
          </details>
        </li>
      </ol>
      <nav
        v-if="position || hasOlder"
        class="mt-4 flex gap-2"
        :aria-label="t('inboxNotice.historyPages')"
      >
        <Button
          type="button"
          variant="outline"
          :disabled="busy || !position"
          @click="page.newer"
          >{{ t('inboxNotice.newer') }}</Button
        ><Button
          type="button"
          variant="outline"
          :disabled="busy || !hasOlder"
          @click="page.older"
          >{{ t('inboxNotice.older') }}</Button
        >
      </nav>
    </template>
  </section>
</template>
