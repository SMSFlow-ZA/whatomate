import type { AuditLogEntry } from '@/services/api'

export function auditContextLabel(log: AuditLogEntry): string {
  if (log.context_available) return 'Captured context'
  return log.context_state === 'unsupported'
    ? 'Unsupported context'
    : 'Legacy · context not recorded'
}

export function auditValue(value: unknown): string {
  if (value === null || value === undefined || value === '') return '—'
  if (typeof value === 'boolean') return value ? 'Yes' : 'No'
  if (typeof value === 'object') return JSON.stringify(value)
  return String(value)
}

// Only destinations implemented by the application are linked, and only after
// the server has verified that the target is currently available to the viewer.
export function auditResourceLink(log: AuditLogEntry): string | undefined {
  if (log.resource_available !== true) return
  const id = encodeURIComponent(log.resource_id)
  const links: Record<string, string> = {
    account: `/settings/accounts/${id}`,
    user: `/settings/users/${id}`,
    api_key: `/settings/api-keys/${id}`,
    role: `/settings/roles/${id}`,
    team: `/settings/teams/${id}`,
    template: `/templates/${id}`,
    campaign: `/campaigns/${id}`,
    chatbot_flow: `/chatbot/flows/${id}`,
    keyword_rule: `/chatbot/keywords/${id}`,
    ivr_flow: `/calling/ivr-flows/${id}`,
    webhook: '/settings/webhooks',
    custom_action: '/settings/custom-actions',
    canned_response: '/settings/canned-responses',
    ai_context: '/chatbot/ai',
    'settings.chatbot': '/settings/chatbot',
    'settings.general': '/settings',
    'settings.calling': '/settings',
  }
  // Case activity is deliberately not reconstructed from a contact-only row.
  return links[log.resource_type]
}

export async function auditFailure(error: unknown): Promise<string> {
  const response = (error as { response?: { status?: number; data?: unknown } })
    ?.response
  if (response?.status === 401) return 'Sign in again to view audit records.'
  if (response?.status === 403)
    return 'Your access changed. Ask an administrator to review your audit permissions.'
  if (response?.status === 409)
    return 'Access or records changed. Refresh the results before exporting again.'
  if (response?.status === 422)
    return 'This export page is too large. Choose a smaller page size or narrower date range.'
  if (response?.status === 400)
    return 'Check the dates, identifiers and filters, then apply them again.'
  let data = response?.data
  if (data instanceof Blob && data.size < 8192) {
    try {
      data = JSON.parse(await data.text())
    } catch {
      /* Do not expose arbitrary provider/server text. */
    }
  }
  const detail = data as
    | {
        data?: { request_id?: string }
        error?: { data?: { request_id?: string } }
      }
    | undefined
  const reference = detail?.data?.request_id ?? detail?.error?.data?.request_id
  const safeReference =
    typeof reference === 'string' && /^[a-f0-9-]{36}$/i.test(reference)
      ? ` Reference: ${reference}.`
      : ''
  return `Audit records are temporarily unavailable. Try again.${safeReference}`
}
