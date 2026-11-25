// @vitest-environment happy-dom
import { expect, it } from 'vitest'
import {
  auditContextLabel,
  auditFailure,
  auditResourceLink,
  auditValue,
} from './auditInvestigation'
import type { AuditLogEntry } from '@/services/api'
const legacy = {
  resource_type: 'contact',
  resource_id: 'synthetic',
  changes: [],
} as unknown as AuditLogEntry
it('never infers historical context or a current resource from an arbitrary reference', () => {
  expect(auditContextLabel(legacy)).toContain('not recorded')
  expect(auditContextLabel({ ...legacy, context_state: 'unsupported' })).toBe(
    'Unsupported context',
  )
  expect(auditResourceLink(legacy)).toBeUndefined()
  expect(
    auditResourceLink({
      ...legacy,
      resource_type: 'user',
      resource_available: true,
    }),
  ).toBe('/settings/users/synthetic')
  expect(
    auditResourceLink({
      ...legacy,
      resource_type: 'user',
      resource_available: false,
    }),
  ).toBeUndefined()
})
it('formats false, zero and structured values without treating them as missing', () => {
  expect(auditValue(false)).toBe('No')
  expect(auditValue(0)).toBe('0')
  expect(auditValue({ synthetic: true })).toBe('{"synthetic":true}')
})
it('shows only fixed safe errors and a validated support reference, never arbitrary server text', async () => {
  expect(
    await auditFailure({
      response: {
        status: 503,
        data: {
          data: { request_id: '00000000-0000-0000-0000-000000000001' },
          message: 'secret server payload',
        },
      },
    }),
  ).toContain('Reference: 00000000-0000-0000-0000-000000000001')
  expect(
    await auditFailure({
      response: {
        status: 503,
        data: {
          data: { request_id: '<script>private</script>' },
          message: 'secret server payload',
        },
      },
    }),
  ).not.toMatch(/secret|script|private/)
  expect(
    await auditFailure({
      response: { status: 422, data: new Blob(['private']) },
    }),
  ).toContain('too large')
})
