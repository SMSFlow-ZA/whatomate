import { randomUUID } from 'node:crypto'
import { readFile } from 'node:fs/promises'
import { expect, test } from '@playwright/test'
import { Client } from 'pg'
import { ApiHelper } from '../../helpers/api'
import { inboxContrast } from '../../helpers/inbox-contrast'

test('investigates scoped audit history, exports safe CSV and recovers errors on both layouts', async ({
  page,
  request,
}, testInfo) => {
  test.setTimeout(180000)
  const database = process.env.TEST_DATABASE_URL
  test.skip(!database, 'Requires an explicitly owned disposable database')
  const destination = new URL(database!)
  expect(['fixture', 'whatomate_test']).toContain(destination.pathname.slice(1))
  expect(
    destination.hostname.startsWith('whatomate-inbox-') ||
      ['localhost', '127.0.0.1'].includes(destination.hostname),
  ).toBe(true)
  expect(['localhost', '127.0.0.1']).toContain(
    new URL(process.env.BASE_URL!).hostname,
  )
  const api = new ApiHelper(request)
  await api.loginAsAdmin()
  const profile = (await (await api.get('/api/me')).json()).data
  const sql = new Client({ connectionString: database })
  await sql.connect()
  const ids = Array.from({ length: 30 }, () => randomUUID()),
    contextId = randomUUID(),
    arrivalId = randomUUID()
  try {
    const initialSettings = await sql.query(
      'SELECT inbox_workflows_enabled,inbox_automation_sends_enabled FROM organizations WHERE id=$1',
      [profile.organization_id],
    )
    expect(initialSettings.rows[0].inbox_automation_sends_enabled).toBe(false)
    for (let i = 0; i < ids.length; i++) {
      await sql.query(
        `INSERT INTO audit_logs(id,organization_id,resource_type,resource_id,user_id,user_name,action,changes,created_at)
        VALUES($1,$2,'user',$3,$3,$4,'updated',$5,now()-interval '1 hour'-($6 * interval '1 second'))`,
        [
          ids[i],
          profile.organization_id,
          profile.id,
          '=Synthetic audit actor',
          JSON.stringify([
            {
              field: 'full_name',
              old_value: 'Synthetic old name',
              new_value: 'Synthetic audit update ' + i,
            },
          ]),
          i,
        ],
      )
    }
    await sql.query(
      `INSERT INTO audit_logs(id,organization_id,resource_type,resource_id,user_id,user_name,action,changes,created_at,context_version,category,event,outcome,reason,actor_verified,request_id)
      VALUES($1,$2,'user',$3,$3,'Synthetic verified actor','event','[]',now()-interval '1 minute',1,'security','auth.sign_in','success','password',true,$4)`,
      [contextId, profile.organization_id, profile.id, randomUUID()],
    )
    await page.context().addCookies((await request.storageState()).cookies)
    await page.addInitScript(
      (user) => localStorage.setItem('user', JSON.stringify(user)),
      profile,
    )
    await page.goto('/settings/audit-logs')
    await expect(
      page.getByRole('heading', { name: 'Recorded activity' }),
    ).toBeVisible()
    await page.getByRole('combobox', { name: 'Action', exact: true }).click()
    await page.getByRole('option', { name: 'Updated', exact: true }).click()
    await page
      .getByText('More filters · event and identifiers', { exact: true })
      .click()
    await page.getByLabel('Resource ID', { exact: true }).fill(profile.id)
    await page
      .getByRole('button', { name: 'Apply filters', exact: true })
      .click()
    await expect(page.locator('tbody tr')).toHaveCount(25)
    await expect(
      page.getByText('30 readable records', { exact: false }),
    ).toBeVisible()
    await page.route(
      '**/api/audit-logs?**',
      (route) => route.fulfill({ status: 503, json: {} }),
      { times: 1 },
    )
    await page.getByRole('button', { name: 'Next', exact: true }).click()
    await expect(
      page.getByText('Record count unavailable', { exact: true }),
    ).toBeVisible()
    await page
      .getByRole('button', { name: 'Retry page 2', exact: true })
      .click()
    await expect(page.locator('tbody tr')).toHaveCount(5)
    await expect(
      page.getByRole('heading', { name: 'Recorded activity' }),
    ).toBeFocused()
    await sql.query(
      `INSERT INTO audit_logs(id,organization_id,resource_type,resource_id,user_id,user_name,action,changes,created_at) VALUES($1,$2,'user',$3,$3,'New arrival outside snapshot','updated','[]',now())`,
      [arrivalId, profile.organization_id, profile.id],
    )
    await page.getByRole('button', { name: 'Previous', exact: true }).click()
    await expect(page.locator('tbody tr')).toHaveCount(25)
    await expect(
      page.getByText('30 readable records', { exact: false }),
    ).toBeVisible()
    await expect(
      page.getByText('New arrival outside snapshot', { exact: true }),
    ).toHaveCount(0)
    await sql.query('DELETE FROM audit_logs WHERE id=$1', [arrivalId])
    const download = page.waitForEvent('download')
    await page.getByRole('button', { name: 'Export CSV', exact: true }).click()
    const file = await download
    const csvPath = testInfo.outputPath('synthetic-audit-export.csv')
    await file.saveAs(csvPath)
    const csv = await readFile(csvPath, 'utf8')
    expect(csv).toContain("'=Synthetic audit actor")
    for (const id of ids) expect(csv).toContain(id)
    await expect(
      page.getByText('30 readable records prepared.', { exact: false }),
    ).toBeVisible()
    async function capture(name: string, results = false) {
      for (const theme of ['light', 'dark'])
        for (const width of [1440, 390]) {
          await page.setViewportSize({ width, height: 1000 })
          await page.evaluate((theme) => {
            document.documentElement.classList.toggle('dark', theme === 'dark')
            document.documentElement.classList.toggle(
              'light',
              theme === 'light',
            )
          }, theme)
          await page.evaluate(async () => {
            await new Promise<void>((resolve) =>
              requestAnimationFrame(() =>
                requestAnimationFrame(() => resolve()),
              ),
            )
            await Promise.all(
              document
                .getAnimations()
                .filter((animation) =>
                  Number.isFinite(
                    Number(animation.effect?.getComputedTiming().endTime),
                  ),
                )
                .map((animation) => animation.finished.catch(() => undefined)),
            )
            document.querySelector('.inbox-page-scroll')?.scrollTo(0, 0)
          })
          if (results)
            await page
              .locator('#audit-results')
              .evaluate((element) => element.scrollIntoView({ block: 'start' }))
          expect(
            await page.evaluate(
              () => document.documentElement.scrollWidth <= innerWidth,
            ),
          ).toBe(true)
          const contrast = await inboxContrast(page, true, true)
          expect(
            contrast.filter((item) => item.ratio + 0.02 < item.minimum),
            'Text contrast',
          ).toEqual([])
          if (process.env.INBOX_CAPTURE)
            await page.screenshot({
              path: testInfo.outputPath(`${name}-${theme}-${width}.png`),
              animations: 'disabled',
              fullPage: false,
            })
        }
      await page.setViewportSize({ width: 1440, height: 1000 })
    }
    await capture('audit-list')
    await capture('audit-results', true)
    await page.goto(`/settings/audit-logs/${ids[0]}`)
    await expect(
      page.getByRole('heading', {
        name: 'Legacy · context not recorded',
        exact: true,
      }),
    ).toBeVisible()
    await expect(
      page.getByText('Synthetic audit update 0', { exact: true }),
    ).toBeVisible()
    await capture('audit-legacy-detail')
    await page.goto(`/settings/audit-logs/${contextId}`)
    await expect(
      page.getByRole('heading', { name: 'Captured context', exact: true }),
    ).toBeVisible()
    await expect(
      page.getByText('Synthetic verified actor', { exact: true }),
    ).toBeVisible()
    await capture('audit-security-detail')
    await page.route(`**/api/audit-logs/${contextId}`, (route) =>
      route.fulfill({
        status: 503,
        json: { message: 'Private server details must not appear' },
      }),
    )
    await page.reload()
    await expect(page.getByRole('alert')).toContainText(
      'temporarily unavailable',
    )
    await expect(page.getByRole('alert')).not.toContainText('Private server')
    await capture('audit-detail-failure')
    await page.unroute(`**/api/audit-logs/${contextId}`)
    await page.getByRole('button', { name: 'Retry record' }).click()
    await expect(
      page.getByRole('heading', { name: 'Captured context', exact: true }),
    ).toBeVisible()
    await page.goto('/settings/audit-logs')
    await expect(
      page.getByRole('heading', { name: 'Export readable records' }),
    ).toBeVisible()
    let release!: () => void
    const hold = new Promise<void>((resolve) => {
      release = resolve
    })
    await page.route('**/api/audit-logs?**', async (route) => {
      await hold
      await route.continue()
    })
    await page.getByRole('button', { name: 'Refresh', exact: true }).click()
    await expect(
      page.getByRole('button', { name: 'Refresh', exact: true }),
    ).toBeDisabled()
    await expect(
      page.getByText('Loading record count…', { exact: true }),
    ).toBeVisible()
    await capture('audit-loading', true)
    release()
    await expect(
      page.getByRole('button', { name: 'Refresh', exact: true }),
    ).toBeEnabled()
    await expect(
      page.getByRole('heading', { name: 'Export readable records' }),
    ).toBeVisible()
    await page.unroute('**/api/audit-logs?**')
    await page.route('**/api/audit-logs?**', (route) =>
      route.fulfill({ status: 503, json: { message: 'Private error' } }),
    )
    await page.getByRole('button', { name: 'Refresh', exact: true }).click()
    await expect(page.getByRole('alert')).toContainText(
      'temporarily unavailable',
    )
    await expect(
      page.getByRole('heading', { name: 'Export readable records' }),
    ).toHaveCount(0)
    await capture('audit-list-failure', true)
    await page.unroute('**/api/audit-logs?**')
    await page.getByRole('button', { name: 'Retry page 1' }).click()
    await expect(
      page.getByRole('heading', { name: 'Export readable records' }),
    ).toBeVisible()
    await page
      .getByText('More filters · event and identifiers', { exact: true })
      .click()
    await page.getByLabel('Request ID', { exact: true }).fill(randomUUID())
    await page
      .getByRole('button', { name: 'Apply filters', exact: true })
      .click()
    await expect(
      page
        .getByText('No readable records match these filters', { exact: true })
        .filter({ visible: true }),
    ).toBeVisible()
    await capture('audit-empty-top')
    await capture('audit-empty', true)
    const stop = await sql.query(
      'SELECT inbox_workflows_enabled,inbox_automation_sends_enabled FROM organizations WHERE id=$1',
      [profile.organization_id],
    )
    expect(stop.rows).toEqual(initialSettings.rows)
  } finally {
    await sql.query('DELETE FROM audit_logs WHERE id=ANY($1::uuid[])', [
      [...ids, contextId, arrivalId],
    ])
    await sql.end()
  }
})

test('retries the failed CSV continuation without restarting or duplicating prior parts', async ({
  page,
  request,
}, testInfo) => {
  test.setTimeout(120000)
  const database = process.env.TEST_DATABASE_URL
  test.skip(!database, 'Requires an explicitly owned disposable database')
  const destination = new URL(database!)
  expect(
    destination.hostname.startsWith('whatomate-inbox-') ||
      ['localhost', '127.0.0.1'].includes(destination.hostname),
  ).toBe(true)
  expect(['fixture', 'whatomate_test']).toContain(destination.pathname.slice(1))
  const api = new ApiHelper(request)
  await api.loginAsAdmin()
  const profile = (await (await api.get('/api/me')).json()).data
  const sql = new Client({ connectionString: database })
  await sql.connect()
  const ids = Array.from({ length: 130 }, () => randomUUID())
  try {
    await sql.query(
      `INSERT INTO audit_logs(id,organization_id,resource_type,resource_id,user_id,user_name,action,changes,created_at)
      SELECT item.id,$2,'user',$3,$3,'Synthetic CSV operator','updated','[]',now()-interval '1 hour'-(item.seq * interval '1 second')
      FROM unnest($1::uuid[]) WITH ORDINALITY AS item(id,seq)`,
      [ids, profile.organization_id, profile.id],
    )
    await page.context().addCookies((await request.storageState()).cookies)
    await page.addInitScript(
      (user) => localStorage.setItem('user', JSON.stringify(user)),
      profile,
    )
    await page.goto('/settings/audit-logs')
    await page.getByRole('combobox', { name: 'Action', exact: true }).click()
    await page.getByRole('option', { name: 'Updated', exact: true }).click()
    await page
      .getByText('More filters · event and identifiers', { exact: true })
      .click()
    await page.getByLabel('Resource ID', { exact: true }).fill(profile.id)
    await page
      .getByRole('button', { name: 'Apply filters', exact: true })
      .click()
    await expect(
      page.getByText('130 readable records', { exact: false }),
    ).toBeVisible()
    await page
      .getByRole('combobox', { name: 'Rows per CSV part', exact: true })
      .click()
    await page.getByRole('option', { name: '100', exact: true }).click()
    const firstDownload = page.waitForEvent('download')
    await page.getByRole('button', { name: 'Export CSV', exact: true }).click()
    const first = await firstDownload,
      firstPath = testInfo.outputPath('csv-part-one.csv')
    await first.saveAs(firstPath)
    await expect(
      page.getByRole('button', { name: 'Download next CSV part', exact: true }),
    ).toBeVisible()
    await page.route(
      '**/api/audit-logs/export?**',
      (route) => route.fulfill({ status: 503, json: {} }),
      { times: 1 },
    )
    await page
      .getByRole('button', { name: 'Download next CSV part', exact: true })
      .click()
    await expect(
      page.getByRole('button', { name: 'Retry CSV part 2', exact: true }),
    ).toBeVisible()
    await expect(
      page.getByRole('button', { name: 'Start new CSV export', exact: true }),
    ).toBeVisible()
    for (const theme of ['light', 'dark'])
      for (const width of [1440, 390]) {
        await page.setViewportSize({ width, height: 1000 })
        await page.evaluate((theme) => {
          document.documentElement.classList.toggle('dark', theme === 'dark')
          document.documentElement.classList.toggle('light', theme === 'light')
        }, theme)
        await page
          .getByRole('heading', {
            name: 'Export readable records',
            exact: true,
          })
          .evaluate((element) => element.scrollIntoView({ block: 'start' }))
        if (process.env.INBOX_CAPTURE)
          await page.screenshot({
            path: testInfo.outputPath(
              `audit-export-retry-${theme}-${width}.png`,
            ),
            animations: 'disabled',
          })
      }
    const secondDownload = page.waitForEvent('download')
    await page
      .getByRole('button', { name: 'Retry CSV part 2', exact: true })
      .click()
    const second = await secondDownload,
      secondPath = testInfo.outputPath('csv-part-two.csv')
    await second.saveAs(secondPath)
    expect(second.suggestedFilename()).toContain('part-2')
    const one = await readFile(firstPath, 'utf8'),
      two = await readFile(secondPath, 'utf8')
    expect(ids.filter((id) => one.includes(id))).toHaveLength(100)
    expect(ids.filter((id) => two.includes(id))).toHaveLength(30)
    for (const id of ids)
      expect(Number(one.includes(id)) + Number(two.includes(id))).toBe(1)
    await expect(
      page.getByText('130 readable records prepared.', { exact: false }),
    ).toBeVisible()
  } finally {
    await sql.query('DELETE FROM audit_logs WHERE id=ANY($1::uuid[])', [ids])
    await sql.end()
  }
})
