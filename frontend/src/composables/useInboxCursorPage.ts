import { computed, onUnmounted, ref, shallowRef, watch } from 'vue'
import { isInboxAccessFailure } from '@/lib/inboxAccess'

export interface InboxCursorPage<T> {
  items: T[]
  hasMore: boolean
  nextCursor: string
}

// A page belongs to an exact viewer/context. Failed navigation retains the
// previous page and position; late responses cannot repopulate another scope.
export function useInboxCursorPage<T>(
  context: () => string,
  fetch: (cursor?: string) => Promise<InboxCursorPage<T>>,
) {
  const items = shallowRef<T[]>([])
  const busy = ref(false)
  const failed = ref(false)
  const ready = ref(false)
  const updatedAt = ref('')
  const position = ref(0)
  const cursors = ref([''])
  const nextCursor = ref('')
  let generation = 0
  async function load(cursor: string, target: number) {
    const request = ++generation,
      scope = context()
    busy.value = true
    failed.value = false
    try {
      const page = await fetch(cursor || undefined)
      if (request !== generation || scope !== context()) return
      items.value = page.items
      position.value = target
      cursors.value = [...cursors.value.slice(0, target), cursor]
      nextCursor.value =
        page.hasMore &&
        page.nextCursor &&
        !cursors.value.includes(page.nextCursor)
          ? page.nextCursor
          : ''
      updatedAt.value = new Date().toISOString()
      ready.value = true
    } catch (error) {
      if (request === generation && scope === context()) {
        failed.value = true
        if (isInboxAccessFailure(error)) {
          items.value = []
          ready.value = false
          updatedAt.value = ''
          position.value = 0
          cursors.value = ['']
          nextCursor.value = ''
        }
      }
    } finally {
      if (request === generation) busy.value = false
    }
  }
  const refresh = () =>
    load(cursors.value[position.value] ?? '', position.value)
  const older = () => {
    if (!busy.value && nextCursor.value)
      return load(nextCursor.value, position.value + 1)
  }
  const newer = () => {
    if (!busy.value && position.value)
      return load(cursors.value[position.value - 1]!, position.value - 1)
  }
  watch(
    context,
    () => {
      generation++
      items.value = []
      ready.value = false
      failed.value = false
      busy.value = false
      updatedAt.value = ''
      position.value = 0
      cursors.value = ['']
      nextCursor.value = ''
      void load('', 0)
    },
    { immediate: true, flush: 'sync' },
  )
  onUnmounted(() => {
    generation++
  })
  return {
    items,
    busy,
    failed,
    ready,
    updatedAt,
    position,
    hasOlder: computed(() => !!nextCursor.value),
    refresh,
    older,
    newer,
  }
}
