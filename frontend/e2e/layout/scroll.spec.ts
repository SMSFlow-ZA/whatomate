import { expect, test } from '@playwright/test'

test.beforeEach(async ({ page }) => {
  await page.route('**/api/**', route => route.fulfill({ json: { data: route.request().url().includes('/catalog') ? { permissions: [], grants: [] } : [] } }))
})

for (const viewport of [{ width: 1280, height: 720 }, { width: 390, height: 640 }, { width: 844, height: 390 }]) {
  test(`all extension pages reach the bottom at ${viewport.width}x${viewport.height}`, async ({ page }) => {
    await page.setViewportSize(viewport)
    for (const module of ['knowledge', 'directory', 'cases', 'ai', 'conversation', 'reports', 'governance']) {
      await page.goto(`/e2e/fixtures/scroll-layout.html?module=${module}`)
      await expect(page.locator(`#main-content main.${module}-page`)).toBeVisible()
      await expect(page).toHaveTitle('Example Employee Relations — demonstration')
      const brand = page.locator('[title="Example Employee Relations — demonstration"]:visible')
      await expect(brand).toHaveText('Example Employee Relations — demonstration')
      expect(await brand.evaluate(el => el.scrollWidth <= el.clientWidth && el.getBoundingClientRect().bottom <= 48)).toBe(true)
      // Knowledge reproduces the real long upload/search form without injected
      // content. Other pages exercise an arbitrarily long extension payload.
      if (module !== 'knowledge') await page.locator(`#main-content main.${module}-page`).evaluate(el => {
        const content = document.createElement('div'); content.style.height = '1500px'; content.textContent = 'Synthetic long layout content'; el.append(content)
        const reference = document.createElement('p'); reference.textContent = 'f'.repeat(256); el.append(reference)
      })
      const bottom = page.locator(`#main-content main.${module}-page`)
      await page.mouse.move(viewport.width - 60, viewport.height / 2)
      await page.mouse.wheel(0, 10000)
      await expect.poll(async () => bottom.evaluate(el => el.getBoundingClientRect().bottom)).toBeLessThanOrEqual(viewport.height + 1)
      expect(await page.locator('#route-content').evaluate(el => el.scrollWidth <= el.clientWidth + 1)).toBe(true)
      if (await page.locator('#route-content').evaluate(el => el.scrollTop) > 300) {
        await expect(page.getByRole('button', { name: 'Scroll to top' })).toBeVisible()
        await page.getByRole('button', { name: 'Scroll to top' }).click()
      } else await page.mouse.wheel(0, -10000)
      await expect.poll(async () => bottom.evaluate(el => el.getBoundingClientRect().top)).toBeGreaterThanOrEqual(0)
    }
  })
  for (const type of ['dialog', 'alert']) test(`long ${type} stays reachable at ${viewport.width}x${viewport.height}`, async ({ page }) => {
    await page.setViewportSize(viewport)
    await page.goto('/e2e/fixtures/scroll-layout.html?module=dialogs')
    await page.getByRole('button', { name: `Open ${type}`, exact: true }).click()
    const dialog = page.getByRole(type === 'dialog' ? 'dialog' : 'alertdialog')
    await expect(dialog).toBeVisible()
    await expect.poll(async () => dialog.evaluate(el => el.getBoundingClientRect().top)).toBeGreaterThanOrEqual(0)
    await page.mouse.move(viewport.width / 2, viewport.height / 2)
    await page.mouse.wheel(0, 10000)
    const finish = page.getByRole('button', { name: `Finish ${type}` })
    await expect(finish).toBeInViewport()
    await finish.click(); await expect(dialog).toBeHidden()
  })
}
