<script setup lang="ts">
import {
  computed,
  nextTick,
  onUnmounted,
  reactive,
  ref,
  shallowRef,
  watch,
} from 'vue'
import { PageHeader, DataTable, type Column } from '@/components/shared'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { ScrollText, RefreshCw, Download, ChevronDown } from 'lucide-vue-next'
import {
  auditLogsService,
  type AuditChoices,
  type AuditFilters,
  type AuditLogEntry,
} from '@/services/api'
import { useInboxViewerContext } from '@/composables/useInboxViewerContext'
import { useAuditPage } from '@/composables/useAuditPage'
import { useAuditExport } from '@/composables/useAuditExport'
import { auditContextLabel, auditFailure } from '@/lib/auditInvestigation'
import { formatDateTime, formatLabel } from '@/lib/utils'
import '@/assets/inbox.css'

const viewer = useInboxViewerContext(),
  page = useAuditPage(() => viewer.value)
const {
  rows,
  busy,
  failure,
  total,
  asOf,
  canExport,
  position,
  hasMore,
  ready,
  attemptedTarget,
} = page
const draft = reactive<Record<string, string>>({}),
  applied = shallowRef<AuditFilters>({ limit: 25 })
const choices = shallowRef<AuditChoices>(),
  choicesError = ref(''),
  choicesBusy = ref(false)
const exporter = useAuditExport(
  () => JSON.stringify([viewer.value, applied.value]),
  () => canExport.value && !busy.value && !dirty.value,
)
const {
  exporting,
  error: exportError,
  notice: exportNotice,
  cursor: exportCursor,
  parts: exportParts,
  retryAvailable: retryExportAvailable,
} = exporter
const exportSize = ref('250'),
  heading = ref<HTMLElement>(),
  advanced = ref(false)
let choicesGeneration = 0
const columns: Column<AuditLogEntry>[] = [
  { key: 'activity', label: 'Activity' },
  { key: 'user_name', label: 'Actor' },
  { key: 'context', label: 'Outcome and context' },
  { key: 'created_at', label: 'Recorded at' },
]
const filterChoices = computed(() => [
  {
    key: 'resource_type',
    label: 'Resource',
    values: (choices.value?.resources ?? []).map((value) => ({
      value,
      label: formatLabel(value),
    })),
  },
  {
    key: 'action',
    label: 'Action',
    values: ['created', 'updated', 'deleted', 'event'].map((value) => ({
      value,
      label: formatLabel(value),
    })),
  },
  {
    key: 'outcome',
    label: 'Outcome',
    values: ['success', 'denied', 'failed', 'unavailable'].map((value) => ({
      value,
      label: value === 'unavailable' ? 'Not recorded' : formatLabel(value),
    })),
  },
  {
    key: 'user_id',
    label: 'Actor',
    values: (choices.value?.actors ?? []).map((actor) => ({
      value: actor.id,
      label: actor.name,
    })),
  },
])
const identifiers = [
  { key: 'request_id', label: 'Request ID' },
  { key: 'account_id', label: 'Account ID' },
  { key: 'case_id', label: 'Case ID' },
  { key: 'resource_id', label: 'Resource ID' },
]
const dirty = computed(
  () => JSON.stringify(cleanDraft()) !== JSON.stringify(applied.value),
)
function cleanDraft(): AuditFilters {
  return {
    limit: 25,
    ...Object.fromEntries(
      Object.entries(draft).filter(([, value]) => value && value !== 'all'),
    ),
  }
}
function clearExport() {
  exporter.reset()
}
async function loadChoices() {
  const request = ++choicesGeneration,
    context = viewer.value
  choicesBusy.value = true
  choicesError.value = ''
  try {
    const response = await auditLogsService.filters()
    if (request === choicesGeneration && context === viewer.value)
      choices.value = response.data.data
  } catch (error) {
    const message = await auditFailure(error)
    if (request === choicesGeneration && context === viewer.value)
      choicesError.value = message
  } finally {
    if (request === choicesGeneration && context === viewer.value)
      choicesBusy.value = false
  }
}
async function apply(reset = false) {
  if (reset) for (const key of Object.keys(draft)) delete draft[key]
  applied.value = cleanDraft()
  page.reset()
  clearExport()
  await page.load(applied.value, 0)
}
async function move(target: number) {
  await page.load(applied.value, target)
  await nextTick()
  heading.value?.focus()
}
function download(continuation = false) {
  return continuation
    ? exporter.next(applied.value, Number(exportSize.value))
    : exporter.start(applied.value, Number(exportSize.value))
}
async function retryResults() {
  await page.retry(applied.value)
  await nextTick()
  heading.value?.focus()
}
watch(
  viewer,
  () => {
    choicesGeneration++
    choices.value = undefined
    choicesError.value = ''
    void apply(true)
    void loadChoices()
  },
  { immediate: true, flush: 'sync' },
)
onUnmounted(() => {
  choicesGeneration++
  clearExport()
})
</script>

<template>
  <div class="inbox-ui inbox-page">
    <PageHeader
      title="Audit logs"
      description="Investigate recorded changes and security events."
      :icon="ScrollText"
    />
    <div class="inbox-page-scroll">
      <div class="inbox-container space-y-6">
        <form
          class="inbox-panel p-4 sm:p-5 space-y-4"
          aria-label="Audit filters"
          @submit.prevent="apply()"
        >
          <div class="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-3">
            <div
              v-for="filter in filterChoices"
              :key="filter.key"
              class="space-y-1.5"
            >
              <label
                :id="`audit-label-${filter.key}`"
                class="text-sm font-medium"
                >{{ filter.label }}</label
              >
              <Select
                :model-value="draft[filter.key] || 'all'"
                @update:model-value="draft[filter.key] = String($event)"
              >
                <SelectTrigger
                  :aria-labelledby="`audit-label-${filter.key}`"
                  class="w-full min-h-11"
                  ><SelectValue
                /></SelectTrigger>
                <SelectContent
                  ><SelectItem value="all"
                    >All
                    {{
                      filter.label === 'Actor'
                        ? 'actors'
                        : filter.label.toLowerCase() + 's'
                    }}</SelectItem
                  >
                  <SelectItem
                    v-for="option in filter.values"
                    :key="option.value"
                    :value="option.value"
                    >{{ option.label }}</SelectItem
                  >
                </SelectContent>
              </Select>
            </div>
            <label class="space-y-1.5 text-sm font-medium"
              >From (UTC)<Input
                v-model="draft.from"
                type="date"
                :max="draft.to || undefined"
                class="inbox-input min-h-11"
            /></label>
            <label class="space-y-1.5 text-sm font-medium"
              >To (UTC, inclusive)<Input
                v-model="draft.to"
                type="date"
                :min="draft.from || undefined"
                class="inbox-input min-h-11"
            /></label>
          </div>
          <details
            class="group"
            :open="advanced"
            @toggle="advanced = ($event.target as HTMLDetailsElement).open"
          >
            <summary
              class="min-h-11 cursor-pointer flex items-center gap-2 font-medium text-sm"
            >
              <ChevronDown
                aria-hidden="true"
                class="h-4 w-4 group-open:rotate-180"
              />
              <span>More filters · event and identifiers</span>
            </summary>
            <div
              class="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-3 gap-3 pt-2"
            >
              <label class="space-y-1.5 text-sm font-medium"
                >Category
                <Select
                  :model-value="draft.category || 'all'"
                  @update:model-value="draft.category = String($event)"
                  ><SelectTrigger class="w-full min-h-11" aria-label="Category"
                    ><SelectValue /></SelectTrigger
                  ><SelectContent
                    ><SelectItem value="all">All categories</SelectItem
                    ><SelectItem value="security">Security</SelectItem
                    ><SelectItem value="configuration"
                      >Configuration</SelectItem
                    ></SelectContent
                  ></Select
                >
              </label>
              <label class="space-y-1.5 text-sm font-medium"
                >Event<Input
                  v-model="draft.event"
                  placeholder="For example, auth.sign_in"
                  maxlength="64"
                  class="inbox-input min-h-11"
                  list="audit-events"
              /></label>
              <datalist id="audit-events">
                <option
                  v-for="event in choices?.events"
                  :key="event"
                  :value="event"
                />
              </datalist>
              <label
                v-for="field in identifiers"
                :key="field.key"
                class="space-y-1.5 text-sm font-medium"
                >{{ field.label
                }}<Input
                  v-model="draft[field.key]"
                  placeholder="Paste the exact UUID"
                  maxlength="36"
                  class="inbox-input min-h-11"
              /></label>
            </div>
          </details>
          <p v-if="choicesBusy" role="status" class="inbox-caption">
            Loading readable filter choices…
          </p>
          <div v-if="choicesError" role="alert" class="space-y-2">
            <p>{{ choicesError }}</p>
            <Button
              type="button"
              variant="outline"
              class="min-h-11"
              @click="loadChoices"
              >Retry filter choices</Button
            >
          </div>
          <p v-if="choices?.actor_choices_limited" class="inbox-caption">
            Showing the first 100 recorded actors. Use an exact actor ID through
            the API for other recorded actors.
          </p>
          <div class="flex items-center gap-3 flex-wrap">
            <Button type="submit" :disabled="busy" class="min-h-11"
              >Apply filters</Button
            ><Button
              type="button"
              variant="ghost"
              :disabled="busy"
              class="min-h-11"
              @click="apply(true)"
              >Clear filters</Button
            ><span v-if="dirty" class="inbox-caption"
              >Filters changed. Apply them to update the results.</span
            >
          </div>
        </form>

        <section
          aria-labelledby="audit-results"
          :aria-busy="busy"
          class="space-y-3"
        >
          <div class="inbox-toolbar">
            <div>
              <h2
                id="audit-results"
                ref="heading"
                tabindex="-1"
                class="font-semibold"
              >
                Recorded activity
              </h2>
              <p class="inbox-caption">
                Newest first · <span v-if="busy">Loading record count…</span
                ><span v-else-if="!ready">Record count unavailable</span
                ><span v-else
                  >{{ total }} readable record{{ total === 1 ? '' : 's' }}</span
                ><span v-if="ready && asOf">
                  · Snapshot {{ formatDateTime(asOf) }}</span
                >
              </p>
            </div>
            <Button
              variant="outline"
              :disabled="busy || dirty"
              class="min-h-11"
              @click="apply()"
              ><RefreshCw class="mr-2 h-4 w-4" />Refresh</Button
            >
          </div>
          <p class="inbox-caption">
            Only records you can currently read are shown. Older records may not
            include identity, request or outcome context.
          </p>
          <div v-if="failure" role="alert" class="inbox-panel p-5 space-y-3">
            <p>{{ failure }}</p>
            <Button variant="outline" class="min-h-11" @click="retryResults"
              >Retry page {{ attemptedTarget + 1 }}</Button
            >
          </div>
          <template v-else>
            <div class="inbox-panel hidden md:block">
              <DataTable
                :items="rows"
                :columns="columns"
                :is-loading="busy"
                empty-title="No readable records match these filters"
                empty-description="Try a wider date range or clear the filters. Missing events may not have been recorded."
              >
                <template #cell-activity="{ item: log }"
                  ><RouterLink
                    :to="`/settings/audit-logs/${log.id}`"
                    class="font-medium underline underline-offset-4 break-words"
                    >{{
                      log.event
                        ? formatLabel(log.event)
                        : `${formatLabel(log.resource_type)} ${log.action}`
                    }}</RouterLink
                  >
                  <p class="inbox-caption mt-1">
                    {{ formatLabel(log.resource_type) }} ·
                    {{ formatLabel(log.action) }}
                  </p></template
                >
                <template #cell-user_name="{ item: log }"
                  ><span>{{ log.user_name }}</span>
                  <p v-if="log.actor_masked" class="inbox-caption">
                    Identity restricted
                  </p></template
                >
                <template #cell-context="{ item: log }"
                  ><span>{{
                    log.context_available
                      ? formatLabel(log.outcome || 'Not recorded')
                      : 'Not recorded'
                  }}</span>
                  <p class="inbox-caption mt-1">
                    {{ auditContextLabel(log) }}
                  </p></template
                >
                <template #cell-created_at="{ item: log }"
                  ><time :datetime="log.created_at" class="whitespace-nowrap">{{
                    formatDateTime(log.created_at)
                  }}</time></template
                >
              </DataTable>
            </div>
            <div class="inbox-panel md:hidden">
              <div v-if="busy" role="status" class="p-5">
                Loading audit records…
              </div>
              <ul v-else-if="rows.length" class="inbox-list">
                <li v-for="log in rows" :key="log.id" class="p-4 space-y-2">
                  <RouterLink
                    :to="`/settings/audit-logs/${log.id}`"
                    class="flex min-h-[44px] items-center font-medium underline underline-offset-4 break-words"
                    >{{
                      log.event
                        ? formatLabel(log.event)
                        : `${formatLabel(log.resource_type)} ${log.action}`
                    }}</RouterLink
                  >
                  <p>
                    {{ log.user_name }} ·
                    {{
                      log.context_available
                        ? formatLabel(log.outcome || 'Not recorded')
                        : 'Outcome not recorded'
                    }}
                  </p>
                  <p class="inbox-caption">{{ auditContextLabel(log) }}</p>
                  <time :datetime="log.created_at" class="inbox-caption">{{
                    formatDateTime(log.created_at)
                  }}</time>
                </li>
              </ul>
              <div v-else class="p-5 space-y-2">
                <h3 class="font-medium">
                  No readable records match these filters
                </h3>
                <p class="inbox-caption">
                  Try a wider date range or clear the filters. Missing events
                  may not have been recorded.
                </p>
              </div>
            </div>
            <nav
              aria-label="Audit pages"
              class="flex items-center gap-3 flex-wrap"
            >
              <Button
                variant="outline"
                :disabled="busy || position === 0 || dirty"
                class="min-h-11"
                @click="move(position - 1)"
                >Previous</Button
              ><span role="status">Page {{ position + 1 }}</span
              ><Button
                variant="outline"
                :disabled="busy || !hasMore || dirty"
                class="min-h-11"
                @click="move(position + 1)"
                >Next</Button
              >
            </nav>
          </template>
        </section>

        <section
          v-if="canExport"
          aria-labelledby="audit-export"
          class="inbox-panel p-4 sm:p-5 space-y-3"
        >
          <h2 id="audit-export" class="font-semibold">
            Export readable records
          </h2>
          <p class="inbox-caption max-w-prose">
            CSV files may contain personal data. Store and share them securely.
            Exports use the applied filters and record server-side generation.
            Each part contains up to the selected number of rows; further parts
            use the same snapshot.
          </p>
          <div class="flex items-end gap-3 flex-wrap">
            <label class="space-y-1.5 text-sm font-medium"
              >Rows per CSV part<Select
                v-model="exportSize"
                :disabled="exporting || !!exportCursor"
                ><SelectTrigger
                  class="w-36 min-h-11"
                  aria-label="Rows per CSV part"
                  ><SelectValue /></SelectTrigger
                ><SelectContent
                  ><SelectItem
                    v-for="size in ['100', '250', '1000']"
                    :key="size"
                    :value="size"
                    >{{ size }}</SelectItem
                  ></SelectContent
                ></Select
              ></label
            ><Button
              :disabled="exporting || busy || dirty"
              class="min-h-11"
              @click="download(false)"
              ><Download class="h-4 w-4 mr-2" />{{
                exporting
                  ? 'Preparing CSV…'
                  : exportParts || exportCursor || retryExportAvailable
                    ? 'Start new CSV export'
                    : 'Export CSV'
              }}</Button
            ><Button
              v-if="exportCursor"
              variant="outline"
              :disabled="exporting || busy || dirty"
              class="min-h-11"
              @click="download(true)"
              >Download next CSV part</Button
            >
          </div>
          <p v-if="exporting" role="status">Preparing readable records…</p>
          <p v-if="exportNotice" role="status">{{ exportNotice }}</p>
          <div v-if="exportError" role="alert" class="space-y-2">
            <p>{{ exportError }}</p>
            <Button
              v-if="retryExportAvailable"
              variant="outline"
              :disabled="exporting || busy || dirty"
              class="min-h-11"
              @click="exporter.retry()"
              >Retry CSV part {{ exportParts + 1 }}</Button
            >
          </div>
        </section>
      </div>
    </div>
  </div>
</template>

<style scoped>
:deep(.inbox-input::placeholder) {
  color: var(--inbox-muted);
  opacity: 1;
}
</style>
