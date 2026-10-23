import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import axios, { AxiosError } from 'axios'

let api: typeof import('./api').api

beforeEach(async () => {
  vi.resetModules()
  // Load Vue before the intentionally minimal cookie-only document stub.
  ;(await import('@/extensions/session')).clearExtensionSession('org:user')
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

it.each(['challenge', 'confirm', 'enroll', 'revoke'])('does not replay an MFA %s after primary authentication refresh', async action => {
  vi.spyOn(axios, 'post').mockResolvedValue({ data: {} })
  let submissions = 0
  api.defaults.adapter = config => {
    submissions++
    if (submissions === 1) return Promise.reject(new AxiosError('unauthorized', 'ERR_BAD_REQUEST', config, undefined, {
      status: 401, statusText: 'Unauthorized', headers: {}, data: {}, config,
    }))
    // The old implementation reaches this response, consuming a one-time operation,
    // then discards it because refresh invalidated the request's revision.
    return Promise.resolve({ data: { data: { verified: true } }, status: 200, statusText: 'OK', headers: {}, config })
  }
  const outcome = api.post(`/extensions/security/mfa/${action}`, { code: '123456' }).catch(error => error)
  await vi.advanceTimersByTimeAsync(0)
  expect(await outcome).toMatchObject({ name: 'AuthenticationRefreshedError', canReconcile: true })
  expect(submissions).toBe(1)
})

it('rejects queued extension requests without replay while ordinary host requests resume', async () => {
  let finish!: () => void
  vi.spyOn(axios, 'post').mockImplementation(() => new Promise(resolve => { finish = () => resolve({ data: {} }) }))
  const attempts = new Map<string, number>()
  api.defaults.adapter = config => {
    const key = config.url!
    const count = (attempts.get(key) || 0) + 1
    attempts.set(key, count)
    if (count === 1) return Promise.reject(new AxiosError('unauthorized', 'ERR_BAD_REQUEST', config, undefined, {
      status: 401, statusText: 'Unauthorized', headers: {}, data: {}, config,
    }))
    return Promise.resolve({ data: { ok: true }, status: 200, statusText: 'OK', headers: {}, config })
  }
  const paths = ['/extensions/security/mfa/challenge', '/extensions/security/mfa', '/me']
  const outcomes = paths.map((url, index) => (index === 0 ? api.post(url, { code: '123456', recovery: true }) : api.get(url)).catch(error => error))
  await vi.advanceTimersByTimeAsync(0)
  finish()
  await vi.advanceTimersByTimeAsync(0)
  const results = await Promise.all(outcomes)
  expect(results[0]).toMatchObject({ name: 'AuthenticationRefreshedError' })
  expect(results[1]).toMatchObject({ name: 'AuthenticationRefreshedError' })
  expect(results[2]).toMatchObject({ data: { ok: true } })
  expect([...attempts.values()]).toEqual([1, 1, 2])
  // A caller may explicitly reconcile with a new read, carrying the new revision.
  await expect(api.get('/extensions/security/mfa')).resolves.toMatchObject({ data: { ok: true } })
})

it.each(['other:user', '', 'org:user'])('does not permit reconciliation after identity/logout/grant invalidation (%s)', async identity => {
  const { clearExtensionSession: invalidate } = await import('@/extensions/session')
  vi.spyOn(axios, 'post').mockImplementation(async () => { invalidate(identity); return { data: {} } })
  const outcome = api.post('/extensions/security/mfa/challenge', {}).catch(error => error)
  await vi.advanceTimersByTimeAsync(0)
  expect(await outcome).toMatchObject({ name: 'AuthenticationRefreshedError', canReconcile: false })
})

it('does not authorize reconciliation when a stale request returns 401 after logout', async () => {
  const { clearExtensionSession: invalidate } = await import('@/extensions/session')
  vi.spyOn(axios, 'post').mockResolvedValue({ data: {} })
  let fail!: () => void
  api.defaults.adapter = config => new Promise((_resolve, reject) => {
    fail = () => reject(new AxiosError('unauthorized', 'ERR_BAD_REQUEST', config, undefined, {
      status: 401, statusText: 'Unauthorized', headers: {}, data: {}, config,
    }))
  })
  const outcome = api.post('/extensions/security/mfa/challenge', {}).catch(error => error)
  await vi.advanceTimersByTimeAsync(0)
  invalidate('')
  fail()
  await vi.advanceTimersByTimeAsync(0)
  expect(await outcome).toMatchObject({ name: 'AuthenticationRefreshedError', canReconcile: false })
})
