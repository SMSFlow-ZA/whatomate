<script setup lang="ts">
import { onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Button } from '@/components/ui/button'
import { useInboxViewerContext } from '@/composables/useInboxViewerContext'
import { inboxIntake, type IntakeSummary } from '@/services/inboxIntake'
const props = defineProps<{
  account: string
  contact: string
  caseId: string
}>()
const { t, te, locale } = useI18n(),
  viewer = useInboxViewerContext()
const rows = ref<IntakeSummary[]>([]),
  loading = ref(false),
  failed = ref(false),
  more = ref(false)
let generation = 0
const label = (state: string) =>
  t(
    te('inboxIntake.outcome.' + state)
      ? 'inboxIntake.outcome.' + state
      : 'inboxIntake.reviewOutcome',
  )
function issued(value: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime())
    ? ''
    : new Intl.DateTimeFormat(locale.value, {
        dateStyle: 'medium',
        timeStyle: 'short',
      }).format(date)
}
async function load() {
  const request = ++generation,
    context = viewer.value,
    { account, contact, caseId } = props
  rows.value = []
  loading.value = true
  failed.value = false
  more.value = false
  try {
    const { data } = await inboxIntake.summary(account, contact, caseId)
    if (request !== generation || context !== viewer.value) return
    if (data.data.case_id !== caseId || !Array.isArray(data.data.summaries))
      throw new Error('intake_context_changed')
    rows.value = data.data.summaries
    more.value = data.data.has_more
  } catch {
    if (request === generation && context === viewer.value) failed.value = true
  } finally {
    if (request === generation && context === viewer.value)
      loading.value = false
  }
}
watch(() => [props.account, props.contact, props.caseId, viewer.value], load, {
  immediate: true,
})
onUnmounted(() => generation++)
</script>
<template>
  <section
    v-if="loading || failed || rows.length"
    class="mt-4 grid min-w-0 gap-3 border-t border-border pt-4"
    :aria-label="t('inboxIntake.handoffSummary')"
  >
    <h3 class="font-medium">{{ t('inboxIntake.handoffSummary') }}</h3>
    <p v-if="loading" role="status" class="inbox-caption">
      {{ t('common.loading') }}
    </p>
    <p v-else-if="failed" role="alert" class="inbox-caption">
      {{ t('inboxIntake.summaryFailed')
      }}<Button variant="outline" size="sm" @click="load">{{
        t('common.retry')
      }}</Button>
    </p>
    <template v-else>
      <p class="inbox-caption">{{ t('inboxIntake.summaryHelp') }}</p>
      <div v-for="row in rows" :key="row.id" class="grid min-w-0 gap-2">
        <p class="flex flex-wrap items-center gap-2 text-sm">
          <span class="font-medium">{{ label(row.state) }}</span
          ><time :datetime="row.issued_at" class="inbox-caption">{{
            issued(row.issued_at)
          }}</time>
        </p>
        <ul v-if="row.outcomes.length" class="grid gap-1 text-sm">
          <li
            v-for="outcome in row.outcomes"
            :key="outcome.definition_id"
            class="break-words"
          >
            <span class="font-medium">{{ outcome.label }}</span> ·
            {{ label(outcome.status) }}
          </li>
        </ul>
        <p v-if="row.contact_fields_hidden" class="inbox-caption">
          {{ t('inboxIntake.hiddenContactResults') }}
        </p>
      </div>
      <p v-if="more" class="inbox-caption">
        {{ t('inboxIntake.moreSummaries') }}
      </p>
    </template>
  </section>
</template>
