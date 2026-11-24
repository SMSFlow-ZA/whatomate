import { expect, test, type APIResponse } from '@playwright/test'
import { Client } from 'pg'
import { ApiHelper } from '../../helpers/api'
import { createTestScope } from '../../framework'

test('observes durable internal risk and breach notices through authenticated HTTP without sending', async ({
  request,
}) => {
  test.setTimeout(180000)
  const database = process.env.TEST_DATABASE_URL
  test.skip(!database, 'An explicitly owned disposable is required')
  const db = new URL(database!),
    host = new URL(process.env.BASE_URL!).hostname
  expect(['fixture', 'whatomate_test']).toContain(db.pathname.slice(1))
  for (const address of [db.hostname, host])
    expect(
      address.startsWith('whatomate-inbox-') ||
        ['localhost', '127.0.0.1'].includes(address),
    ).toBe(true)
  const api = new ApiHelper(request),
    scope = createTestScope('internal-notices')
  const data = async (response: APIResponse) => {
    expect(
      response.ok(),
      'HTTP ' + response.status() + ' ' + new URL(response.url()).pathname,
    ).toBe(true)
    return (await response.json()).data
  }
  await api.loginAsAdmin()
  const feature = (await data(await api.get('/api/inbox/workflows'))).enabled
  await data(await api.put('/api/inbox/workflows', { enabled: true }))
  const automatic = await data(await api.get('/api/inbox/automation'))
  expect(automatic.dispatch_available).toBe(false)
  const profile = await data(await api.get('/api/me'))
  const account = await api.createWhatsAppAccount({
    name: scope.name('internal targets'),
    phone_id: scope.phone().replace(/\D/g, ''),
    business_id: '100000000054',
    access_token: 'synthetic-never-send',
  })
  const contact = await api.createContact(
    scope.phone(),
    'Synthetic internal notice enquiry',
  )
  const base = '/api/inbox/sla/escalations/' + account.id
  const thread = '/api/inbox/accounts/' + account.id + '/contacts/' + contact.id
  try {
    const initial = await data(await api.get(base))
    expect(initial.settings.enabled).toBe(false)
    expect(initial.settings.revision).toBe(0)
    expect(initial.delivery).toBe('in_app')
    const directory = await data(await api.get(base + '/recipients'))
    expect(
      directory.recipients.some(
        (person: { id: string }) => person.id === profile.id,
      ),
    ).toBe(true)
    await data(
      await api.put('/api/inbox/sla/policies/' + account.id, {
        first_reply_minutes: 1,
        next_reply_minutes: 1,
        resolution_minutes: 120,
        business_hours: false,
        timezone: 'UTC',
        shifts: [],
        date_exceptions: [],
      }),
    )
    const settings = (
      await data(
        await api.put(base, {
          revision: 0,
          enabled: true,
          at_risk: true,
          breach: true,
          recipient_ids: [profile.id],
        }),
      )
    ).settings
    const opened = (
      await data(await api.put(thread, { status: 'open', priority: 'high' }))
    ).case
    const listURL = '/api/inbox/sla/notices?account_id=' + account.id
    let notices: {
      id: string
      threshold: string
      case_id: string
      policy_version_id: string
      read_at?: string
    }[] = []
    // Observe the real existing worker and clock, not a public test endpoint or fabricated delivery event.
    await expect
      .poll(
        async () => {
          notices = (await data(await api.get(listURL))).notices
          return [...new Set(notices.map((notice) => notice.threshold))].sort()
        },
        { timeout: 125000, intervals: [2000] },
      )
      .toEqual(['at_risk', 'breach'])
    expect(notices).toHaveLength(2)
    for (const notice of notices) {
      expect(notice.case_id).toBe(opened.id)
      expect(notice.policy_version_id).toBe(opened.sla_policy_id)
    }
    const risk = notices.find((notice) => notice.threshold === 'at_risk')!
    await data(await api.put('/api/inbox/sla/notices/' + risk.id + '/read', {}))
    const afterRead = (await data(await api.get(listURL))).notices.find(
      (notice: { id: string }) => notice.id === risk.id,
    )
    expect(afterRead.read_at).toBeTruthy()
    await data(await api.put('/api/inbox/sla/notices/' + risk.id + '/read', {}))
    expect(
      (await data(await api.get(listURL))).notices.find(
        (notice: { id: string }) => notice.id === risk.id,
      ).read_at,
    ).toBe(afterRead.read_at)
    const unread = await data(await api.get(listURL + '&unread=true'))
    expect(unread.notices).toHaveLength(1)
    const history = await data(await api.get(base + '/history'))
    expect(history.events).toHaveLength(2)
    expect(
      history.events.every(
        (event: { available_count: number; suppressed_count: number }) =>
          event.available_count === 1 && event.suppressed_count === 0,
      ),
    ).toBe(true)
    const unchanged = (await data(await api.get(thread))).case
    expect(unchanged.version).toBe(opened.version)
    expect(unchanged.sla_policy_id).toBe(opened.sla_policy_id)
    const stale = await api.put(base, {
      revision: 0,
      enabled: false,
      at_risk: false,
      breach: false,
      recipient_ids: [],
    })
    expect(stale.status()).toBe(409)
    await api.login('agent@test.com', 'password')
    expect((await data(await api.get(listURL))).notices).toHaveLength(0)
    expect(
      (
        await api.put('/api/inbox/sla/notices/' + risk.id + '/read', {})
      ).status(),
    ).toBe(404)
    expect((await api.get(base)).status()).toBe(403)
    await api.loginAsAdmin()
    await data(
      await api.put(base, {
        revision: settings.revision,
        enabled: false,
        at_risk: true,
        breach: true,
        recipient_ids: [profile.id],
      }),
    )
    expect(await data(await api.get('/api/inbox/automation'))).toEqual(
      automatic,
    )
    const client = new Client({ connectionString: database })
    await client.connect()
    try {
      const rows = await client.query(
        'SELECT threshold,count(*)::int AS total FROM inbox_sla_escalation_events WHERE organization_id=$1 AND account_id=$2 GROUP BY threshold ORDER BY threshold',
        [profile.organization_id, account.id],
      )
      expect(rows.rows).toEqual([
        { threshold: 'at_risk', total: 1 },
        { threshold: 'breach', total: 1 },
      ])
      const outgoing = await client.query(
        "SELECT count(*)::int AS total FROM messages WHERE organization_id=$1 AND whats_app_account=$2 AND direction='outgoing'",
        [profile.organization_id, account.name],
      )
      expect(outgoing.rows[0].total).toBe(0)
    } finally {
      await client.end()
    }
  } finally {
    await api.loginAsAdmin()
    const current = await api.get(base)
    if (current.ok()) {
      const settings = (await current.json()).data.settings
      await api.put(base, {
        revision: settings.revision,
        enabled: false,
        at_risk: false,
        breach: false,
        recipient_ids: [],
      })
    }
    await api.del('/api/inbox/sla/policies/' + account.id)
    await api.del('/api/contacts/' + contact.id)
    await api.del('/api/accounts/' + account.id)
    await api.put('/api/inbox/workflows', { enabled: feature })
  }
})
