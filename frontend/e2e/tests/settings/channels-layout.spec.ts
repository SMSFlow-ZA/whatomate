import { test, expect } from '@playwright/test'
import { login, TEST_USERS } from '../../helpers'

const id = '00000000-0000-4000-8000-00000000c010'
const name = 'CustomerSMSConnection'.repeat(5)

test.beforeEach(async ({ page }) => {
  await login(page, {
    email: process.env.E2E_ADMIN_EMAIL || TEST_USERS.admin.email,
    password: process.env.E2E_ADMIN_PASSWORD || TEST_USERS.admin.password,
    role: 'admin',
  })
  let enabled = true
  const connection = () => ({
    id, name, provider: 'smsflow', enabled, max_segments: 3,
    capabilities: { text: true, inbound: false },
    balance: { amount: '0', unit: 'credits', currency: '' },
    low_balance: true, balance_threshold: '100', poll_failures: 1,
    error_code: 'validation_required',
  })
  await page.route('**/api/channel-connections', async route => {
    await route.fulfill({ json: { status: 'success', data: { connections: [connection()] } } })
  })
  await page.route('**/api/channel-connections/' + id, async route => {
    expect(route.request().method()).toBe('PUT')
    const body = route.request().postDataJSON()
    expect(body.enabled).toBe(false)
    expect(body.credentials).toBeUndefined()
    enabled = false
    await route.fulfill({ json: { status: 'success', data: connection() } })
  })
  await page.route('**/api/channels/providers', async route => {
    await route.fulfill({ json: { status: 'success', data: { credentials_configured: true, providers: [] } } })
  })
})

for (const width of [320, 390, 1280]) {
  test(`channel actions and long names fit a ${width}px viewport`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 })
    await page.goto('/settings/channels')
    const heading = page.getByRole('heading', { name, exact: true })
    await expect(heading).toBeVisible()
    const card = page.locator('section').filter({ has: heading })
    for (const button of await card.getByRole('button').all()) {
      const bounds = await button.boundingBox()
      expect(bounds).not.toBeNull()
      expect(bounds!.x).toBeGreaterThanOrEqual(0)
      expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(width)
    }
    expect(await card.evaluate(element => element.scrollWidth - element.clientWidth)).toBeLessThanOrEqual(1)
    await card.getByRole('button', { name: 'Stop pending SMS dispatch', exact: true }).click()
    await expect(card.getByText('Dispatch disabled', { exact: true })).toBeVisible()
    await expect(card.getByRole('button', { name: 'Enable dispatch', exact: true })).toBeVisible()
  })
}

test('account state explains one-way capability and retains empty credential fields', async ({ page }) => {
  await page.goto('/settings/channels')
  await expect(page.getByText(/No inbound route/)).toBeVisible()
  await expect(page.getByText(/Low balance/, { exact: false }).first()).toBeVisible()
  await expect(page.getByText(/Already-submitted messages are retained for reconciliation/)).toBeVisible()
  await page.getByRole('button', { name: 'Edit / rotate credentials', exact: true }).click()
  await expect(page.getByLabel('Client ID', { exact: true })).toHaveValue('')
  await expect(page.getByLabel('Client secret', { exact: true })).toHaveValue('')
  await expect(page.getByLabel('Maximum segments per message')).toHaveValue('3')
})
