import { onMounted, onUnmounted, ref } from 'vue'
import { onBeforeRouteLeave, onBeforeRouteUpdate } from 'vue-router'

// Reuse the application's leave dialog for both route and local editor changes.
// A cancelled leave keeps the editor and its focus; no page reload is required.
export function useInboxDraftGuard(dirty: () => boolean) {
  const discardOpen = ref(false)
  let pending: (() => void) | undefined,
    finishRoute: ((leave: boolean) => void) | undefined
  function confirmDiscard() {
    discardOpen.value = false
    const action = pending
    pending = undefined
    finishRoute?.(true)
    finishRoute = undefined
    action?.()
  }
  function cancelDiscard() {
    discardOpen.value = false
    pending = undefined
    finishRoute?.(false)
    finishRoute = undefined
  }
  function requestDiscard(action: () => void) {
    if (!dirty()) {
      action()
      return
    }
    pending = action
    discardOpen.value = true
  }
  const guard = () =>
    !dirty() ||
    new Promise<boolean>((resolve) => {
      finishRoute = resolve
      discardOpen.value = true
    })
  onBeforeRouteLeave(guard)
  onBeforeRouteUpdate(guard)
  const beforeUnload = (event: BeforeUnloadEvent) => {
    if (dirty()) {
      event.preventDefault()
      event.returnValue = ''
    }
  }
  onMounted(() => window.addEventListener('beforeunload', beforeUnload))
  onUnmounted(() => {
    window.removeEventListener('beforeunload', beforeUnload)
    finishRoute?.(false)
  })
  return { discardOpen, confirmDiscard, cancelDiscard, requestDiscard }
}
