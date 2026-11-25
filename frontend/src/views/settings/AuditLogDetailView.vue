<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { Button } from '@/components/ui/button'
import { PageHeader } from '@/components/shared'
import AuditChanges from '@/components/shared/AuditChanges.vue'
import { auditLogsService, type AuditLogEntry } from '@/services/api'
import { useInboxViewerContext } from '@/composables/useInboxViewerContext'
import {
  auditContextLabel,
  auditFailure,
  auditResourceLink,
} from '@/lib/auditInvestigation'
import { formatDateTime, formatLabel } from '@/lib/utils'
import { ScrollText } from 'lucide-vue-next'
import '@/assets/inbox.css'

const route = useRoute(),
  viewer = useInboxViewerContext()
const log = ref<AuditLogEntry>(),
  busy = ref(false),
  failure = ref(''),
  missing = ref(false)
const scope = computed(() => JSON.stringify([viewer.value, route.params.id]))
const resourceLink = computed(() =>
  log.value ? auditResourceLink(log.value) : undefined,
)
const title = computed(() =>
  log.value
    ? `${formatLabel(log.value.resource_type)} ${log.value.action}`
    : 'Audit record',
)
let generation = 0
async function load() {
  const request = ++generation,
    context = scope.value,
    id = String(route.params.id ?? '')
  log.value = undefined
  busy.value = true
  missing.value = false
  failure.value = ''
  if (
    !/^[a-f\d]{8}-(?:[a-f\d]{4}-){3}[a-f\d]{12}$/i.test(id) ||
    id === '00000000-0000-0000-0000-000000000000'
  ) {
    missing.value = true
    busy.value = false
    return
  }
  try {
    const response = await auditLogsService.get(id)
    if (request !== generation || context !== scope.value) return
    const result = response.data.data
    if (result?.id !== id) throw new Error('Mismatched audit record')
    log.value = result
  } catch (error) {
    const message = await auditFailure(error)
    if (request !== generation || context !== scope.value) return
    if ((error as { response?: { status?: number } })?.response?.status === 404)
      missing.value = true
    else failure.value = message
  } finally {
    if (request === generation && context === scope.value) busy.value = false
  }
}
watch(scope, load, { immediate: true, flush: 'sync' })
onUnmounted(() => generation++)
</script>
<template>
  <div class="inbox-ui inbox-page">
    <PageHeader
      :title="title"
      :icon="ScrollText"
      back-link="/settings/audit-logs"
      :breadcrumbs="[
        { label: 'Settings', href: '/settings' },
        { label: 'Audit logs', href: '/settings/audit-logs' },
        { label: 'Record details' },
      ]"
    />
    <div class="inbox-page-scroll">
      <div class="inbox-container space-y-5" :aria-busy="busy">
        <p v-if="busy" role="status" class="inbox-panel p-5">
          Loading audit record…
        </p>
        <div v-else-if="failure" role="alert" class="inbox-panel p-5 space-y-3">
          <p>{{ failure }}</p>
          <Button variant="outline" class="min-h-11" @click="load"
            >Retry record</Button
          >
        </div>
        <div v-else-if="missing" class="inbox-panel p-5 space-y-2">
          <h2 class="font-semibold">No readable audit record</h2>
          <p>
            This record is unavailable or outside your current access. Return to
            Audit logs to investigate other records.
          </p>
        </div>
        <template v-else-if="log">
          <section
            class="inbox-panel p-5 space-y-3"
            aria-labelledby="audit-context-title"
          >
            <h2 id="audit-context-title" class="font-semibold">
              {{ auditContextLabel(log) }}
            </h2>
            <p v-if="!log.context_available" class="max-w-prose">
              This historical record does not have supported request, origin or
              outcome context. No identity or outcome has been inferred from the
              change history.
            </p>
            <p v-else class="max-w-prose">
              This records what the application observed. A local success does
              not independently prove an external provider completed an
              operation.
            </p>
            <dl
              class="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-3 gap-4 text-sm"
            >
              <div>
                <dt class="text-muted-foreground">Recorded actor</dt>
                <dd class="mt-1 break-words">
                  {{ log.user_name
                  }}<span v-if="log.actor_masked"> · identity restricted</span
                  ><span
                    v-else-if="log.context_available && !log.actor_verified"
                  >
                    · not an authenticated human actor</span
                  >
                </dd>
              </div>
              <div>
                <dt class="text-muted-foreground">Recorded at</dt>
                <dd class="mt-1">
                  {{ formatDateTime(log.created_at) }}
                  <p class="text-muted-foreground">
                    UTC: {{ new Date(log.created_at).toISOString() }}
                  </p>
                </dd>
              </div>
              <div>
                <dt class="text-muted-foreground">Resource and action</dt>
                <dd class="mt-1">
                  {{ formatLabel(log.resource_type) }} ·
                  {{ formatLabel(log.action) }}
                </dd>
              </div>
              <template v-if="log.context_available">
                <div>
                  <dt class="text-muted-foreground">Event and outcome</dt>
                  <dd class="mt-1 break-words">
                    {{ formatLabel(log.event || 'Not recorded') }} ·
                    {{ formatLabel(log.outcome || 'Not recorded') }}
                  </dd>
                </div>
                <div>
                  <dt class="text-muted-foreground">Reason</dt>
                  <dd class="mt-1 break-words">
                    {{ formatLabel(log.reason || 'Not recorded') }}
                  </dd>
                </div>
                <div>
                  <dt class="text-muted-foreground">Origin</dt>
                  <dd class="mt-1 break-all">
                    {{
                      log.origin_masked
                        ? 'Restricted to readers with staff directory access'
                        : log.origin_ip
                          ? `${log.origin_ip} (${log.origin_source || 'source unavailable'})`
                          : 'Not recorded'
                    }}
                  </dd>
                </div>
              </template>
            </dl>
          </section>
          <section
            class="inbox-panel p-5 space-y-4"
            aria-labelledby="audit-changes"
          >
            <h2 id="audit-changes" class="font-semibold">Changes</h2>
            <AuditChanges :log="log" />
          </section>
          <section
            class="inbox-panel p-5 space-y-4"
            aria-labelledby="audit-references"
          >
            <h2 id="audit-references" class="font-semibold">
              Investigation references
            </h2>
            <dl class="grid grid-cols-1 sm:grid-cols-2 gap-4 text-sm">
              <div>
                <dt class="text-muted-foreground">Audit record ID</dt>
                <dd class="font-mono break-all mt-1">{{ log.id }}</dd>
              </div>
              <div>
                <dt class="text-muted-foreground">Resource ID</dt>
                <dd class="font-mono break-all mt-1">{{ log.resource_id }}</dd>
              </div>
              <template v-if="log.context_available"
                ><div
                  v-for="reference in [
                    { label: 'Request ID', value: log.request_id },
                    { label: 'Account ID', value: log.account_id },
                    { label: 'Case ID', value: log.case_id },
                  ]"
                  :key="reference.label"
                >
                  <dt class="text-muted-foreground">{{ reference.label }}</dt>
                  <dd class="font-mono break-all mt-1">
                    {{ reference.value || 'Not recorded' }}
                  </dd>
                </div></template
              >
            </dl>
            <RouterLink
              v-if="resourceLink"
              :to="resourceLink"
              class="inline-flex min-h-[44px] items-center underline underline-offset-4"
              >View resource</RouterLink
            >
            <p v-else class="inbox-caption">
              A current resource link is unavailable. The recorded reference is
              retained for investigation.
            </p>
          </section>
        </template>
      </div>
    </div>
  </div>
</template>
