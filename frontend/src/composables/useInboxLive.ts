import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import {
  inboxLive,
  inboxWatchKey,
  type InboxLiveHint,
  type InboxLiveState,
  type InboxWatchScope,
} from '@/services/inboxLive'

interface LiveOptions {
  context?: () => string
  active?: () => boolean
  blocked?: () => boolean
  refresh: (hint: InboxLiveHint) => void | Promise<unknown>
  denied: () => void
  clear?: () => void
}

// Only mounted, visible contexts subscribe. Hints are held while the caller has
// drafts/reviews/selection or a request in flight; loss of access clears the
// caller's private state immediately instead of waiting for that edit to finish.
export function useInboxLive(
  scope: () => InboxWatchScope | null,
  options: LiveOptions,
) {
  const state = ref<InboxLiveState>('polling')
  const pending = ref(false)
  const visible = ref(document.visibilityState === 'visible')
  let stop = () => {}
  let generation = 0
  let bindingGeneration = 0
  let running = false
  let reset = false
  let disposed = false
  const active = computed(() => visible.value && (options.active?.() ?? true))
  const blocked = computed(() => options.blocked?.() ?? false)

  async function flush() {
    if (disposed || !pending.value || !active.value || blocked.value || running)
      return
    const request = generation
    pending.value = false
    running = true
    const hint = { reset, denied: false }
    reset = false
    let succeeded = false
    try {
      const applied = await options.refresh(hint)
      if (applied === false) {
        if (request === generation) pending.value = true
        return
      }
      succeeded = true
    } catch {
      if (request === generation) pending.value = true
    } finally {
      running = false
      // Drain a hint received during a successful request or an old context's
      // lookup. Failed reads stay pending without an automatic failure loop.
      if (pending.value && (succeeded || request !== generation)) void flush()
    }
  }

  function requestRefresh() {
    if (disposed) return
    pending.value = reset = true
    void flush()
  }

  watch(
    () => [
      active.value,
      options.context?.() ?? '',
      scope() ? inboxWatchKey(scope()!) : '',
    ],
    () => {
      generation++
      const binding = ++bindingGeneration
      stop()
      state.value = 'polling'
      pending.value = reset = false
      const selected = scope()
      const context = options.context?.() ?? ''
      const key = selected ? inboxWatchKey(selected) : ''
      const current = () =>
        !disposed &&
        active.value &&
        binding === bindingGeneration &&
        context === (options.context?.() ?? '') &&
        key === (scope() ? inboxWatchKey(scope()!) : '')
      stop = () => {}
      if (!active.value || !selected) return
      stop = inboxLive.watch(
        selected,
        (hint) => {
          if (!current()) return
          if (hint.denied) {
            generation++
            pending.value = reset = false
            options.denied()
          } else {
            if (hint.clear) {
              generation++
              options.clear?.()
            }
            pending.value = true
            reset ||= hint.reset
            void flush()
          }
        },
        (value) => {
          if (!current()) return
          state.value = value
        },
      )
    },
    { immediate: true },
  )

  watch(blocked, (value) => {
    if (!value) void flush()
  })
  function visibility() {
    visible.value = document.visibilityState === 'visible'
  }
  onMounted(() => document.addEventListener('visibilitychange', visibility))
  onUnmounted(() => {
    disposed = true
    bindingGeneration++
    generation++
    pending.value = reset = false
    stop()
    document.removeEventListener('visibilitychange', visibility)
  })
  return { state, pending, flush, requestRefresh }
}
