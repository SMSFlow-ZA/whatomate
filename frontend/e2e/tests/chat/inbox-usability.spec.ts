import { expect, test } from '@playwright/test'
import { ApiHelper } from '../../helpers/api'
import { createTestScope } from '../../framework'

test('manages inbox settings with keyboard navigation, safe defaults and responsive layouts', async ({
  page,
  request,
}, testInfo) => {
  page.setDefaultTimeout(12000)
  const scope = createTestScope('inbox-usability')
  const api = new ApiHelper(request)
  await api.loginAsAdmin()
  const before = (await (await api.get('/api/inbox/workflows')).json()).data
    .enabled
  expect((await api.put('/api/inbox/workflows', { enabled: true })).ok()).toBeTruthy()
  const automaticBefore = (
    await (await api.get('/api/inbox/automation')).json()
  ).data.enabled
  const account = await api.createWhatsAppAccount({
    name: scope.name('support'),
    phone_id: scope.phone().replace(/\D/g, ''),
    business_id: '100000000005',
    access_token: 'local-fixture-no-send',
  })
  const profile = (await (await api.get('/api/me')).json()).data
  const createdTeam = await api.post('/api/teams', {
    name: scope.name('team'),
    assignment_strategy: 'round_robin',
  })
  expect(createdTeam.ok()).toBeTruthy()
  const team = (await createdTeam.json()).data.team.id
  expect(
    (
      await api.post('/api/teams/' + team + '/members', {
        user_id: profile.id,
        role: 'agent',
      })
    ).ok(),
  ).toBeTruthy()
  let workflow = '',
    field = ''
  try {
    expect(
      (await api.put('/api/inbox/workflows', { enabled: true })).ok(),
    ).toBeTruthy()
    await page.context().addCookies((await request.storageState()).cookies)
    await page.addInitScript(
      (user) => localStorage.setItem('user', JSON.stringify(user)),
      profile,
    )
    await page.goto('/settings/inbox-workflows')
    await page.getByRole('button', { name: 'New rule', exact: true }).first().click()
    const ruleForm = page.locator('#inbox-workflow-editor')
    await ruleForm.getByLabel('Workflow name').fill(scope.name('safe-rule'))
    await ruleForm
      .getByRole('combobox', { name: 'WhatsApp account', exact: true })
      .selectOption(account.id)
    await ruleForm
      .getByRole('combobox', { name: 'Action', exact: true })
      .selectOption('priority')
    await ruleForm
      .getByRole('combobox', { name: 'Action value', exact: true })
      .selectOption('high')
    const [savedRule] = await Promise.all([
      page.waitForResponse(
        (r) =>
          new URL(r.url()).pathname === '/api/inbox/workflow-definitions' &&
          r.request().method() === 'POST',
      ),
      ruleForm.getByRole('button', { name: 'Save', exact: true }).click(),
    ])
    expect(savedRule.ok()).toBeTruthy()
    const rule = (await savedRule.json()).data.workflow
    workflow = rule.id
    expect(rule.enabled_at).toBeFalsy()
    const rulesTab = page.getByRole('tab', { name: 'Rules', exact: true })
    await rulesTab.focus()
    await page.keyboard.press('ArrowRight')
    await expect(
      page.getByRole('tab', { name: 'Macros', exact: true }),
    ).toBeFocused()
    await expect(
      page.getByRole('tab', { name: 'Macros', exact: true }),
    ).toHaveAttribute('aria-selected', 'true')
    await page.keyboard.press('End')
    await expect(
      page.getByRole('tab', { name: 'Response targets', exact: true }),
    ).toBeFocused()
    await page
      .getByRole('combobox', { name: 'WhatsApp account', exact: true })
      .selectOption(account.id)
    await page.getByLabel('Count business hours').check()
    await page
      .getByLabel('Time zone', { exact: true })
      .fill('Africa/Johannesburg')
    await page
      .getByRole('button', { name: 'Add business hours', exact: true })
      .click()
    const [savedTargets] = await Promise.all([
      page.waitForResponse(
        (r) =>
          new URL(r.url()).pathname ===
            '/api/inbox/sla/policies/' + account.id &&
          r.request().method() === 'PUT',
      ),
      page.getByRole('button', { name: 'Save', exact: true }).click(),
    ])
    expect(savedTargets.ok()).toBeTruthy()
    await page
      .getByRole('button', { name: 'Remove targets', exact: true })
      .click()
    await expect(page.getByRole('alertdialog')).toBeVisible()
    await page
      .getByRole('alertdialog')
      .getByRole('button', { name: 'Cancel', exact: true })
      .click()
    expect(
      (
        await (await api.get('/api/inbox/sla/policies')).json()
      ).data.policies.some(
        (p: { account_id: string }) => p.account_id === account.id,
      ),
    ).toBeTruthy()

    await page.goto('/settings/teams/' + team)
    const capacity = page.getByRole('region', {
      name: 'Inbox workload',
      exact: true,
    })
    await capacity
      .getByRole('combobox', { name: 'WhatsApp account', exact: true })
      .selectOption(account.id)
    await capacity
      .getByRole('spinbutton', { name: 'Case limit for ' + profile.full_name })
      .fill('3')
    const [savedCapacity] = await Promise.all([
      page.waitForResponse(
        (r) =>
          new URL(r.url()).pathname.includes('/capacity/' + account.id) &&
          r.request().method() === 'PUT',
      ),
      capacity.getByRole('button', { name: 'Save', exact: true }).click(),
    ])
    expect(savedCapacity.ok()).toBeTruthy()

    await page.goto('/settings/custom-fields')
    await page.getByRole('button', { name: 'New field', exact: true }).first().click()
    const fieldForm = page.locator('#inbox-field-editor')
    const label = scope.name('Reference')
    await fieldForm.getByLabel('Field label', { exact: true }).fill(label)
    await fieldForm
      .getByLabel('Field key', { exact: true })
      .fill('reference_' + Date.now())
    const [savedField] = await Promise.all([
      page.waitForResponse(
        (r) =>
          new URL(r.url()).pathname === '/api/inbox/fields' &&
          r.request().method() === 'POST',
      ),
      fieldForm.getByRole('button', { name: 'Save', exact: true }).click(),
    ])
    expect(savedField.ok()).toBeTruthy()
    field = (await savedField.json()).data.definition.id
    const row = page
      .locator('li')
      .filter({ has: page.getByText(label, { exact: true }) })
    await row.getByRole('button', { name: 'Edit', exact: true }).click()
    await expect(page.getByLabel('Field key', { exact: true })).toBeDisabled()
    await page
      .locator('#inbox-field-editor')
      .getByRole('button', { name: 'Cancel', exact: true })
      .click()
    await row
      .getByRole('button', { name: 'Archive field ' + label, exact: true })
      .click()
    await expect(page.getByRole('alertdialog')).toBeVisible()
    await page
      .getByRole('alertdialog')
      .getByRole('button', { name: 'Cancel', exact: true })
      .click()
    await expect(row).toBeVisible()

    for (const theme of ['dark', 'light']) {
      await page.evaluate(
        (value) => localStorage.setItem('color-mode', value),
        theme,
      )
      for (const width of [1440, 390]) {
        await page.setViewportSize({
          width,
          height: width === 390 ? 844 : 1000,
        })
        for (const [name, path] of [
          ['workflows', '/settings/inbox-workflows'],
          ['fields', '/settings/custom-fields'],
          ['team', '/settings/teams/' + team],
          ['supervisor', '/analytics/agents'],
        ] as const) {
          await page.goto(path, { waitUntil: 'networkidle' })
          if (name === 'supervisor') {
            await page
              .getByRole('tab', { name: 'Live inbox', exact: true })
              .click()
            await expect(
              page.getByRole('heading', {
                name: 'Inbox deadlines',
                exact: true,
              }),
            ).toBeVisible()
          }
          if (name === 'team') {
            const workload = page.getByRole('region', {
              name: 'Inbox workload',
              exact: true,
            })
            await workload
              .getByRole('combobox', { name: 'WhatsApp account', exact: true })
              .selectOption(account.id)
            await expect(
              workload.getByRole('spinbutton', {
                name: 'Case limit for ' + profile.full_name,
              }),
            ).toHaveValue('3')
            await workload.scrollIntoViewIfNeeded()
          }
          expect(
            await page.evaluate(() => document.documentElement.scrollWidth),
          ).toBeLessThanOrEqual(width)
          await expect(
            page.getByRole('link', { name: 'Source code (AGPL)' }),
          ).toBeVisible()
          if (process.env.INBOX_CAPTURE)
            await page.screenshot({
              path: testInfo.outputPath(
                'inbox-' + name + '-' + theme + '-' + width + '.png',
              ),
              fullPage: true,
            })
        }
      }
    }
    expect(
      (await (await api.get('/api/inbox/automation')).json()).data.enabled,
    ).toBe(automaticBefore)
  } finally {
    if (workflow) await api.del('/api/inbox/workflow-definitions/' + workflow)
    if (field) await api.put('/api/inbox/fields/' + field, { archive: true })
    await api.del('/api/inbox/sla/policies/' + account.id)
    await api.del('/api/teams/' + team)
    await api.del('/api/accounts/' + account.id)
    await api.put('/api/inbox/workflows', { enabled: before })
  }
})

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
