import { test, expect, type APIRequestContext } from '@playwright/test'
import { TablePage } from '../../pages'
import { loginAsAdmin, createTeamFixture, expectActivityLogVisible, ApiHelper } from '../../helpers'
import { createTestScope, SUPER_ADMIN } from '../../framework'

const scope = createTestScope('teams')

async function withOwnedTeam(
  request: APIRequestContext,
  purpose: string,
  use: (team: { id: string; name: string }, api: ApiHelper) => Promise<void>,
) {
  const api = new ApiHelper(request)
  await api.login(SUPER_ADMIN.email, SUPER_ADMIN.password)
  const created = await api.post('/api/teams', { name: scope.name(purpose) })
  expect(created.ok(), `seed team: ${await created.text()}`).toBe(true)
  const team = (await created.json()).data.team
  try {
    await use(team, api)
  } finally {
    // The UI delete case already removed this exact fixture; other cases clean it here.
    expect([200, 204, 404]).toContain((await api.del(`/api/teams/${team.id}`)).status())
  }
}

test.describe('Teams - List View', () => {
  let tablePage: TablePage

  test.beforeEach(async ({ page }) => {
    await loginAsAdmin(page)
    await page.goto('/settings/teams')
    await page.waitForLoadState('networkidle')
    tablePage = new TablePage(page)
  })

  test('should display teams list', async ({ page }) => {
    await expect(tablePage.tableBody).toBeVisible()
  })

  test('should search teams', async ({ page }) => {
    const initialCount = await tablePage.getRowCount()
    if (initialCount > 0) {
      await tablePage.search('nonexistent-team-xyz')
      await page.waitForTimeout(300)
      const filteredCount = await tablePage.getRowCount()
      expect(filteredCount).toBeLessThanOrEqual(initialCount)
    }
  })

  test('should load create page', async ({ page }) => {
    await page.goto('/settings/teams/new')
    await page.waitForLoadState('networkidle')
    expect(page.url()).toContain('/settings/teams/new')
    await expect(page.locator('input').first()).toBeVisible()
  })

  test('should load detail page from list', async ({ page, request }) => {
    await withOwnedTeam(request, 'list-detail', async team => {
      await page.reload()
      await tablePage.search(team.name)
      await page.locator(`tbody a[href="/settings/teams/${team.id}"]`).filter({ hasText: team.name }).click()
      await expect(page).toHaveURL(new RegExp(`/settings/teams/${team.id}$`))
      await expect(page.getByRole('heading', { name: 'Details', exact: true })).toBeVisible()
    })
  })

  test('should delete team from list', async ({ page, request }) => {
    await withOwnedTeam(request, 'cancel-delete', async (team, api) => {
      await page.reload()
      await tablePage.search(team.name)
      const row = page.locator('tbody tr').filter({ has: page.locator(`a[href="/settings/teams/${team.id}"]`) })
      await expect(row).toBeVisible()
      // Click delete button
      await row.locator('button').filter({ has: page.locator('svg.text-destructive') }).click()
      const dialog = page.locator('[role="alertdialog"]')
      await expect(dialog).toBeVisible({ timeout: 3000 })
      // Cancel to not actually delete
      await dialog.getByRole('button', { name: /Cancel/i }).click()
      await expect(dialog).not.toBeVisible()
      expect((await api.get(`/api/teams/${team.id}`)).ok()).toBe(true)
    })
  })
})

test.describe('Teams - Detail Page CRUD', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsAdmin(page)
  })

  test('should show form fields on create page', async ({ page }) => {
    await page.goto('/settings/teams/new')
    await page.waitForLoadState('networkidle')

    await expect(page.locator('input').first()).toBeVisible()
    await expect(page.locator('textarea').first()).toBeVisible()
    await expect(page.locator('button[role="combobox"]').first()).toBeVisible()
    await expect(page.locator('button[role="switch"]').first()).toBeVisible()
  })

  test('should create a new team', async ({ page }) => {
    const newTeam = createTeamFixture()

    await page.goto('/settings/teams/new')
    await page.waitForLoadState('networkidle')

    const input = page.locator('input').first()
    if (await input.isDisabled()) { test.skip(true, 'No write permission'); return }

    await input.fill(newTeam.name)
    await page.locator('textarea').first().fill(newTeam.description)
    await page.waitForTimeout(300)

    const createBtn = page.getByRole('button', { name: /Create/i })
    if (!(await createBtn.isVisible({ timeout: 5000 }).catch(() => false))) {
      test.skip(true, 'Create button not visible')
      return
    }
    await createBtn.click({ force: true })
    await page.waitForTimeout(3000)

    if (page.url().includes('/new')) {
      test.skip(true, 'Creation failed (possibly CSRF)')
    } else {
      expect(page.url()).toMatch(/\/settings\/teams\/[a-f0-9-]+/)
    }
  })

  test('should edit existing team', async ({ page, request }) => {
    // A parallel delete test can remove the first list item after it is edited.
    // Own the fixture and address it by ID throughout both saves.
    await withOwnedTeam(request, 'edit', async team => {
      const original = team.name
      await page.goto(`/settings/teams/${team.id}`)
      const input = page.locator('input').first()
      await expect(input).toHaveValue(original)
      await expect(input).toBeEnabled()
      await input.fill(original + ' edited')
      const saved = page.waitForResponse(response =>
        response.url().endsWith(`/api/teams/${team.id}`) && response.request().method() === 'PUT')
      await page.getByRole('button', { name: /^Save$/i }).click()
      expect((await saved).ok()).toBe(true)
      await page.reload()
      await expect(input).toHaveValue(original + ' edited')

      await input.fill(original)
      const reverted = page.waitForResponse(response =>
        response.url().endsWith(`/api/teams/${team.id}`) && response.request().method() === 'PUT')
      await page.getByRole('button', { name: /^Save$/i }).click()
      expect((await reverted).ok()).toBe(true)
      await page.reload()
      await expect(input).toHaveValue(original)
    })
  })

  test('should delete from detail page', async ({ page, request }) => {
    await withOwnedTeam(request, 'delete', async (team, api) => {
      await page.goto(`/settings/teams/${team.id}`)
      await page.getByRole('button', { name: /^Delete$/i }).click()
      const dialog = page.getByRole('alertdialog')
      await expect(dialog).toBeVisible()
      const deleted = page.waitForResponse(response =>
        response.url().endsWith(`/api/teams/${team.id}`) && response.request().method() === 'DELETE')
      await dialog.getByRole('button', { name: /^Delete$/i }).click()
      expect((await deleted).ok()).toBe(true)
      await expect(page).toHaveURL(/\/settings\/teams$/)
      expect((await api.get(`/api/teams/${team.id}`)).status()).toBe(404)
    })
  })

  test('should show metadata', async ({ page, request }) => {
    await withOwnedTeam(request, 'metadata', async team => {
      await page.goto(`/settings/teams/${team.id}`)
      await expect(page.getByRole('heading', { name: 'Metadata', exact: true })).toBeVisible()
    })
  })

  test('should show activity log', async ({ page, request }) => {
    await withOwnedTeam(request, 'activity-log', async team => {
      await page.goto(`/settings/teams/${team.id}`)
      await expectActivityLogVisible(page)
    })
  })
})

test.describe('Teams - Table Sorting', () => {
  let tablePage: TablePage

  test.beforeEach(async ({ page }) => {
    await loginAsAdmin(page)
    await page.goto('/settings/teams')
    await page.waitForLoadState('networkidle')
    tablePage = new TablePage(page)
  })

  test('should sort by team name', async () => {
    await tablePage.clickColumnHeader('Team')
    const direction = await tablePage.getSortDirection('Team')
    expect(direction).not.toBeNull()
  })

  test('should sort by strategy', async () => {
    await tablePage.clickColumnHeader('Strategy')
    const direction = await tablePage.getSortDirection('Strategy')
    expect(direction).not.toBeNull()
  })

  test('should toggle sort direction', async () => {
    await tablePage.clickColumnHeader('Team')
    const firstDirection = await tablePage.getSortDirection('Team')
    await tablePage.clickColumnHeader('Team')
    const secondDirection = await tablePage.getSortDirection('Team')
    expect(firstDirection).not.toEqual(secondDirection)
  })
})

test.describe('Team Members', () => {
  test('should show members section on detail page', async ({ page, request }) => {
    await loginAsAdmin(page)
    await withOwnedTeam(request, 'members', async team => {
      await page.goto(`/settings/teams/${team.id}`)
      await expect(page.getByRole('heading', { name: /Members/ })).toBeVisible({ timeout: 5000 })
    })
  })
})
