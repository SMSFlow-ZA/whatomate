import { test, expect } from '@playwright/test'
import { Client } from 'pg'
import { randomUUID } from 'node:crypto'
import { writeFile } from 'node:fs/promises'
import { ApiHelper } from '../../helpers/api'
import { createTestScope } from '../../framework'
import { inboxContrast } from '../../helpers/inbox-contrast'

test('reviews personalised replies and variable selection without sending', async ({
  page,
  request,
}, testInfo) => {
  test.setTimeout(120000)
  page.setDefaultTimeout(15000)
  const database = process.env.TEST_DATABASE_URL
  test.skip(!database, 'Requires an explicitly disposable database')
  const dbURL = new URL(database!)
  expect(['fixture', 'whatomate_test']).toContain(dbURL.pathname.slice(1))
  expect(
    dbURL.hostname === '127.0.0.1' ||
      dbURL.hostname === 'localhost' ||
      dbURL.hostname.startsWith('whatomate-inbox-'),
  ).toBeTruthy()
  const api = new ApiHelper(request),
    scope = createTestScope('inbox-personalisation')
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
    business_id: '100000000009',
    access_token: 'synthetic-do-not-send',
  })
  const contact = await api.createContact(
    scope.phone(),
    'Example personalised enquiry',
  )
  const thread = '/api/inbox/accounts/' + account.id + '/contacts/' + contact.id
  const savedRequest = await api.post('/api/canned-responses', {
    name: scope.name('Reviewed saved reply'),
    content: 'Hello {{contact_name}}',
    personalise: true,
    buttons: [
      { id: 'case-status', title: 'Status {{case_status}}', type: 'reply' },
    ],
  })
  expect(savedRequest.ok()).toBe(true)
  const saved = (await savedRequest.json()).data
  let client: Client | undefined
  let completed = false
  try {
    const current = (await (await api.put(thread, { status: 'open' })).json())
      .data.case
    client = new Client({ connectionString: database })
    await client.connect()
    await client.query(
      "INSERT INTO messages(id,organization_id,whats_app_account,contact_id,direction,message_type,content,status,created_at,updated_at) VALUES($1,$2,$3,$4,'incoming','text','Synthetic preview fixture; do not send','read',NOW(),NOW())",
      [randomUUID(), profile.organization_id, account.name, contact.id],
    )
    await client.query(
      'UPDATE contacts SET last_inbound_at=NOW(),last_message_at=NOW() WHERE id=$1 AND organization_id=$2',
      [contact.id, profile.organization_id],
    )
    const url =
      '/chat/' +
      contact.id +
      '?inbox_account=' +
      encodeURIComponent(account.name) +
      '&inbox_account_id=' +
      account.id
    await page.context().addCookies((await request.storageState()).cookies)
    await page.addInitScript(
      (user) => localStorage.setItem('user', JSON.stringify(user)),
      profile,
    )
    await page.goto('/settings/canned-responses/' + saved.id)
    await expect(
      page.getByRole('checkbox', {
        name: 'Personalise from case details',
        exact: true,
      }),
    ).toBeChecked()
    await page
      .getByRole('textbox', { name: 'Content', exact: true })
      .fill('Welcome {{contact_name}}')
    await page.getByRole('button', { name: 'Save', exact: true }).click()
    await expect
      .poll(
        async () =>
          (await (await api.get('/api/canned-responses/' + saved.id)).json())
            .data.content,
      )
      .toBe('Welcome {{contact_name}}')
    for (const viewport of [
      { width: 1366, height: 900 },
      { width: 390, height: 844 },
    ]) {
      for (const theme of ['light', 'dark']) {
        await page.setViewportSize(viewport)
        await page.goto(url)
        await page.evaluate(
          (theme) => localStorage.setItem('color-mode', theme),
          theme,
        )
        await page.reload()
        await expect(page.locator('html')).toHaveClass(new RegExp(theme))
        await page.goto('/settings/canned-responses/' + saved.id)
        const mode = page.getByRole('checkbox', {
          name: 'Personalise from case details',
          exact: true,
        })
        await expect(mode).toBeChecked()
        const title = page.locator('[data-inbox-variable-input="0/title"]')
        await title.fill('')
        await title.focus()
        await page
          .getByRole('button', { name: 'Insert variable', exact: true })
          .nth(1)
          .click()
        const variableSearch = page.getByRole('searchbox', {
          name: 'Search variables',
          exact: true,
        })
        await variableSearch.fill('Case priority')
        await variableSearch.press('ArrowDown')
        await page
          .getByLabel('Available variables', { exact: true })
          .press('Enter')
        await expect(title).toHaveValue('{{case_priority}}')
        await expect(title).toBeFocused()
        await expect(
          page.getByRole('searchbox', {
            name: 'Search variables',
            exact: true,
          }),
        ).toHaveCount(0)
        await expect
          .poll(async () =>
            (await inboxContrast(page)).filter(
              (item) => item.ratio < item.minimum,
            ),
          )
          .toEqual([])
        expect(
          await page.evaluate(() => document.documentElement.scrollWidth),
        ).toBeLessThanOrEqual(viewport.width)
        if (process.env.INBOX_CAPTURE)
          await page.screenshot({
            path: testInfo.outputPath(
              `canned-editor-${theme}-${viewport.width}.png`,
            ),
            fullPage: true,
          })
        // Restore the original authored label before leaving the editor.
        await title.fill('Status {{case_status}}')
        await page.getByRole('button', { name: 'Save', exact: true }).click()
        await expect(
          page.getByRole('button', { name: 'Save', exact: true }),
        ).not.toBeVisible()
        await page.goto(url)
        const composer = page.getByPlaceholder('Type a message...')
        await expect(composer).toBeVisible()
        await composer.fill('Hello ')
        await page
          .getByRole('button', { name: 'Insert variable', exact: true })
          .click()
        const search = page.getByRole('searchbox', {
          name: 'Search variables',
          exact: true,
        })
        const picker = page.locator('.inbox-variable-picker')
        await expect(
          picker.getByRole('listbox', { name: 'Available variables' }),
        ).toBeVisible()
        const pickerLayout = await picker.evaluate((element) => {
          const bounds = element.getBoundingClientRect()
          return {
            width: bounds.width,
            viewport: innerWidth,
            overflow: element.scrollWidth > element.clientWidth,
            optionSize: parseFloat(
              getComputedStyle(element.querySelector('[role=option]')!)
                .fontSize,
            ),
          }
        })
        expect(pickerLayout.width).toBeLessThanOrEqual(
          pickerLayout.viewport - 20,
        )
        expect(pickerLayout.overflow).toBe(false)
        expect(pickerLayout.optionSize).toBeLessThanOrEqual(14)
        if (process.env.INBOX_CAPTURE)
          await page.screenshot({
            path: testInfo.outputPath(
              `variable-picker-${theme}-${viewport.width}.png`,
            ),
            animations: 'disabled',
            fullPage: true,
          })
        await search.fill('Customer name')
        await search.press('ArrowDown')
        await page
          .getByLabel('Available variables', { exact: true })
          .press('Enter')
        await expect(composer).toHaveValue('Hello {{contact_name}}')
        await expect(composer).toBeFocused()
        await expect(
          page.getByRole('searchbox', {
            name: 'Search variables',
            exact: true,
          }),
        ).toHaveCount(0)
        await expect(
          page.getByRole('checkbox', {
            name: 'Personalise this reply',
            exact: true,
          }),
        ).toBeChecked()
        await composer.press('Enter')
        const preview = page.getByRole('dialog', {
          name: 'Review personalised reply',
          exact: true,
        })
        await expect(preview).toBeVisible()
        await expect(
          preview.getByText('Hello Example personalised enquiry', {
            exact: true,
          }),
        ).toBeVisible()
        await expect(
          preview.getByRole('button', {
            name: 'Send reviewed reply',
            exact: true,
          }),
        ).toBeEnabled()
        const targetMetrics = await preview
          .getByRole('button')
          .evaluateAll((buttons) =>
            buttons.map((button) => {
              const bounds = button.getBoundingClientRect(),
                style = getComputedStyle(button)
              return {
                label: button.textContent?.trim(),
                height: bounds.height,
                width: bounds.width,
                minHeight: style.minHeight,
                minWidth: style.minWidth,
                classes: button.className,
              }
            }),
          )
        const metricsPath = testInfo.outputPath(
          `reply-targets-${theme}-${viewport.width}.json`,
        )
        await writeFile(metricsPath, JSON.stringify(targetMetrics, null, 2))
        await testInfo.attach(`reply-targets-${theme}-${viewport.width}`, {
          path: metricsPath,
          contentType: 'application/json',
        })
        for (const name of ['Cancel', 'Send reviewed reply', 'Close']) {
          const button = preview.getByRole('button', { name, exact: true })
          // Measure the resting hit target after the opening transform settles.
          await expect
            .poll(async () => (await button.boundingBox())?.height ?? 0)
            .toBeGreaterThanOrEqual(44)
          await expect
            .poll(async () => (await button.boundingBox())?.width ?? 0)
            .toBeGreaterThanOrEqual(44)
        }
        // Only review the synthetic message. Never click its Send action.
        expect(
          await page.evaluate(() => document.documentElement.scrollWidth),
        ).toBeLessThanOrEqual(viewport.width)
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
              `personalisation-${theme}-${viewport.width}.png`,
            ),
            fullPage: true,
          })
        await preview
          .getByRole('button', { name: 'Cancel', exact: true })
          .click()
        await expect(preview).not.toBeVisible()
        // The reviewed saved reply retains its rich payload and does not erase the draft.
        const draft = await composer.inputValue()
        await page.locator('#canned-response-picker-button').click()
        await page.getByPlaceholder('Search responses...').fill(saved.name)
        await page
          .getByRole('button')
          .filter({ has: page.getByText(saved.name, { exact: true }) })
          .click()
        await expect(preview).toBeVisible()
        await expect(
          preview.getByText('Welcome Example personalised enquiry', {
            exact: true,
          }),
        ).toBeVisible()
        await expect(
          preview.getByText('Status open', { exact: true }),
        ).toBeVisible()
        await expect(
          preview.getByRole('button', {
            name: 'Send reviewed reply',
            exact: true,
          }),
        ).toBeEnabled()
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
              `canned-review-${theme}-${viewport.width}.png`,
            ),
            fullPage: true,
          })
        await preview
          .getByRole('button', { name: 'Cancel', exact: true })
          .click()
        await expect(composer).toHaveValue(draft)
      }
    }
    // Missing values are displayed, but cannot be confirmed.
    const missing = await api.post('/api/inbox/message-preview', {
      case_id: current.id,
      text: 'Reference {{case.field.unknown}}',
    })
    expect(missing.ok()).toBe(true)
    const data = (await missing.json()).data
    expect(data.send_unavailable).toBe(true)
    expect(data.confirmation).toBe('')
    expect(data.reply.missing_variables).toEqual([
      { key: 'case.field.unknown', reason: 'unavailable' },
    ])
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
    completed = true
  } finally {
    await client?.end()
    // Do not replace the primary UI failure with requests through an already
    // disposed context. The explicitly disposable runner removes its database.
    if (completed) {
      await api.del('/api/canned-responses/' + saved.id)
      await api.del('/api/contacts/' + contact.id)
      await api.del('/api/accounts/' + account.id)
      await api.put('/api/inbox/workflows', { enabled: before })
    }
  }
})
