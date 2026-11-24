import { describe, expect, it } from 'vitest'
import {
  applyInboxDates,
  emptyInboxDates,
  inboxDateDraft,
  inboxQueueOrders,
  readInboxDiscovery,
} from './inboxDiscovery'

describe('queue discovery validation', () => {
  it('retains all six fixed orders and legacy defaults without retaining unknown properties', () => {
    expect(readInboxDiscovery(undefined)).toEqual({ order: 'priority_waiting' })
    expect(readInboxDiscovery({})).toEqual({ order: 'priority_waiting' })
    for (const order of inboxQueueOrders)
      expect(readInboxDiscovery({ order })).toEqual({ order })
    for (const invalid of [
      null,
      [],
      'oldest',
      { order: false },
      { order: 'sql' },
      { hidden: true },
      { timezone: 'Local' },
      { timezone: '+02:00' },
      { opened_from: '2026-01-01T00:00:00Z' },
      { timezone: 'UTC', opened_from: '2026-02-30T00:00:00Z' },
      { timezone: 'UTC', activity_from: '2026-01-01T00:00:00.0000001Z' },
      { timezone: 'UTC', activity_from: '9999-12-31T23:00:00-02:00' },
    ])
      expect(readInboxDiscovery(invalid)).toBeNull()
  })
  it('keeps exact microsecond instants, validates order and copies input', () => {
    const original = {
      order: 'oldest',
      timezone: 'UTC',
      opened_from: '2026-01-01T00:00:00.000001Z',
      opened_before: '2026-01-01T00:00:00.000002Z',
    }
    expect(readInboxDiscovery(original)).toEqual(original)
    expect(
      readInboxDiscovery({ ...original, opened_before: original.opened_from }),
    ).toBeNull()
    expect(
      readInboxDiscovery({
        ...original,
        opened_from: original.opened_before,
        opened_before: original.opened_from,
      }),
    ).toBeNull()
    const read = readInboxDiscovery(original)!
    read.order = 'newest'
    expect(original.order).toBe('oldest')
  })
})

describe('local calendar boundaries', () => {
  it.each([
    ['2026-03-08', 23],
    ['2026-11-01', 25],
  ])('uses actual local day length for %s', (date, hours) => {
    const result = applyInboxDates(
      {
        ...emptyInboxDates('America/New_York'),
        openedFrom: date,
        openedThrough: date,
      },
      { order: 'oldest' },
    )
    expect(
      (Date.parse(result.opened_before!) - Date.parse(result.opened_from!)) /
        3600000,
    ).toBe(hours)
    expect(result.order).toBe('oldest')
    expect(inboxDateDraft(result)).toEqual({
      draft: {
        ...emptyInboxDates('America/New_York'),
        openedFrom: date,
        openedThrough: date,
      },
      exact: false,
    })
  })
  it('accepts open ranges, clears dates and canonicalizes supported timezone aliases', () => {
    const result = applyInboxDates(
      { ...emptyInboxDates('US/Eastern'), activityThrough: '2026-11-01' },
      { order: 'latest_activity' },
    )
    expect(result.activity_from).toBeUndefined()
    expect(result.activity_before).toBe('2026-11-02T05:00:00.000Z')
    expect(result.timezone).toBe('America/New_York')
    expect(applyInboxDates(emptyInboxDates('UTC'), result)).toEqual({
      order: 'latest_activity',
      timezone: 'UTC',
    })
  })
  it('handles midnight gaps and rejects skipped authored dates and excessive years', () => {
    const result = applyInboxDates(
      {
        ...emptyInboxDates('America/Sao_Paulo'),
        openedFrom: '2018-11-04',
        openedThrough: '2018-11-04',
      },
      {},
    )
    expect(result.opened_from).toBe('2018-11-04T03:00:00.000Z')
    expect(inboxDateDraft(result).exact).toBe(false)
    for (const key of ['openedFrom', 'openedThrough'] as const)
      expect(() =>
        applyInboxDates(
          { ...emptyInboxDates('Pacific/Apia'), [key]: '2011-12-30' },
          {},
        ),
      ).toThrow('skippedDay')
    expect(() =>
      applyInboxDates(
        { ...emptyInboxDates(), openedThrough: '9999-12-31' },
        {},
      ),
    ).toThrow()
    expect(() =>
      applyInboxDates({ ...emptyInboxDates(), openedFrom: '0000-01-01' }, {}),
    ).toThrow()
  })
  it('rejects malformed, reversed and invalid-zone drafts', () => {
    expect(() =>
      applyInboxDates(
        { ...emptyInboxDates('Local'), openedFrom: '2026-01-01' },
        {},
      ),
    ).toThrow('timezone')
    expect(() =>
      applyInboxDates(
        {
          ...emptyInboxDates(),
          openedFrom: '2026-01-03',
          openedThrough: '2026-01-01',
        },
        {},
      ),
    ).toThrow('range')
    expect(() =>
      applyInboxDates({ ...emptyInboxDates(), openedFrom: '2026-02-30' }, {}),
    ).toThrow()
  })
  it('identifies exact API boundaries without changing or widening them', () => {
    const original = {
      order: 'oldest' as const,
      timezone: 'UTC',
      opened_from: '2026-01-01T12:34:00.000001Z',
      opened_before: '2026-01-02T00:00:00Z',
    }
    const copy = JSON.stringify(original)
    const draft = inboxDateDraft(original)
    expect(draft.exact).toBe(true)
    expect(draft.draft.openedThrough).toBe('2026-01-01')
    expect(JSON.stringify(original)).toBe(copy)
  })
})
