<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import { useInboxViewerContext } from '@/composables/useInboxViewerContext'
import { useInboxCursorPage } from '@/composables/useInboxCursorPage'
import { useAuthStore } from '@/stores/auth'
import {
  inboxIntegrations,
  type IntegrationRecovery,
  type IntegrationExecution,
} from '@/services/inboxIntegrations'

const props = defineProps<{
  runId?: string
  executionId?: string
  users?: { id: string; full_name: string }[]
  operations?: Pick<IntegrationExecution, 'id' | 'action_index'>[]
}>()
const { t, te } = useI18n()
const viewer = useInboxViewerContext()
const auth = useAuthStore()
const operator = computed(() =>
  ['settings.general', 'chat', 'custom_actions'].every((resource) =>
    auth.hasPermission(resource, 'write'),
  ),
)
const scope = computed(() =>
  JSON.stringify([
    viewer.value,
    props.runId,
    props.executionId,
    operator.value,
  ]),
)
const {
  items,
  busy,
  failed,
  ready,
  position,
  hasOlder,
  refresh,
  older,
  newer,
} = useInboxCursorPage(
  () => scope.value,
  async (cursor) => {
    if (props.runId && !operator.value)
      throw new Error('Operator access required')
    const { data } = props.runId
      ? await inboxIntegrations.runRecoveries(props.runId, cursor)
      : await inboxIntegrations.recoveries(props.executionId!, cursor)
    return {
      items: data.data.recoveries,
      hasMore: data.data.has_more,
      nextCursor: data.data.next_cursor,
    }
  },
)
const evidence = ref<Record<string, string>>({})
const heading = ref<HTMLElement>()
async function movePage(direction: 'older' | 'newer') {
  const context = scope.value
  await (direction === 'older' ? older() : newer())
  await nextTick()
  if (context === scope.value) heading.value?.focus()
}
const evidenceBusy = ref<Record<string, boolean>>({})
const evidenceFailed = ref<Record<string, boolean>>({})
let generation = 0
watch(
  [scope, items, busy],
  () => {
    generation++
    evidence.value = {}
    evidenceBusy.value = {}
    evidenceFailed.value = {}
  },
  { flush: 'sync' },
)
onUnmounted(() => {
  generation++
})
async function showEvidence(record: IntegrationRecovery) {
  if (
    !operator.value ||
    !record.has_evidence ||
    !hasOperation(record) ||
    evidenceBusy.value[record.id]
  )
    return
  const request = generation,
    context = scope.value
  evidenceBusy.value[record.id] = true
  evidenceFailed.value[record.id] = false
  try {
    const { data } = await inboxIntegrations.recoveryEvidence(
      record.execution_id,
      record.id,
    )
    if (request !== generation || context !== scope.value || !operator.value)
      return
    evidence.value[record.id] = data.data.evidence_reference
  } catch {
    if (request === generation && context === scope.value)
      evidenceFailed.value[record.id] = true
  } finally {
    if (request === generation) evidenceBusy.value[record.id] = false
  }
}
function label(kind: string) {
  const key = 'inboxIntegrationHistory.decision.' + kind
  return te?.(key) ? t(key) : t('inboxIntegrationHistory.recordedDecision')
}
function hasOperation(record: IntegrationRecovery) {
  return (
    !!record.execution_id &&
    record.execution_id !== '00000000-0000-0000-0000-000000000000'
  )
}
function operationLabel(record: IntegrationRecovery) {
  if (!hasOperation(record))
    return t(
      record.run_id || props.runId
        ? 'inboxIntegrationHistory.workflowDecision'
        : 'inboxIntegrationHistory.scopeNotRecorded',
    )
  const item = props.operations?.find((item) => item.id === record.execution_id)
  return item
    ? t('inboxIntegrationHistory.actionNumber', {
        number: item.action_index + 1,
      })
    : t('inboxIntegrationHistory.externalOperation')
}
function actor(id: string) {
  return (
    props.users?.find((user) => user.id === id)?.full_name ||
    t('inboxQuality.formerUser')
  )
}
function priorState(value: string) {
  for (const prefix of [
    'inboxIntegrationHistory.state.',
    'inboxRules.runState.',
  ]) {
    if (te?.(prefix + value)) return t(prefix + value)
  }
  return t('inboxIntegrationHistory.unrecognisedState')
}
function date(value: string) {
  const at = new Date(value)
  return Number.isFinite(at.valueOf())
    ? at.toLocaleString()
    : t('inboxIntegrationHistory.notRecorded')
}
</script>
<template>
  <section
    class="integration-records grid min-w-0 gap-3"
    :aria-label="t('inboxIntegrationHistory.decisions')"
  >
    <div class="flex flex-wrap items-center gap-2">
      <h4 ref="heading" tabindex="-1" class="mr-auto text-sm font-semibold">
        {{ t('inboxIntegrationHistory.decisions') }}
      </h4>
      <Button
        type="button"
        variant="outline"
        size="sm"
        :disabled="busy"
        @click="refresh"
      >
        {{ t('common.refresh') }}
      </Button>
    </div>
    <p v-if="busy" class="inbox-caption" role="status">
      {{ t('common.loading') }}
    </p>
    <div v-if="failed" role="alert" class="grid gap-2">
      <p>{{ t('inboxIntegrationHistory.recordsError') }}</p>
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
      {{ t('inboxIntegrationHistory.noDecisions') }}
    </p>
    <ol v-if="items.length" class="grid gap-3">
      <li
        v-for="record in items"
        :key="record.id"
        class="grid min-w-0 gap-1 border-l-2 border-border pl-3 text-sm"
      >
        <p class="font-medium">{{ label(record.kind) }}</p>
        <p class="inbox-caption break-words">
          {{ operationLabel(record)
          }}<span v-if="hasOperation(record)">
            · {{ record.execution_id.slice(0, 8) }}</span
          >
        </p>
        <p class="inbox-caption break-words">
          {{ actor(record.actor_id) }} ·
          <time :datetime="record.created_at">{{
            date(record.created_at)
          }}</time>
        </p>
        <p class="whitespace-pre-wrap break-words">{{ record.reason }}</p>
        <p
          v-if="
            ['confirm_succeeded', 'confirm_not_applied'].includes(record.kind)
          "
          class="inbox-caption"
        >
          {{ t('inboxIntegrationHistory.manualHelp') }}
        </p>
        <details class="mt-1">
          <summary class="min-h-8 cursor-pointer py-1 text-sm">
            {{ t('inboxIntegrationHistory.auditDetails') }}
          </summary>
          <dl class="mt-2 grid gap-2">
            <div v-if="hasOperation(record)">
              <dt class="inbox-caption">
                {{ t('inboxIntegrationHistory.operationId') }}
              </dt>
              <dd class="break-all font-mono text-xs">
                {{ record.execution_id }}
              </dd>
            </div>
            <div v-if="record.run_id || runId">
              <dt class="inbox-caption">
                {{ t('inboxIntegrationHistory.runId') }}
              </dt>
              <dd class="break-all font-mono text-xs">
                {{ record.run_id || runId }}
              </dd>
            </div>
            <div>
              <dt class="inbox-caption">
                {{ t('inboxIntegrationHistory.priorState') }}
              </dt>
              <dd>{{ priorState(record.from_state) }}</dd>
            </div>
            <div>
              <dt class="inbox-caption">
                {{ t('inboxIntegrationHistory.priorSource') }}
              </dt>
              <dd>
                {{
                  t(
                    'inboxIntegrationHistory.origin.' +
                      (['provider', 'manual'].includes(
                        record.from_result_origin ?? '',
                      )
                        ? record.from_result_origin
                        : 'none'),
                  )
                }}
              </dd>
            </div>
            <div v-if="record.provider_status">
              <dt class="inbox-caption">
                {{ t('inboxIntegrationHistory.providerStatus') }}
              </dt>
              <dd>HTTP {{ record.provider_status }}</dd>
            </div>
            <div v-if="record.duration_ms !== undefined">
              <dt class="inbox-caption">
                {{ t('inboxIntegrationHistory.duration') }}
              </dt>
              <dd>{{ record.duration_ms }} ms</dd>
            </div>
            <div v-if="record.prior_started_at">
              <dt class="inbox-caption">
                {{ t('inboxIntegrationHistory.priorStarted') }}
              </dt>
              <dd>
                <time :datetime="record.prior_started_at">{{
                  date(record.prior_started_at)
                }}</time>
              </dd>
            </div>
            <div v-if="record.prior_completed_at">
              <dt class="inbox-caption">
                {{ t('inboxIntegrationHistory.priorCompleted') }}
              </dt>
              <dd>
                <time :datetime="record.prior_completed_at">{{
                  date(record.prior_completed_at)
                }}</time>
              </dd>
            </div>
            <div>
              <dt class="inbox-caption">
                {{ t('inboxIntegrationHistory.recordId') }}
              </dt>
              <dd class="break-all font-mono text-xs">{{ record.id }}</dd>
            </div>
            <div>
              <dt class="inbox-caption">
                {{ t('inboxIntegrationHistory.recordedBy') }}
              </dt>
              <dd class="break-all font-mono text-xs">{{ record.actor_id }}</dd>
            </div>
            <div>
              <dt class="inbox-caption">
                {{ t('inboxIntegrationHistory.attemptsAtDecision') }}
              </dt>
              <dd>{{ record.attempt_count }}</dd>
            </div>
          </dl>
        </details>
        <template v-if="record.has_evidence">
          <template v-if="operator && hasOperation(record)">
            <Button
              v-if="!(record.id in evidence)"
              type="button"
              variant="outline"
              size="sm"
              class="mt-2 justify-self-start"
              :disabled="evidenceBusy[record.id] || busy"
              @click="showEvidence(record)"
            >
              {{
                t(
                  evidenceBusy[record.id]
                    ? 'common.loading'
                    : 'inboxIntegrationHistory.inspectEvidence',
                )
              }}
            </Button>
            <div v-else class="mt-2 grid gap-1">
              <p class="inbox-caption">
                {{ t('inboxIntegrationHistory.investigationReference') }}
              </p>
              <p class="whitespace-pre-wrap break-all">
                {{
                  evidence[record.id] ||
                  t('inboxIntegrationHistory.notRecorded')
                }}
              </p>
            </div>
            <p v-if="evidenceFailed[record.id]" role="alert">
              {{ t('inboxIntegrationHistory.evidenceError') }}
            </p>
          </template>
          <p v-else class="inbox-caption">
            {{ t('inboxIntegrationHistory.evidenceRestricted') }}
          </p>
        </template>
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
