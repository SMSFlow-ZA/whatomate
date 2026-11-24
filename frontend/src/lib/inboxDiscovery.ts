import {
  parseAbsolute,
  parseDate,
  toCalendarDate,
} from '@internationalized/date'
import type { InboxQueueDiscovery, InboxQueueOrder } from '@/services/inbox'

export const inboxQueueOrders: InboxQueueOrder[] = [
  'priority_waiting',
  'latest_activity',
  'longest_unanswered',
  'newest',
  'oldest',
  'nearest_deadline',
]
export const inboxDateKeys = [
  'opened_from',
  'opened_before',
  'activity_from',
  'activity_before',
] as const
export function inboxTimezoneValid(zone: string) {
  if (
    !zone ||
    zone === 'Local' ||
    zone.length > 128 ||
    !/^(UTC|GMT|[A-Za-z_]+(?:\/[A-Za-z0-9_+.-]+)+)$/.test(zone)
  )
    return false
  try {
    new Intl.DateTimeFormat('en', { timeZone: zone }).format()
    return true
  } catch {
    return false
  }
}
export function readInboxDiscovery(value: unknown): InboxQueueDiscovery | null {
  if (value === undefined) return { order: 'priority_waiting' }
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null
  const raw = value as Record<string, unknown>
  if (
    Object.keys(raw).some(
      (key) => !['order', 'timezone', ...inboxDateKeys].includes(key),
    )
  )
    return null
  if (raw.order !== undefined && typeof raw.order !== 'string') return null
  const order = raw.order || 'priority_waiting'
  if (!inboxQueueOrders.includes(order as InboxQueueOrder)) return null
  const result: InboxQueueDiscovery = { order: order as InboxQueueOrder }
  if (raw.timezone !== undefined) {
    if (typeof raw.timezone !== 'string' || !inboxTimezoneValid(raw.timezone))
      return null
    result.timezone = new Intl.DateTimeFormat('en', {
      timeZone: raw.timezone,
    }).resolvedOptions().timeZone
  }
  for (const key of inboxDateKeys) {
    const at = raw[key]
    if (at === undefined) continue
    if (
      typeof at !== 'string' ||
      !/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,6})?(?:Z|[+-]\d\d:\d\d)$/.test(
        at,
      )
    )
      return null
    try {
      const instant = parseAbsolute(at, 'UTC')
      const year = new Date(at).getUTCFullYear()
      if (
        instant.year < 1 ||
        instant.year > 9999 ||
        year < 1 ||
        year > 9999 ||
        at.startsWith('0000-')
      )
        return null
    } catch {
      return null
    }
    result[key] = at
  }
  if (inboxDateKeys.some((key) => result[key]) && !result.timezone) return null
  for (const [from, before] of [
    ['opened_from', 'opened_before'],
    ['activity_from', 'activity_before'],
  ] as const) {
    if (
      result[from] &&
      result[before] &&
      inboxInstantMicroseconds(result[from]!) >=
        inboxInstantMicroseconds(result[before]!)
    )
      return null
  }
  return result
}

function inboxInstantMicroseconds(value: string) {
  const fraction = value.match(/\.(\d+)/)?.[1] ?? ''
  return (
    BigInt(Date.parse(value)) * 1000n +
    BigInt((fraction + '000000').slice(3, 6))
  )
}

export interface InboxDateDraft {
  timezone: string
  openedFrom: string
  openedThrough: string
  activityFrom: string
  activityThrough: string
}
export const emptyInboxDates = (timezone = 'UTC'): InboxDateDraft => ({
  timezone,
  openedFrom: '',
  openedThrough: '',
  activityFrom: '',
  activityThrough: '',
})

function localStart(day: string, zone: string) {
  const date = parseDate(day)
  if (date.year < 1 || date.year > 9999) throw new Error('dates')
  const instant = date.toDate(zone)
  if (
    toCalendarDate(parseAbsolute(instant.toISOString(), zone)).toString() !==
    day
  )
    throw new Error('skippedDay')
  return instant.toISOString()
}
export function applyInboxDates(
  draft: InboxDateDraft,
  current: InboxQueueDiscovery,
): InboxQueueDiscovery {
  if (!inboxTimezoneValid(draft.timezone)) throw new Error('timezone')
  const timezone = new Intl.DateTimeFormat('en', {
    timeZone: draft.timezone,
  }).resolvedOptions().timeZone
  const result: InboxQueueDiscovery = {
    order: current.order || 'priority_waiting',
    timezone,
  }
  for (const [from, through, keyFrom, keyBefore] of [
    ['openedFrom', 'openedThrough', 'opened_from', 'opened_before'],
    ['activityFrom', 'activityThrough', 'activity_from', 'activity_before'],
  ] as const) {
    if (draft[from]) result[keyFrom] = localStart(draft[from], draft.timezone)
    if (draft[through]) {
      localStart(draft[through], draft.timezone) // Reject a skipped authored date too.
      const next = parseDate(draft[through]).add({ days: 1 }).toString()
      if (next <= draft[through]) throw new Error('dates')
      result[keyBefore] = localStart(next, draft.timezone)
    }
    if (draft[from] && draft[through] && draft[from] > draft[through])
      throw new Error('range')
  }
  if (!readInboxDiscovery(result)) throw new Error('dates')
  return result
}

// A saved API view may use exact instants. Never silently widen them to days.
export function inboxDateDraft(value: InboxQueueDiscovery): {
  draft: InboxDateDraft
  exact: boolean
} {
  const timezone =
    value.timezone || Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  const draft = emptyInboxDates(timezone)
  let exact = false
  for (const [key, field, end] of [
    ['opened_from', 'openedFrom', false],
    ['opened_before', 'openedThrough', true],
    ['activity_from', 'activityFrom', false],
    ['activity_before', 'activityThrough', true],
  ] as const) {
    if (!value[key]) continue
    try {
      const date = toCalendarDate(parseAbsolute(value[key]!, timezone))
      if (
        localStart(date.toString(), timezone) !==
          new Date(value[key]!).toISOString() ||
        /\.\d{4,}/.test(value[key]!)
      )
        exact = true
      draft[field] = (end ? date.subtract({ days: 1 }) : date).toString()
    } catch {
      exact = true
    }
  }
  return { draft, exact }
}
