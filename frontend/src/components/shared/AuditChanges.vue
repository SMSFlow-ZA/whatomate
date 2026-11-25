<script setup lang="ts">
import type { AuditLogEntry } from '@/services/api'
import { auditValue } from '@/lib/auditInvestigation'
import { formatLabel } from '@/lib/utils'
defineProps<{ log: AuditLogEntry }>()
</script>
<template>
  <dl v-if="log.changes?.length" class="space-y-4">
    <div
      v-for="(change, index) in log.changes"
      :key="index"
      class="min-w-0 border-b border-border pb-4 last:border-0 last:pb-0"
    >
      <dt class="font-medium break-words">{{ formatLabel(change.field) }}</dt>
      <dd class="grid grid-cols-1 sm:grid-cols-2 gap-3 mt-2 text-sm">
        <div
          v-if="log.action === 'updated' || log.action === 'deleted'"
          class="min-w-0"
        >
          <span class="text-muted-foreground">Previous value</span>
          <p class="whitespace-pre-wrap [overflow-wrap:anywhere] mt-1">
            {{ auditValue(change.old_value) }}
          </p>
        </div>
        <div v-if="log.action !== 'deleted'" class="min-w-0">
          <span class="text-muted-foreground">{{
            log.action === 'event' ? 'Recorded value' : 'New value'
          }}</span>
          <p class="whitespace-pre-wrap [overflow-wrap:anywhere] mt-1">
            {{ auditValue(change.new_value) }}
          </p>
        </div>
      </dd>
    </div>
  </dl>
  <p v-else class="text-sm text-muted-foreground">
    No field changes are available for this record. Security events can record
    an outcome without a configuration change.
  </p>
</template>
