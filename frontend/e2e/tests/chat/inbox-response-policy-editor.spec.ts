import { expect, test, type APIResponse } from '@playwright/test'
import { ApiHelper } from '../../helpers/api'
import { createTestScope } from '../../framework'
import { inboxContrast } from '../../helpers/inbox-contrast'

test('edits prospective response policies and date exceptions without changing a sample or enabling sends', async ({
  page,
  request,
}, testInfo) => {
  test.setTimeout(180000)
  page.setDefaultTimeout(12000)
  const database = process.env.TEST_DATABASE_URL
  test.skip(!database, 'An explicitly owned disposable is required')
  const owned = new URL(database!),
    host = new URL(process.env.BASE_URL!).hostname
  expect(['fixture', 'whatomate_test']).toContain(owned.pathname.slice(1))
  for (const address of [owned.hostname, host])
    expect(
      address.startsWith('whatomate-inbox-') ||
        ['127.0.0.1', 'localhost'].includes(address),
    ).toBe(true)
  const data = async (response: APIResponse) => {
    expect(
      response.ok(),
      'HTTP ' + response.status() + ' ' + new URL(response.url()).pathname,
    ).toBe(true)
    return (await response.json()).data
  }
  const api = new ApiHelper(request),
    scope = createTestScope('response-editor')
  await api.loginAsAdmin()
  const enabled = (await data(await api.get('/api/inbox/workflows'))).enabled
  await data(await api.put('/api/inbox/workflows', { enabled: true }))
  const automation = await data(await api.get('/api/inbox/automation'))
  expect(automation.dispatch_available).toBe(false)
  const account = await api.createWhatsAppAccount({
    name: scope.name('policy inbox'),
    phone_id: scope.phone().replace(/\D/g, ''),
    business_id: '100000000053',
    access_token: 'synthetic-never-send',
  })
  const contact = await api.createContact(
    scope.phone(),
    'Example enquiry - response policy review',
  )
  const thread =
      '/api/inbox/accounts/' + account.id + '/contacts/' + contact.id,
    base = '/api/inbox/response-policies/' + account.id
  const targets = {
    first_reply_minutes: 30,
    next_reply_minutes: 60,
    resolution_minutes: 480,
    business_hours: true,
    timezone: 'Africa/Johannesburg',
    shifts: [{ Day: 1, StartMinute: 480, EndMinute: 1020 }],
    date_exceptions: [],
  }
  let qualificationField = ''
  async function revealSection(name: string) {
    const target =
      name === 'Additional conditions'
        ? page.getByText(name, { exact: true })
        : page.getByRole('heading', { name, exact: true })
    await target.evaluate((el) => {
      const container = el.closest<HTMLElement>('.inbox-page-scroll')
      if (!container) throw new Error('Missing inbox scrolling surface')
      // Scroll the application's content surface, preserving the surrounding shell.
      for (
        let parent = container.parentElement;
        parent;
        parent = parent.parentElement
      )
        parent.scrollTop = 0
      window.scrollTo(0, 0)
      container.scrollTop +=
        el.getBoundingClientRect().top -
        container.getBoundingClientRect().top -
        16
    })
    await expect(
      page.getByRole('heading', { name: 'Inbox workflows', exact: true }),
    ).toBeInViewport()
  }
  async function setTheme(theme: string) {
    await page.evaluate(async (theme) => {
      document.documentElement.classList.remove('light', 'dark')
      document.documentElement.classList.add(theme)
      await new Promise<void>((done) =>
        requestAnimationFrame(() => requestAnimationFrame(() => done())),
      )
      await Promise.all(
        document
          .getAnimations()
          .filter(
            (animation) =>
              animation.effect?.getComputedTiming().iterations !== Infinity,
          )
          .map((animation) => animation.finished.catch(() => {})),
      )
    }, theme)
  }
  try {
    await data(await api.put('/api/inbox/sla/policies/' + account.id, targets))
    const original = (
      await data(await api.put(thread, { status: 'open', priority: 'high' }))
    ).case
    const profile = await data(await api.get('/api/me'))
    await page.context().addCookies((await request.storageState()).cookies)
    await page.addInitScript(
      (user) => localStorage.setItem('user', JSON.stringify(user)),
      profile,
    )
    await page.goto('/settings/inbox-workflows')
    await page
      .getByRole('tab', { name: 'Response targets', exact: true })
      .click()
    await page
      .getByRole('combobox', { name: 'WhatsApp account', exact: true })
      .selectOption(account.id)
    // A cancelled local calendar edit must not mutate the cached saved default.
    await page
      .getByRole('button', { name: 'Add date exception', exact: true })
      .click()
    await page.getByLabel(/^Local date/).fill('2026-12-25')
    await page
      .getByRole('tab', { name: 'Conditional policies', exact: true })
      .click()
    await expect(page.getByRole('alertdialog')).toBeVisible()
    await page
      .getByRole('alertdialog')
      .getByRole('button', { name: 'Stay', exact: true })
      .click()
    await expect(page.getByLabel(/^Local date/)).toHaveValue('2026-12-25')
    await page
      .getByRole('tab', { name: 'Conditional policies', exact: true })
      .click()
    await page
      .getByRole('alertdialog')
      .getByRole('button', { name: 'Leave', exact: true })
      .click()
    await page
      .getByRole('tab', { name: 'Account default', exact: true })
      .click()
    await expect(page.getByLabel(/^Local date/)).toHaveCount(0)
    await page
      .getByRole('tab', { name: 'Conditional policies', exact: true })
      .click()
    await page
      .getByRole('button', { name: 'New conditional policy', exact: true })
      .click()
    await page
      .getByLabel('Policy name', { exact: true })
      .fill('Priority enquiries')
    await page
      .getByLabel('Description', { exact: true })
      .fill('Earlier response for urgent enquiries. New cases only.')
    await page
      .getByRole('button', { name: 'Add condition', exact: true })
      .click()
    await page
      .getByRole('combobox', { name: 'Field', exact: true })
      .selectOption('priority')
    await page
      .getByRole('combobox', { name: 'Value', exact: true })
      .selectOption('high')
    await page.getByLabel('First reply (minutes)', { exact: true }).fill('10')
    await page
      .getByRole('button', { name: 'Add date exception', exact: true })
      .click()
    await expect(page.getByLabel(/^Local date/)).toBeFocused()
    await page.getByLabel(/^Local date/).fill('2026-12-25')
    await expect(
      page.getByText(
        'Closed all day. Add hours to open on this date instead.',
        { exact: true },
      ),
    ).toBeVisible()
    await page
      .getByRole('button', { name: 'Add hours on this date', exact: true })
      .click()
    const save = page.getByRole('button', { name: 'Save', exact: true })
    const [createdResponse] = await Promise.all([
      page.waitForResponse(
        (r) =>
          new URL(r.url()).pathname === base && r.request().method() === 'POST',
      ),
      save.click(),
    ])
    let policy = (await data(createdResponse)).policy
    expect(policy.enabled).toBe(false)
    let list = (await data(await api.get(base))).policies
    expect(list[0].targets.date_exceptions).toEqual([
      {
        date: '2026-12-25',
        intervals: [{ start_minute: 540, end_minute: 1020 }],
      },
    ])
    await page
      .getByRole('button', { name: 'Edit Priority enquiries', exact: true })
      .click()
    await page.getByLabel('Enabled for new cases', { exact: true }).check()
    const [updatedResponse] = await Promise.all([
      page.waitForResponse(
        (r) =>
          new URL(r.url()).pathname === base + '/' + policy.id &&
          r.request().method() === 'PUT',
      ),
      save.click(),
    ])
    policy = (await data(updatedResponse)).policy
    await page
      .getByRole('button', {
        name: 'Clone Priority enquiries as a disabled policy',
        exact: true,
      })
      .click()
    await expect(
      page.getByRole('button', {
        name: 'Edit Priority enquiries (copy)',
        exact: true,
      }),
    ).toBeVisible()
    list = (await data(await api.get(base))).policies
    expect(list[1].enabled).toBe(false)
    await page
      .getByRole('button', {
        name: 'Move Priority enquiries (copy) up',
        exact: true,
      })
      .click()
    await expect(page.locator('[data-policy-id]').first()).toContainText(
      'Priority enquiries (copy)',
    )
    await page
      .getByRole('button', { name: 'Preview policy selection', exact: true })
      .click()
    await page
      .getByRole('combobox', { name: 'Sample case', exact: true })
      .selectOption(original.id)
    await page
      .getByRole('button', { name: 'Preview this case', exact: true })
      .click()
    await expect(
      page.getByText(
        'Policy for a new case with these facts: Priority enquiries',
        { exact: true },
      ),
    ).toBeVisible()
    await expect(
      page.getByText(
        'The sample retains its assigned version: ' + original.sla_policy_id,
        { exact: true },
      ),
    ).toBeVisible()
    const sampleLink = page.getByRole('link', {
      name: 'Open the unchanged sample case',
      exact: true,
    })
    expect(await sampleLink.getAttribute('href')).toContain(
      'case_id=' + original.id,
    )
    const unchanged = (await data(await api.get(thread))).case
    expect(unchanged.version).toBe(original.version)
    expect(unchanged.sla_policy_id).toBe(original.sla_policy_id)
    await expect(page.locator('[data-sonner-toast]')).toHaveCount(0, {
      timeout: 12000,
    })
    for (const width of [1366, 390])
      for (const theme of ['light', 'dark']) {
        await page.setViewportSize({
          width,
          height: width === 390 ? 844 : 1000,
        })
        await setTheme(theme)
        await expect(page.locator('.inbox-ui').last()).toBeVisible()
        expect(
          await page.evaluate(
            () => document.documentElement.scrollWidth <= innerWidth + 1,
          ),
        ).toBe(true)
        const contrast = await inboxContrast(page)
        expect(
          contrast.filter((item) => item.ratio + 0.02 < item.minimum),
        ).toEqual([])
        const controls = await page
          .locator('.inbox-ui button:visible')
          .evaluateAll((elements) =>
            elements
              .filter((el) => !(el as HTMLButtonElement).disabled)
              .map((el) => ({
                label: el.getAttribute('aria-label') || el.textContent,
                h: el.getBoundingClientRect().height,
              })),
          )
        if (width === 390)
          expect(controls.filter((control) => control.h < 44)).toEqual([])
        if (process.env.INBOX_CAPTURE) {
          await revealSection('Conditional policies')
          await page.screenshot({
            path: testInfo.outputPath(
              'response-policy-list-' + theme + '-' + width + '.png',
            ),
            fullPage: false,
            animations: 'disabled',
          })
          await revealSection('Preview selection for a new case')
          await page.screenshot({
            path: testInfo.outputPath(
              'response-policy-preview-' + theme + '-' + width + '.png',
            ),
            fullPage: false,
            animations: 'disabled',
          })
        }
      }
    await page.setViewportSize({ width: 1366, height: 1000 })
    await page
      .getByRole('button', { name: 'Edit Priority enquiries', exact: true })
      .click()
    for (const width of [1366, 390])
      for (const theme of ['light', 'dark']) {
        await page.setViewportSize({
          width,
          height: width === 390 ? 844 : 1000,
        })
        await setTheme(theme)
        expect(
          await page.evaluate(
            () => document.documentElement.scrollWidth <= innerWidth + 1,
          ),
        ).toBe(true)
        if (process.env.INBOX_CAPTURE) {
          await revealSection('Edit policy')
          await page.screenshot({
            path: testInfo.outputPath(
              'response-policy-editor-' + theme + '-' + width + '.png',
            ),
            fullPage: false,
            animations: 'disabled',
          })
          await revealSection('Date exceptions')
          expect(
            await page
              .locator('.inbox-response-targets')
              .evaluate((el) => getComputedStyle(el).colorScheme),
          ).toBe(theme)
          await page.screenshot({
            path: testInfo.outputPath(
              'response-policy-calendar-' + theme + '-' + width + '.png',
            ),
            fullPage: false,
            animations: 'disabled',
          })
        }
      }
    // A concurrent edit must hold the authored draft; it must not overwrite the newer version.
    const current = (await data(await api.get(base))).policies.find(
      (row: { id: string }) => row.id === policy.id,
    )
    await data(
      await api.put(base + '/' + policy.id, {
        revision: current.revision,
        description: current.definition.description,
        conditions: current.definition.conditions,
        targets: current.targets,
        enabled: true,
        name: 'Concurrent policy change',
      }),
    )
    await page
      .getByLabel('Description', { exact: true })
      .fill('Retain this draft on conflict')
    await save.click()
    await expect(
      page.getByText(
        'This policy or its order changed. Your draft is still here. Refresh the saved list before reviewing another change.',
      ),
    ).toBeVisible()
    await expect(page.getByLabel('Description', { exact: true })).toHaveValue(
      'Retain this draft on conflict',
    )
    await expect(save).toBeDisabled()
    await page
      .getByRole('button', { name: 'Refresh saved settings', exact: true })
      .click()
    await page
      .getByRole('alertdialog')
      .getByRole('button', { name: 'Leave', exact: true })
      .click()
    await expect(
      page.getByRole('button', {
        name: 'Edit Concurrent policy change',
        exact: true,
      }),
    ).toBeVisible()
    await page
      .getByRole('button', {
        name: 'Archive Priority enquiries (copy)',
        exact: true,
      })
      .click()
    const [archivedResponse] = await Promise.all([
      page.waitForResponse(
        (response) =>
          new URL(response.url()).pathname.startsWith(base + '/') &&
          response.request().method() === 'DELETE',
      ),
      page
        .getByRole('alertdialog')
        .getByRole('button', { name: 'Archive policy', exact: true })
        .click(),
    ])
    expect((await data(archivedResponse)).archived).toBe(true)
    await expect(page.getByRole('alertdialog')).toHaveCount(0)
    await expect(
      page.getByRole('button', {
        name: 'Edit Priority enquiries (copy)',
        exact: true,
      }),
    ).toHaveCount(0)
    // Metadata failure preserves the recorded predicate without exposing a generic value editor.
    qualificationField = (
      await data(
        await api.post('/api/inbox/fields', {
          scope: 'case',
          key: 'qualification_' + Date.now().toString(36),
          label: 'Synthetic qualification',
          kind: 'boolean',
          required_on_resolve: false,
        }),
      )
    ).definition.id
    const savedPolicy = (await data(await api.get(base))).policies.find(
      (row: { definition: { name: string } }) =>
        row.definition.name === 'Concurrent policy change',
    )
    expect(savedPolicy).toBeTruthy()
    await data(
      await api.put(base + '/' + savedPolicy.id, {
        revision: savedPolicy.revision,
        name: savedPolicy.definition.name,
        description: savedPolicy.definition.description,
        enabled: true,
        targets: savedPolicy.targets,
        conditions: {
          match: 'all',
          predicates: [
            {
              field: 'custom_field',
              definition_id: qualificationField,
              operator: 'eq',
              value: false,
            },
          ],
        },
      }),
    )
    const serverSaved = (await data(await api.get(base))).policies.find(
      (row: { id: string }) => row.id === savedPolicy.id,
    )
    expect(serverSaved.definition.conditions.predicates[0].definition_id).toBe(
      qualificationField,
    )
    const [refreshedPolicies] = await Promise.all([
      page.waitForResponse(
        (response) =>
          new URL(response.url()).pathname === base &&
          response.request().method() === 'GET',
      ),
      page.getByRole('button', { name: 'Refresh', exact: true }).click(),
    ])
    const refreshedRow = (await data(refreshedPolicies)).policies.find(
      (row: { id: string }) => row.id === savedPolicy.id,
    )
    expect(refreshedRow.definition.conditions.predicates[0].definition_id).toBe(
      qualificationField,
    )
    await page.route('**/api/inbox/fields', (route) =>
      route.fulfill({
        status: 503,
        contentType: 'application/json',
        body: '{"error":"synthetic metadata lookup failure"}',
      }),
    )
    await page
      .getByRole('button', {
        name: 'Edit Concurrent policy change',
        exact: true,
      })
      .click()
    await expect(
      page.getByRole('combobox', { name: 'Field', exact: true }),
    ).toHaveValue('custom:' + qualificationField)
    await expect(
      page.getByRole('combobox', { name: 'Comparison', exact: true }),
    ).toBeDisabled()
    await expect(save).toBeDisabled()
    await expect(page.locator('[data-sonner-toast]')).toHaveCount(0, {
      timeout: 12000,
    })
    for (const width of [1366, 390])
      for (const theme of ['light', 'dark']) {
        await page.setViewportSize({
          width,
          height: width === 390 ? 844 : 1000,
        })
        await setTheme(theme)
        await revealSection('Additional conditions')
        if (process.env.INBOX_CAPTURE)
          await page.screenshot({
            path: testInfo.outputPath(
              'response-policy-metadata-failure-' +
                theme +
                '-' +
                width +
                '.png',
            ),
            fullPage: false,
            animations: 'disabled',
          })
      }
    await page.unroute('**/api/inbox/fields')
    await page.getByRole('button', { name: 'Retry', exact: true }).click()
    await expect(
      page.getByRole('combobox', {
        name: 'Synthetic qualification',
        exact: true,
      }),
    ).toHaveValue('false')
    await expect(save).toBeEnabled()
    const stillSaved = (await data(await api.get(base))).policies.find(
      (row: { id: string }) => row.id === savedPolicy.id,
    )
    expect(stillSaved.revision).toBe(savedPolicy.revision + 1)
    expect(stillSaved.definition.conditions.predicates[0].value).toBe(false)
    expect(await data(await api.get('/api/inbox/automation'))).toEqual(
      automation,
    )
  } finally {
    await api.loginAsAdmin()
    for (let i = 0; i < 4; i++) {
      const response = await api.get(base)
      if (!response.ok()) break
      const rows = (await response.json()).data.policies
      if (!rows.length) break
      await data(
        await api.del(base + '/' + rows[0].id, undefined, {
          revision: rows[0].revision,
        }),
      )
    }
    await api.del('/api/inbox/sla/policies/' + account.id)
    if (qualificationField)
      await api.del('/api/inbox/fields/' + qualificationField)
    await api.del('/api/contacts/' + contact.id)
    await api.del('/api/accounts/' + account.id)
    await api.put('/api/inbox/workflows', { enabled })
  }
})
