import { expect, test, type APIResponse } from '@playwright/test'
import { ApiHelper } from '../../helpers/api'
import { createTestScope } from '../../framework'

test('qualifies normal HTTP response-policy selection, preview and scoped edits without sending', async ({
  request,
}) => {
  const database = process.env.TEST_DATABASE_URL
  test.skip(!database, 'An explicitly owned disposable database is required')
  const db = new URL(database!)
  expect(['fixture', 'whatomate_test']).toContain(db.pathname.slice(1))
  expect(
    db.hostname.startsWith('whatomate-inbox-') ||
      ['127.0.0.1', 'localhost'].includes(db.hostname),
  ).toBe(true)
  const host = new URL(process.env.BASE_URL!).hostname
  expect(
    host.startsWith('whatomate-inbox-') ||
      ['127.0.0.1', 'localhost'].includes(host),
  ).toBe(true)
  const scope = createTestScope('response-policies')
  const api = new ApiHelper(request)
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
  const account = await api.createWhatsAppAccount({
    name: scope.name('policy inbox'),
    phone_id: scope.phone().replace(/\D/g, ''),
    business_id: '100000000052',
    access_token: 'synthetic-never-send',
  })
  const contacts = [
    await api.createContact(scope.phone(), 'Synthetic existing case'),
    await api.createContact(scope.phone(), 'Synthetic future case'),
  ]
  const base = '/api/inbox/response-policies/' + account.id
  const thread = (contact: string) =>
    '/api/inbox/accounts/' + account.id + '/contacts/' + contact
  const targets = {
    first_reply_minutes: 30,
    next_reply_minutes: 60,
    resolution_minutes: 120,
    business_hours: false,
    timezone: 'UTC',
    shifts: [],
    date_exceptions: [],
  }
  try {
    await data(await api.put('/api/inbox/sla/policies/' + account.id, targets))
    const existing = (
      await data(
        await api.put(thread(contacts[0].id), {
          status: 'open',
          priority: 'high',
        }),
      )
    ).case
    const body = {
      name: 'Synthetic priority response policy',
      description: 'Prospective HTTP acceptance only',
      enabled: true,
      conditions: {
        match: 'all',
        predicates: [{ field: 'priority', operator: 'eq', value: 'high' }],
      },
      targets: { ...targets, first_reply_minutes: 10 },
    }
    let policy = (await data(await api.post(base, body))).policy
    expect(policy.enabled).toBe(false)
    policy = (
      await data(
        await api.put(base + '/' + policy.id, {
          ...body,
          revision: policy.revision,
        }),
      )
    ).policy
    expect(policy.enabled).toBe(true)
    const sample = await data(
      await api.post(base + '/preview', { case_id: existing.id }),
    )
    expect(sample.prospective_only).toBe(true)
    expect(sample.existing_policy_version).toBe(existing.sla_policy_id)
    expect(sample.evaluations[0]).toMatchObject({
      policy_id: policy.id,
      selected: true,
      reason: 'first_matching_policy',
    })
    const retained = (await data(await api.get(thread(contacts[0].id)))).case
    expect(retained.sla_policy_id).toBe(existing.sla_policy_id)
    expect(retained.version).toBe(existing.version)
    const future = (
      await data(
        await api.put(thread(contacts[1].id), {
          status: 'open',
          priority: 'high',
        }),
      )
    ).case
    expect(future.sla_policy_id).toBe(policy.version_id)
    expect(future.sla_selection).toMatchObject({
      kind: 'conditional',
      policy_id: policy.id,
      position: 0,
    })
    const display = (await data(await api.get(thread(contacts[1].id)))).case
    expect(display.sla.policy_name).toBe(body.name)
    const clone = (
      await data(
        await api.post(base + '/' + policy.id + '/clone', {
          revision: policy.revision,
        }),
      )
    ).policy
    expect(clone.enabled).toBe(false)
    expect(clone.version_id).not.toBe(policy.version_id)
    const updated = (
      await data(
        await api.put(base + '/' + policy.id, {
          ...body,
          name: 'Synthetic edited future policy',
          revision: policy.revision,
        }),
      )
    ).policy
    const stale = await api.put(base + '/' + policy.id, {
      ...body,
      revision: policy.revision,
    })
    expect(stale.status()).toBe(409)
    expect(await stale.text()).toContain('response_policy_conflict')
    const sameCase = (await data(await api.get(thread(contacts[1].id)))).case
    expect(sameCase.sla.policy_name).toBe(body.name)
    expect(sameCase.sla_policy_id).toBe(policy.version_id)
    expect(updated.version_id).not.toBe(policy.version_id)
    const order = await data(
      await api.put(base + '/order', {
        order: [
          { id: clone.id, revision: clone.revision },
          { id: updated.id, revision: updated.revision },
        ],
      }),
    )
    expect(order.reordered).toBe(true)
    await api.login('agent@test.com', 'password')
    expect((await api.get(base)).status()).toBe(403)
    expect(
      (await api.post(base + '/preview', { case_id: existing.id })).status(),
    ).toBe(403)
    await api.loginAsAdmin()
    expect(await data(await api.get('/api/inbox/automation'))).toEqual(
      sendsBefore,
    )
  } finally {
    await api.loginAsAdmin()
    // Fetch current revisions after every archive because ordering is compacted.
    for (let i = 0; i < 3; i++) {
      const list = await api.get(base)
      if (!list.ok()) break
      const remaining = (await list.json()).data.policies
      if (!remaining.length) break
      await data(
        await api.del(base + '/' + remaining[0].id, undefined, {
          revision: remaining[0].revision,
        }),
      )
    }
    await api.del('/api/inbox/sla/policies/' + account.id)
    for (const contact of contacts) await api.del('/api/contacts/' + contact.id)
    await api.del('/api/accounts/' + account.id)
    await api.put('/api/inbox/workflows', { enabled: enabledBefore })
  }
})
