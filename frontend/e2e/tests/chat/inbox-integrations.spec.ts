import { test, expect } from '@playwright/test'
import { Client } from 'pg'
import { randomUUID } from 'node:crypto'
import { ApiHelper } from '../../helpers/api'
import { createTestScope } from '../../framework'
import { inboxContrast } from '../../helpers/inbox-contrast'

test('configures approved integration actions with held dispatch and responsive review', async ({
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
    ['127.0.0.1', 'localhost'].includes(dbURL.hostname) ||
      dbURL.hostname.startsWith('whatomate-inbox-'),
  ).toBeTruthy()
  const api = new ApiHelper(request),
    scope = createTestScope('inbox-integrations')
  await api.loginAsAdmin()
  const before = (await (await api.get('/api/inbox/workflows')).json()).data
    .enabled
  await api.put('/api/inbox/workflows', { enabled: true })
  const dispatch = (await (await api.get('/api/inbox/automation')).json()).data
  const settings = (
    await (await api.get('/api/inbox/integration-settings')).json()
  ).data
  expect(settings.dispatch_available).toBe(false)
  expect(dispatch.dispatch_available).toBe(false)
  const profile = (await (await api.get('/api/me')).json()).data
  const fieldRequest = await api.post('/api/inbox/fields', {
    scope: 'case',
    key: 'reference_' + randomUUID().replace(/-/g, '').slice(0, 10),
    label: scope.name('Service reference'),
    kind: 'text',
    required_on_resolve: false,
  })
  expect(fieldRequest.ok()).toBe(true)
  const field = (await fieldRequest.json()).data.definition
  const account = await api.createWhatsAppAccount({
    name: scope.name('Support'),
    phone_id: scope.phone().replace(/\D/g, ''),
    business_id: '100000000009',
    access_token: 'synthetic-do-not-send',
  })
  const contact = await api.createContact(
    scope.phone(),
    'Synthetic service enquiry',
  )
  const current = (
    await (
      await api.put(
        '/api/inbox/accounts/' + account.id + '/contacts/' + contact.id,
        { status: 'open' },
      )
    ).json()
  ).data.case
  const integrationName = scope.name('Create service request')
  let integrationId = '',
    macroId = '',
    completed = false
  const client = new Client({ connectionString: database })
  await client.connect()
  try {
    await page.context().addCookies((await request.storageState()).cookies)
    await page.addInitScript((user) => {
      localStorage.setItem('user', JSON.stringify(user))
      localStorage.setItem('locale', 'en')
    }, profile)
    await page.goto('/settings/inbox-workflows?tab=integrations')
    await expect(
      page.getByRole('heading', { name: 'Integrations', exact: true }),
    ).toBeVisible()
    const newButton = page.getByRole('button', {
      name: 'New integration',
      exact: true,
    })
    await expect(newButton).toBeEnabled()
    await newButton.focus()
    await page.keyboard.press('Enter')
    await expect(
      page.getByRole('heading', { name: 'Integration settings', exact: true }),
    ).toBeFocused()
    await page
      .getByRole('textbox', { name: 'Name', exact: true })
      .fill(integrationName)
    await page
      .getByLabel('Destination', { exact: true })
      .selectOption('synthetic_crm')
    await page
      .getByLabel('Replacement credential', { exact: true })
      .fill('synthetic-browser-secret')
    await page.getByRole('button', { name: 'Add input', exact: true }).click()
    await page.getByLabel('Input 1 name', { exact: true }).fill('customer')
    await page.getByRole('button', { name: 'Add output', exact: true }).click()
    await page
      .getByLabel('Output 1 property', { exact: true })
      .fill('reference')
    await page
      .getByLabel('Output 1 destination field', { exact: true })
      .selectOption(field.id)
    await page.getByRole('button', { name: 'Save', exact: true }).click()
    await expect(
      page
        .getByRole('status')
        .filter({ hasText: 'Integration saved disabled' }),
    ).toBeVisible()
    await expect(page.locator('input[type=password]')).toHaveCount(0)
    const metadata = (
      await (await api.get('/api/inbox/integrations')).json()
    ).data.integrations.find(
      (item: { name: string }) => item.name === integrationName,
    )
    expect(metadata.enabled).toBe(false)
    expect(metadata.has_credential).toBe(true)
    expect(metadata.credential).toBeUndefined()
    integrationId = metadata.id
    for (const viewport of [
      { width: 1366, height: 900 },
      { width: 390, height: 844 },
    ]) {
      for (const theme of ['light', 'dark']) {
        await page.setViewportSize(viewport)
        await page.evaluate(
          (value) => localStorage.setItem('color-mode', value),
          theme,
        )
        await page.reload()
        await expect(page.locator('html')).toHaveClass(new RegExp(theme))
        await page
          .getByRole('button', { name: 'Edit ' + integrationName, exact: true })
          .click()
        await expect(
          page.getByLabel('Input 1 name', { exact: true }),
        ).toHaveValue('customer')
        await expect(
          page.getByLabel('Output 1 destination field', { exact: true }),
        ).toHaveValue(field.id)
        await expect(page.locator('input[type=password]')).toHaveCount(0)
        await expect(
          page.getByRole('checkbox', {
            name: 'Enable this definition',
            exact: true,
          }),
        ).not.toBeChecked()
        await expect
          .poll(async () =>
            page.evaluate(
              () => document.documentElement.scrollWidth - innerWidth,
            ),
          )
          .toBeLessThanOrEqual(1)
        await expect
          .poll(async () =>
            (await inboxContrast(page)).filter(
              (item) => item.ratio < item.minimum,
            ),
          )
          .toEqual([])
        const sizes = await page
          .getByRole('button', { name: 'Save', exact: true })
          .evaluate((node) => ({
            width: node.getBoundingClientRect().width,
            height: node.getBoundingClientRect().height,
          }))
        const minimum = viewport.width <= 640 ? 44 : 28
        expect(sizes.width).toBeGreaterThanOrEqual(minimum)
        expect(sizes.height).toBeGreaterThanOrEqual(minimum)
        if (process.env.INBOX_CAPTURE)
          await page.screenshot({
            path: testInfo.outputPath(
              'integration-editor-' + theme + '-' + viewport.width + '.png',
            ),
            fullPage: true,
          })
        await page
          .locator('.inbox-integration-output-row')
          .scrollIntoViewIfNeeded()
        if (process.env.INBOX_CAPTURE)
          await page.screenshot({
            path: testInfo.outputPath(
              'integration-outputs-' + theme + '-' + viewport.width + '.png',
            ),
            fullPage: true,
          })
        await page
          .getByRole('heading', { name: 'Named inputs', exact: true })
          .scrollIntoViewIfNeeded()
        if (process.env.INBOX_CAPTURE)
          await page.screenshot({
            path: testInfo.outputPath(
              'integration-mappings-' + theme + '-' + viewport.width + '.png',
            ),
            fullPage: true,
          })
        const reference = page.getByRole('textbox', {
          name: 'Reference property (optional)',
          exact: true,
        })
        await reference.fill('request-id')
        await expect(reference).toHaveAttribute('aria-invalid', 'true')
        const referenceError = page.getByRole('alert').filter({
          hasText: 'Use a lowercase property name starting with a letter',
        })
        await expect(referenceError).toBeVisible()
        await referenceError.scrollIntoViewIfNeeded()
        await expect(
          page.getByRole('button', { name: 'Save', exact: true }),
        ).toBeDisabled()
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
              'integration-reference-error-' +
                theme +
                '-' +
                viewport.width +
                '.png',
            ),
            fullPage: true,
          })
        await reference.fill('')
        await expect(reference).not.toHaveAttribute('aria-invalid', 'true')
        await page
          .getByRole('textbox', { name: 'Description', exact: true })
          .fill('Unsaved synthetic draft')
        await page
          .getByRole('button', { name: 'All integrations', exact: true })
          .click()
        await expect(page.getByRole('alertdialog')).toBeVisible()
        await page.getByRole('button', { name: 'Stay', exact: true }).click()
        await expect(
          page.getByRole('textbox', { name: 'Description', exact: true }),
        ).toHaveValue('Unsaved synthetic draft')
        await page
          .getByRole('textbox', { name: 'Description', exact: true })
          .fill('')
        await page
          .getByRole('button', { name: 'All integrations', exact: true })
          .click()
        await expect(
          page.getByRole('button', { name: 'New integration', exact: true }),
        ).toBeFocused()
        await expect(
          page.getByRole('button', {
            name: 'Review enabling dispatch',
            exact: true,
          }),
        ).toBeDisabled()
      }
    }
    await page.setViewportSize({ width: 1366, height: 900 })
    await page.getByRole('tab', { name: 'Macros', exact: true }).click()
    await page.getByRole('button', { name: 'New macro', exact: true }).click()
    const macroName = scope.name('Service request macro')
    await page
      .getByRole('textbox', { name: 'Workflow name', exact: true })
      .fill(macroName)
    await page
      .getByRole('combobox', { name: 'Action', exact: true })
      .selectOption('integration')
    await page
      .getByLabel('Integration definition', { exact: true })
      .selectOption(integrationId)
    const mapping = page
      .locator('label')
      .filter({ hasText: /^customer ·/ })
      .locator('textarea')
    await mapping.fill('{{contact_name}}')
    await page
      .getByRole('button', { name: 'Save', exact: true })
      .first()
      .click()
    await expect(
      page.locator('.inbox-list-title').filter({ hasText: macroName }),
    ).toBeVisible()
    const workflows = (
      await (await api.get('/api/inbox/workflow-definitions?kind=macro')).json()
    ).data.workflows
    const macro = workflows.find(
      (item: { name: string }) => item.name === macroName,
    )
    macroId = macro.id
    expect(macro.actions[0].integration_inputs).toEqual({
      customer: '{{contact_name}}',
    })
    const previewResponse = await api.post(
      '/api/inbox/workflow-definitions/' + macroId + '/preview',
      { case_id: current.id },
    )
    expect(previewResponse.ok()).toBe(true)
    const preview = (await previewResponse.json()).data
    expect(preview.actions_eligible).toBe(false)
    expect(preview.integrations['0'].inputs).toEqual({
      customer: 'Synthetic service enquiry',
    })
    expect(preview.integrations['0'].destination).toBe(
      'https://service.example.invalid',
    )
    expect(JSON.stringify(preview)).not.toContain('synthetic-browser-secret')
    const changed = await api.put('/api/inbox/integrations/' + integrationId, {
      name: metadata.name,
      description: metadata.description,
      endpoint_key: metadata.endpoint_key,
      auth_mode: metadata.auth_mode,
      enabled: false,
      outputs: metadata.outputs,
      reference_key: metadata.reference_key,
      inputs: [{ name: 'client', kind: 'text', required: true }],
    })
    expect(changed.ok()).toBe(true)
    for (const viewport of [
      { width: 1366, height: 900 },
      { width: 390, height: 844 },
    ]) {
      for (const theme of ['light', 'dark']) {
        await page.setViewportSize(viewport)
        await page.evaluate(
          (value) => localStorage.setItem('color-mode', value),
          theme,
        )
        await page.goto('/settings/inbox-workflows')
        await page.reload()
        await page.getByRole('tab', { name: 'Macros', exact: true }).click()
        await page
          .locator('.inbox-list-row')
          .filter({ hasText: macroName })
          .getByRole('button', { name: 'Edit', exact: true })
          .click()
        await expect(
          page.getByRole('heading', {
            name: 'Mappings no longer in this definition',
            exact: true,
          }),
        ).toBeVisible()
        await page
          .getByRole('button', { name: 'Remove mapping customer', exact: true })
          .scrollIntoViewIfNeeded()
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
              'integration-schema-repair-' +
                theme +
                '-' +
                viewport.width +
                '.png',
            ),
            fullPage: true,
          })
      }
    }
    await page
      .locator('label')
      .filter({ hasText: /^client ·/ })
      .locator('textarea')
      .fill('{{contact_name}}')
    await page
      .getByRole('button', { name: 'Remove mapping customer', exact: true })
      .click()
    await expect(
      page.getByRole('heading', {
        name: 'Mappings no longer in this definition',
        exact: true,
      }),
    ).toHaveCount(0)
    await page
      .getByRole('button', { name: 'Save', exact: true })
      .first()
      .click()
    await expect(
      page.locator('.inbox-list-title').filter({ hasText: macroName }),
    ).toBeVisible()
    const repaired = (
      await (await api.get('/api/inbox/workflow-definitions?kind=macro')).json()
    ).data.workflows.find((item: { id: string }) => item.id === macroId)
    expect(repaired.actions[0].integration_inputs).toEqual({
      client: '{{contact_name}}',
    })
    expect(
      (await (await api.get('/api/inbox/integration-settings')).json()).data,
    ).toEqual(settings)
    expect(
      (await (await api.get('/api/inbox/automation')).json()).data,
    ).toEqual(dispatch)
    expect(
      (
        await client.query(
          'SELECT count(*)::int AS n FROM inbox_integration_executions WHERE organization_id=$1 AND case_id=$2',
          [profile.organization_id, current.id],
        )
      ).rows[0].n,
    ).toBe(0)
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
    await client.end()
    if (completed) {
      if (macroId) await api.del('/api/inbox/workflow-definitions/' + macroId)
      if (integrationId)
        await api.del('/api/inbox/integrations/' + integrationId)
      await api.put('/api/inbox/fields/' + field.id, { archive: true })
      await api.del('/api/contacts/' + contact.id)
      await api.del('/api/accounts/' + account.id)
      await api.put('/api/inbox/workflows', { enabled: before })
    }
  }
})
