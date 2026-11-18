import { expect as baseExpect, test } from '@playwright/test'
import { ApiHelper } from '../../helpers/api'
import { createTestScope } from '../../framework'

const expect = baseExpect.configure({ timeout: 20000 })

test('starts, snoozes and resolves a case inside the chat thread', async ({ page, request }, testInfo) => {
  const scope = createTestScope('inbox-lifecycle')
  const api = new ApiHelper(request)
  await api.loginAsAdmin()
  const before = await api.get('/api/inbox/workflows')
  expect(before.ok()).toBeTruthy()
  const wasEnabled = (await before.json()).data.enabled
  const account = await api.createWhatsAppAccount({
    name: scope.name('inbox'),
    phone_id: scope.phone().replace(/\D/g, ''),
    business_id: '100000000001',
    access_token: 'local-test-fixture',
  })
  const contact = await api.createContact('+12025550189', 'E2E Demo customer')
  try {
    expect((await api.put('/api/inbox/workflows', { enabled: true })).ok()).toBeTruthy()
    const profile = await api.get('/api/me')
    expect(profile.ok()).toBeTruthy()
    await page.context().addCookies((await request.storageState()).cookies)
    await page.addInitScript(user => localStorage.setItem('user', JSON.stringify(user)), (await profile.json()).data)
    await page.goto('/chat/' + contact.id + '?inbox_account=' + encodeURIComponent(account.name) + '&inbox_account_id=' + account.id, { waitUntil: 'domcontentloaded' })
    await page.getByRole('button', { name: 'Start case' }).click()
    const status = page.getByRole('combobox', { name: 'Case status' })
    const priority = page.getByRole('combobox', { name: 'Case priority' })
    await expect(status).toHaveValue('open')
    await priority.selectOption('urgent')
    await expect(priority).toHaveValue('urgent')
    await status.selectOption('snoozed')
    const tomorrow = new Date(Date.now() + 48 * 60 * 60 * 1000)
    const localDate = new Date(tomorrow.getTime() - tomorrow.getTimezoneOffset() * 60_000).toISOString().slice(0, 16)
    await page.getByLabel('Snooze until').fill(localDate)
    await page.getByRole('button', { name: 'Save', exact: true }).click()
    await expect(status).toHaveValue('snoozed')
    await expect(page.getByRole('link', { name: /E2E Demo customer/ })).toContainText('Snoozed')
    if (process.env.INBOX_CAPTURE) await page.screenshot({ path: testInfo.outputPath('inbox-case-desktop.png') })
    await page.setViewportSize({ width: 390, height: 844 })
    await status.selectOption('resolved')
    await expect(status).toHaveValue('resolved')
    if (process.env.INBOX_CAPTURE) await page.screenshot({ path: testInfo.outputPath('inbox-case-mobile.png') })
    await status.selectOption('open')
    await expect(status).toHaveValue('open')
  } finally {
    await api.put('/api/inbox/workflows', { enabled: wasEnabled })
    await api.del('/api/contacts/' + contact.id)
    await api.del('/api/accounts/' + account.id)
  }
})
