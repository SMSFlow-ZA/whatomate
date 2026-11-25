import { onUnmounted, ref } from 'vue'
import { auditLogsService, type AuditFilters } from '@/services/api'
import { auditFailure } from '@/lib/auditInvestigation'

function saveCSV(blob: Blob, part: number) {
  const url = URL.createObjectURL(blob),
    link = document.createElement('a')
  link.href = url
  link.download = `whatomate-audit-${new Date().toISOString().slice(0, 10)}-part-${part}.csv`
  document.body.appendChild(link)
  link.click()
  link.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

export function useAuditExport(
  context: () => string,
  permitted: () => boolean,
  save = saveCSV,
) {
  const exporting = ref(false),
    error = ref(''),
    notice = ref(''),
    cursor = ref(''),
    rows = ref(0),
    parts = ref(0),
    retryAvailable = ref(false)
  let generation = 0,
    failed: AuditFilters | undefined
  function reset() {
    generation++
    exporting.value = false
    error.value = notice.value = cursor.value = ''
    rows.value = parts.value = 0
    retryAvailable.value = false
    failed = undefined
  }
  async function run(params: AuditFilters) {
    if (exporting.value || !permitted()) return
    const request = ++generation,
      scope = context()
    exporting.value = true
    error.value = ''
    retryAvailable.value = false
    try {
      const response = await auditLogsService.export(params)
      if (request !== generation || scope !== context()) return
      const count = Number(response.headers['x-audit-export-rows']),
        more = response.headers['x-audit-has-more'] === 'true'
      const next = more
        ? String(response.headers['x-audit-next-cursor'] ?? '')
        : ''
      if (
        !(response.data instanceof Blob) ||
        response.data.size > 4 << 20 ||
        !String(response.headers['content-type']).startsWith('text/csv') ||
        !Number.isInteger(count) ||
        count < 0 ||
        count > (params.limit ?? 1000) ||
        (more && !next)
      )
        throw new Error('Invalid export response')
      save(response.data, parts.value + 1)
      parts.value++
      rows.value += count
      cursor.value = next
      failed = undefined
      notice.value = `${rows.value} readable record${rows.value === 1 ? '' : 's'} prepared.${next ? ' More records remain; download the next CSV part.' : ' This export is complete for the selected filters.'}`
    } catch (problem) {
      const message = await auditFailure(problem)
      if (request !== generation || scope !== context()) return
      failed = { ...params }
      retryAvailable.value = true
      error.value = message
    } finally {
      if (request === generation && scope === context()) exporting.value = false
    }
  }
  async function start(filters: AuditFilters, limit: number) {
    if (exporting.value || !permitted()) return
    reset()
    await run({ ...filters, cursor: undefined, limit })
  }
  async function next(filters: AuditFilters, limit: number) {
    if (!cursor.value) return
    await run({ ...filters, cursor: cursor.value, limit })
  }
  async function retry() {
    if (failed) await run({ ...failed })
  }
  onUnmounted(reset)
  return {
    exporting,
    error,
    notice,
    cursor,
    rows,
    parts,
    retryAvailable,
    reset,
    start,
    next,
    retry,
  }
}
