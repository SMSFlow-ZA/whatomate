import { expect, test } from '@playwright/test'
import { ApiHelper } from '../../helpers/api'
import { createTestScope } from '../../framework'

test('filters queues, saves a view, and reviews a bulk resolve', async ({ page, request }, testInfo) => {
  const scope = createTestScope('inbox-queues')
  const api = new ApiHelper(request)
  await api.loginAsAdmin()
  const before = await api.get('/api/inbox/workflows')
  expect(before.ok()).toBeTruthy()
  const wasEnabled = (await before.json()).data.enabled
  const account = await api.createWhatsAppAccount({
    name: scope.name('inbox'),
    phone_id: scope.phone().replace(/\D/g, ''),
    business_id: '100000000002',
    access_token: 'local-test-fixture',
  })
  const names = ['E2E Example customer A', 'E2E Example customer B']
  const contacts = [
    await api.createContact('+12025550190', names[0]),
    await api.createContact('+12025550191', names[1]),
  ]
  const viewName = scope.name('saved')
  try {
    expect((await api.put('/api/inbox/workflows', { enabled: true })).ok()).toBeTruthy()
    for (const contact of contacts) {
      const path = '/api/inbox/accounts/' + account.id + '/contacts/' + contact.id
      expect((await api.put(path, { status: 'open', priority: 'urgent' })).ok()).toBeTruthy()
    }
    const profile = await api.get('/api/me')
    expect(profile.ok()).toBeTruthy()
    const { data: user } = await profile.json()
    await page.context().addCookies((await request.storageState()).cookies)
    await page.addInitScript(user => localStorage.setItem('user', JSON.stringify(user)), user)
    await page.goto('/chat')
    await page.getByRole('button', { name: 'Queues', exact: true }).click()
    await page.getByRole('combobox', { name: 'Queues', exact: true }).selectOption('unassigned')
    await page.locator('summary').filter({ hasText: 'Saved views' }).click()
    await page.getByRole('textbox', { name: 'View name' }).fill(viewName)
    await page.getByRole('button', { name: 'Save view', exact: true }).click()
    await expect(page.getByRole('combobox', { name: 'Saved views' }).locator('option:checked')).toHaveText(viewName)
    await page.locator('summary').filter({ hasText: 'Saved views' }).click()

    for (const name of names) await page.getByRole('checkbox', { name: 'Select case for ' + name }).check()
    await page.getByRole('button', { name: 'Review changes', exact: true }).click()
    await expect(page.getByText('Apply this change to 2 selected cases?')).toBeVisible()
    if (process.env.INBOX_CAPTURE) await page.screenshot({ path: testInfo.outputPath('inbox-queue-review.png') })
    await page.getByRole('button', { name: 'Apply changes', exact: true }).click()
    await expect(page.getByText('Updated 2 of 2 cases')).toBeVisible()
    await expect(page.getByText('No cases match these filters.')).toBeVisible()
  } finally {
    const views = await api.get('/api/inbox/views')
    if (views.ok()) {
      const saved = (await views.json()).data.views.find((item: { name: string }) => item.name === viewName)
      if (saved) await api.del('/api/inbox/views/' + saved.id)
    }
    await api.put('/api/inbox/workflows', { enabled: wasEnabled })
    for (const contact of contacts) await api.del('/api/contacts/' + contact.id)
    await api.del('/api/accounts/' + account.id)
  }
})
