import { expect, test } from '@playwright/test'
import { Client } from 'pg'
import { randomUUID } from 'node:crypto'
import { ApiHelper } from '../../helpers/api'
import { createTestScope } from '../../framework'
import { inboxContrast } from '../../helpers/inbox-contrast'

test('qualifies queues, routing previews, required resolution, workload presets and historical targets', async ({
  page,
  request,
}, testInfo) => {
  test.setTimeout(180000)
  const database = process.env.TEST_DATABASE_URL
  test.skip(
    !database,
    'Synthetic message fixtures require an explicit disposable database',
  )
  const databaseURL = new URL(database!)
  expect(['fixture', 'whatomate_test']).toContain(databaseURL.pathname.slice(1))
  expect(
    databaseURL.hostname === '127.0.0.1' ||
      databaseURL.hostname === 'localhost' ||
      databaseURL.hostname.startsWith('whatomate-inbox-'),
  ).toBeTruthy()
  const scope = createTestScope('inbox-quality'),
    api = new ApiHelper(request)
  await api.loginAsAdmin()
  const before = (await (await api.get('/api/inbox/workflows')).json()).data
    .enabled
  expect(
    (await api.put('/api/inbox/workflows', { enabled: true })).ok(),
  ).toBeTruthy()
  const sendsBefore = (await (await api.get('/api/inbox/automation')).json())
    .data
  expect(sendsBefore.dispatch_available).toBe(false)
  const profile = (await (await api.get('/api/me')).json()).data
  const account = await api.createWhatsAppAccount({
    name: scope.name('support'),
    phone_id: scope.phone().replace(/\D/g, ''),
    business_id: '100000000006',
    access_token: 'synthetic-do-not-send',
  })
  const contact = await api.createContact(
    scope.phone(),
    'Example customer — order enquiry',
  )
  const thread = '/api/inbox/accounts/' + account.id + '/contacts/' + contact.id
  const fields: string[] = [],
    workflows: string[] = []
  let team = '',
    preset = ''
  try {
    expect(
      (
        await api.put('/api/inbox/sla/policies/' + account.id, {
          first_reply_minutes: 30,
          next_reply_minutes: 60,
          resolution_minutes: 480,
          business_hours: false,
          timezone: 'Africa/Johannesburg',
          shifts: [],
        })
      ).ok(),
    ).toBeTruthy()
    const opened = (await (await api.put(thread, { status: 'open' })).json())
      .data.case
    expect(
      (
        await api.post(thread + '/assignment', {
          case_id: opened.id,
          version: opened.version,
          assignee_id: profile.id,
        })
      ).ok(),
    ).toBeTruthy()
    const client = new Client({ connectionString: database })
    await client.connect()
    try {
      await client.query(
        "INSERT INTO messages (id,organization_id,whats_app_account,contact_id,direction,message_type,content,status,created_at,updated_at) VALUES ($1,$2,$3,$4,'incoming','text',$5,'read',NOW(),NOW())",
        [
          randomUUID(),
          profile.organization_id,
          account.name,
          contact.id,
          'Hello, could you help me track my order?',
        ],
      )
      await client.query(
        'UPDATE inbox_cases SET last_inbound_at=NOW() WHERE id=$1 AND organization_id=$2',
        [opened.id, profile.organization_id],
      )
      await client.query(
        'UPDATE contacts SET last_inbound_at=NOW(),last_message_at=NOW() WHERE id=$1 AND organization_id=$2',
        [contact.id, profile.organization_id],
      )
    } finally {
      await client.end()
    }
    for (const definition of [
      { scope: 'case', key: 'ready', label: 'Ready to close', kind: 'boolean' },
      {
        scope: 'contact',
        key: 'order_amount',
        label: 'Order amount',
        kind: 'number',
      },
    ]) {
      const response = await api.post('/api/inbox/fields', {
        ...definition,
        required_on_resolve: true,
        description: 'Synthetic acceptance detail',
      })
      expect(response.ok()).toBeTruthy()
      fields.push((await response.json()).data.definition.id)
    }
    const macroResponse = await api.post('/api/inbox/workflow-definitions', {
      name: scope.name('follow-up'),
      description: 'Review the order enquiry without sending a message',
      kind: 'macro',
      visibility: 'personal',
      trigger: '',
      position: 0,
      conditions: { account_id: account.id },
      actions: [
        { type: 'note', value: 'Synthetic order follow-up' },
        { type: 'status', value: 'waiting_customer' },
      ],
    })
    expect(macroResponse.ok()).toBeTruthy()
    const macro = (await macroResponse.json()).data.workflow
    workflows.push(macro.id)
    const ruleResponse = await api.post('/api/inbox/workflow-definitions', {
      name: scope.name('keyword-rule'),
      description: 'Disabled synthetic routing example',
      kind: 'rule',
      trigger: 'incoming_message',
      position: 12,
      conditions: { message_contains: 'order' },
      actions: [{ type: 'snooze', value: '', duration_minutes: 15 }],
    })
    expect(ruleResponse.ok()).toBeTruthy()
    const rule = (await ruleResponse.json()).data.workflow
    workflows.push(rule.id)
    expect(rule.enabled_at).toBeFalsy()
    await page.context().addCookies((await request.storageState()).cookies)
    await page.addInitScript(
      (user) => localStorage.setItem('user', JSON.stringify(user)),
      profile,
    )
    await page.goto('/chat')
    await expect(
      page.getByRole('button', { name: 'Mine', exact: true }),
    ).toHaveAttribute('aria-pressed', 'true')
    await page.getByRole('button', { name: 'Filters', exact: true }).click()
    await page
      .getByRole('combobox', { name: 'WhatsApp account', exact: true })
      .selectOption(account.id)
    await page.keyboard.press('Escape')
    await expect(
      page.getByText('Hello, could you help me track my order?', {
        exact: true,
      }),
    ).toBeVisible()
    await page.reload()
    await expect(
      page.getByRole('button', { name: 'Mine', exact: true }),
    ).toHaveAttribute('aria-pressed', 'true')
    await page
      .getByRole('button')
      .filter({ hasText: 'Example customer — order enquiry' })
      .click()
    await expect(
      page.getByRole('combobox', { name: 'Case status', exact: true }),
    ).toHaveValue('open')
    await page.keyboard.press('Control+Alt+m')
    await page
      .getByRole('dialog')
      .getByRole('button', { name: macro.name, exact: true })
      .click()
    await page.getByRole('button', { name: 'Apply macro', exact: true }).click()
    await expect(
      page.getByRole('combobox', { name: 'Case status', exact: true }),
    ).toHaveValue('waiting_customer')
    await page
      .getByText('Response targets and deadlines', { exact: true })
      .click()
    await expect(page.getByText(/Resolution · Paused/)).toBeVisible()
    const contrastEvidence: unknown[] = []
    for (const theme of ['dark', 'light']) {
      await page.setViewportSize({ width: 390, height: 844 })
      await page.evaluate(
        (theme) => localStorage.setItem('color-mode', theme),
        theme,
      )
      await page.reload()
      await expect(page.locator('html')).toHaveClass(
        new RegExp(theme === 'light' ? 'light' : 'dark'),
      )
      await expect(
        page.getByRole('combobox', { name: 'Case status', exact: true }),
      ).toHaveValue('waiting_customer')
      await page
        .getByText('Response targets and deadlines', { exact: true })
        .click()
      await expect
        .poll(() =>
          page
            .locator('aside[aria-label="Main navigation"]')
            .evaluate((element) => element.getBoundingClientRect().right),
        )
        .toBeLessThanOrEqual(0)
      expect(
        await page.evaluate(() => document.documentElement.scrollWidth),
      ).toBeLessThanOrEqual(390)
      const contrasts = await inboxContrast(page)
      contrastEvidence.push({ theme, contrasts })
      expect(contrasts.filter((item) => item.ratio < item.minimum)).toEqual([])
      if (process.env.INBOX_CAPTURE)
        await page.screenshot({
          path: testInfo.outputPath('quality-case-' + theme + '-mobile.png'),
          animations: 'disabled',
        })
    }
    await testInfo.attach('measured-inbox-contrast', {
      body: JSON.stringify(contrastEvidence, null, 2),
      contentType: 'application/json',
    })
    await page.getByRole('button', { name: 'Resolve', exact: true }).click()
    const dialog = page.getByRole('dialog')
    await dialog
      .getByRole('combobox', { name: 'Ready to close', exact: true })
      .selectOption('false')
    await dialog
      .getByRole('spinbutton', { name: 'Order amount', exact: true })
      .fill('0')
    if (process.env.INBOX_CAPTURE)
      await page.screenshot({
        path: testInfo.outputPath('quality-required-details-mobile.png'),
        animations: 'disabled',
      })
    await dialog
      .getByRole('button', { name: 'Save and resolve', exact: true })
      .click()
    await expect(
      page.getByRole('combobox', { name: 'Case status', exact: true }),
    ).toHaveValue('resolved')
    const after = (await (await api.get(thread)).json()).data.case
    expect(after.sla_policy_id).toBe(opened.sla_policy_id)
    await page.setViewportSize({ width: 1440, height: 1000 })
    const teamResponse = await api.post('/api/teams', {
      name: scope.name('demo-support'),
      assignment_strategy: 'round_robin',
    })
    expect(teamResponse.ok()).toBeTruthy()
    team = (await teamResponse.json()).data.team.id
    expect(
      (
        await api.post('/api/teams/' + team + '/members', {
          user_id: profile.id,
          role: 'agent',
        })
      ).ok(),
    ).toBeTruthy()
    await page.goto('/settings/teams/' + team)
    const capacity = page.getByRole('region', {
      name: 'Inbox workload',
      exact: true,
    })
    await capacity
      .getByRole('combobox', { name: 'WhatsApp account', exact: true })
      .selectOption(account.id)
    await page.getByText('Capacity presets', { exact: true }).first().click()
    await page.getByRole('button', { name: 'Add', exact: true }).click()
    const presetForm = page
      .locator('form')
      .filter({ has: page.getByLabel('Preset name', { exact: true }) })
    await presetForm
      .getByLabel('Preset name', { exact: true })
      .fill(scope.name('four-active'))
    await presetForm.getByRole('spinbutton').fill('4')
    const [savedPreset] = await Promise.all([
      page.waitForResponse(
        (r) =>
          new URL(r.url()).pathname === '/api/inbox/capacity-presets' &&
          r.request().method() === 'POST',
      ),
      presetForm.getByRole('button', { name: 'Save', exact: true }).click(),
    ])
    expect(savedPreset.ok()).toBeTruthy()
    preset = (await savedPreset.json()).data.preset.id
    await page
      .getByRole('combobox', { name: 'Capacity presets', exact: true })
      .selectOption(preset)
    await page
      .getByRole('button', { name: 'Preview and apply', exact: true })
      .click()
    await expect(
      page.getByRole('dialog').getByText('Unlimited → 4', { exact: true }),
    ).toBeVisible()
    await page
      .getByRole('dialog')
      .getByRole('button', { name: 'Apply changes', exact: true })
      .click()
    await expect(
      capacity.getByRole('spinbutton', {
        name: 'Case limit for ' + profile.full_name,
      }),
    ).toHaveValue('4')
    await page.goto('/analytics/agents')
    await page.getByRole('tab', { name: 'Live inbox', exact: true }).click()
    await page.getByRole('tab', { name: 'Agent workload', exact: true }).click()
    await expect(
      page.getByRole('heading', { name: 'Agent workload', exact: true }),
    ).toBeVisible()
    await expect(
      page
        .locator('.inbox-list-title')
        .filter({ hasText: profile.full_name })
        .first(),
    ).toBeVisible()
    const workload = (
      await (
        await api.get(
          '/api/inbox/workload?account_id=' + account.id + '&team_id=' + team,
        )
      ).json()
    ).data.agents
    expect(workload).toContainEqual(
      expect.objectContaining({
        agent_id: profile.id,
        name: profile.full_name,
        max_active: 4,
      }),
    )
    if (process.env.INBOX_CAPTURE)
      await page.screenshot({
        path: testInfo.outputPath('quality-workload-desktop.png'),
        fullPage: true,
      })
    await page
      .getByRole('tab', { name: 'Completed response targets', exact: true })
      .click()
    await expect(
      page
        .getByRole('link', { name: /Example customer — order enquiry/ })
        .first(),
    ).toBeVisible()
    if (process.env.INBOX_CAPTURE)
      await page.screenshot({
        path: testInfo.outputPath('quality-historical-targets-desktop.png'),
        animations: 'disabled',
        fullPage: true,
      })
    expect(
      (
        await (
          await api.get('/api/inbox/workflow-definitions?kind=rule')
        ).json()
      ).data.workflows.find((item: { id: string }) => item.id === rule.id)
        .enabled_at,
    ).toBeFalsy()
    expect(
      (await (await api.get('/api/inbox/automation')).json()).data,
    ).toEqual(sendsBefore)
  } finally {
    for (const id of workflows)
      await api.del('/api/inbox/workflow-definitions/' + id)
    for (const id of fields)
      await api.put('/api/inbox/fields/' + id, { archive: true })
    if (preset) await api.del('/api/inbox/capacity-presets/' + preset)
    if (team) await api.del('/api/teams/' + team)
    await api.del('/api/inbox/sla/policies/' + account.id)
    await api.del('/api/contacts/' + contact.id)
    await api.del('/api/accounts/' + account.id)
    await api.put('/api/inbox/workflows', { enabled: before })
  }
})
