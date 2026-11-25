// @vitest-environment happy-dom
import { createApp, h, nextTick, ref, type App } from 'vue'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { useInboxLive } from './useInboxLive'
import {
  inboxLive,
  inboxWatchKey,
  type InboxLiveClient,
  type InboxWatchScope,
} from '@/services/inboxLive'

const account = '10000000-0000-4000-8000-000000000001'
const otherAccount = '10000000-0000-4000-8000-000000000002'
const epoch = '20000000-0000-4000-8000-000000000001'
let app: App | undefined
let host: HTMLElement
let visible = 'visible'
let send = vi.fn<Parameters<InboxLiveClient['connected']>[0]>()
beforeEach(() => {
  vi.useFakeTimers()
  visible = 'visible'
  vi.spyOn(document, 'visibilityState', 'get').mockImplementation(
    () => visible as DocumentVisibilityState,
  )
  send = vi.fn<Parameters<InboxLiveClient['connected']>[0]>()
  inboxLive.connected(send)
  host = document.createElement('div')
  document.body.append(host)
})
afterEach(() => {
  app?.unmount()
  app = undefined
  host.remove()
  inboxLive.disconnected()
  vi.restoreAllMocks()
  vi.useRealTimers()
})
async function tick(ms = 0) {
  vi.advanceTimersByTime(ms)
  await nextTick()
  await Promise.resolve()
}
function emit(scope: InboxWatchScope, sequence: number, allowed = true) {
  inboxLive.receive({
    v: 1,
    generation: send.mock.lastCall![0].generation,
    epoch,
    sequence,
    available: true,
    state: 'update',
    changes: [{ scope: inboxWatchKey(scope), allowed, reset: true }],
  })
}
function mount(setup: () => void) {
  app = createApp({
    setup() {
      setup()
      return () => h('p', 'Synthetic inbox')
    },
  })
  app.mount(host)
}

it('ignores revocation for an old binding before the context watcher has flushed', async () => {
  const selected = ref(account),
    context = ref('first'),
    clear = vi.fn(),
    refresh = vi.fn()
  mount(() => {
    useInboxLive(() => ({ kind: 'queue', account_id: selected.value }), {
      context: () => context.value,
      refresh,
      denied: clear,
      clear,
    })
  })
  await tick(600)
  context.value = 'second'
  emit({ kind: 'queue', account_id: account }, 1, false)
  expect(clear).not.toHaveBeenCalled()
  await tick(600)
  selected.value = otherAccount
  emit({ kind: 'queue', account_id: account }, 2, false)
  expect(clear).not.toHaveBeenCalled()
  await tick(600)
  expect(refresh).not.toHaveBeenCalled()
})

it('preserves edits during verification outages and resumes after recovery', async () => {
  const blocked = ref(true),
    clear = vi.fn(),
    refresh = vi.fn().mockResolvedValue(undefined)
  let live!: ReturnType<typeof useInboxLive>
  mount(() => {
    live = useInboxLive(() => ({ kind: 'queue', account_id: account }), {
      blocked: () => blocked.value,
      refresh,
      denied: clear,
      clear,
    })
  })
  await tick(600)
  inboxLive.receive({
    v: 1,
    generation: send.mock.lastCall![0].generation,
    epoch,
    sequence: 1,
    available: false,
    state: 'unavailable',
    changes: [],
  })
  await tick(200)
  expect(live.state.value).toBe('polling')
  expect(live.pending.value).toBe(true)
  expect(clear).not.toHaveBeenCalled()
  expect(refresh).not.toHaveBeenCalled()
  emit({ kind: 'queue', account_id: account }, 2)
  await tick(200)
  expect(live.state.value).toBe('live')
  blocked.value = false
  await tick()
  expect(refresh).toHaveBeenCalledOnce()
  expect(clear).not.toHaveBeenCalled()
})

it('holds hints during an edit and drains once without changing the authored draft', async () => {
  const blocked = ref(true),
    draft = ref('Authored reply stays here')
  const refresh = vi.fn().mockResolvedValue(undefined)
  let live!: ReturnType<typeof useInboxLive>
  mount(() => {
    live = useInboxLive(() => ({ kind: 'queue', account_id: account }), {
      blocked: () => blocked.value,
      refresh,
      denied: vi.fn(),
    })
  })
  await tick(600)
  emit({ kind: 'queue', account_id: account }, 1)
  await tick(200)
  expect(live.pending.value).toBe(true)
  expect(refresh).not.toHaveBeenCalled()
  blocked.value = false
  await tick()
  expect(refresh).toHaveBeenCalledOnce()
  expect(draft.value).toBe('Authored reply stays here')
  expect(live.pending.value).toBe(false)
})

it('coalesces an in-flight refresh and does not replay work after unmount', async () => {
  let release!: () => void
  const waiting = new Promise<void>((resolve) => {
    release = resolve
  })
  const refresh = vi
    .fn()
    .mockReturnValueOnce(waiting)
    .mockResolvedValue(undefined)
  mount(() => {
    useInboxLive(() => ({ kind: 'queue', account_id: account }), {
      refresh,
      denied: vi.fn(),
    })
  })
  await tick(600)
  emit({ kind: 'queue', account_id: account }, 1)
  await tick(200)
  emit({ kind: 'queue', account_id: account }, 2)
  await tick(200)
  expect(refresh).toHaveBeenCalledOnce()
  release()
  await tick()
  expect(refresh).toHaveBeenCalledTimes(2)
  app?.unmount()
  app = undefined
  await tick(600)
  expect(send.mock.lastCall![0].scopes).toEqual([])
  expect(refresh).toHaveBeenCalledTimes(2)
})

it('clears revoked private state immediately even when editing', async () => {
  const clear = vi.fn(),
    refresh = vi.fn(),
    blocked = ref(true)
  let live!: ReturnType<typeof useInboxLive>
  mount(() => {
    live = useInboxLive(() => ({ kind: 'queue', account_id: account }), {
      blocked: () => blocked.value,
      refresh,
      denied: clear,
    })
  })
  await tick(600)
  emit({ kind: 'queue', account_id: account }, 1, false)
  await tick(200)
  expect(clear).toHaveBeenCalledOnce()
  expect(live.state.value).toBe('denied')
  expect(refresh).not.toHaveBeenCalled()
  expect(live.pending.value).toBe(false)
})

it('detaches hidden contexts and fences late hints across account changes', async () => {
  const selected = ref(account),
    refresh = vi.fn().mockResolvedValue(undefined)
  mount(() => {
    useInboxLive(() => ({ kind: 'queue', account_id: selected.value }), {
      context: () => selected.value,
      refresh,
      denied: vi.fn(),
    })
  })
  await tick(600)
  const generation = send.mock.lastCall![0].generation
  selected.value = otherAccount
  await tick()
  inboxLive.receive({
    v: 1,
    generation,
    epoch,
    sequence: 1,
    available: true,
    state: 'update',
    changes: [
      {
        scope: inboxWatchKey({ kind: 'queue', account_id: account }),
        allowed: true,
        reset: true,
      },
    ],
  })
  await tick(600)
  expect(refresh).not.toHaveBeenCalled()
  expect(send.mock.lastCall![0].scopes).toEqual([
    { kind: 'queue', account_id: otherAccount },
  ])
  visible = 'hidden'
  document.dispatchEvent(new Event('visibilitychange'))
  await tick()
  expect(send.mock.lastCall![0].scopes).toEqual([])
  visible = 'visible'
  document.dispatchEvent(new Event('visibilitychange'))
  await tick(600)
  await tick(600)
  expect(send.mock.lastCall![0].scopes).toEqual([
    { kind: 'queue', account_id: otherAccount },
  ])
})

it('keeps failed reads pending without an automatic retry loop', async () => {
  const refresh = vi.fn().mockRejectedValue(new Error('Synthetic API outage'))
  let live!: ReturnType<typeof useInboxLive>
  mount(() => {
    live = useInboxLive(() => ({ kind: 'queue', account_id: account }), {
      refresh,
      denied: vi.fn(),
    })
  })
  await tick(600)
  emit({ kind: 'queue', account_id: account }, 1)
  await tick(200)
  await tick(5_000)
  expect(refresh).toHaveBeenCalledOnce()
  expect(live.pending.value).toBe(true)
})

it('retains a deferred result until a draft becomes safe to refresh', async () => {
  const blocked = ref(false)
  const refresh = vi
    .fn()
    .mockResolvedValueOnce(false)
    .mockResolvedValue(undefined)
  let live!: ReturnType<typeof useInboxLive>
  mount(() => {
    live = useInboxLive(() => ({ kind: 'queue', account_id: account }), {
      blocked: () => blocked.value,
      refresh,
      denied: vi.fn(),
    })
  })
  await tick(600)
  emit({ kind: 'queue', account_id: account }, 1)
  await tick(200)
  expect(refresh).toHaveBeenCalledOnce()
  expect(live.pending.value).toBe(true)
  blocked.value = true
  await tick()
  blocked.value = false
  await tick()
  expect(refresh).toHaveBeenCalledTimes(2)
  expect(live.pending.value).toBe(false)
})

it('clears a lost queue grant before refreshing even when a bulk selection blocks ordinary updates', async () => {
  const blocked = ref(true),
    calls: string[] = []
  mount(() => {
    useInboxLive(() => ({ kind: 'queue', account_id: account }), {
      blocked: () => blocked.value,
      clear: () => {
        calls.push('clear')
        blocked.value = false
      },
      refresh: () => {
        calls.push('refresh')
      },
      denied: vi.fn(),
    })
  })
  await tick(600)
  inboxLive.receive({
    v: 1,
    generation: send.mock.lastCall![0].generation,
    epoch,
    sequence: 1,
    available: true,
    state: 'update',
    changes: [
      {
        scope: inboxWatchKey({ kind: 'queue', account_id: account }),
        allowed: true,
        reset: true,
        clear: true,
      },
    ],
  })
  expect(calls[0]).toBe('clear')
  await tick()
  expect(calls).toEqual(['clear', 'refresh'])
})
