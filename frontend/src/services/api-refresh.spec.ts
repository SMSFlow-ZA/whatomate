import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import axios, { AxiosError } from 'axios'
import { clearExtensionSession } from '@/extensions/session'

let api: typeof import('./api').api

beforeEach(async () => {
  clearExtensionSession('org:user')
  vi.resetModules()
  vi.useFakeTimers()
  vi.stubGlobal('window', { __BASE_PATH__: '', dispatchEvent: vi.fn(), location: { href: '' } })
  vi.stubGlobal('document', { cookie: '' })
  vi.stubGlobal('localStorage', { getItem: () => null, removeItem: vi.fn() })
  vi.stubGlobal('navigator', {})
  api = (await import('./api')).api
  api.defaults.adapter = config => Promise.reject(new AxiosError('unauthorized', 'ERR_BAD_REQUEST', config, undefined, {
    status: 401, statusText: 'Unauthorized', headers: {}, data: {}, config,
  }))
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

it('bounds a stalled refresh, rejects all queued requests, and releases the in-tab mutex', async () => {
  const refresh = vi.spyOn(axios, 'post').mockImplementation((_url, _data, config) => new Promise((_resolve, reject) => {
    config?.signal?.addEventListener?.('abort', () => reject(new Error('refresh aborted')))
  }))
  const outcomes: string[] = []
  const requests = ['/me', '/accounts'].map(url => api.get(url).then(() => outcomes.push('unexpected success'), () => outcomes.push('denied')))
  await vi.advanceTimersByTimeAsync(0)
  expect(refresh).toHaveBeenCalledTimes(1)
  await vi.advanceTimersByTimeAsync(30000)
  expect(outcomes).toEqual(['denied', 'denied'])
  await Promise.all(requests)
  expect(window.location.href).toBe('/login')
  expect(refresh.mock.calls[0]?.[2]?.timeout).toBe(30000)
  expect(refresh.mock.calls[0]?.[2]?.signal?.aborted).toBe(true)

  refresh.mockRejectedValueOnce(new Error('fresh attempt denied'))
  await expect(api.get('/me')).rejects.toThrow('unauthorized')
  expect(refresh).toHaveBeenCalledTimes(2)
})

it('aborts an unavailable cross-tab refresh lock without making a refresh request', async () => {
  let lockSignal: AbortSignal | undefined
  const request = vi.fn((_name, options) => new Promise((_resolve, reject) => {
    lockSignal = options.signal
    lockSignal?.addEventListener('abort', () => reject(new Error('lock wait aborted')))
  }))
  vi.stubGlobal('navigator', { locks: { request } })
  const refresh = vi.spyOn(axios, 'post')
  let result = 'waiting'
  const pending = api.get('/me').then(() => { result = 'unexpected success' }, () => { result = 'denied' })
  await vi.advanceTimersByTimeAsync(0)
  await vi.advanceTimersByTimeAsync(30000)
  expect(result).toBe('denied')
  await pending
  expect(lockSignal?.aborted).toBe(true)
  expect(refresh).not.toHaveBeenCalled()
  expect(window.location.href).toBe('/login')
})

it('retains cross-tab serialization and cancels its deadline after a successful refresh', async () => {
  const request = vi.fn(async (_name, _options, callback) => callback())
  vi.stubGlobal('navigator', { locks: { request } })
  const refresh = vi.spyOn(axios, 'post').mockResolvedValue({ data: {} })
  let calls = 0
  api.defaults.adapter = config => {
    calls++
    if (calls === 1) return Promise.reject(new AxiosError('unauthorized', 'ERR_BAD_REQUEST', config, undefined, {
      status: 401, statusText: 'Unauthorized', headers: {}, data: {}, config,
    }))
    return Promise.resolve({ data: { ok: true }, status: 200, statusText: 'OK', headers: {}, config })
  }
  const pending = expect(api.get('/me')).resolves.toMatchObject({ data: { ok: true } })
  await vi.advanceTimersByTimeAsync(0)
  await pending
  expect(request).toHaveBeenCalledTimes(1)
  expect(refresh).toHaveBeenCalledTimes(1)
  await vi.advanceTimersByTimeAsync(30000)
  expect(refresh.mock.calls[0]?.[2]?.signal?.aborted).toBe(false)
  expect(vi.getTimerCount()).toBe(0)
  expect(window.location.href).toBe('')
})
