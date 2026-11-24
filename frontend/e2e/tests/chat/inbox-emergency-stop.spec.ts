import { test, expect, type Locator } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { Client } from 'pg'
import { ApiHelper } from '../../helpers/api'
import { inboxContrast } from '../../helpers/inbox-contrast'

test('stops a held workflow with the feature disabled and retains its uncertain provider receipt', async ({
  page,
  request,
}, testInfo) => {
  test.setTimeout(150000)
  const database = process.env.TEST_DATABASE_URL
  test.skip(!database, 'Requires explicitly owned disposable database')
  const address = new URL(database!)
  expect(['fixture', 'whatomate_test']).toContain(address.pathname.slice(1))
  expect(
    ['127.0.0.1', 'localhost'].includes(address.hostname) ||
      address.hostname.startsWith('whatomate-inbox-'),
  ).toBe(true)
  expect(process.env.INBOX_INVESTIGATION_FIXTURE).toBeTruthy()
  const fixture = JSON.parse(
    readFileSync(process.env.INBOX_INVESTIGATION_FIXTURE!, 'utf8'),
  )
  expect(fixture.synthetic).toBe(true)
  expect(fixture.fake_provider_calls).toBe(4)
  const scenario = fixture.scenarios['emergency-stop']
  expect(scenario).toBeTruthy()
  const keyless = process.env.INBOX_BROWSER_KEYLESS === '1'
  const label = keyless ? 'keyless' : 'disabled'
  const api = new ApiHelper(request)
  await api.loginAsAdmin()
  const profile = (await (await api.get('/api/me')).json()).data
  expect(profile.organization_id).toBe(fixture.organization_id)
  const before = (await (await api.get('/api/inbox/workflows')).json()).data
    .enabled
  const automation = (await (await api.get('/api/inbox/automation')).json())
    .data
  const integrations = (
    await (await api.get('/api/inbox/integration-settings')).json()
  ).data
  expect(automation.dispatch_available).toBe(false)
  expect(integrations.dispatch_available).toBe(false)
  const db = new Client({ connectionString: database })
  await db.connect()
  await page.context().addCookies((await request.storageState()).cookies)
  await page.addInitScript((user) => {
    localStorage.setItem('user', JSON.stringify(user))
    localStorage.setItem('locale', 'en')
  }, profile)
  const snapshot = async (name: string, view?: Locator) => {
    if (view)
      await view.evaluate(async (element) => {
        await Promise.all(
          element
            .getAnimations({ subtree: true })
            .filter((animation) =>
              Number.isFinite(
                Number(animation.effect?.getComputedTiming().endTime),
              ),
            )
            .map((animation) => animation.finished.catch(() => undefined)),
        )
      })
    await expect
      .poll(async () =>
        (await inboxContrast(page)).filter(
          (entry) => entry.ratio < entry.minimum,
        ),
      )
      .toEqual([])
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true)
    if (process.env.INBOX_CAPTURE)
      await page.screenshot({
        path: testInfo.outputPath(name + '.png'),
        fullPage: true,
        animations: 'disabled',
      })
  }
  try {
    expect(
      (await api.put('/api/inbox/workflows', { enabled: false })).ok(),
    ).toBe(true)
    let review!: Locator
    for (const viewport of [
      { width: 1366, height: 900 },
      { width: 390, height: 844 },
    ]) {
      for (const theme of ['light', 'dark']) {
        await page.setViewportSize(viewport)
        await page.goto('/settings/inbox-workflows')
        await page.evaluate(
          (value) => localStorage.setItem('color-mode', value),
          theme,
        )
        await page.reload()
        await expect(page.locator('html')).toHaveClass(new RegExp(theme))
        await page
          .getByText('Stop a workflow by run identifier', { exact: true })
          .click()
        const input = page.getByRole('textbox', {
          name: /^Workflow run identifier/,
        })
        await input.fill(scenario.RunID)
        await snapshot(
          'emergency-' + label + '-entry-' + theme + '-' + viewport.width,
        )
        await page
          .getByRole('button', {
            name: 'Review stopping workflow',
            exact: true,
          })
          .click()
        review = page.getByRole('dialog')
        await expect(
          review.getByRole('heading', {
            name: 'Review stopping remaining actions',
            exact: true,
          }),
        ).toBeFocused()
        await expect(review).toContainText(scenario.RunID)
        await expect(review).toContainText(scenario.CaseID)
        await expect(review).toContainText(fixture.account_id)
        await expect(review).toContainText(
          'An earlier provider outcome is unresolved',
        )
        await expect(
          review.getByRole('button', {
            name: 'Stop remaining actions',
            exact: true,
          }),
        ).toBeDisabled()
        await snapshot(
          'emergency-' + label + '-review-' + theme + '-' + viewport.width,
          review,
        )
        if (viewport.width !== 390 || theme !== 'dark')
          await review
            .getByRole('button', { name: 'Cancel', exact: true })
            .click()
      }
    }
    let submitted: Record<string, unknown> | undefined
    const path = '**/api/inbox/workflow-runs/' + scenario.RunID + '/cancel'
    await page.route(path, async (route) => {
      submitted = route.request().postDataJSON()
      const accepted = await route.fetch()
      expect(accepted.ok()).toBe(true)
      expect((await accepted.json()).data.cancelled).toBe(true)
      await route.abort('failed')
    })
    await review
      .getByRole('textbox', {
        name: 'Investigation or operational reason',
        exact: true,
      })
      .fill('Synthetic emergency stop; preserve unresolved provider outcome')
    await review
      .getByRole('button', { name: 'Stop remaining actions', exact: true })
      .click()
    await expect(
      review.getByRole('heading', {
        name: 'Decision acknowledgement unconfirmed',
        exact: true,
      }),
    ).toBeFocused()
    await snapshot('emergency-' + label + '-unconfirmed-dark-390', review)
    await page.unroute(path)
    await review
      .getByRole('button', { name: 'Check recorded decision', exact: true })
      .click()
    await expect(review).toHaveCount(0)
    expect(submitted?.operation_id).toBeTruthy()
    const repeated = await api.post(
      '/api/inbox/workflow-runs/' + scenario.RunID + '/cancel',
      submitted!,
    )
    expect(repeated.ok()).toBe(true)
    expect((await repeated.json()).data.recovery.id).toBe(
      submitted!.operation_id,
    )
    expect(
      (
        await db.query('SELECT state FROM inbox_workflow_runs WHERE id=$1', [
          scenario.RunID,
        ])
      ).rows[0].state,
    ).toBe('sequence_cancelled')
    expect(
      (
        await db.query(
          'SELECT state,attempt_count::int AS attempts FROM inbox_integration_executions WHERE id=$1',
          [scenario.ExecutionID],
        )
      ).rows[0],
    ).toEqual({ state: 'unknown', attempts: 1 })
    expect(
      (
        await db.query(
          "SELECT count(*)::int AS n FROM inbox_integration_recoveries WHERE run_id=$1 AND kind='cancel_remaining'",
          [scenario.RunID],
        )
      ).rows[0].n,
    ).toBe(1)
    expect(
      (
        await db.query(
          "SELECT count(*)::int AS n FROM messages WHERE organization_id=$1 AND direction='outgoing'",
          [fixture.organization_id],
        )
      ).rows[0].n,
    ).toBe(0)
    expect(
      (await (await api.get('/api/inbox/workflows')).json()).data.enabled,
    ).toBe(false)
    expect((await api.get('/api/inbox/automation')).ok()).toBe(false)
    expect(
      (
        await db.query(
          'SELECT inbox_automation_sends_enabled AS enabled FROM organizations WHERE id=$1',
          [fixture.organization_id],
        )
      ).rows[0].enabled,
    ).toBe(automation.enabled)
    expect(
      (await (await api.get('/api/inbox/integration-settings')).json()).data,
    ).toEqual(integrations)
  } finally {
    await api.put('/api/inbox/workflows', { enabled: before })
    await db.end()
  }
})
