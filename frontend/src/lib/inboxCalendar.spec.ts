import { describe, expect, it } from 'vitest'
import {
  copyInboxTargets,
  defaultInboxTargets,
  inboxCalendarIssues,
  inboxMinuteTime,
  inboxTimeMinute,
} from './inboxCalendar'

const calendar = () => ({
  ...defaultInboxTargets('inbox-a'),
  business_hours: true,
  timezone: 'Africa/Johannesburg',
  shifts: [{ Day: 1, StartMinute: 480, EndMinute: 1020 }],
})
const codes = (policy: ReturnType<typeof defaultInboxTargets>) =>
  inboxCalendarIssues(policy).map((issue) => issue.code)

describe('response calendar authoring boundaries', () => {
  it('copies nested calendars without changing the cached policy and normalizes older nullable dates', () => {
    const saved = {
      ...calendar(),
      date_exceptions: [{ date: '2028-02-29', intervals: null }],
    }
    const draft = copyInboxTargets(saved)
    draft.shifts![0]!.StartMinute = 600
    draft.date_exceptions![0]!.intervals!.push({
      start_minute: 600,
      end_minute: 900,
    })
    expect(saved.shifts[0]!.StartMinute).toBe(480)
    expect(saved.date_exceptions[0]!.intervals).toBeNull()
    expect(codes(draft)).toEqual([])
  })
  it('allows a closed holiday, adjacent hours, leap day and an end at midnight', () => {
    const policy = calendar()
    policy.shifts.push({ Day: 1, StartMinute: 1020, EndMinute: 1440 })
    policy.date_exceptions = [
      { date: '2028-02-29', intervals: [] },
      {
        date: '2026-12-24',
        intervals: [
          { start_minute: 480, end_minute: 720 },
          { start_minute: 720, end_minute: 1440 },
        ],
      },
    ]
    expect(codes(policy)).toEqual([])
    expect(inboxMinuteTime(1440)).toBe('24:00')
    expect(
      inboxTimeMinute({ target: { value: '08:15' } } as unknown as Event),
    ).toBe(495)
  })
  it.each([
    '2026-02-29',
    '2026-11-31',
    '2026-1-02',
    '0000-01-01',
    'not-a-date',
  ])('rejects an invalid date without throwing: %s', (date) => {
    const policy = calendar()
    policy.date_exceptions = [{ date, intervals: [] }]
    expect(codes(policy)).toContain('date')
  })
  it('reports overlapping, invalid, duplicate and excessive exception inputs', () => {
    const policy = calendar()
    policy.shifts.push({ Day: 1, StartMinute: 900, EndMinute: 1100 })
    policy.date_exceptions = [
      {
        date: '2026-12-25',
        intervals: [
          { start_minute: 500, end_minute: 600 },
          { start_minute: 550, end_minute: 700 },
        ],
      },
      {
        date: '2026-12-25',
        intervals: [{ start_minute: -1, end_minute: 600 }],
      },
    ]
    expect(codes(policy)).toEqual(
      expect.arrayContaining([
        'weeklyOverlap',
        'overlap',
        'duplicateDate',
        'intervals',
      ]),
    )
    policy.date_exceptions = Array.from({ length: 367 }, () => ({
      date: '2026-12-25',
      intervals: [],
    }))
    expect(codes(policy)).toContain('dateLimit')
  })
  it('requires explicit removal of dates before using elapsed time and keeps authored dates intact', () => {
    const policy = {
      ...calendar(),
      business_hours: false,
      date_exceptions: [{ date: '2026-12-25', intervals: [] }],
    }
    expect(codes(policy)).toEqual(['elapsedExceptions'])
    expect(policy.date_exceptions).toHaveLength(1)
  })
  it.each([0, 43201, 1.5, NaN])(
    'rejects invalid minute targets: %s',
    (minutes) => {
      expect(codes({ ...calendar(), first_reply_minutes: minutes })).toContain(
        'targets',
      )
    },
  )
  it.each(['Local', 'Unknown/Timezone', ''])(
    'requires a valid explicit business timezone: %s',
    (timezone) => {
      expect(codes({ ...calendar(), timezone })).toContain('timezone')
    },
  )
  it('rejects overnight and fractional shifts instead of silently changing their meaning', () => {
    expect(
      codes({
        ...calendar(),
        shifts: [{ Day: 1, StartMinute: 1320, EndMinute: 120 }],
      }),
    ).toContain('weekly')
    expect(
      codes({
        ...calendar(),
        shifts: [{ Day: 1.5, StartMinute: 480, EndMinute: 1020 }],
      }),
    ).toContain('weekly')
  })
})
