export interface InboxWatchScope {
  kind:
    | 'queue'
    | 'case'
    | 'conversation'
    | 'contact'
    | 'capacity'
    | 'workload'
    | 'supervisor'
  account_id?: string
  team_id?: string
  case_id?: string
  contact_id?: string
}
export type InboxLiveState = 'connecting' | 'live' | 'polling' | 'denied'
export interface InboxLiveHint {
  reset: boolean
  denied: boolean
  clear?: boolean
}
interface ScopeChange {
  scope: string
  allowed: boolean
  reset: boolean
  clear?: boolean
  resource?: string
  version?: number
}
interface Frame {
  v: number
  generation: number
  epoch: string
  sequence: number
  available: boolean
  state: string
  changes: ScopeChange[] | null
}
interface Watcher {
  scope: InboxWatchScope
  hint: (hint: InboxLiveHint) => void
  state: (state: InboxLiveState) => void
}
type WatchMessage = { v: 1; generation: number; scopes: InboxWatchScope[] }
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const priorities: Record<InboxWatchScope['kind'], number> = {
  case: 0,
  conversation: 0,
  queue: 1,
  contact: 2,
  capacity: 3,
  supervisor: 4,
  workload: 5,
}
export function inboxWatchKey(scope: InboxWatchScope) {
  return [
    scope.kind,
    scope.account_id ?? '',
    scope.team_id ?? '',
    scope.case_id ?? '',
    scope.contact_id ?? '',
  ].join('/')
}
function validScope(scope: InboxWatchScope) {
  if (!Object.prototype.hasOwnProperty.call(priorities, scope.kind))
    return false
  const ids = [scope.account_id, scope.team_id, scope.case_id, scope.contact_id]
  if (
    ids.some(
      (id) =>
        id !== undefined &&
        (!uuid.test(id) || /^0{8}-0{4}-0{4}-0{4}-0{12}$/.test(id)),
    )
  )
    return false
  switch (scope.kind) {
    case 'case':
      return (
        !!scope.case_id &&
        !scope.account_id &&
        !scope.team_id &&
        !scope.contact_id
      )
    case 'conversation':
      return (
        !!scope.account_id &&
        !!scope.contact_id &&
        !scope.team_id &&
        !scope.case_id
      )
    case 'contact':
      return (
        !!scope.contact_id &&
        !scope.account_id &&
        !scope.team_id &&
        !scope.case_id
      )
    case 'capacity':
      return (
        !!scope.account_id &&
        !!scope.team_id &&
        !scope.case_id &&
        !scope.contact_id
      )
    default:
      return !scope.case_id && !scope.contact_id
  }
}
function validFrame(value: unknown): value is Frame {
  if (!value || typeof value !== 'object') return false
  const f = value as Frame
  return (
    f.v === 1 &&
    Number.isSafeInteger(f.generation) &&
    f.generation > 0 &&
    typeof f.epoch === 'string' &&
    uuid.test(f.epoch) &&
    Number.isSafeInteger(f.sequence) &&
    f.sequence > 0 &&
    typeof f.available === 'boolean' &&
    ['watching', 'update', 'expired', 'rate_limited', 'unavailable'].includes(
      f.state,
    ) &&
    (f.changes === null ||
      (Array.isArray(f.changes) &&
        f.changes.length <= 3 &&
        f.changes.every(
          (c) =>
            c &&
            typeof c.scope === 'string' &&
            c.scope.length <= 170 &&
            typeof c.allowed === 'boolean' &&
            typeof c.reset === 'boolean' &&
            (c.clear === undefined || typeof c.clear === 'boolean') &&
            (c.resource === undefined ||
              (typeof c.resource === 'string' &&
                /^[a-f0-9]{64}$/.test(c.resource))) &&
            (c.version === undefined ||
              (Number.isSafeInteger(c.version) && c.version > 0)) &&
            !!c.resource === (c.version !== undefined),
        )))
  )
}

// Payloads only request authorised HTTP refreshes. They never populate records,
// grant permission, select cases, alter drafts or dispatch work. Transport is
// injected by the existing socket service to avoid a store/router import cycle.
export class InboxLiveClient {
  private watchers = new Map<symbol, Watcher>()
  private send: ((message: WatchMessage) => void) | null = null
  private generation = 0
  private epoch = ''
  private sequence = 0
  private versions = new Map<string, number>()
  private active = new Set<string>()
  private pending = new Map<symbol, InboxLiveHint>()
  private changeTimer?: ReturnType<typeof setTimeout>
  private hintTimer?: ReturnType<typeof setTimeout>
  private renewTimer?: ReturnType<typeof setInterval>

  watch(
    scope: InboxWatchScope,
    hint: Watcher['hint'],
    state: Watcher['state'],
  ) {
    if (!validScope(scope)) {
      state('polling')
      return () => {}
    }
    const id = Symbol('inbox-watch')
    // Copy client-known values; no live caller object can mutate a sent scope.
    this.watchers.set(id, { scope: { ...scope }, hint, state })
    this.changed()
    return () => {
      this.watchers.delete(id)
      this.pending.delete(id)
      this.changed()
    }
  }

  connected(send: (message: WatchMessage) => void) {
    this.stopTimers()
    this.send = send
    this.generation = 0
    this.changed()
  }

  disconnected() {
    this.stopTimers()
    this.send = null
    this.active.clear()
    this.epoch = ''
    this.sequence = 0
    this.versions.clear()
    this.pending.clear()
    for (const watcher of this.watchers.values()) watcher.state('polling')
  }

  private stopTimers() {
    clearTimeout(this.changeTimer)
    clearTimeout(this.hintTimer)
    clearInterval(this.renewTimer)
    this.changeTimer = this.hintTimer = this.renewTimer = undefined
  }

  private changed() {
    clearTimeout(this.changeTimer)
    clearInterval(this.renewTimer)
    this.renewTimer = undefined
    this.generation++ // Fence old-context frames before the new watch is sent.
    this.epoch = ''
    this.sequence = 0
    this.active.clear()
    this.versions.clear()
    this.pending.clear()
    for (const watcher of this.watchers.values())
      watcher.state(this.send ? 'connecting' : 'polling')
    if (!this.send) return
    if (!this.watchers.size) {
      this.transmit()
      return
    }
    this.changeTimer = setTimeout(() => this.transmit(), 600)
    this.renewTimer = setInterval(() => {
      this.generation++
      this.epoch = ''
      this.sequence = 0
      this.versions.clear()
      this.transmit()
    }, 30_000)
  }

  private transmit() {
    if (!this.send) return
    const unique = new Map<string, InboxWatchScope>()
    for (const watcher of this.watchers.values())
      unique.set(inboxWatchKey(watcher.scope), watcher.scope)
    const scopes = [...unique.values()]
      .sort(
        (a, b) =>
          priorities[a.kind] - priorities[b.kind] ||
          inboxWatchKey(a).localeCompare(inboxWatchKey(b)),
      )
      .slice(0, 3)
    this.active = new Set(scopes.map(inboxWatchKey))
    for (const watcher of this.watchers.values()) {
      if (!this.active.has(inboxWatchKey(watcher.scope)))
        watcher.state('polling')
    }
    this.send({ v: 1, generation: this.generation, scopes })
  }

  receive(value: unknown) {
    if (
      !this.send ||
      !validFrame(value) ||
      value.generation !== this.generation
    )
      return
    const frame = value
    if (frame.epoch === this.epoch && frame.sequence <= this.sequence) return
    const reset =
      frame.epoch !== this.epoch || frame.sequence !== this.sequence + 1
    this.epoch = frame.epoch
    this.sequence = frame.sequence
    if (reset) this.versions.clear()
    const changes = new Map(
      (frame.changes ?? [])
        .filter((c) => this.active.has(c.scope))
        .map((c) => [c.scope, c]),
    )
    // Revision acceptance belongs to a scope, not its first mounted consumer.
    // Multiple visible panels can watch the same case and must all receive an
    // accepted hint before that resource revision is treated as a duplicate.
    const accepted = new Map<string, InboxLiveHint | null>()
    for (const [id, watcher] of this.watchers) {
      const key = inboxWatchKey(watcher.scope)
      if (!this.active.has(key)) continue
      const change = changes.get(key)
      if (
        frame.state === 'rate_limited' ||
        frame.state === 'expired' ||
        frame.state === 'unavailable'
      ) {
        watcher.state('polling')
        this.enqueue(id, { reset: true, denied: false })
        continue
      }
      if (!change) {
        if (reset) this.enqueue(id, { reset: true, denied: false })
        continue
      }
      watcher.state(
        !change.allowed ? 'denied' : frame.available ? 'live' : 'connecting',
      )
      if (accepted.has(key)) {
        const hint = accepted.get(key)
        if (hint) this.enqueue(id, hint)
        continue
      }
      let refresh = reset || change.reset || !change.allowed
      if (change.resource && change.version !== undefined) {
        const versionKey = key + '/' + change.resource
        if (change.version > (this.versions.get(versionKey) ?? 0)) {
          if (this.versions.size >= 256) {
            this.versions.clear()
            refresh = true
          }
          this.versions.set(versionKey, change.version)
          refresh = true
        }
      }
      const hint = refresh
        ? {
            reset: reset || change.reset,
            denied: !change.allowed,
            ...(change.clear ? { clear: true } : {}),
          }
        : null
      accepted.set(key, hint)
      if (hint) this.enqueue(id, hint)
    }
  }

  private enqueue(id: symbol, hint: InboxLiveHint) {
    if (hint.denied || hint.clear) {
      this.pending.delete(id)
      this.watchers.get(id)?.hint(hint)
      return
    }
    const old = this.pending.get(id)
    this.pending.set(id, {
      reset: hint.reset || !!old?.reset,
      denied: hint.denied || !!old?.denied,
      ...(hint.clear || old?.clear ? { clear: true } : {}),
    })
    if (this.hintTimer !== undefined) return
    this.hintTimer = setTimeout(() => {
      this.hintTimer = undefined
      const pending = this.pending
      this.pending = new Map()
      for (const [key, value] of pending) this.watchers.get(key)?.hint(value)
    }, 200)
  }
}

export const inboxLive = new InboxLiveClient()
