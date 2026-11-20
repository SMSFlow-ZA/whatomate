// Shared boundaries for inbox shortcuts and editable policy snapshots.
export function inboxTextEntry(event: KeyboardEvent) {
  const target = event.target as HTMLElement | null
  return !!target?.closest(
    'input,textarea,select,[contenteditable="true"],[role="dialog"]',
  )
}

export function cloneInboxPolicy<T extends { shifts?: unknown[] }>(
  policy: T,
): T {
  return JSON.parse(JSON.stringify(policy)) as T
}
