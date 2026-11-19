import { expect, test } from '@playwright/test'
import { ApiHelper } from '../../helpers/api'
import { createTestScope } from '../../framework'

test('assigned agents can complete required case fields without rule administration access', async ({
  page,
  request,
}, testInfo) => {
  page.setDefaultTimeout(12000)
  const scope = createTestScope('inbox-agent-fields')
  const api = new ApiHelper(request)
  await api.loginAsAdmin()
  const before = (await (await api.get('/api/inbox/workflows')).json()).data
    .enabled
  const account = await api.createWhatsAppAccount({
    name: scope.name('inbox'),
    phone_id: scope.phone().replace(/\D/g, ''),
    business_id: '100000000006',
    access_token: 'local-fixture-no-send',
  })
  const contact = await api.createContact(scope.phone(), scope.name('customer'))
  const label = scope.name('Reference')
  const definition = await api.post('/api/inbox/fields', {
    label,
    key: 'agent_reference_' + Date.now(),
    scope: 'case',
    kind: 'text',
    required_on_resolve: true,
  })
  expect(definition.ok()).toBeTruthy()
  const field = (await definition.json()).data.definition.id
  const thread = '/api/inbox/accounts/' + account.id + '/contacts/' + contact.id
  try {
    expect(
      (await api.put('/api/inbox/workflows', { enabled: true })).ok(),
    ).toBeTruthy()
    const users = (await (await api.get('/api/users')).json()).data.users
    const agent = users.find(
      (u: { email: string }) => u.email === 'agent@test.com',
    )
    expect(agent).toBeTruthy()
    const opened = await api.put(thread, { status: 'open', priority: 'normal' })
    const current = (await opened.json()).data.case
    const assignment = await api.post('/api/inbox/cases/bulk', {
      action: 'assign',
      items: [{ id: current.id, version: current.version }],
      assignee_id: agent.id,
    })
    expect((await assignment.json()).data.results[0].result).toBe('updated')
    await api.login('agent@test.com', 'password')
    const profile = (await (await api.get('/api/me')).json()).data
    expect(
      (await api.get('/api/inbox/workflow-definitions?kind=rule')).status(),
    ).toBe(403)
    expect(
      (await api.put('/api/inbox/automation', { enabled: true })).status(),
    ).toBe(403)
    await page.context().addCookies((await request.storageState()).cookies)
    await page.addInitScript(
      (user) => localStorage.setItem('user', JSON.stringify(user)),
      profile,
    )
    await page.goto(
      '/chat/' +
        contact.id +
        '?inbox_account=' +
        encodeURIComponent(account.name) +
        '&inbox_account_id=' +
        account.id,
    )
    await page.getByRole('button', { name: 'Resolve', exact: true }).click()
    const input = page.getByRole('textbox', { name: label, exact: true })
    await expect(input).toBeEditable()
    await input.fill('DEMO-AGENT-REFERENCE')
    const row = page.locator('li').filter({ has: input })
    await row.getByRole('button', { name: 'Save', exact: true }).click()
    await expect(
      row.getByRole('button', { name: 'Save', exact: true }),
    ).toBeDisabled()
    if (process.env.INBOX_CAPTURE)
      await page.screenshot({
        path: testInfo.outputPath('inbox-assigned-agent-required-fields.png'),
      })
    await page.getByRole('button', { name: 'Resolve', exact: true }).click()
    await expect(
      page.getByRole('combobox', { name: 'Case status', exact: true }),
    ).toHaveValue('resolved')
    expect(
      (await (await api.get('/api/inbox/automation')).json()).data.enabled,
    ).toBe(false)
  } finally {
    await api.loginAsAdmin()
    await api.put('/api/inbox/fields/' + field, { archive: true })
    await api.del('/api/contacts/' + contact.id)
    await api.del('/api/accounts/' + account.id)
    await api.put('/api/inbox/workflows', { enabled: before })
  }
})
