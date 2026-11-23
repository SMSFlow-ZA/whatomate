import { test, expect } from '@playwright/test'
import { Client } from 'pg'
import { randomUUID } from 'node:crypto'
import { ApiHelper } from '../../helpers/api'
import { createTestScope } from '../../framework'
import { inboxContrast } from '../../helpers/inbox-contrast'

test('reviews exact historical evidence, keyboard previews, resolution recovery and all-case dry runs', async ({
  page,
  request,
}, testInfo) => {
  test.setTimeout(180000)
  const database = process.env.TEST_DATABASE_URL
  test.skip(!database, 'Requires an explicitly disposable database')
  const dbURL = new URL(database!)
  expect(['fixture', 'whatomate_test']).toContain(dbURL.pathname.slice(1))
  expect(
    dbURL.hostname === '127.0.0.1' ||
      dbURL.hostname === 'localhost' ||
      dbURL.hostname.startsWith('whatomate-inbox-'),
  ).toBeTruthy()
  const scope = createTestScope('inbox-followup'),
    api = new ApiHelper(request)
  await api.loginAsAdmin()
  const before = (await (await api.get('/api/inbox/workflows')).json()).data
    .enabled
  await api.put('/api/inbox/workflows', { enabled: true })
  const dispatch = (await (await api.get('/api/inbox/automation')).json()).data
  expect(dispatch.dispatch_available).toBe(false)
  const profile = (await (await api.get('/api/me')).json()).data
  const account = await api.createWhatsAppAccount({
    name: scope.name('support'),
    phone_id: scope.phone().replace(/\D/g, ''),
    business_id: '100000000008',
    access_token: 'synthetic-do-not-send',
  })
  const contact = await api.createContact(
    scope.phone(),
    'Example historical enquiry',
  )
  const thread = '/api/inbox/accounts/' + account.id + '/contacts/' + contact.id
  const contacts = [contact.id],
    fields: string[] = [],
    workflows: string[] = []
  let client: Client | undefined
  try {
    const field = (
      await (
        await api.post('/api/inbox/fields', {
          scope: 'case',
          key: scope
            .name('reference')
            .toLowerCase()
            .replace(/[^a-z0-9_]/g, '_')
            .slice(0, 64),
          label: 'Historical reference',
          kind: 'text',
        })
      ).json()
    ).data.definition
    fields.push(field.id)
    let old = (await (await api.put(thread, { status: 'open' })).json()).data
      .case
    await api.put(thread + '/fields/' + field.id, {
      subject_id: old.id,
      value: 'OLD-REFERENCE',
    })
    old = (
      await (
        await api.put(thread, {
          case_id: old.id,
          version: old.version,
          status: 'resolved',
        })
      ).json()
    ).data.case
    let current = (
      await (
        await api.put(thread, {
          case_id: old.id,
          version: old.version,
          status: 'open',
          priority: 'high',
        })
      ).json()
    ).data.case
    await api.put(thread + '/fields/' + field.id, {
      subject_id: current.id,
      value: 'CURRENT-REFERENCE',
    })
    await api.put('/api/inbox/fields/' + field.id, { archive: true })
    client = new Client({ connectionString: database })
    await client.connect()
    await client.query(
      "INSERT INTO messages(id,organization_id,whats_app_account,contact_id,direction,message_type,content,status,created_at,updated_at) VALUES($1,$2,$3,$4,'incoming','text','Synthetic history review; no real message','read',NOW(),NOW())",
      [randomUUID(), profile.organization_id, account.name, contact.id],
    )
    await client.query(
      'UPDATE contacts SET last_inbound_at=NOW(),last_message_at=NOW() WHERE id=$1 AND organization_id=$2',
      [contact.id, profile.organization_id],
    )
    await client.query(
      "INSERT INTO inbox_case_activities(id,created_at,updated_at,organization_id,account_id,case_id,kind,detail) SELECT gen_random_uuid(),NOW()+n*interval '1 millisecond',NOW(),$1,$2,$3,'workflow_note','Synthetic history record '||lpad(n::text,3,'0') FROM generate_series(1,55) n",
      [profile.organization_id, account.id, old.id],
    )
    const url = (id: string) =>
      '/chat/' +
      contact.id +
      '?inbox_account=' +
      encodeURIComponent(account.name) +
      '&inbox_account_id=' +
      account.id +
      '&case_id=' +
      id
    await page.context().addCookies((await request.storageState()).cookies)
    await page.addInitScript(
      (user) => localStorage.setItem('user', JSON.stringify(user)),
      profile,
    )
    await page.goto(url(old.id))
    await expect(
      page.getByText('Viewing historical case ' + old.id.slice(0, 8), {
        exact: true,
      }),
    ).toBeVisible()
    await expect(
      page.getByRole('combobox', { name: 'Case status' }),
    ).toHaveCount(0)
    await expect(page.getByPlaceholder('Type a message...')).toHaveCount(0)
    await page
      .getByRole('button', { name: 'Case details', exact: true })
      .click()
    await expect(
      page.getByRole('textbox', { name: 'Historical reference', exact: true }),
    ).toHaveValue('OLD-REFERENCE')
    await expect(
      page.getByRole('textbox', { name: 'Historical reference', exact: true }),
    ).toBeDisabled()
    await page.getByText('Case activity', { exact: true }).click()
    await page
      .getByRole('button', { name: 'Load older activity', exact: true })
      .click()
    await expect(
      page.getByText('Synthetic history record 001', { exact: false }),
    ).toBeVisible()
    await expect(
      page.getByText('Synthetic history record 055', { exact: false }),
    ).toBeVisible()
    if (process.env.INBOX_CAPTURE)
      await page.screenshot({
        path: testInfo.outputPath('followup-historical-desktop.png'),
        fullPage: true,
      })
    await page
      .getByRole('link', { name: 'Return to current case', exact: true })
      .click()
    await expect(
      page.getByRole('combobox', { name: 'Case status' }),
    ).toHaveValue('open')
    // Dismiss details so the composer remains the protected focus surface.
    await page.getByRole('button', { name: 'Contact Info' }).click()
    await page.getByRole('button', { name: 'Resolve', exact: true }).click()
    const confirmation = page.getByRole('dialog')
    await expect(
      confirmation.getByRole('heading', {
        name: 'Resolve this case?',
        exact: true,
      }),
    ).toBeVisible()
    await expect(
      confirmation.getByText('Example historical enquiry', { exact: false }),
    ).toBeVisible()
    await confirmation
      .getByRole('button', { name: 'Cancel', exact: true })
      .click()
    const macro = (
      await (
        await api.post('/api/inbox/workflow-definitions', {
          name: scope.name('internal-macro'),
          kind: 'macro',
          actions: [{ type: 'note', value: 'Synthetic preview only' }],
        })
      ).json()
    ).data.workflow
    workflows.push(macro.id)
    await page.getByRole('button', { name: 'Macros', exact: true }).click()
    const picker = page.getByRole('dialog'),
      option = picker.getByRole('button', { name: macro.name, exact: true })
    await option.focus()
    await option.press('Enter')
    await expect(
      picker.getByText(
        'Preview ready. Review the recipient and actions before applying.',
        { exact: true },
      ),
    ).toHaveCount(1)
    await expect
      .poll(() =>
        page.evaluate(() => document.activeElement?.getAttribute('aria-label')),
      )
      .toBe(macro.name)
    await picker.press('Escape')
    const required = (
      await (
        await api.post('/api/inbox/fields', {
          scope: 'case',
          key: scope
            .name('required')
            .toLowerCase()
            .replace(/[^a-z0-9_]/g, '_')
            .slice(0, 64),
          label: 'Review reference',
          kind: 'text',
          required_on_resolve: true,
        })
      ).json()
    ).data.definition
    fields.push(required.id)
    await page.getByRole('button', { name: 'Resolve', exact: true }).click()
    const details = page.getByRole('dialog'),
      input = details.getByRole('textbox', { name: /Review reference/ })
    await input.fill('Draft reference')
    await page.route('**' + thread + '/resolve', (route) =>
      route.fulfill({
        status: 400,
        contentType: 'application/json',
        body: JSON.stringify({
          status: 'error',
          message: 'Synthetic validation failure',
          data: {
            code: 'invalid_values',
            field_errors: [
              { definition_id: required.id, code: 'invalid_value' },
            ],
          },
        }),
      }),
    )
    await details
      .getByRole('button', { name: 'Save and resolve', exact: true })
      .click()
    await expect(input).toHaveAttribute('aria-invalid', 'true')
    await expect(input).toBeFocused()
    await expect(input).toHaveValue('Draft reference')
    if (process.env.INBOX_CAPTURE)
      await page.screenshot({
        path: testInfo.outputPath('followup-resolution-recovery.png'),
        fullPage: true,
      })
    await details.getByRole('button', { name: 'Cancel', exact: true }).click()
    await page.unroute('**' + thread + '/resolve')
    await api.put('/api/inbox/fields/' + required.id, { archive: true })
    let last: any
    for (let i = 0; i < 51; i++) {
      const c = await api.createContact(scope.phone(), 'Chooser enquiry ' + i)
      contacts.push(c.id)
      last = (
        await (
          await api.put(
            '/api/inbox/accounts/' + account.id + '/contacts/' + c.id,
            { status: 'open' },
          )
        ).json()
      ).data.case
    }
    const rule = (
      await (
        await api.post('/api/inbox/workflow-definitions', {
          name: scope.name('disabled-rule'),
          kind: 'rule',
          trigger: 'case_opened',
          actions: [{ type: 'note', value: 'Synthetic preview only' }],
        })
      ).json()
    ).data.workflow
    workflows.push(rule.id)
    await page.goto('/settings/inbox-workflows')
    const row = page.locator('li').filter({ hasText: rule.name })
    await row.getByRole('button', { name: 'Dry run', exact: true }).click()
    const dryRun = page.getByRole('region', { name: 'Dry run', exact: true })
    await expect(
      dryRun.getByRole('button', { name: 'Next', exact: true }),
    ).toBeEnabled()
    await dryRun.getByRole('button', { name: 'Next', exact: true }).click()
    const chooser = dryRun.getByRole('combobox', {
      name: 'Choose a case',
      exact: true,
    })
    await expect(
      chooser.locator('option[value="' + last.id + '"]'),
    ).toHaveCount(1)
    await chooser.selectOption(last.id)
    await dryRun
      .getByRole('button', { name: 'Review actions', exact: true })
      .click()
    await expect(
      dryRun.getByText('This case matches the workflow.', { exact: true }),
    ).toBeVisible()
    expect(
      (
        await (
          await api.get('/api/inbox/workflow-definitions?kind=rule')
        ).json()
      ).data.workflows.find((w: any) => w.id === rule.id).enabled_at,
    ).toBeFalsy()
    expect(
      (await (await api.get('/api/inbox/automation')).json()).data,
    ).toEqual(dispatch)
    expect(
      (
        await client.query(
          "SELECT count(*)::int AS n FROM messages WHERE organization_id=$1 AND whats_app_account=$2 AND direction='outgoing'",
          [profile.organization_id, account.name],
        )
      ).rows[0].n,
    ).toBe(0)
    for (const theme of ['light', 'dark']) {
      await page.setViewportSize({ width: 390, height: 844 })
      await page.goto(url(old.id))
      await page.evaluate((theme) => {
        document.documentElement.classList.toggle('light', theme === 'light')
        document.documentElement.classList.toggle('dark', theme === 'dark')
      }, theme)
      await expect(
        page.getByText('Viewing historical case ' + old.id.slice(0, 8), {
          exact: true,
        }),
      ).toBeVisible()
      expect(
        await page.evaluate(() => document.documentElement.scrollWidth),
      ).toBeLessThanOrEqual(390)
      await expect
        .poll(async () =>
          (await inboxContrast(page)).filter(
            (item) => item.ratio < item.minimum,
          ),
        )
        .toEqual([])
      if (process.env.INBOX_CAPTURE)
        await page.screenshot({
          path: testInfo.outputPath(
            'followup-historical-' + theme + '-mobile.png',
          ),
          fullPage: true,
        })
    }
  } finally {
    await client?.end()
    for (const id of workflows)
      await api.del('/api/inbox/workflow-definitions/' + id)
    for (const id of fields)
      await api.put('/api/inbox/fields/' + id, { archive: true })
    for (const id of contacts) await api.del('/api/contacts/' + id)
    await api.del('/api/accounts/' + account.id)
    await api.put('/api/inbox/workflows', { enabled: before })
  }
})
