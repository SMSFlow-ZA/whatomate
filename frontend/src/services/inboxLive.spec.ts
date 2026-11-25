import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  InboxLiveClient,
  inboxWatchKey,
  type InboxWatchScope,
} from './inboxLive'

const account = '10000000-0000-4000-8000-000000000001'
const team = '10000000-0000-4000-8000-000000000002'
const caseID = '10000000-0000-4000-8000-000000000003'
const contact = '10000000-0000-4000-8000-000000000004'
const epoch = '20000000-0000-4000-8000-000000000001'
const otherEpoch = '20000000-0000-4000-8000-000000000002'
const queue: InboxWatchScope = { kind: 'queue', account_id: account }
let client: InboxLiveClient
let send = vi.fn<Parameters<InboxLiveClient['connected']>[0]>()
const hints = () => vi.fn()
const states = () => vi.fn()
const advance = (ms: number) => vi.advanceTimersByTime(ms)
const generation = () => send.mock.lastCall![0].generation as number
function frame(sequence: number, options: Record<string, unknown> = {}) {
  return {
    v: 1,
    generation: generation(),
    epoch,
    sequence,
    available: true,
    state: 'update',
    changes: [
      {
        scope: inboxWatchKey(queue),
        allowed: true,
        reset: false,
        resource: 'a'.repeat(64),
        version: sequence,
      },
    ],
    ...options,
  }
}
beforeEach(() => {
  vi.useFakeTimers()
  client = new InboxLiveClient()
  send = vi.fn<Parameters<InboxLiveClient['connected']>[0]>()
})
afterEach(() => {
  client.disconnected()
  vi.useRealTimers()
})

describe('scoped inbox refresh hints', () => {
  it('fans each accepted revision out to every panel sharing the same scope', () => {
    const first = hints(),
      second = hints()
    client.connected(send)
    client.watch(queue, first, states())
    client.watch(queue, second, states())
    advance(600)
    client.receive(frame(1))
    advance(200)
    first.mockClear()
    second.mockClear()
    client.receive(frame(2))
    advance(200)
    expect(first).toHaveBeenCalledOnce()
    expect(second).toHaveBeenCalledOnce()
    client.receive(
      frame(3, {
        changes: [
          {
            scope: inboxWatchKey(queue),
            allowed: true,
            reset: false,
            resource: 'a'.repeat(64),
            version: 2,
          },
        ],
      }),
    )
    advance(200)
    expect(first).toHaveBeenCalledOnce()
    expect(second).toHaveBeenCalledOnce()
  })
  it('clears revoked private rows immediately without waiting for burst coalescing', () => {
    const hint = hints()
    client.connected(send)
    client.watch(queue, hint, states())
    advance(600)
    client.receive(frame(1))
    hint.mockClear()
    client.receive(
      frame(2, {
        changes: [
          {
            scope: inboxWatchKey(queue),
            allowed: true,
            reset: true,
            clear: true,
          },
        ],
      }),
    )
    expect(hint).toHaveBeenCalledOnce()
    expect(hint).toHaveBeenLastCalledWith({
      reset: true,
      denied: false,
      clear: true,
    })
    advance(200)
    expect(hint).toHaveBeenCalledOnce()
  })
  it('bounds and deduplicates visible contexts within the existing frame limit', () => {
    client.connected(send)
    client.watch(queue, hints(), states())
    client.watch(queue, hints(), states())
    client.watch({ kind: 'case', case_id: caseID }, hints(), states())
    client.watch(
      { kind: 'capacity', account_id: account, team_id: team },
      hints(),
      states(),
    )
    const extra = states()
    client.watch(
      { kind: 'workload', account_id: account, team_id: team },
      hints(),
      extra,
    )
    advance(600)
    const message = send.mock.lastCall![0]
    expect(message.scopes).toHaveLength(3)
    expect(message.scopes.map((scope: InboxWatchScope) => scope.kind)).toEqual([
      'case',
      'queue',
      'capacity',
    ])
    expect(
      JSON.stringify({ type: 'inbox_watch', payload: message }).length,
    ).toBeLessThanOrEqual(512)
    expect(extra).toHaveBeenLastCalledWith('polling')
  })

  it('coalesces bursts and ignores duplicate or older resource revisions', () => {
    const hint = hints(),
      state = states()
    client.connected(send)
    client.watch(queue, hint, state)
    advance(600)
    client.receive(frame(1))
    advance(200)
    expect(hint).toHaveBeenCalledWith({ reset: true, denied: false })
    expect(state).toHaveBeenLastCalledWith('live')
    hint.mockClear()
    client.receive(frame(2))
    client.receive(frame(3))
    client.receive(frame(4))
    advance(200)
    expect(hint).toHaveBeenCalledOnce()
    hint.mockClear()
    client.receive(
      frame(5, {
        changes: [
          {
            scope: inboxWatchKey(queue),
            allowed: true,
            reset: false,
            resource: 'a'.repeat(64),
            version: 4,
          },
        ],
      }),
    )
    client.receive(
      frame(6, {
        changes: [
          {
            scope: inboxWatchKey(queue),
            allowed: true,
            reset: false,
            resource: 'a'.repeat(64),
            version: 3,
          },
        ],
      }),
    )
    advance(200)
    expect(hint).not.toHaveBeenCalled()
    client.receive(frame(6))
    advance(200)
    expect(hint).not.toHaveBeenCalled()
  })

  it('resynchronises after gaps or a restored lower revision in a new epoch', () => {
    const hint = hints()
    client.connected(send)
    client.watch(queue, hint, states())
    advance(600)
    client.receive(frame(1))
    advance(200)
    hint.mockClear()
    client.receive(frame(5))
    advance(200)
    expect(hint).toHaveBeenLastCalledWith({ reset: true, denied: false })
    hint.mockClear()
    client.receive(frame(1, { epoch: otherEpoch }))
    advance(200)
    expect(hint).toHaveBeenLastCalledWith({ reset: true, denied: false })
    hint.mockClear()
    client.receive(frame(2, { epoch: otherEpoch }))
    advance(200)
    expect(hint).toHaveBeenLastCalledWith({ reset: false, denied: false })
  })

  it('fences old context immediately and cancels queued callbacks when unmounted', () => {
    const oldHint = hints(),
      newHint = hints()
    client.connected(send)
    const stop = client.watch(queue, oldHint, states())
    advance(600)
    const old = frame(1)
    client.receive(old)
    stop()
    client.watch(
      { kind: 'conversation', account_id: account, contact_id: contact },
      newHint,
      states(),
    )
    client.receive(old)
    advance(600)
    expect(oldHint).not.toHaveBeenCalled()
    expect(newHint).not.toHaveBeenCalled()
    expect(send.mock.lastCall![0].scopes).toEqual([
      { kind: 'conversation', account_id: account, contact_id: contact },
    ])
  })

  it('keeps denial distinct from transport outages and retains independent polling', () => {
    const hint = hints(),
      state = states()
    client.connected(send)
    client.watch(queue, hint, state)
    advance(600)
    client.receive(
      frame(1, {
        changes: [{ scope: inboxWatchKey(queue), allowed: false, reset: true }],
      }),
    )
    advance(200)
    expect(state).toHaveBeenLastCalledWith('denied')
    expect(hint).toHaveBeenLastCalledWith({ reset: true, denied: true })
    client.disconnected()
    expect(state).toHaveBeenLastCalledWith('polling')
    hint.mockClear()
    client.receive(frame(2))
    advance(200)
    expect(hint).not.toHaveBeenCalled()
    client.connected(send)
    advance(600)
    expect(generation()).toBe(1)
    client.receive(
      frame(1, {
        epoch: otherEpoch,
        available: false,
        changes: [{ scope: inboxWatchKey(queue), allowed: true, reset: true }],
      }),
    )
    expect(state).toHaveBeenLastCalledWith('connecting')
  })

  it('renews scope authority and unsubscribes without leaving a timer', () => {
    client.connected(send)
    const stop = client.watch(queue, hints(), states())
    advance(600)
    const prior = generation()
    advance(30_000)
    expect(generation()).toBeGreaterThan(prior)
    stop()
    expect(send.mock.lastCall![0].scopes).toEqual([])
    const before = send.mock.calls.length
    advance(90_000)
    expect(send).toHaveBeenCalledTimes(before)
  })

  it('rejects malformed frames, unknown contexts and unsupported client scopes', () => {
    const hint = hints()
    client.connected(send)
    client.watch(queue, hint, states())
    advance(600)
    for (const bad of [
      null,
      {},
      frame(1, { v: 2 }),
      frame(1, { sequence: NaN }),
      frame(1, { epoch: 'unsafe' }),
      frame(1, { generation: generation() + 1 }),
      frame(1, {
        changes: [
          {
            scope: inboxWatchKey(queue),
            allowed: true,
            reset: false,
            resource: 'not-an-opaque-key',
            version: 1,
          },
        ],
      }),
    ])
      client.receive(bad)
    advance(200)
    expect(hint).not.toHaveBeenCalled()
    client.receive(frame(1))
    advance(200)
    hint.mockClear()
    client.receive(
      frame(2, {
        changes: [
          {
            scope: inboxWatchKey({ kind: 'queue', team_id: team }),
            allowed: true,
            reset: true,
          },
        ],
      }),
    )
    advance(200)
    expect(hint).not.toHaveBeenCalled()
    const badState = states()
    client.watch(
      { kind: 'toString' } as unknown as InboxWatchScope,
      hint,
      badState,
    )
    expect(badState).toHaveBeenCalledWith('polling')
  })
})
