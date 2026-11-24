import type { InboxSLAPolicy } from '@/services/inbox'
import { cloneInboxPolicy } from './inboxInteraction'

export function defaultInboxTargets(account: string): InboxSLAPolicy {
  return {
    account_id: account,
    first_reply_minutes: 30,
    next_reply_minutes: 60,
    resolution_minutes: 480,
    business_hours: false,
    timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC',
    shifts: [],
    date_exceptions: [],
  }
}
export function copyInboxTargets(policy: InboxSLAPolicy): InboxSLAPolicy {
  const copy = cloneInboxPolicy(policy)
  copy.shifts ??= []
  copy.date_exceptions = (copy.date_exceptions ?? []).map((item) => ({
    ...item,
    intervals: item.intervals ?? [],
  }))
  copy.timezone ||= 'UTC'
  return copy
}
export interface CalendarIssue {
  code: string
  exception?: number
}
const validInterval = (from: number, to: number) =>
  Number.isInteger(from) &&
  Number.isInteger(to) &&
  from >= 0 &&
  to <= 1440 &&
  from < to
function overlaps(intervals: { from: number; to: number }[]) {
  const sorted = [...intervals].sort((a, b) => a.from - b.from)
  return sorted.some((item, i) => i > 0 && item.from < sorted[i - 1]!.to)
}
export function inboxCalendarIssues(policy: InboxSLAPolicy): CalendarIssue[] {
  const issues: CalendarIssue[] = []
  if (
    [
      policy.first_reply_minutes,
      policy.next_reply_minutes,
      policy.resolution_minutes,
    ].some((value) => !Number.isInteger(value) || value < 1 || value > 43200)
  )
    issues.push({ code: 'targets' })
  const exceptions = policy.date_exceptions ?? []
  if (!policy.business_hours) {
    if (exceptions.length) issues.push({ code: 'elapsedExceptions' })
    return issues
  }
  try {
    if (!policy.timezone || policy.timezone === 'Local')
      throw new Error('timezone')
    new Intl.DateTimeFormat('en', { timeZone: policy.timezone }).format()
  } catch {
    issues.push({ code: 'timezone' })
  }
  const shifts = policy.shifts ?? []
  if (
    !shifts.length ||
    shifts.length > 28 ||
    shifts.some(
      (shift) =>
        !Number.isInteger(shift.Day) ||
        shift.Day < 0 ||
        shift.Day > 6 ||
        !validInterval(shift.StartMinute, shift.EndMinute),
    )
  )
    issues.push({ code: 'weekly' })
  if (
    Array.from({ length: 7 }, (_, day) =>
      shifts.filter((shift) => shift.Day === day),
    ).some((day) =>
      overlaps(
        day.map((shift) => ({ from: shift.StartMinute, to: shift.EndMinute })),
      ),
    )
  )
    issues.push({ code: 'weeklyOverlap' })
  if (exceptions.length > 366) issues.push({ code: 'dateLimit' })
  const dates = new Set<string>()
  exceptions.forEach((item, index) => {
    const date = new Date(item.date + 'T00:00:00Z')
    if (
      !/^\d{4}-\d{2}-\d{2}$/.test(item.date) ||
      !Number.isFinite(date.valueOf()) ||
      date.getUTCFullYear() < 1 ||
      date.toISOString().slice(0, 10) !== item.date
    )
      issues.push({ code: 'date', exception: index })
    if (dates.has(item.date))
      issues.push({ code: 'duplicateDate', exception: index })
    dates.add(item.date)
    const intervals = item.intervals ?? []
    if (
      intervals.length > 28 ||
      intervals.some(
        (interval) =>
          !validInterval(interval.start_minute, interval.end_minute),
      )
    )
      issues.push({ code: 'intervals', exception: index })
    if (
      overlaps(
        intervals.map((interval) => ({
          from: interval.start_minute,
          to: interval.end_minute,
        })),
      )
    )
      issues.push({ code: 'overlap', exception: index })
  })
  return issues
}
export const inboxMinuteTime = (value: number) =>
  [Math.floor(value / 60), value % 60]
    .map((n) => String(n).padStart(2, '0'))
    .join(':')
export function inboxTimeMinute(event: Event) {
  const [hours, minutes] = (event.target as HTMLInputElement).value
    .split(':')
    .map(Number)
  return hours! * 60 + minutes!
}
