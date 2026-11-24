import { test, expect } from '@playwright/test'
import { Client } from 'pg'
import { randomUUID } from 'node:crypto'
import { ApiHelper } from '../../helpers/api'
import { createTestScope } from '../../framework'
import { inboxContrast } from '../../helpers/inbox-contrast'

test('reviews recorded integration truth and cursor history without dispatching', async ({
  page,
  request,
}, testInfo) => {
  test.setTimeout(120000)
  page.setDefaultTimeout(15000)
  const database = process.env.TEST_DATABASE_URL
  test.skip(!database, 'Requires an explicitly disposable database')
  const dbURL = new URL(database!)
  expect(['fixture', 'whatomate_test']).toContain(dbURL.pathname.slice(1))
  expect(
    ['127.0.0.1', 'localhost'].includes(dbURL.hostname) ||
      dbURL.hostname.startsWith('whatomate-inbox-'),
  ).toBeTruthy()
  const api = new ApiHelper(request),
    scope = createTestScope('integration-history')
  await api.loginAsAdmin()
  const before = (await (await api.get('/api/inbox/workflows')).json()).data
    .enabled
  await api.put('/api/inbox/workflows', { enabled: true })
  const dispatch = (await (await api.get('/api/inbox/automation')).json()).data
  const integrationSettings = (
    await (await api.get('/api/inbox/integration-settings')).json()
  ).data
  expect(dispatch.dispatch_available).toBe(false)
  expect(integrationSettings.dispatch_available).toBe(false)
  const profile = (await (await api.get('/api/me')).json()).data
  const account = await api.createWhatsAppAccount({
    name: scope.name('Synthetic support'),
    phone_id: scope.phone().replace(/\D/g, ''),
    business_id: '100000000009',
    access_token: 'synthetic-do-not-send',
  })
  const contact = await api.createContact(
    scope.phone(),
    'Synthetic recovery enquiry',
  )
  const current = (
    await (
      await api.put(
        '/api/inbox/accounts/' + account.id + '/contacts/' + contact.id,
        { status: 'open' },
      )
    ).json()
  ).data.case
  const definitionResponse = await api.post('/api/inbox/integrations', {
    name: scope.name('Synthetic fixture service'),
    description: 'Recorded fixture only; never submitted',
    endpoint_key: 'synthetic_crm',
    auth_mode: 'none',
    enabled: false,
    inputs: [],
    outputs: [],
    reference_key: '',
  })
  expect(definitionResponse.ok()).toBe(true)
  const definition = (await definitionResponse.json()).data.integration
  const macroResponse = await api.post('/api/inbox/workflow-definitions', {
    kind: 'macro',
    name: scope.name('Synthetic stopped sequence'),
    description: 'Read-only synthetic history fixture',
    visibility: 'organization',
    trigger: '',
    position: 0,
    conditions: {},
    actions: [
      { type: 'integration', value: definition.id, integration_inputs: {} },
      { type: 'integration', value: definition.id, integration_inputs: {} },
    ],
  })
  expect(macroResponse.ok()).toBe(true)
  const macro = (await macroResponse.json()).data.workflow
  const runId = randomUUID(),
    operationId = randomUUID(),
    secondOperationId = randomUUID()
  const client = new Client({ connectionString: database })
  await client.connect()
  let completed = false
  try {
    // These are labelled historical fixtures, not actual provider receipts.
    // The cancelled parent prevents any dispatcher from claiming this work.
    await client.query(
      `INSERT INTO inbox_workflow_runs(id,created_at,updated_at,organization_id,workflow_id,case_id,account_id,contact_id,actor_id,kind,workflow_name,workflow_updated_at,state,sequenced,next_action,action_outcomes)
      VALUES($1,NOW(),NOW(),$2,$3,$4,$5,$6,$7,'macro',$8,NOW(),'sequence_cancelled',true,0,$9::jsonb)`,
      [
        runId,
        profile.organization_id,
        macro.id,
        current.id,
        account.id,
        contact.id,
        profile.id,
        macro.name,
        JSON.stringify([
          {
            index: 0,
            type: 'integration',
            status: 'cancelled',
            reason: 'workflow_cancelled',
          },
          {
            index: 1,
            type: 'integration',
            status: 'cancelled',
            reason: 'workflow_cancelled',
          },
        ]),
      ],
    )
    await client.query(
      `INSERT INTO inbox_integration_executions(id,created_at,updated_at,organization_id,run_id,action_index,integration_id,case_id,account_id,contact_id,actor_id,definition_digest,request_digest,case_digest,encrypted_request,encrypted_result,state,outcome,attempt_count,result_origin,provider_status,duration_millis,started_at,completed_at)
      VALUES($1,NOW(),NOW(),$2,$3,0,$4,$5,$6,$7,$8,$9,$9,$9,'PRIVATE_SYNTHETIC_REQUEST','PRIVATE_SYNTHETIC_RESULT','succeeded','output_context_changed',1,'provider',200,12,NOW(),NOW())`,
      [
        operationId,
        profile.organization_id,
        runId,
        definition.id,
        current.id,
        account.id,
        contact.id,
        profile.id,
        '0'.repeat(64),
      ],
    )
    await client.query(
      `INSERT INTO inbox_integration_executions(id,created_at,updated_at,organization_id,run_id,action_index,integration_id,case_id,account_id,contact_id,actor_id,definition_digest,request_digest,case_digest,encrypted_request,encrypted_result,state,outcome,attempt_count,result_origin,provider_status,duration_millis,started_at,completed_at)
      SELECT $2,NOW(),NOW(),organization_id,run_id,1,integration_id,case_id,account_id,contact_id,actor_id,definition_digest,request_digest,case_digest,encrypted_request,encrypted_result,'failed','manually_confirmed_no_effect',1,'manual',0,0,started_at,completed_at FROM inbox_integration_executions WHERE id=$1`,
      [operationId, secondOperationId],
    )
    await client.query(
      `INSERT INTO inbox_integration_recoveries(id,created_at,updated_at,organization_id,execution_id,run_id,actor_id,kind,from_state,from_outcome,from_result_origin,attempt_count,provider_status,duration_millis,reason,request_digest,encrypted_evidence)
      SELECT gen_random_uuid(),NOW()+n*interval '1 millisecond',NOW(),$1,CASE WHEN n%2=0 THEN $2::uuid ELSE $6::uuid END,$3,$4,CASE WHEN n%2=0 THEN 'apply_outputs' ELSE 'confirm_not_applied' END,'succeeded','','provider',1,200,12,'Synthetic operator record '||lpad(n::text,3,'0'),$5,'' FROM generate_series(1,52) n`,
      [
        profile.organization_id,
        operationId,
        runId,
        profile.id,
        '0'.repeat(64),
        secondOperationId,
      ],
    )
    await client.query(
      `INSERT INTO inbox_integration_recoveries(id,created_at,updated_at,organization_id,execution_id,run_id,actor_id,kind,from_state,from_outcome,from_result_origin,attempt_count,provider_status,duration_millis,reason,request_digest,encrypted_evidence)
      VALUES(gen_random_uuid(),NOW()+interval '1 second',NOW(),$1,'00000000-0000-0000-0000-000000000000',$2,$3,'cancel_remaining','integrating','','',0,0,0,'Synthetic workflow stop',$4,'')`,
      [profile.organization_id, runId, profile.id, '0'.repeat(64)],
    )
    const histories = (
      await (
        await api.get('/api/inbox/integration-executions?run_id=' + runId)
      ).json()
    ).data
    expect(histories.executions).toHaveLength(2)
    expect(JSON.stringify(histories)).not.toContain('PRIVATE_SYNTHETIC')
    await page.context().addCookies((await request.storageState()).cookies)
    await page.addInitScript((user) => {
      localStorage.setItem('user', JSON.stringify(user))
      localStorage.setItem('locale', 'en')
    }, profile)
    await page.goto('/settings/inbox-workflows?tab=history')
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
        await page.goto('/settings/inbox-workflows?tab=history')
        await page.reload()
        await expect(page.locator('html')).toHaveClass(new RegExp(theme))
        const row = page
          .locator('.inbox-list-row')
          .filter({ hasText: macro.name })
        const expand = row
          .locator('summary')
          .filter({ hasText: /^Integration operations$/ })
        await expand.focus()
        await page.keyboard.press('Enter')
        const operations = row.locator('.integration-run')
        await expect(
          operations.getByText('Provider response', { exact: true }),
        ).toBeVisible()
        await expect(
          operations.getByText('Succeeded', { exact: true }),
        ).toBeVisible()
        await expect(
          operations.getByText(
            'Successful result; output disposition not recorded',
            { exact: true },
          ),
        ).toBeVisible()
        await expect(
          row.getByText('Remaining actions stopped', { exact: true }),
        ).toBeVisible()
        await expect(page.locator('body')).not.toContainText(
          'PRIVATE_SYNTHETIC',
        )
        await expect
          .poll(async () =>
            (await inboxContrast(page)).filter(
              (item) => item.ratio < item.minimum,
            ),
          )
          .toEqual([])
        await operations
          .getByRole('heading', { name: 'Action 1', exact: true })
          .evaluate((element) => element.scrollIntoView({ block: 'start' }))
        if (viewport.width === 390) {
          const box = await operations
            .locator(':scope > ol > li')
            .first()
            .boundingBox()
          expect(box).not.toBeNull()
          expect(box!.width).toBeGreaterThan(260)
        }
        if (process.env.INBOX_CAPTURE)
          await page.screenshot({
            path: testInfo.outputPath(
              'integration-history-' + theme + '-' + viewport.width + '.png',
            ),
            fullPage: true,
          })
        const decisionButton = operations.getByRole('button', {
          name: 'Recovery decisions',
          exact: true,
        })
        await decisionButton.click()
        const records = operations.locator('.integration-records')
        await expect(
          records.getByText('Synthetic operator record 052', { exact: true }),
        ).toBeVisible()
        await expect(
          records.getByText('Workflow-level decision', { exact: true }),
        ).toBeVisible()
        await records
          .getByRole('heading', { name: 'Recovery decisions', exact: true })
          .evaluate((element) => element.scrollIntoView({ block: 'start' }))
        if (process.env.INBOX_CAPTURE)
          await page.screenshot({
            path: testInfo.outputPath(
              'integration-decision-scope-' +
                theme +
                '-' +
                viewport.width +
                '.png',
            ),
            fullPage: true,
          })
        await records
          .getByRole('button', { name: 'Older records', exact: true })
          .click()
        await expect(
          records.getByText('Synthetic operator record 001', { exact: true }),
        ).toBeVisible()
        await expect(records.getByText('Page 2', { exact: true })).toBeVisible()
        const firstDecision = records
          .locator('li')
          .filter({ hasText: 'Synthetic operator record 002' })
        const secondDecision = records
          .locator('li')
          .filter({ hasText: 'Synthetic operator record 001' })
        await expect(firstDecision).toContainText('Action 1')
        await expect(secondDecision).toContainText('Action 2')
        await firstDecision.locator('summary').click()
        await expect(
          firstDecision.getByText(operationId, { exact: true }),
        ).toBeVisible()
        await firstDecision.locator('summary').click()
        await secondDecision.locator('summary').click()
        await expect(
          secondDecision.getByText(secondOperationId, { exact: true }),
        ).toBeVisible()
        await secondDecision.locator('summary').click()
        await expect(
          records.getByText('Synthetic operator record 052', { exact: true }),
        ).toHaveCount(0)
        await expect
          .poll(async () =>
            (await inboxContrast(page)).filter(
              (item) => item.ratio < item.minimum,
            ),
          )
          .toEqual([])
        if (process.env.INBOX_CAPTURE)
          await page.screenshot({
            path: testInfo.outputPath(
              'integration-decisions-' + theme + '-' + viewport.width + '.png',
            ),
            fullPage: true,
          })
        await records
          .getByRole('button', { name: 'Newer records', exact: true })
          .click()
        await expect(
          records.getByText('Synthetic operator record 052', { exact: true }),
        ).toBeVisible()
      }
    }
    await client.query(
      "UPDATE inbox_integration_executions SET result_origin='manual',outputs_discarded_at=NOW(),outcome='manually_confirmed_success' WHERE id=$1",
      [operationId],
    )
    await page
      .locator('.integration-run')
      .getByRole('button', { name: 'Refresh', exact: true })
      .first()
      .click()
    await expect(
      page
        .locator('.integration-run > ol > li')
        .filter({
          has: page.getByRole('heading', { name: 'Action 1', exact: true }),
        })
        .getByText('Manual investigation', { exact: true }),
    ).toBeVisible()
    await expect(
      page.getByText('Selected outputs discarded', { exact: true }),
    ).toBeVisible()
    expect(
      (
        await client.query(
          'SELECT attempt_count::int AS attempt_count FROM inbox_integration_executions WHERE id=$1',
          [operationId],
        )
      ).rows[0].attempt_count,
    ).toBe(1)
    expect(
      (
        await client.query(
          "SELECT count(*)::int AS n FROM messages WHERE organization_id=$1 AND whats_app_account=$2 AND direction='outgoing'",
          [profile.organization_id, account.name],
        )
      ).rows[0].n,
    ).toBe(0)
    expect(
      (await (await api.get('/api/inbox/automation')).json()).data,
    ).toEqual(dispatch)
    expect(
      (await (await api.get('/api/inbox/integration-settings')).json()).data,
    ).toEqual(integrationSettings)
    completed = true
  } finally {
    // Own synthetic fixture records only; the runner also removes its database.
    await client.query(
      'DELETE FROM inbox_integration_recoveries WHERE run_id=$1',
      [runId],
    )
    await client.query(
      'DELETE FROM inbox_integration_executions WHERE run_id=$1',
      [runId],
    )
    await client.query('DELETE FROM inbox_workflow_runs WHERE id=$1', [runId])
    await client.end()
    if (completed) {
      await api.del('/api/inbox/workflow-definitions/' + macro.id)
      await api.del('/api/inbox/integrations/' + definition.id)
      await api.del('/api/contacts/' + contact.id)
      await api.del('/api/accounts/' + account.id)
      await api.put('/api/inbox/workflows', { enabled: before })
    }
  }
})
