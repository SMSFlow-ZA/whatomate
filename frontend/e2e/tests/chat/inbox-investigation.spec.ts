import { test, expect, type Locator } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { Client } from 'pg'
import { ApiHelper } from '../../helpers/api'
import { inboxContrast } from '../../helpers/inbox-contrast'

interface Scenario {
  RunID: string
  ExecutionID: string
  CaseID: string
  ContactID: string
  Name: string
}
interface Fixture {
  organization_id: string
  account_id: string
  synthetic: boolean
  fake_provider_calls: number
  scenarios: Record<string, Scenario>
  fields: { id: string; key: string; scope: string }[]
}
async function settle(dialog: Locator) {
  await dialog.evaluate(async (element) => {
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
  await expect(dialog).toHaveCSS('opacity', '1')
}

test('records investigated facts, applies exact typed outputs and discards held provider values without dispatch', async ({
  page,
  request,
}, testInfo) => {
  test.setTimeout(180000)
  page.setDefaultTimeout(15000)
  const database = process.env.TEST_DATABASE_URL
  test.skip(!database, 'Requires an explicitly owned disposable database')
  const address = new URL(database!)
  expect(['fixture', 'whatomate_test']).toContain(address.pathname.slice(1))
  expect(
    ['localhost', '127.0.0.1'].includes(address.hostname) ||
      address.hostname.startsWith('whatomate-inbox-'),
  ).toBe(true)
  expect(process.env.INBOX_INVESTIGATION_FIXTURE).toBeTruthy()
  const fixture: Fixture = JSON.parse(
    readFileSync(process.env.INBOX_INVESTIGATION_FIXTURE!, 'utf8'),
  )
  expect(fixture.synthetic).toBe(true)
  expect(fixture.fake_provider_calls).toBe(3)
  const api = new ApiHelper(request)
  await api.loginAsAdmin()
  const profile = (await (await api.get('/api/me')).json()).data
  expect(profile.organization_id).toBe(fixture.organization_id)
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
  await page.goto('/chat')
  const open = async (scenario: Scenario, action: string) => {
    await page.goto('/inbox/cases/' + scenario.CaseID + '/workflows')
    const row = page.locator('[data-workflow-run="' + scenario.RunID + '"]')
    await row
      .locator('summary')
      .filter({ hasText: /^Integration operations$/ })
      .click()
    const operation = row.locator('.integration-run > ol > li').first()
    await operation
      .locator('summary')
      .filter({ hasText: /^Investigation and selected outputs$/ })
      .click()
    await operation.getByRole('button', { name: action, exact: true }).click()
    const review = page.getByRole('dialog')
    await expect(
      review.getByRole('heading', { name: action, exact: true }),
    ).toBeFocused()
    return review
  }
  const snapshot = async (name: string, review: Locator) => {
    await settle(review)
    await expect
      .poll(async () =>
        (await inboxContrast(page)).filter(
          (entry) => entry.ratio < entry.minimum,
        ),
      )
      .toEqual([])
    expect(
      await review.evaluate(
        (element) => element.scrollWidth <= element.clientWidth,
      ),
    ).toBe(true)
    if (process.env.INBOX_CAPTURE)
      await page.screenshot({
        path: testInfo.outputPath(name + '.png'),
        fullPage: true,
        animations: 'disabled',
      })
  }
  const authored = async (review: Locator) => {
    await review
      .getByRole('textbox', { name: /^Investigation reference/ })
      .fill('SYNTHETIC-INVESTIGATION-1')
    await review
      .getByRole('checkbox', {
        name: 'Include investigated result values',
        exact: true,
      })
      .check()
    await review.getByRole('spinbutton', { name: /^Verified count/ }).fill('0')
    await review
      .getByRole('combobox', { name: /^Verified flag/ })
      .selectOption('false')
    await review
      .getByRole('textbox', { name: /^Verified text/ })
      .fill('Investigated value')
    await review.getByLabel(/^Verified date/).fill('2026-11-24')
    await review
      .getByRole('combobox', { name: /^Verified option/ })
      .selectOption('Qualified')
    await review
      .getByRole('textbox', { name: /^Result reference/ })
      .fill('INVESTIGATED-EXTERNAL-1')
  }
  const closeDraft = async (review: Locator) => {
    await review.getByRole('button', { name: 'Cancel', exact: true }).click()
    await page
      .getByRole('alertdialog')
      .getByRole('button', { name: 'Leave', exact: true })
      .click()
    await expect(review).toHaveCount(0)
  }
  const typed = fixture.scenarios.typed!
  const readValues = async (scenario: Scenario) => {
    const rows = (
      await db.query(
        'SELECT definition_id,value FROM inbox_field_values WHERE organization_id=$1 AND definition_id=ANY($2::uuid[]) AND subject_id=ANY($3::uuid[])',
        [
          fixture.organization_id,
          fixture.fields.map((field) => field.id),
          [scenario.CaseID, scenario.ContactID],
        ],
      )
    ).rows
    return Object.fromEntries(
      fixture.fields.map((field) => [
        field.key,
        rows.find((row) => row.definition_id === field.id)?.value,
      ]),
    )
  }
  const initial = {
    verified_count: 7,
    verified_flag: true,
    verified_text: 'Existing value',
    verified_date: '2026-11-20',
    verified_option: 'Pending',
  }
  const updated = {
    verified_count: 0,
    verified_flag: false,
    verified_text: 'Investigated value',
    verified_date: '2026-11-24',
    verified_option: 'Qualified',
  }
  try {
    let review!: Locator
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
        review = await open(typed, 'Review confirmed success')
        await authored(review)
        await expect(review.getByLabel(/^Verified date/)).toHaveCSS(
          'color-scheme',
          theme,
        )
        await review
          .getByRole('heading', {
            name: 'Review confirmed success',
            exact: true,
          })
          .evaluate((element) => element.scrollIntoView({ block: 'start' }))
        await snapshot(
          'investigation-form-' + theme + '-' + viewport.width,
          review,
        )
        if (viewport.width === 390 && theme === 'dark') {
          await review.getByLabel(/^Verified date/).scrollIntoViewIfNeeded()
          await snapshot('investigation-typed-fields-dark-390', review)
        }
        await review
          .getByRole('button', {
            name: 'Review investigated outcome',
            exact: true,
          })
          .click()
        await expect(review).toContainText('This is an operator attestation')
        await expect(
          review.getByRole('heading', {
            name: 'Investigation review ready',
            exact: true,
          }),
        ).toBeFocused()
        await expect(
          review.getByRole('button', {
            name: 'Record investigated success',
            exact: true,
          }),
        ).toBeDisabled()
        await expect(
          review.getByRole('textbox', { name: /^Investigation reference/ }),
        ).toHaveCount(0)
        await snapshot(
          'investigation-preview-' + theme + '-' + viewport.width,
          review,
        )
        if (viewport.width !== 390 || theme !== 'dark') await closeDraft(review)
      }
    }
    expect(await readValues(typed)).toEqual(initial)
    let lost: Record<string, unknown> | undefined
    const recoveryPath =
      '**/api/inbox/integration-executions/' + typed.ExecutionID + '/recover'
    await page.route(recoveryPath, async (route) => {
      lost = route.request().postDataJSON()
      const accepted = await route.fetch()
      expect(accepted.ok()).toBe(true)
      expect((await accepted.json()).data.recovery.kind).toBe(
        'confirm_succeeded',
      )
      await route.abort('failed')
    })
    await review
      .getByRole('textbox', {
        name: 'Investigation or operational reason',
        exact: true,
      })
      .fill(
        'Synthetic finding independently checked; retain selected values only',
      )
    await review
      .getByRole('button', { name: 'Record investigated success', exact: true })
      .click()
    await expect(
      review.getByRole('heading', {
        name: 'Decision acknowledgement unconfirmed',
        exact: true,
      }),
    ).toBeFocused()
    await snapshot('investigation-unconfirmed-dark-390', review)
    await page.unroute(recoveryPath)
    await review
      .getByRole('button', { name: 'Check recorded decision', exact: true })
      .click()
    await expect(review).toHaveCount(0)
    expect(
      (
        await api.post(
          '/api/inbox/integration-executions/' + typed.ExecutionID + '/recover',
          lost!,
        )
      ).ok(),
    ).toBe(true)
    expect(
      (
        await db.query(
          "SELECT count(*)::int AS n FROM inbox_integration_recoveries WHERE execution_id=$1 AND kind='confirm_succeeded'",
          [typed.ExecutionID],
        )
      ).rows[0].n,
    ).toBe(1)
    expect(await readValues(typed)).toEqual(initial)

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
        review = await open(typed, 'Review selected field updates')
        await expect(review).toContainText('Current value')
        await expect(review).toContainText('Proposed value')
        await snapshot(
          'investigation-apply-' + theme + '-' + viewport.width,
          review,
        )
        if (viewport.width !== 390 || theme !== 'dark')
          await review
            .getByRole('button', { name: 'Cancel', exact: true })
            .click()
      }
    }
    await review
      .getByRole('textbox', {
        name: 'Investigation or operational reason',
        exact: true,
      })
      .fill('Apply reviewed synthetic values together')
    const applying = page.waitForRequest(
      (request) =>
        request
          .url()
          .endsWith(
            '/integration-executions/' + typed.ExecutionID + '/recover',
          ) && request.method() === 'POST',
    )
    await review
      .getByRole('button', { name: 'Apply reviewed field values', exact: true })
      .click()
    const appliedInput = (await applying).postDataJSON()
    await expect(review).toHaveCount(0)
    expect(await readValues(typed)).toEqual(updated)
    const version = (
      await db.query('SELECT version FROM inbox_cases WHERE id=$1', [
        typed.CaseID,
      ])
    ).rows[0].version
    expect(
      (
        await api.post(
          '/api/inbox/integration-executions/' + typed.ExecutionID + '/recover',
          appliedInput,
        )
      ).ok(),
    ).toBe(true)
    expect(
      (
        await db.query('SELECT version FROM inbox_cases WHERE id=$1', [
          typed.CaseID,
        ])
      ).rows[0].version,
    ).toBe(version)
    expect(
      (
        await db.query(
          "SELECT count(*)::int AS n FROM inbox_integration_recoveries WHERE execution_id=$1 AND kind='apply_outputs'",
          [typed.ExecutionID],
        )
      ).rows[0].n,
    ).toBe(1)

    const noEffect = fixture.scenarios['no-effect']!
    review = await open(noEffect, 'Review confirmed no effect')
    await expect(
      review.getByRole('checkbox', {
        name: 'Include investigated result values',
        exact: true,
      }),
    ).toHaveCount(0)
    await review
      .getByRole('textbox', { name: /^Investigation reference/ })
      .fill('SYNTHETIC-NO-EFFECT-1')
    await review
      .getByRole('button', { name: 'Review investigated outcome', exact: true })
      .click()
    await snapshot('investigation-no-effect-dark-390', review)
    await review
      .getByRole('textbox', {
        name: 'Investigation or operational reason',
        exact: true,
      })
      .fill('Independently verified synthetic no-effect finding')
    await review
      .getByRole('button', { name: 'Record no-effect finding', exact: true })
      .click()
    await expect(review).toHaveCount(0)
    expect(
      (
        await db.query(
          'SELECT state,result_origin,attempt_count::int AS attempts FROM inbox_integration_executions WHERE id=$1',
          [noEffect.ExecutionID],
        )
      ).rows[0],
    ).toEqual({ state: 'failed', result_origin: 'manual', attempts: 1 })

    const provider = fixture.scenarios['provider-success']!
    review = await open(provider, 'Review discarding selected values')
    await expect(review).toContainText(
      'successful external outcome and its recorded evidence remain intact',
    )
    await expect(review).not.toContainText('FAKE-PROVIDER-1')
    await snapshot('investigation-discard-dark-390', review)
    await review
      .getByRole('textbox', {
        name: 'Investigation or operational reason',
        exact: true,
      })
      .fill('Discard selected synthetic values and retain the provider outcome')
    await review
      .getByRole('button', { name: 'Discard selected values', exact: true })
      .click()
    await expect(review).toHaveCount(0)
    expect(
      (
        await db.query(
          'SELECT state,result_origin,outputs_discarded_at,attempt_count::int AS attempts FROM inbox_integration_executions WHERE id=$1',
          [provider.ExecutionID],
        )
      ).rows[0],
    ).toMatchObject({
      state: 'succeeded',
      result_origin: 'provider',
      attempts: 1,
    })
    expect(
      (
        await db.query(
          'SELECT outputs_discarded_at FROM inbox_integration_executions WHERE id=$1',
          [provider.ExecutionID],
        )
      ).rows[0].outputs_discarded_at,
    ).toBeTruthy()
    expect(await readValues(provider)).toEqual(initial)
    expect(
      (
        await db.query(
          "SELECT count(*)::int AS n FROM messages WHERE organization_id=$1 AND direction='outgoing'",
          [fixture.organization_id],
        )
      ).rows[0].n,
    ).toBe(0)
    expect(
      (await (await api.get('/api/inbox/automation')).json()).data,
    ).toEqual(automation)
    expect(
      (await (await api.get('/api/inbox/integration-settings')).json()).data,
    ).toEqual(integrations)
  } finally {
    await db.end()
  }
})
