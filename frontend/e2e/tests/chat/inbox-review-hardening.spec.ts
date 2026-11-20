import { test, expect } from '@playwright/test'
import { ApiHelper } from '../../helpers/api'
import { createTestScope } from '../../framework'
import { inboxContrast } from '../../helpers/inbox-contrast'

test.use({ hasTouch: true })

test('reviews scope, selection, snooze, keyboard boundaries and configuration recovery', async ({
  page,
  request,
}, testInfo) => {
  const scope = createTestScope('inbox-review'),
    api = new ApiHelper(request)
  await api.loginAsAdmin()
  const before = (await (await api.get('/api/inbox/workflows')).json()).data
    .enabled
  await api.put('/api/inbox/workflows', { enabled: true })
  const dispatch = (await (await api.get('/api/inbox/automation')).json()).data
  expect(dispatch.dispatch_available).toBe(false)
  const profile = (await (await api.get('/api/me')).json()).data
  const account = await api.createWhatsAppAccount({
    name: scope.name('review'),
    phone_id: scope.phone().replace(/\D/g, ''),
    business_id: '100000000007',
    access_token: 'synthetic-do-not-send',
  })
  const contact = await api.createContact(scope.phone(), 'Review customer')
  const thread = '/api/inbox/accounts/' + account.id + '/contacts/' + contact.id
  try {
    expect(
      (
        await api.put('/api/inbox/sla/policies/' + account.id, {
          first_reply_minutes: 30,
          next_reply_minutes: 60,
          resolution_minutes: 480,
          timezone: 'Africa/Johannesburg',
          business_hours: true,
          shifts: [1, 2, 3, 4, 5].map((Day) => ({
            Day,
            StartMinute: 480,
            EndMinute: 1020,
          })),
        })
      ).ok(),
    ).toBeTruthy()
    const c = (await (await api.put(thread, { status: 'open' })).json()).data
      .case
    expect(
      (
        await api.post(thread + '/assignment', {
          case_id: c.id,
          version: c.version,
          assignee_id: profile.id,
        })
      ).ok(),
    ).toBeTruthy()
    await page.context().addCookies((await request.storageState()).cookies)
    await page.addInitScript(
      (user) => localStorage.setItem('user', JSON.stringify(user)),
      profile,
    )
    await page.goto('/chat')
    await page.getByRole('button', { name: /^Filters(?: \(\d+\))?$/ }).click()
    await page
      .getByRole('combobox', { name: 'WhatsApp account', exact: true })
      .selectOption(account.id)
    await page
      .getByRole('searchbox', { name: 'Search agents', exact: true })
      .fill(profile.full_name)
    await page
      .getByRole('combobox', { name: 'Assignee', exact: true })
      .selectOption(profile.id)
    await page.keyboard.press('Escape')
    await expect(page.getByTestId('inbox-filter-summary')).toContainText(
      account.name,
    )
    await expect(
      page.getByRole('button', { name: 'Filters (2)', exact: true }),
    ).toBeVisible()
    await page
      .getByRole('checkbox', {
        name: 'Select all eligible cases on this page',
        exact: true,
      })
      .check()
    await expect(
      page.getByRole('button', { name: 'Review changes', exact: true }),
    ).toBeVisible()
    await page
      .getByRole('checkbox', {
        name: 'Select all eligible cases on this page',
        exact: true,
      })
      .uncheck()
    await page
      .getByRole('button')
      .filter({ hasText: 'Review customer' })
      .click()
    await page
      .getByRole('combobox', { name: 'Case status', exact: true })
      .selectOption('snoozed')
    await page
      .getByRole('button', { name: 'Next working day', exact: true })
      .tap()
    await expect(page.locator('input[type="datetime-local"]')).not.toHaveValue(
      '',
    )
    await page.getByRole('button', { name: 'Cancel', exact: true }).click()
    expect((await (await api.get(thread)).json()).data.case.status).toBe('open')
    const composer = page.getByRole('textbox').last()
    await composer.focus()
    await page.keyboard.press('Control+Alt+m')
    await expect(page.getByRole('dialog')).toHaveCount(0)
    await page.goto('/settings/custom-fields')
    await page
      .getByRole('button', { name: /New field/ })
      .first()
      .click()
    await page
      .getByRole('textbox', { name: 'Field label', exact: true })
      .fill('Review choices')
    await page
      .getByRole('combobox', { name: 'Field type', exact: true })
      .selectOption('select')
    await page
      .getByRole('textbox', { name: 'Choices, one per line', exact: true })
      .fill('New\nNew')
    await expect(
      page
        .getByRole('alert')
        .filter({ hasText: 'duplicates an earlier choice' }),
    ).toBeVisible()
    await page
      .getByRole('textbox', { name: 'Choices, one per line', exact: true })
      .fill('New\nComplete')
    await expect(
      page.getByText('Choice on line 2 duplicates an earlier choice.'),
    ).toHaveCount(0)
    for (const theme of ['light', 'dark']) {
      await page.setViewportSize({ width: 390, height: 844 })
      await page.evaluate((theme) => {
        document.documentElement.classList.toggle('light', theme === 'light')
        document.documentElement.classList.toggle('dark', theme === 'dark')
      }, theme)
      expect(
        await page.evaluate(() => document.documentElement.scrollWidth),
      ).toBeLessThanOrEqual(390)
      await expect.poll(() => page.locator('aside[aria-label="Main navigation"]').evaluate(element => element.getBoundingClientRect().right)).toBeLessThanOrEqual(0)
      await expect.poll(async () => (await inboxContrast(page)).filter(item => item.ratio < item.minimum)).toEqual([])
      if (process.env.INBOX_CAPTURE)
        await page.screenshot({
          path: testInfo.outputPath(
            'review-field-editor-' + theme + '-mobile.png',
          ),
          fullPage: true,
        })
    }
    // Cancel the owned editor explicitly; no customer data or messages are used.
    await page.getByRole('button', { name: 'Cancel', exact: true }).click()
    const discard = page.getByRole('alertdialog')
    if (await discard.isVisible())
      await discard.getByRole('button', { name: 'Leave', exact: true }).click()
    expect(
      (await (await api.get('/api/inbox/automation')).json()).data,
    ).toEqual(dispatch)
  } finally {
    await api.del('/api/inbox/sla/policies/' + account.id)
    await api.del('/api/contacts/' + contact.id)
    await api.del('/api/accounts/' + account.id)
    await api.put('/api/inbox/workflows', { enabled: before })
  }
})
