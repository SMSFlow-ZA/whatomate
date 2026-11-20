import { expect, test } from '@playwright/test'
import { ApiHelper } from '../../helpers/api'
import { createTestScope } from '../../framework'

test('runs the inbox support workflow on an isolated workspace', async ({
  page,
  request,
}, testInfo) => {
  test.setTimeout(120000)
  page.setDefaultTimeout(20000)
  const scope = createTestScope('inbox-support')
  const api = new ApiHelper(request)
  await api.loginAsAdmin()
  const before = await api.get('/api/inbox/workflows')
  expect(before.ok()).toBeTruthy()
  const wasEnabled = (await before.json()).data.enabled
  const account = await api.createWhatsAppAccount({
    name: scope.name('inbox'),
    phone_id: scope.phone().replace(/\D/g, ''),
    business_id: '100000000004',
    access_token: 'local-fixture-no-messages',
  })
  const contact = await api.createContact(
    '+12025550197',
    'Example inbox support customer',
  )
  const thread = '/api/inbox/accounts/' + account.id + '/contacts/' + contact.id
  const workflowPath = '/api/inbox/workflow-definitions'
  const ids: string[] = []
  let field = ''
  let team = ''
  try {
    expect(
      (await api.put('/api/inbox/workflows', { enabled: true })).ok(),
    ).toBeTruthy()
    expect(
      (
        await api.put('/api/inbox/sla/policies/' + account.id, {
          first_reply_minutes: 60,
          next_reply_minutes: 60,
          resolution_minutes: 180,
          timezone: 'Africa/Johannesburg',
          business_hours: false,
          shifts: [],
        })
      ).ok(),
    ).toBeTruthy()

    const profile = await api.get('/api/me')
    const { data: user } = await profile.json()
    const teamName = scope.name('support')
    const teamReply = await api.post('/api/teams', {
      name: teamName,
      assignment_strategy: 'round_robin',
    })
    expect(teamReply.ok()).toBeTruthy()
    team = (await teamReply.json()).data.team.id
    expect(
      (
        await api.post('/api/teams/' + team + '/members', {
          user_id: user.id,
          role: 'agent',
        })
      ).ok(),
    ).toBeTruthy()
    const capacity = '/api/inbox/teams/' + team + '/capacity/' + account.id
    expect(
      (await api.put(capacity, { agent_id: user.id, max_active: 2 })).ok(),
    ).toBeTruthy()
    const workload = await api.get(capacity)
    expect((await workload.json()).data.agents).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ agent_id: user.id, max_active: 2 }),
      ]),
    )

    await page.context().addCookies((await request.storageState()).cookies)
    await page.addInitScript(
      (user) => localStorage.setItem('user', JSON.stringify(user)),
      user,
    )

    await page.goto('/settings/custom-fields', {
      waitUntil: 'domcontentloaded',
    })
    await page
      .getByRole('button', { name: 'New field', exact: true })
      .first()
      .click()
    const fieldForm = page.locator('form').filter({
      has: page.getByRole('heading', { name: 'Add a custom field' }),
    })
    const label = scope.name('Reference')
    await fieldForm.getByLabel('Field label', { exact: true }).fill(label)
    const key = 'reference_' + Date.now()
    await fieldForm.getByLabel('Advanced: edit the generated field key').check()
    await fieldForm.getByLabel('Field key', { exact: true }).fill(key)
    await fieldForm.getByLabel('Required to resolve').check()
    const [savedField] = await Promise.all([
      page.waitForResponse(
        (response) =>
          new URL(response.url()).pathname === '/api/inbox/fields' &&
          response.request().method() === 'POST',
        { timeout: 12000 },
      ),
      fieldForm.getByRole('button', { name: 'Save', exact: true }).click(),
    ])
    expect(savedField.ok()).toBeTruthy()
    field = (await savedField.json()).data.definition.id
    await expect(page.getByText(key, { exact: false })).toBeVisible({
      timeout: 15000,
    })

    const rule = {
      name: scope.name('triage'),
      kind: 'rule',
      trigger: 'case_opened',
      position: 1,
      conditions: { account_id: account.id },
      actions: [{ type: 'priority', value: 'high' }],
    }
    const createRule = await api.post(workflowPath, rule)
    expect(createRule.ok()).toBeTruthy()
    const ruleID = (await createRule.json()).data.workflow.id
    ids.push(ruleID)
    expect(
      (
        await api.put(workflowPath + '/' + ruleID, { ...rule, enabled: true })
      ).ok(),
    ).toBeTruthy()

    const macroName = scope.name('waiting-for-details')
    const macro = await api.post(workflowPath, {
      name: macroName,
      kind: 'macro',
      trigger: '',
      position: 0,
      conditions: { account_id: account.id },
      actions: [
        { type: 'note', value: 'Synthetic support follow-up' },
        { type: 'status', value: 'waiting_customer' },
      ],
    })
    expect(macro.ok()).toBeTruthy()
    ids.push((await macro.json()).data.workflow.id)

    const chat =
      '/chat/' +
      contact.id +
      '?inbox_account=' +
      encodeURIComponent(account.name) +
      '&inbox_account_id=' +
      account.id
    await page.goto(chat, { waitUntil: 'domcontentloaded' })
    await page.getByRole('button', { name: 'Start case' }).click()
    await expect
      .poll(
        async () => (await (await api.get(thread)).json()).data.case?.priority,
        { timeout: 12000 },
      )
      .toBe('high')

    await page.getByRole('button', { name: 'Macros', exact: true }).click()
    await page
      .getByRole('dialog')
      .getByRole('button', { name: macroName, exact: true })
      .click()
    await expect(
      page.getByText('This case matches the workflow.'),
    ).toBeVisible()
    await page.getByRole('button', { name: 'Apply macro' }).click()
    await expect
      .poll(
        async () =>
          (await (await api.get(thread)).json()).data.case?.sla?.resolution
            ?.status,
      )
      .toBe('paused')
    await page
      .getByText('Response targets and deadlines', { exact: true })
      .click()
    await expect(
      page.getByLabel('Inbox workflow').getByText(/Resolution · Paused/),
    ).toBeVisible({ timeout: 25000 })

    await page.getByRole('button', { name: 'Resolve', exact: true }).click()
    const requiredInput = page.getByRole('textbox', {
      name: label,
      exact: true,
    })
    await expect(requiredInput).toBeVisible()
    await expect(requiredInput).toBeEditable()
    await requiredInput.fill('SUPPORT-FIXTURE')
    await page
      .getByRole('button', { name: 'Save and resolve', exact: true })
      .click()
    await expect(
      page.getByRole('combobox', { name: 'Case status' }),
    ).toHaveValue('resolved')
    const archived = await api.put('/api/inbox/fields/' + field, {
      archive: true,
    })
    expect(archived.ok()).toBeTruthy()
    const filter = encodeURIComponent(
      JSON.stringify({
        fields: [{ definition_id: field, value: 'SUPPORT-FIXTURE' }],
      }),
    )
    const queue = await api.get('/api/inbox/queue?filters=' + filter)
    expect((await queue.json()).data.cases).toHaveLength(1)

    await page.goto('/settings/inbox-workflows', {
      waitUntil: 'domcontentloaded',
    })
    await page.getByRole('tab', { name: 'History', exact: true }).click()
    await expect(
      page.getByText('Actions applied', { exact: false }).first(),
    ).toBeVisible()
    await page.getByRole('tab', { name: 'Rules', exact: true }).click()
    await page
      .locator('li')
      .filter({ hasText: rule.name })
      .getByRole('button', { name: 'Edit', exact: true })
      .click()
    const teamOption = page.locator('option', { hasText: teamName })
    const ruleTeam = page.locator('select').filter({ has: teamOption })
    await ruleTeam.selectOption(team)
    await expect(ruleTeam).toHaveValue(team)

    await page.getByRole('button', { name: 'Cancel', exact: true }).click()
    await page.getByRole('button', { name: 'Leave', exact: true }).click()
    await page.goto('/analytics/agents', { waitUntil: 'domcontentloaded' })
    await page.getByRole('tab', { name: 'Live inbox', exact: true }).click()
    const slaReport = page
      .locator('section')
      .filter({ has: page.getByRole('heading', { name: 'Inbox deadlines' }) })
    const reportTeam = slaReport.locator('select').filter({ has: teamOption })
    await reportTeam.selectOption(team)
    await expect(reportTeam).toHaveValue(team)
    if (process.env.INBOX_CAPTURE)
      await page.screenshot({
        path: testInfo.outputPath('inbox-workflow-history-desktop.png'),
        fullPage: true,
      })

    await page.setViewportSize({ width: 390, height: 844 })
    await page.addInitScript(() => localStorage.setItem('color-mode', 'light'))
    await page.goto(chat, { waitUntil: 'domcontentloaded' })
    await expect(
      page.getByRole('combobox', { name: 'Case status' }),
    ).toHaveValue('resolved')
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(390)
    if (process.env.INBOX_CAPTURE)
      await page.screenshot({
        path: testInfo.outputPath('inbox-support-case-mobile.png'),
      })
  } finally {
    for (const id of ids) await api.del(workflowPath + '/' + id)
    if (field) await api.put('/api/inbox/fields/' + field, { archive: true })
    if (team) await api.del('/api/teams/' + team)
    await api.del('/api/inbox/sla/policies/' + account.id)
    await api.put('/api/inbox/workflows', { enabled: wasEnabled })
    await api.del('/api/contacts/' + contact.id)
    await api.del('/api/accounts/' + account.id)
  }
})
