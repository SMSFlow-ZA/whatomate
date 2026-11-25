import { computed, onUnmounted, ref, shallowRef } from 'vue'
import {
  auditLogsService,
  type AuditFilters,
  type AuditLogEntry,
} from '@/services/api'
import { auditFailure } from '@/lib/auditInvestigation'

export function useAuditPage(context: () => string) {
  const rows = shallowRef<AuditLogEntry[]>([]),
    busy = ref(false),
    failure = ref('')
  const total = ref(0),
    asOf = ref(''),
    canExport = ref(false),
    position = ref(0),
    attemptedTarget = ref(0),
    ready = ref(false)
  const next = ref(''),
    cursors = new Map<number, string>([[0, '']])
  let generation = 0
  function reset() {
    generation++
    rows.value = []
    failure.value = ''
    total.value = 0
    asOf.value = ''
    canExport.value = false
    busy.value = false
    position.value = 0
    attemptedTarget.value = 0
    ready.value = false
    next.value = ''
    cursors.clear()
    cursors.set(0, '')
  }
  async function load(filters: AuditFilters, target = position.value) {
    const request = ++generation,
      scope = context()
    busy.value = true
    attemptedTarget.value = target
    ready.value = false
    failure.value = ''
    // A failed refresh must not leave stale sensitive rows or export controls.
    rows.value = []
    canExport.value = false
    try {
      const cursor =
        target === position.value + 1 ? next.value : cursors.get(target)
      if (cursor === undefined || (target > 0 && !cursor))
        throw new Error('Missing cursor')
      const response = await auditLogsService.list({
        ...filters,
        cursor: cursor || undefined,
      })
      if (request !== generation || scope !== context()) return
      const page = response.data.data
      if (
        !Array.isArray(page.audit_logs) ||
        !Number.isFinite(page.total) ||
        !page.first_cursor
      )
        throw new Error('Invalid audit response')
      rows.value = page.audit_logs
      total.value = page.total
      asOf.value = page.as_of
      canExport.value = page.capabilities?.export === true
      position.value = target
      cursors.set(target, cursor ?? '')
      cursors.set(0, page.first_cursor)
      next.value = page.has_more ? (page.next_cursor ?? '') : ''
      ready.value = true
    } catch (error) {
      const message = await auditFailure(error)
      if (request !== generation || scope !== context()) return
      failure.value = message
    } finally {
      if (request === generation && scope === context()) busy.value = false
    }
  }
  onUnmounted(reset)
  return {
    rows,
    busy,
    failure,
    total,
    asOf,
    canExport,
    position,
    attemptedTarget,
    ready,
    retry: (filters: AuditFilters) => load(filters, attemptedTarget.value),
    hasMore: computed(() => !!next.value),
    reset,
    load,
  }
}
