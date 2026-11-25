<script setup lang="ts">
import { computed, watch } from 'vue'
import { auditContextLabel } from '@/lib/auditInvestigation'
import { formatDateTime, formatLabel } from '@/lib/utils'
import { useAuditPage } from '@/composables/useAuditPage'
import { useInboxViewerContext } from '@/composables/useInboxViewerContext'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'

const props = defineProps<{ resourceType: string; resourceId: string }>()
const viewer = useInboxViewerContext()
const context = computed(() =>
  JSON.stringify([viewer.value, props.resourceType, props.resourceId]),
)
const page = useAuditPage(() => context.value)
const { rows, busy, failure, position, hasMore, attemptedTarget } = page
const filters = () => ({
  resource_type: props.resourceType,
  resource_id: props.resourceId,
  limit: 10,
})
watch(
  context,
  () => {
    page.reset()
    void page.load(filters(), 0)
  },
  { immediate: true, flush: 'sync' },
)
</script>
<template>
  <Card
    ><CardHeader
      ><CardTitle class="text-sm font-medium"
        >Activity log</CardTitle
      ></CardHeader
    ><CardContent :aria-busy="busy" class="space-y-4">
      <p v-if="busy" role="status" class="text-sm text-muted-foreground">
        Loading recorded activity…
      </p>
      <div v-else-if="failure" role="alert" class="space-y-2">
        <p class="text-sm">{{ failure }}</p>
        <Button
          variant="outline"
          class="min-h-11"
          @click="page.retry(filters())"
          >Retry activity page {{ attemptedTarget + 1 }}</Button
        >
      </div>
      <ul v-else-if="rows.length" class="space-y-4">
        <li
          v-for="log in rows"
          :key="log.id"
          class="border-b border-border pb-4 last:border-0 space-y-1 min-w-0"
        >
          <RouterLink
            :to="`/settings/audit-logs/${log.id}`"
            class="flex min-h-[44px] items-center text-sm font-medium underline underline-offset-4 break-words"
            >{{
              log.event ? formatLabel(log.event) : formatLabel(log.action)
            }}
            · {{ log.user_name }}</RouterLink
          >
          <p class="text-xs text-muted-foreground">
            {{ auditContextLabel(log)
            }}<span v-if="log.context_available">
              · {{ formatLabel(log.outcome || 'Not recorded') }}</span
            >
          </p>
          <time
            :datetime="log.created_at"
            class="block text-xs text-muted-foreground"
            >{{ formatDateTime(log.created_at) }}</time
          >
        </li>
      </ul>
      <p v-else class="text-sm text-muted-foreground">
        No readable activity is recorded for this resource.
      </p>
      <nav
        v-if="!failure"
        aria-label="Activity pages"
        class="flex gap-2 items-center flex-wrap"
      >
        <Button
          variant="outline"
          :disabled="busy || position === 0"
          class="min-h-11"
          @click="page.load(filters(), position - 1)"
          >Previous</Button
        ><span class="text-xs">Page {{ position + 1 }}</span
        ><Button
          variant="outline"
          :disabled="busy || !hasMore"
          class="min-h-11"
          @click="page.load(filters(), position + 1)"
          >Next</Button
        >
      </nav>
    </CardContent></Card
  >
</template>
