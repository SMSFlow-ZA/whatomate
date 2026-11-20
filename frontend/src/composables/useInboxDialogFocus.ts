import { watch, type Ref } from 'vue'

// These dialogs can be opened by a shortcut or an external case action, so
// there may be no DialogTrigger for the primitive to restore focus to.
export function useInboxDialogFocus(open: Ref<boolean>) {
  let opener: HTMLElement | null = null
  watch(
    open,
    (value, previous) => {
      if (value && !previous)
        opener =
          document.activeElement instanceof HTMLElement
            ? document.activeElement
            : null
    },
    { flush: 'sync' },
  )
  return (event: Event) => {
    if (opener?.isConnected) {
      event.preventDefault()
      opener.focus({ preventScroll: true })
    }
  }
}
