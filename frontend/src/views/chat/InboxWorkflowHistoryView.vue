<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, shallowRef, watch } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import InboxIntegrationRun from '@/components/settings/InboxIntegrationRun.vue'
import InboxIntegrationReview from '@/components/settings/InboxIntegrationReview.vue'
import { useInboxViewerContext } from '@/composables/useInboxViewerContext'
import { inboxService, type InboxWorkflowRun } from '@/services/inbox'
import type { IntegrationControlReview } from '@/services/inboxIntegrations'
import '@/assets/inbox.css'

const route = useRoute(),
  viewer = useInboxViewerContext(),
  { t, te } = useI18n()
const caseId = computed(() =>
  typeof route.params.caseId === 'string' ? route.params.caseId : '',
)
const valid = computed(
  () =>
    /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(
      caseId.value,
    ) && caseId.value !== '00000000-0000-0000-0000-000000000000',
)
const runs = shallowRef<InboxWorkflowRun[]>([]),
  request = shallowRef<IntegrationControlReview>()
const reviewer = ref<InstanceType<typeof InboxIntegrationReview>>()
const busy = ref(false),
  failed = ref(false),
  ready = ref(false),
  offset = ref(0),
  more = ref(false),
  expanded = ref(''),
  revision = ref(0),
  updatedAt = ref('')
const scope = computed(() => JSON.stringify([viewer.value, caseId.value]))
let generation = 0
const heading = ref<HTMLElement>()
const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone
const returnTo = computed(() => {
  const run = runs.value.find((item) => item.contact_id && item.account_id)
  return run
    ? {
        name: 'chat-conversation',
        params: { contactId: run.contact_id },
        query: {
          inbox_account: run.account_name,
          inbox_account_id: run.account_id,
          case_id: caseId.value,
        },
      }
    : { name: 'chat-conversation' }
})
async function load(target = offset.value) {
  if (!valid.value) return
  const current = ++generation,
    context = scope.value,
    id = caseId.value
  busy.value = true
  failed.value = false
  try {
    const { data } = await inboxService.workflowRuns(id, target)
    if (current !== generation || context !== scope.value) return
    const page = data.data.runs
    if (!Array.isArray(page) || page.some((run) => run.case_id !== id))
      throw new Error('Inconsistent case history')
    runs.value = page
    offset.value = target
    more.value = data.data.has_more === true
    ready.value = true
    updatedAt.value = new Date().toISOString()
  } catch {
    if (current === generation && context === scope.value) failed.value = true
  } finally {
    if (current === generation) busy.value = false
  }
}
function move(target: number) {
  if (busy.value) return
  reviewer.value?.confirmLeave(() => {
    request.value = undefined
    void load(target).then(() => nextTick(() => heading.value?.focus()))
  })
}
async function changed(runId: string) {
  const context = scope.value
  revision.value++
  await load()
  await nextTick()
  if (context === scope.value)
    document
      .querySelector<HTMLElement>('[data-workflow-run="' + runId + '"] h2')
      ?.focus()
}
watch(
  scope,
  () => {
    generation++
    runs.value = []
    request.value = undefined
    busy.value = false
    failed.value = false
    ready.value = false
    offset.value = 0
    more.value = false
    expanded.value = ''
    updatedAt.value = ''
    void load(0)
  },
  { immediate: true, flush: 'sync' },
)
onUnmounted(() => {
  generation++
})
function state(value: string) {
  const key = 'inboxRules.runState.' + value
  return te?.(key) ? t(key) : t('inboxIntegrationHistory.unrecognisedState')
}
</script>
<template>
  <main class="inbox-ui mx-auto grid w-full max-w-5xl gap-5 p-4 sm:p-6">
    <header class="flex flex-wrap items-start justify-between gap-3">
      <div class="min-w-0 flex-1">
        <h1 ref="heading" tabindex="-1" class="text-xl font-semibold">
          {{ t('inboxRecovery.caseHistoryTitle') }}
        </h1>
        <p class="inbox-caption mt-1">
          {{ t('inboxRecovery.caseHistoryHelp') }}
        </p>
      </div>
      <Button as-child variant="outline" class="min-h-[44px] sm:min-h-9"
        ><RouterLink :to="returnTo">{{
          t('inboxRecovery.backToInbox')
        }}</RouterLink></Button
      >
    </header>
    <InboxIntegrationReview
      ref="reviewer"
      :request="request"
      @close="request = undefined"
      @changed="changed"
    />
    <p v-if="!valid" role="alert">{{ t('inboxRecovery.invalidCase') }}</p>
    <template v-else>
      <div class="flex flex-wrap items-center gap-3">
        <p v-if="updatedAt" class="inbox-caption">
          {{ t('inboxIntegrationHistory.updated') }}
          <time :datetime="updatedAt">{{
            new Date(updatedAt).toLocaleTimeString()
          }}</time>
          · {{ timezone }}
        </p>
        <Button
          type="button"
          variant="outline"
          :disabled="busy"
          @click="move(offset)"
          >{{ t('common.refresh') }}</Button
        >
      </div>
      <p v-if="busy" role="status">{{ t('common.loading') }}</p>
      <div v-if="failed" role="alert" class="grid gap-2">
        <p>{{ t('inboxRecovery.caseHistoryError') }}</p>
        <Button
          type="button"
          variant="outline"
          class="justify-self-start"
          :disabled="busy"
          @click="move(offset)"
          >{{ t('common.retry') }}</Button
        >
      </div>
      <p v-if="ready && !runs.length" class="inbox-caption">
        {{ t('inboxRecovery.caseHistoryEmpty') }}
      </p>
      <ol v-if="runs.length" class="inbox-list">
        <li
          v-for="run in runs"
          :key="run.id"
          class="inbox-list-row"
          :data-workflow-run="run.id"
        >
          <div class="inbox-list-copy">
            <div class="flex flex-col gap-2 sm:flex-row sm:items-start">
              <h2 tabindex="-1" class="inbox-list-title min-w-0 flex-1">
                {{ run.workflow_name }}
              </h2>
              <span class="inbox-badge self-start">{{ state(run.state) }}</span>
            </div>
            <p class="inbox-caption mt-1 break-words">
              {{ run.contact_name }} · {{ run.account_name }}
            </p>
            <p class="inbox-caption mt-1">
              {{ run.actor_name || t('inboxQuality.formerUser') }} ·
              <time :datetime="run.created_at">{{
                new Date(run.created_at).toLocaleString()
              }}</time>
            </p>
            <details
              v-if="run.sequenced"
              class="mt-3"
              :open="expanded === run.id"
              @toggle="
                expanded = ($event.target as HTMLDetailsElement).open
                  ? run.id
                  : expanded === run.id
                    ? ''
                    : expanded
              "
            >
              <summary class="min-h-11 cursor-pointer py-2 text-sm font-medium">
                {{ t('inboxIntegrationHistory.title') }}
              </summary>
              <InboxIntegrationRun
                v-if="expanded === run.id"
                :run="run"
                :revision="revision"
                @review="request = $event"
              />
            </details>
          </div>
        </li>
      </ol>
      <nav
        v-if="offset || more"
        :aria-label="t('inboxRecovery.caseHistoryTitle')"
        class="flex flex-wrap items-center gap-2"
      >
        <Button
          type="button"
          variant="outline"
          :disabled="busy || !offset"
          @click="move(Math.max(0, offset - 50))"
          >{{ t('inboxIntegrationHistory.newer') }}</Button
        >
        <span class="inbox-caption">{{
          t('inboxIntegrationHistory.page', { number: offset / 50 + 1 })
        }}</span>
        <Button
          type="button"
          variant="outline"
          :disabled="busy || !more"
          @click="move(offset + 50)"
          >{{ t('inboxIntegrationHistory.older') }}</Button
        >
      </nav>
    </template>
  </main>
</template>
