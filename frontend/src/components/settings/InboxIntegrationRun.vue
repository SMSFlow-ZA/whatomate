<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import { useAuthStore } from '@/stores/auth'
import { useInboxViewerContext } from '@/composables/useInboxViewerContext'
import { useInboxCursorPage } from '@/composables/useInboxCursorPage'
import {
  inboxIntegrations,
  type IntegrationExecution,
  type IntegrationControlReview,
} from '@/services/inboxIntegrations'
import type { InboxWorkflowRun } from '@/services/inbox'
import InboxIntegrationRecords from './InboxIntegrationRecords.vue'

const props = defineProps<{
  run: InboxWorkflowRun
  users?: { id: string; full_name: string }[]
  revision?: number
}>()
const emit = defineEmits<{ review: [request: IntegrationControlReview] }>()
const { t, te } = useI18n()
const auth = useAuthStore()
const viewer = useInboxViewerContext()
const operator = computed(() =>
  ['settings.general', 'chat', 'custom_actions'].every((resource) =>
    auth.hasPermission(resource, 'write'),
  ),
)
const canReviewSequence = computed(
  () =>
    props.run.sequenced &&
    ['sequence_review', 'sequence_blocked'].includes(props.run.state) &&
    auth.hasPermission('chat', 'write') &&
    (props.run.actor_id === auth.user?.id ||
      auth.hasPermission('settings.general', 'write')),
)
const heading = ref<HTMLElement>()
async function movePage(direction: 'older' | 'newer') {
  const context = viewer.value,
    runId = props.run.id
  await (direction === 'older' ? older() : newer())
  await nextTick()
  if (context === viewer.value && runId === props.run.id) heading.value?.focus()
}
const runRecords = ref(false)
const {
  items,
  busy,
  failed,
  ready,
  updatedAt,
  position,
  hasOlder,
  refresh,
  older,
  newer,
} = useInboxCursorPage(
  () => JSON.stringify([viewer.value, props.run.id, props.revision]),
  async (cursor) => {
    const { data } = await inboxIntegrations.executions({
      run_id: props.run.id,
      cursor,
    })
    return {
      items: data.data.executions,
      hasMore: data.data.has_more,
      nextCursor: data.data.next_cursor,
    }
  },
)
const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone
function date(value: string) {
  const at = new Date(value)
  return Number.isFinite(at.valueOf())
    ? at.toLocaleString()
    : t('inboxIntegrationHistory.notRecorded')
}
function state(value: string) {
  const key = 'inboxIntegrationHistory.state.' + value
  return te?.(key) ? t(key) : t('inboxIntegrationHistory.unrecognisedState')
}
function outcome(value?: string) {
  const key = 'inboxIntegrationHistory.outcome.' + value
  return te?.(key) ? t(key) : t('inboxIntegrationHistory.outcomeOther')
}
function disposition(item: IntegrationExecution) {
  return t(
    'inboxIntegrationHistory.' +
      (item.outputs_applied_at
        ? 'outputsApplied'
        : item.outputs_discarded_at
          ? 'outputsDiscarded'
          : item.state === 'succeeded'
            ? 'outputsPending'
            : 'outputsNotApplied'),
  )
}
function actor(id: string) {
  return (
    props.users?.find((user) => user.id === id)?.full_name ||
    t('inboxQuality.formerUser')
  )
}
</script>
<template>
  <section
    class="integration-run mt-4 grid min-w-0 gap-4 border-t border-border pt-4"
    :aria-label="t('inboxIntegrationHistory.title')"
    :data-inbox-integration-run="run.id"
  >
    <div class="flex flex-wrap items-start gap-2">
      <div class="mr-auto min-w-0">
        <h3 ref="heading" tabindex="-1" class="font-semibold">
          {{ t('inboxIntegrationHistory.title') }}
        </h3>
        <p class="inbox-caption mt-1 max-w-prose">
          {{ t('inboxIntegrationHistory.currentHelp') }}
        </p>
      </div>
      <Button
        type="button"
        variant="outline"
        size="sm"
        :disabled="busy"
        @click="refresh"
        >{{ t('common.refresh') }}</Button
      >
    </div>
    <p
      v-if="['cancelled', 'sequence_cancelled'].includes(run.state)"
      class="inbox-notice"
    >
      {{ t('inboxIntegrationHistory.cancelledHelp') }}
    </p>
    <p v-if="updatedAt" class="inbox-caption">
      {{ t('inboxIntegrationHistory.updated') }}
      <time :datetime="updatedAt">{{ date(updatedAt) }}</time> · {{ timezone }}
    </p>
    <p v-if="busy" role="status" class="inbox-caption">
      {{ t('common.loading') }}
    </p>
    <div v-if="failed" class="grid gap-2" role="alert">
      <p>{{ t('inboxIntegrationHistory.loadError') }}</p>
      <Button
        type="button"
        variant="outline"
        size="sm"
        class="justify-self-start"
        :disabled="busy"
        @click="refresh"
        >{{ t('common.retry') }}</Button
      >
    </div>
    <p v-if="ready && !items.length" class="inbox-caption">
      {{ t('inboxIntegrationHistory.noExecutions') }}
    </p>
    <ol v-if="items.length" class="grid gap-4">
      <li
        v-for="item in items"
        :key="item.id"
        class="grid min-w-0 gap-3 rounded-lg border border-border p-3"
      >
        <div class="flex flex-wrap items-center gap-2">
          <h4 class="mr-auto font-medium">
            {{
              t('inboxIntegrationHistory.actionNumber', {
                number: item.action_index + 1,
              })
            }}
          </h4>
          <span class="inbox-badge">{{ state(item.state) }}</span>
        </div>
        <p v-if="item.outcome" class="text-sm">{{ outcome(item.outcome) }}</p>
        <dl class="grid grid-cols-1 gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
          <div>
            <dt class="inbox-caption">
              {{ t('inboxIntegrationHistory.resultSource') }}
            </dt>
            <dd>
              {{
                t(
                  'inboxIntegrationHistory.origin.' +
                    (['provider', 'manual'].includes(item.result_origin ?? '')
                      ? item.result_origin
                      : 'none'),
                )
              }}
            </dd>
          </div>
          <div>
            <dt class="inbox-caption">
              {{ t('inboxIntegrationHistory.fieldDisposition') }}
            </dt>
            <dd>{{ disposition(item) }}</dd>
          </div>
          <div>
            <dt class="inbox-caption">
              {{ t('inboxIntegrationHistory.attempts') }}
            </dt>
            <dd>{{ item.attempt_count }}</dd>
          </div>
          <div>
            <dt class="inbox-caption">
              {{ t('inboxIntegrationHistory.originalActor') }}
            </dt>
            <dd>{{ actor(item.actor_id) }}</dd>
          </div>
          <div v-if="item.provider_status">
            <dt class="inbox-caption">
              {{ t('inboxIntegrationHistory.providerStatus') }}
            </dt>
            <dd>HTTP {{ item.provider_status }}</dd>
          </div>
          <div v-if="item.duration_ms !== undefined">
            <dt class="inbox-caption">
              {{ t('inboxIntegrationHistory.duration') }}
            </dt>
            <dd>{{ item.duration_ms }} ms</dd>
          </div>
          <div v-if="item.started_at">
            <dt class="inbox-caption">
              {{ t('inboxIntegrationHistory.started') }}
            </dt>
            <dd>
              <time :datetime="item.started_at">{{
                date(item.started_at)
              }}</time>
            </dd>
          </div>
          <div v-if="item.completed_at">
            <dt class="inbox-caption">
              {{ t('inboxIntegrationHistory.completed') }}
            </dt>
            <dd>
              <time :datetime="item.completed_at">{{
                date(item.completed_at)
              }}</time>
            </dd>
          </div>
        </dl>
        <p v-if="item.result_origin === 'manual'" class="inbox-caption">
          {{ t('inboxIntegrationHistory.manualHelp') }}
        </p>
        <p
          v-if="['unknown', 'submitting'].includes(item.state)"
          class="inbox-notice"
        >
          {{ t('inboxIntegrationHistory.unresolvedHelp') }}
        </p>
        <details>
          <summary class="min-h-8 cursor-pointer py-1 text-sm">
            {{ t('inboxIntegrationHistory.operationDetails') }}
          </summary>
          <dl class="mt-2 grid gap-2 text-sm">
            <div>
              <dt class="inbox-caption">
                {{ t('inboxIntegrationHistory.operationId') }}
              </dt>
              <dd class="break-all font-mono text-xs">{{ item.id }}</dd>
            </div>
            <div>
              <dt class="inbox-caption">
                {{ t('inboxIntegrationHistory.definitionId') }}
              </dt>
              <dd class="break-all font-mono text-xs">
                {{ item.integration_id }}
              </dd>
            </div>
            <div>
              <dt class="inbox-caption">
                {{ t('inboxIntegrationHistory.originalActor') }}
              </dt>
              <dd class="break-all font-mono text-xs">{{ item.actor_id }}</dd>
            </div>
            <div v-if="item.retry_until">
              <dt class="inbox-caption">
                {{ t('inboxIntegrationHistory.retryUntil') }}
              </dt>
              <dd>
                <time :datetime="item.retry_until">{{
                  date(item.retry_until)
                }}</time>
              </dd>
              <dd class="inbox-caption">
                {{ t('inboxIntegrationHistory.retryHelp') }}
              </dd>
            </div>
          </dl>
        </details>
        <Button
          v-if="
            operator &&
            ['unknown', 'failed'].includes(item.state) &&
            item.attempt_count > 0
          "
          type="button"
          variant="outline"
          size="sm"
          class="justify-self-start"
          :disabled="busy || failed"
          @click="emit('review', { kind: 'retry', run, execution: item })"
          >{{ t('inboxRecovery.reviewRetry') }}</Button
        >
      </li>
    </ol>
    <div v-if="position || hasOlder" class="flex flex-wrap items-center gap-2">
      <Button
        type="button"
        variant="outline"
        size="sm"
        :disabled="busy || !position"
        @click="movePage('newer')"
        >{{ t('inboxIntegrationHistory.newer') }}</Button
      >
      <span class="inbox-caption">{{
        t('inboxIntegrationHistory.page', { number: position + 1 })
      }}</span>
      <Button
        type="button"
        variant="outline"
        size="sm"
        :disabled="busy || !hasOlder"
        @click="movePage('older')"
        >{{ t('inboxIntegrationHistory.older') }}</Button
      >
    </div>
    <div class="grid gap-3">
      <Button
        v-if="canReviewSequence"
        type="button"
        variant="outline"
        size="sm"
        class="justify-self-start"
        :disabled="busy || failed"
        @click="emit('review', { kind: 'resume', run })"
        >{{ t('inboxRecovery.reviewResume') }}</Button
      >
      <Button
        v-if="
          operator &&
          !['sequence_cancelled', 'sent', 'applied'].includes(run.state)
        "
        type="button"
        variant="outline"
        size="sm"
        class="justify-self-start"
        @click="emit('review', { kind: 'cancel', run })"
        >{{ t('inboxRecovery.reviewCancel') }}</Button
      >
      <Button
        type="button"
        variant="outline"
        size="sm"
        class="justify-self-start"
        :aria-expanded="runRecords"
        @click="runRecords = !runRecords"
        >{{ t('inboxIntegrationHistory.decisions') }}</Button
      >
      <InboxIntegrationRecords
        v-if="runRecords"
        :key="run.id + viewer"
        :run-id="run.id"
        :users="users"
        :operations="items"
      />
    </div>
  </section>
</template>
<style scoped>
@media (max-width: 640px) {
  button,
  summary {
    min-height: 2.75rem;
  }
}
</style>
