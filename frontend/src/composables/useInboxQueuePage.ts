import { computed, onUnmounted, ref, shallowRef } from 'vue'
import type { QueueCase } from '@/services/inbox'

export interface InboxQueuePage {
  cases: QueueCase[]
  counts?: Record<string, number>
  has_more: boolean
  next_cursor?: string
  queue_revision?: string
  updated_at?: string
}
const shape = (page: InboxQueuePage) =>
  JSON.stringify([
    page.cases.map((row) => [row.id, row.version]),
    page.counts ?? {},
  ])

// Queue paging needs conflict/staged-refresh semantics; receipt-history paging
// intentionally retains its independent existing contract.
export function useInboxQueuePage(
  context: () => string,
  fetch: (cursor?: string, revision?: string) => Promise<InboxQueuePage>,
) {
  const rows = shallowRef<QueueCase[]>([]),
    counts = ref<Record<string, number>>({})
  const loading = ref(false),
    failed = ref(false),
    changed = ref(false),
    invalid = ref(false),
    denied = ref(false)
  const updatedAt = ref(''),
    position = ref(0),
    next = ref('')
  const previous = shallowRef(new Map<number, string>([[0, '']]))
  let generation = 0,
    currentShape = '',
    currentCursor = '',
    currentRevision = ''
  function invalidate() {
    generation++
    loading.value = false
  }
  function reset() {
    invalidate()
    rows.value = []
    counts.value = {}
    updatedAt.value = ''
    position.value = 0
    next.value = ''
    changed.value = invalid.value = denied.value = failed.value = false
    previous.value = new Map([[0, '']])
    currentShape = ''
    currentCursor = ''
    currentRevision = ''
  }
  async function load(cursor: string, target: number, background = false) {
    const request = ++generation,
      scope = context()
    loading.value = true
    failed.value = false
    try {
      const page = await fetch(
        cursor || undefined,
        background || target > 0 || cursor !== '' || target < position.value
          ? currentRevision || undefined
          : undefined,
      )
      if (request !== generation || scope !== context()) return
      const incomingShape = shape(page)
      if (background && currentShape && incomingShape !== currentShape) {
        changed.value = true
        return
      }
      rows.value = page.cases
      counts.value = page.counts ?? {}
      position.value = target
      currentShape = incomingShape
      currentCursor = cursor
      currentRevision = page.queue_revision ?? ''
      const history = new Map(previous.value)
      history.set(target, cursor)
      for (const pageNumber of history.keys())
        if (pageNumber > target || pageNumber < target - 49)
          history.delete(pageNumber)
      previous.value = history
      next.value =
        page.has_more && page.next_cursor && page.next_cursor !== cursor
          ? page.next_cursor
          : ''
      updatedAt.value = new Date().toISOString()
      changed.value = invalid.value = denied.value = false
    } catch (error) {
      if (request !== generation || scope !== context()) return
      const response = (
        error as { response?: { status?: number; data?: { code?: string } } }
      ).response
      if ([401, 403, 404].includes(response?.status ?? 0)) {
        reset()
        denied.value = true
      } else if (
        response?.status === 409 ||
        (response?.status === 400 &&
          response.data?.code === 'inbox_discovery_invalid')
      ) {
        changed.value = true
        invalid.value = response?.status === 400
      } else failed.value = true
    } finally {
      if (request === generation) loading.value = false
    }
  }
  onUnmounted(invalidate)
  return {
    rows,
    counts,
    loading,
    failed,
    changed,
    invalid,
    denied,
    updatedAt,
    position,
    hasMore: computed(() => !!next.value),
    hasPrevious: computed(
      () => position.value > 0 && previous.value.has(position.value - 1),
    ),
    historyTrimmed: computed(
      () => position.value > 0 && !previous.value.has(position.value - 1),
    ),
    reset,
    invalidate,
    refresh: () => {
      currentRevision = ''
      return load('', 0)
    },
    poll: () =>
      !changed.value &&
      !invalid.value &&
      load(currentCursor, position.value, true),
    older: () =>
      !loading.value &&
      !changed.value &&
      !!next.value &&
      load(next.value, position.value + 1),
    newer: () =>
      !loading.value &&
      !changed.value &&
      previous.value.has(position.value - 1) &&
      load(previous.value.get(position.value - 1)!, position.value - 1),
  }
}
