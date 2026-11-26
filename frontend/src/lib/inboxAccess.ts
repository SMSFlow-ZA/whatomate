// A confirmed loss of identity, permission or resource scope invalidates
// previously fetched private data. Transport/provider outages do not prove loss
// of access and must not discard an authored draft.
export function isInboxAccessFailure(error: unknown): boolean {
  const status = (error as { response?: { status?: number } } | null)?.response
    ?.status
  return status === 401 || status === 403 || status === 404
}
