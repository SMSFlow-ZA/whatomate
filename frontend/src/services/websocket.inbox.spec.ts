// @vitest-environment happy-dom
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, expect, it, vi } from 'vitest'
vi.mock('@/router', () => ({ default: { push: vi.fn() } }))
vi.mock('@/services/api', () => ({ contactsService: {}, messagesService: {} }))
import { wsService } from './websocket'
import { inboxLive, inboxWatchKey } from './inboxLive'
import { useAuthStore } from '@/stores/auth'
import { useTransfersStore } from '@/stores/transfers'
import { useContactsStore } from '@/stores/contacts'

class FakeSocket {
  static OPEN = 1
  static CONNECTING = 0
  readyState = 0
  onopen?: () => void
  onclose?: () => void
  onmessage?: (event: { data: string }) => void
  onerror?: () => void
  sent: unknown[] = []
  constructor() {
    sockets.push(this)
  }
  send(value: string) {
    this.sent.push(JSON.parse(value))
  }
  close() {
    this.readyState = 3
  }
  open() {
    this.readyState = 1
    this.onopen?.()
  }
}
let sockets: FakeSocket[] = []
let stop = () => {}

it('does not request the transfer directory on reconnect without read access', () => {
  setActivePinia(createPinia())
  vi.spyOn(useAuthStore(), 'hasPermission').mockReturnValue(false)
  const transfers = vi
    .spyOn(useTransfersStore(), 'fetchTransfers')
    .mockResolvedValue(undefined)
  const contacts = vi
    .spyOn(useContactsStore(), 'fetchContacts')
    .mockResolvedValue(undefined)
  ;(wsService as unknown as { refreshStaleData: () => void }).refreshStaleData()
  expect(contacts).toHaveBeenCalledOnce()
  expect(transfers).not.toHaveBeenCalled()
})
afterEach(() => {
  stop()
  wsService.disconnect()
  sockets = []
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

it('authenticates before watching and ignores late frames and closure from a replaced socket', async () => {
  setActivePinia(createPinia())
  vi.useFakeTimers()
  vi.stubGlobal('WebSocket', FakeSocket)
  vi.spyOn(
    wsService as unknown as { refreshStaleData: () => void },
    'refreshStaleData',
  ).mockImplementation(() => {})
  const scope = { kind: 'queue' as const },
    hint = vi.fn(),
    state = vi.fn()
  stop = inboxLive.watch(scope, hint, state)
  await wsService.connect(async () => 'synthetic-ticket')
  const first = sockets[0]!
  first.open()
  vi.advanceTimersByTime(600)
  expect(first.sent[0]).toEqual({
    type: 'auth',
    payload: { token: 'synthetic-ticket' },
  })
  expect(first.sent[1]).toMatchObject({
    type: 'inbox_watch',
    payload: { v: 1, scopes: [scope] },
  })
  first.readyState = 3
  first.onclose?.()
  await wsService.connect()
  const second = sockets[1]!
  second.open()
  vi.advanceTimersByTime(600)
  const message = second.sent[1] as { payload: { generation: number } }
  const event = {
    data: JSON.stringify({
      type: 'inbox_state',
      payload: {
        v: 1,
        generation: message.payload.generation,
        epoch: '20000000-0000-4000-8000-000000000001',
        sequence: 1,
        available: true,
        state: 'watching',
        changes: [{ scope: inboxWatchKey(scope), allowed: true, reset: true }],
      },
    }),
  }
  first.onmessage?.(event)
  first.onclose?.()
  vi.advanceTimersByTime(200)
  expect(hint).not.toHaveBeenCalled()
  expect(wsService.getIsConnected()).toBe(true)
  second.onmessage?.(event)
  vi.advanceTimersByTime(200)
  expect(hint).toHaveBeenCalledOnce()
  expect(state).toHaveBeenLastCalledWith('live')
})
