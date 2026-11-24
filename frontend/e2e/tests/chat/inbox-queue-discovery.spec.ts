import { expect, test, type APIResponse } from '@playwright/test'
import { Client } from 'pg'
import { ApiHelper } from '../../helpers/api'
import { createTestScope } from '../../framework'

test('qualifies queue discovery through normal HTTP while preserving legacy queues', async ({
  page,
  request,
}) => {
  test.setTimeout(180000)
  const database = process.env.TEST_DATABASE_URL
  test.skip(!database, 'An explicitly owned disposable is required')
  const dbURL = new URL(database!)
  expect(['fixture', 'whatomate_test']).toContain(dbURL.pathname.slice(1))
  for (const host of [dbURL.hostname, new URL(process.env.BASE_URL!).hostname])
    expect(
      host.startsWith('whatomate-inbox-') ||
        ['localhost', '127.0.0.1'].includes(host),
    ).toBe(true)
  const api = new ApiHelper(request)
  const scope = createTestScope('queue-discovery')
  const data = async (response: APIResponse) => {
    expect(
      response.ok(),
      'HTTP ' + response.status() + ' ' + new URL(response.url()).pathname,
    ).toBe(true)
    return (await response.json()).data
  }
  await api.loginAsAdmin()
  const enabledBefore = (await data(await api.get('/api/inbox/workflows')))
    .enabled
  await data(await api.put('/api/inbox/workflows', { enabled: true }))
  const sendsBefore = await data(await api.get('/api/inbox/automation'))
  expect(sendsBefore.dispatch_available).toBe(false)
  const profile = await data(await api.get('/api/me'))
  const account = await api.createWhatsAppAccount({
    name: scope.name('queue discovery'),
    phone_id: scope.phone().replace(/\D/g, ''),
    business_id: '100000000055',
    access_token: 'synthetic-never-send',
  })
  const contacts: { id: string; profile_name: string }[] = []
  const cases: string[] = []
  const client = new Client({ connectionString: database })
  let viewID = ''
  const filters = { account_id: account.id, queue: 'unassigned' }
  const queue = (discovery?: object, cursor?: string) => {
    const query = new URLSearchParams({
      filters: JSON.stringify(filters),
      include_counts: 'true',
    })
    if (discovery) query.set('discovery', JSON.stringify(discovery))
    if (cursor) query.set('cursor', cursor)
    return '/api/inbox/queue?' + query
  }
  await client.connect()
  try {
    const indexes = await client.query(
      "SELECT indexname FROM pg_indexes WHERE tablename='inbox_cases' AND indexname IN ('idx_inbox_discovery_opened','idx_inbox_discovery_activity') ORDER BY indexname",
    )
    expect(indexes.rows.map((row) => row.indexname)).toEqual([
      'idx_inbox_discovery_activity',
      'idx_inbox_discovery_opened',
    ])
    for (let i = 0; i < 55; i++) {
      const contact = await api.createContact(
        scope.phone(),
        'Synthetic queue discovery ' + String(i).padStart(2, '0'),
      )
      contacts.push(contact)
      const result = await data(
        await api.put(
          '/api/inbox/accounts/' + account.id + '/contacts/' + contact.id,
          { status: 'open', priority: 'normal' },
        ),
      )
      cases.push(result.case.id)
    }
    // Clearly owned timestamp fixtures; no webhook, provider or outbound calls.
    await client.query(
      "UPDATE inbox_cases c SET opened_at='2026-11-01T04:00:00Z'::timestamptz+(s.n-1)*interval '1 minute',last_inbound_at='2026-11-01T04:00:00Z'::timestamptz+(s.n-1)*interval '1 minute' FROM unnest($1::uuid[]) WITH ORDINALITY s(id,n) WHERE c.id=s.id AND c.organization_id=$2 AND c.account_id=$3",
      [cases, profile.organization_id, account.id],
    )
    const discovery = { order: 'oldest' }
    const first = await data(await api.get(queue(discovery)))
    expect(first.cases.map((row: { id: string }) => row.id)).toEqual(
      cases.slice(0, 50),
    )
    expect(first.counts.unassigned).toBe(55)
    expect(first.paging).toBe('keyset')
    const second = await data(
      await api.get(queue(discovery, first.next_cursor)),
    )
    expect(second.cases.map((row: { id: string }) => row.id)).toEqual(
      cases.slice(50),
    )
    expect(second.has_more).toBe(false)
    const range = {
      order: 'latest_activity',
      timezone: 'America/New_York',
      opened_from: '2026-11-01T00:10:00-04:00',
      opened_before: '2026-11-01T00:37:00-04:00',
    }
    const ranged = await data(await api.get(queue(range)))
    expect(ranged.counts.all).toBe(27)
    expect(ranged.cases.map((row: { id: string }) => row.id)).toEqual(
      cases.slice(10, 37).reverse(),
    )
    const saved = await data(
      await api.post('/api/inbox/views', {
        name: scope.name('saved discovery'),
        filters,
        discovery: range,
      }),
    )
    viewID = saved.view.id
    await data(
      await api.put('/api/inbox/views/' + viewID, {
        name: scope.name('legacy rename'),
        filters,
      }),
    )
    const views = await data(await api.get('/api/inbox/views'))
    expect(views.discovery_version).toBe(1)
    expect(
      views.views.find((view: { id: string }) => view.id === viewID).discovery
        .order,
    ).toBe('latest_activity')
    await data(
      await api.put(
        '/api/inbox/accounts/' + account.id + '/contacts/' + contacts[0].id,
        {
          case_id: first.cases[0].id,
          priority: 'high',
          version: first.cases[0].version,
        },
      ),
    )
    const stale = await api.get(queue(discovery, first.next_cursor))
    expect(stale.status()).toBe(409)
    expect(await stale.text()).toContain('inbox_queue_changed')
    expect((await api.get(queue({ order: 'unsupported' }))).status()).toBe(400)
    const refreshed = await data(await api.get(queue(discovery)))
    expect(refreshed.cases).toHaveLength(50)
    const legacy = await data(await api.get(queue() + '&offset=50'))
    expect(legacy.paging).toBe('offset')
    expect(legacy.cases).toHaveLength(5)
    expect(legacy.next_cursor).toBe('')

    // Existing UI still opens and uses legacy queue behaviour. New sort/date
    // controls have their own acceptance after this backend foundation.
    await page.context().addCookies((await request.storageState()).cookies)
    await page.addInitScript(
      (user) => localStorage.setItem('user', JSON.stringify(user)),
      profile,
    )
    await page.goto('/chat')
    await page.getByRole('button', { name: 'Queues', exact: true }).click()
    await page.getByRole('button', { name: 'Unassigned', exact: true }).click()
    await page.getByRole('button', { name: /^Filters(?: \(\d+\))?$/ }).click()
    await page
      .getByRole('combobox', { name: 'WhatsApp account', exact: true })
      .selectOption(account.id)
    await page.keyboard.press('Escape')
    await expect(
      page.getByRole('checkbox', {
        name: 'Select case for Synthetic queue discovery 00',
        exact: true,
      }),
    ).toBeVisible()
    expect(await data(await api.get('/api/inbox/automation'))).toEqual(
      sendsBefore,
    )
    const outgoing = await client.query(
      "SELECT count(*)::int AS total FROM messages WHERE organization_id=$1 AND whats_app_account=$2 AND direction='outgoing'",
      [profile.organization_id, account.name],
    )
    expect(outgoing.rows[0].total).toBe(0)
  } finally {
    if (viewID) await api.del('/api/inbox/views/' + viewID)
    for (const contact of contacts) await api.del('/api/contacts/' + contact.id)
    await api.del('/api/accounts/' + account.id)
    await api.put('/api/inbox/workflows', { enabled: enabledBefore })
    await client.end()
  }
})
