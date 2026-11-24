import { test, expect, type Locator, type APIResponse } from '@playwright/test'
import { existsSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { createHash, randomUUID } from 'node:crypto'
import { Client } from 'pg'
import { ApiHelper } from '../../helpers/api'
import { inboxContrast } from '../../helpers/inbox-contrast'

test('keeps one provider object through an in-flight crash, reviewed retry, receipt replay and rule redelivery', async ({
  page,
  request,
}, testInfo) => {
  test.setTimeout(480000)
  test.skip(
    process.env.INBOX_PROVIDER_ACCEPTANCE !== 'synthetic-owned',
    'Explicit owned fake-provider host only',
  )
  expect(process.env.INBOX_PROVIDER_ARTIFACTS).toBe('/artifacts')
  const database = process.env.TEST_DATABASE_URL!
  const address = new URL(database)
  expect(address.hostname).toBe('whatomate-inbox-fork-browser-db')
  expect(address.pathname).toBe('/fixture')
  const db = new Client({ connectionString: database })
  await db.connect()
  const api = new ApiHelper(request)
  await api.loginAsAdmin()
  const data = async (
    response: Pick<APIResponse, 'ok' | 'json' | 'status' | 'url'>,
  ) => {
    expect(
      response.ok(),
      'HTTP ' + response.status() + ' ' + new URL(response.url()).pathname,
    ).toBe(true)
    return (await response.json()).data
  }
  const profile = await data(await api.get('/api/me'))
  const org = profile.organization_id
  const featureBefore = await data(await api.get('/api/inbox/workflows'))
  await data(await api.put('/api/inbox/workflows', { enabled: true }))
  const suffix = randomUUID().replace(/-/g, '').slice(0, 10)
  const path = '/api/inbox/workflow-definitions'
  const before = await data(await api.get('/api/inbox/automation'))
  expect(before).toEqual({ enabled: false, dispatch_available: false })
  const integrationState = await data(
    await api.get('/api/inbox/integration-settings'),
  )
  expect(integrationState.dispatch_available).toBe(true)
  await data(
    await api.put('/api/inbox/integration-settings', { enabled: true }),
  )
  await page.context().addCookies((await request.storageState()).cookies)
  await page.addInitScript((user) => {
    if (!localStorage.getItem('user'))
      localStorage.setItem('user', JSON.stringify(user))
    localStorage.setItem('locale', 'en')
  }, profile)
  const account = await api.createWhatsAppAccount({
    name: 'Synthetic provider inbox ' + suffix,
    phone_id: '100000000031',
    business_id: '100000000039',
    access_token: 'synthetic-never-send',
  })
  const contact = await api.createContact(
    '+12025550101',
    'Synthetic positive macro customer',
  )
  const thread = '/api/inbox/accounts/' + account.id + '/contacts/' + contact.id
  const current = (await data(await api.put(thread, { status: 'open' }))).case
  const count = (
    await data(
      await api.post('/api/inbox/fields', {
        scope: 'case',
        key: 'count_' + suffix,
        label: 'Synthetic provider count',
        kind: 'number',
      }),
    )
  ).definition
  const active = (
    await data(
      await api.post('/api/inbox/fields', {
        scope: 'contact',
        key: 'active_' + suffix,
        label: 'Synthetic provider active',
        kind: 'boolean',
      }),
    )
  ).definition
  const definition = async (key: string) => {
    const authored = {
      name: 'Synthetic provider ' + key + ' ' + suffix,
      description: 'Owned fake-provider acceptance only',
      endpoint_key: key,
      auth_mode: 'none',
      enabled: false,
      inputs: [{ name: 'customer', kind: 'text', required: true }],
      outputs: [
        { name: 'count', definition_id: count.id },
        { name: 'active', definition_id: active.id },
      ],
      reference_key: 'reference',
    }
    const created = (
      await data(await api.post('/api/inbox/integrations', authored))
    ).integration
    expect(created.enabled).toBe(false)
    return (
      await data(
        await api.put('/api/inbox/integrations/' + created.id, {
          ...authored,
          enabled: true,
        }),
      )
    ).integration
  }
  const interrupted = await definition('synthetic_crash')
  const once = await definition('synthetic_once')
  const macroBody = {
    name: 'Synthetic reviewed provider macro ' + suffix,
    kind: 'macro',
    conditions: { account_id: account.id },
    actions: [
      {
        type: 'integration',
        value: interrupted.id,
        integration_inputs: { customer: '{{contact_name}}' },
      },
      { type: 'note', value: 'Synthetic macro provider completed ' + suffix },
    ],
  }
  const macro = (await data(await api.post(path, macroBody))).workflow
  const execution = async (run: string) =>
    (
      await db.query(
        'SELECT id,state,attempt_count::int AS attempt_count,started_at,retry_until,request_digest,definition_digest,encrypted_request,outputs_applied_at,result_origin FROM inbox_integration_executions WHERE organization_id=$1 AND run_id=$2',
        [org, run],
      )
    ).rows[0]
  const provider = async (id: string) =>
    (
      await db.query(
        'SELECT operation_key,calls::int AS calls FROM whatomate_synthetic_provider.operations WHERE operation_key=$1',
        [id],
      )
    ).rows
  const notes = async (caseId: string, text: string) =>
    (
      await db.query(
        "SELECT count(*)::int AS n FROM inbox_case_activities WHERE organization_id=$1 AND case_id=$2 AND kind='workflow_note' AND detail=$3",
        [org, caseId, text],
      )
    ).rows[0].n
  const checkpoint = async (
    name: string,
    runId: string,
    caseId: string,
    note: string,
  ) => {
    const operation = await execution(runId),
      records = await provider(operation.id)
    writeFileSync(
      join('/artifacts', 'checkpoint-' + name + '.json'),
      JSON.stringify(
        {
          synthetic: true,
          run_id: runId,
          execution_id: operation.id,
          state: operation.state,
          attempts: operation.attempt_count,
          provider_objects: records.length,
          provider_calls: records[0]?.calls,
          workflow_notes: await notes(caseId, note),
          started_at: operation.started_at,
          retry_until: operation.retry_until,
        },
        null,
        2,
      ),
      { flag: 'wx', mode: 0o600 },
    )
  }
  const restart = async (number: number) => {
    writeFileSync(
      join('/artifacts', 'restart-' + number + '.requested'),
      'synthetic-owned',
      { flag: 'wx', mode: 0o600 },
    )
    await expect
      .poll(
        () => {
          const file = join('/artifacts', 'restart-' + number + '.ready')
          return (
            existsSync(file) &&
            /^[a-f0-9]{64}$/.test(readFileSync(file, 'utf8').trim())
          )
        },
        { timeout: 90000, intervals: [200, 500, 1000] },
      )
      .toBe(true)
  }
  const snapshot = async (name: string, dialog?: Locator) => {
    if (dialog)
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
  let rule: { id: string; enabled_at?: string | null } | undefined
  let ruleDefinition: Record<string, unknown> | undefined
  try {
    await page.goto(
      '/chat/' +
        contact.id +
        '?inbox_account=' +
        encodeURIComponent(account.name) +
        '&inbox_account_id=' +
        account.id,
    )
    await page.getByRole('button', { name: 'Macros', exact: true }).click()
    const picker = page.getByRole('dialog', { name: 'Macros', exact: true })
    await picker.getByRole('button', { name: macro.name, exact: true }).click()
    await expect(picker).toContainText('Synthetic positive macro customer')
    await expect(
      picker.getByRole('button', { name: 'Apply macro', exact: true }),
    ).toBeEnabled()
    const [queued] = await Promise.all([
      page.waitForResponse(
        (response) =>
          new URL(response.url()).pathname ===
            path + '/' + macro.id + '/execute' &&
          response.request().method() === 'POST',
      ),
      picker.getByRole('button', { name: 'Apply macro', exact: true }).click(),
    ])
    const originalMacro = queued.request().postDataJSON()
    const run = (await data(queued)).run
    expect(run.sequenced).toBe(true)
    await expect.poll(async () => !!(await execution(run.id))?.id).toBe(true)
    const initial = await execution(run.id)
    await expect
      .poll(async () => await provider(initial.id))
      .toEqual([{ operation_key: initial.id, calls: 1 }])
    expect((await execution(run.id)).state).toBe('submitting')
    expect(await notes(current.id, macroBody.actions[1]!.value)).toBe(0)
    const requestHash = createHash('sha256')
      .update(initial.encrypted_request)
      .digest('hex')
    await checkpoint(
      'before-crash',
      run.id,
      current.id,
      macroBody.actions[1]!.value,
    )
    // Crash after the independent provider committed, before application receipt.
    await restart(1)
    console.log(
      'Owned process replaced after provider commit; waiting for the normal interrupted-submission recovery deadline.',
    )
    await expect
      .poll(async () => (await execution(run.id)).state, {
        timeout: 180000,
        intervals: [500, 1000, 2000],
      })
      .toBe('unknown')
    await expect
      .poll(
        async () =>
          (
            await db.query(
              'SELECT state FROM inbox_workflow_runs WHERE organization_id=$1 AND id=$2',
              [org, run.id],
            )
          ).rows[0].state,
      )
      .toBe('sequence_unknown')
    expect(await provider(initial.id)).toEqual([
      { operation_key: initial.id, calls: 1 },
    ])
    await checkpoint(
      'recovered-unknown',
      run.id,
      current.id,
      macroBody.actions[1]!.value,
    )
    let review!: Locator
    for (const viewport of [
      { width: 1366, height: 900 },
      { width: 390, height: 844 },
    ]) {
      for (const theme of ['light', 'dark']) {
        await page.setViewportSize(viewport)
        await page.goto('/inbox/cases/' + current.id + '/workflows')
        await page.evaluate(
          (value) => localStorage.setItem('color-mode', value),
          theme,
        )
        await page.reload()
        const row = page.locator('[data-workflow-run="' + run.id + '"]')
        await row
          .locator('summary')
          .filter({ hasText: 'Integration operations' })
          .first()
          .click()
        await row
          .getByRole('button', {
            name: 'Review retry eligibility',
            exact: true,
          })
          .click()
        review = page.getByRole('dialog')
        await expect(
          review.getByRole('heading', {
            name: 'Review original-operation retry',
            exact: true,
          }),
        ).toBeFocused()
        await expect(review).toContainText('Synthetic positive macro customer')
        await expect(review).toContainText(initial.id)
        await expect(
          review.getByRole('button', {
            name: 'Authorise original-operation retry',
            exact: true,
          }),
        ).toBeDisabled()
        await snapshot('provider-retry-' + theme + '-' + viewport.width, review)
        if (viewport.width !== 390 || theme !== 'dark')
          await review
            .getByRole('button', { name: 'Cancel', exact: true })
            .click()
      }
    }
    let retryBody: Record<string, unknown> | undefined
    const retryPath =
      '**/api/inbox/integration-executions/' + initial.id + '/retry'
    await page.route(retryPath, async (route) => {
      retryBody = route.request().postDataJSON()
      const accepted = await route.fetch()
      expect(accepted.ok()).toBe(true)
      expect((await accepted.json()).data.recovery.kind).toBe('retry')
      await route.abort('failed')
    })
    await review
      .getByRole('textbox', {
        name: 'Investigation or operational reason',
        exact: true,
      })
      .fill(
        'Synthetic provider contract verified; replay original operation only',
      )
    await review
      .getByRole('button', {
        name: 'Authorise original-operation retry',
        exact: true,
      })
      .click()
    await expect(
      review.getByRole('heading', {
        name: 'Decision acknowledgement unconfirmed',
        exact: true,
      }),
    ).toBeFocused()
    await expect
      .poll(async () => (await execution(run.id)).state)
      .toBe('succeeded')
    await expect
      .poll(
        async () =>
          (
            await db.query(
              'SELECT state FROM inbox_workflow_runs WHERE organization_id=$1 AND id=$2',
              [org, run.id],
            )
          ).rows[0].state,
      )
      .toBe('applied')
    expect(await provider(initial.id)).toEqual([
      { operation_key: initial.id, calls: 2 },
    ])
    expect(await notes(current.id, macroBody.actions[1]!.value)).toBe(1)
    await checkpoint(
      'retried-once',
      run.id,
      current.id,
      macroBody.actions[1]!.value,
    )
    await snapshot('provider-retry-unconfirmed-dark-390', review)
    await page.unroute(retryPath)
    // Retain the actual browser's private pending UUID/body across server restart.
    await restart(2)
    const repeatedRequest = page.waitForRequest(
      (request) =>
        request
          .url()
          .endsWith(
            '/api/inbox/integration-executions/' + initial.id + '/retry',
          ) && request.method() === 'POST',
    )
    await review
      .getByRole('button', { name: 'Resubmit identical decision', exact: true })
      .click()
    expect((await repeatedRequest).postDataJSON()).toEqual(retryBody)
    await expect(review).toHaveCount(0)
    const replay = await Promise.all([
      api.post(path + '/' + macro.id + '/execute', originalMacro),
      api.post(
        '/api/inbox/integration-executions/' + initial.id + '/retry',
        retryBody!,
      ),
    ])
    for (const response of replay) expect(response.ok()).toBe(true)
    const final = await execution(run.id)
    expect(final.state).toBe('succeeded')
    expect(final.attempt_count).toBe(2)
    expect(final.result_origin).toBe('provider')
    expect(final.outputs_applied_at).toBeTruthy()
    expect(
      createHash('sha256').update(final.encrypted_request).digest('hex'),
    ).toBe(requestHash)
    expect(final.request_digest).toBe(initial.request_digest)
    expect(final.definition_digest).toBe(initial.definition_digest)
    expect(final.retry_until.toISOString()).toBe(
      initial.retry_until.toISOString(),
    )
    expect(await provider(initial.id)).toEqual([
      { operation_key: initial.id, calls: 2 },
    ])
    expect(await notes(current.id, macroBody.actions[1]!.value)).toBe(1)
    expect(
      (
        await db.query(
          "SELECT count(*)::int AS n FROM inbox_integration_recoveries WHERE organization_id=$1 AND execution_id=$2 AND kind='retry'",
          [org, initial.id],
        )
      ).rows[0].n,
    ).toBe(1)
    const values = (
      await db.query(
        'SELECT definition_id,value FROM inbox_field_values WHERE organization_id=$1 AND definition_id=ANY($2::uuid[]) AND subject_id=ANY($3::uuid[])',
        [org, [count.id, active.id], [current.id, contact.id]],
      )
    ).rows
    expect(values).toHaveLength(2)
    expect(values.find((row) => row.definition_id === count.id)?.value).toBe(0)
    expect(values.find((row) => row.definition_id === active.id)?.value).toBe(
      false,
    )
    await page.reload()
    await page
      .locator('[data-workflow-run="' + run.id + '"] summary')
      .filter({ hasText: 'Integration operations' })
      .first()
      .click()
    await expect(
      page.getByText('Selected outputs applied', { exact: true }),
    ).toBeVisible()
    await snapshot('provider-macro-completed-dark-390')
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
        await page.reload()
        const row = page.locator('[data-workflow-run="' + run.id + '"]')
        await row
          .locator('summary')
          .filter({ hasText: 'Integration operations' })
          .first()
          .click()
        const history = page.locator(
          '[data-inbox-integration-run="' + run.id + '"]',
        )
        await expect(
          history
            .locator('dt')
            .filter({ hasText: /^Original execution actor$/ })
            .first()
            .locator('..')
            .locator('dd'),
        ).toHaveText(profile.full_name)
        await history
          .getByRole('button', { name: 'Recovery decisions', exact: true })
          .click()
        await expect(
          history.getByText(
            'Synthetic provider contract verified; replay original operation only',
            { exact: true },
          ),
        ).toBeVisible()
        await expect(history).not.toContainText('Former user')
        await snapshot('provider-current-actor-' + theme + '-' + viewport.width)
        if (viewport.width === 390) {
          const decision = history
            .getByText(
              'Synthetic provider contract verified; replay original operation only',
              { exact: true },
            )
            .locator('..')
          await expect(decision).toContainText(profile.full_name)
          await decision.evaluate((element) =>
            element.scrollIntoView({ block: 'center', behavior: 'auto' }),
          )
          await snapshot('provider-recovery-actor-' + theme + '-390')
        }
      }
    }
    await api.login('admin@admin.com', 'admin')
    const otherProfile = await data(await api.get('/api/me'))
    expect(otherProfile.id).not.toBe(profile.id)
    await page.context().addCookies((await request.storageState()).cookies)
    await page.evaluate(
      (user) => localStorage.setItem('user', JSON.stringify(user)),
      otherProfile,
    )
    await page.reload()
    const otherRow = page.locator('[data-workflow-run="' + run.id + '"]')
    await otherRow
      .locator('summary')
      .filter({ hasText: 'Integration operations' })
      .first()
      .click()
    const otherHistory = page.locator(
      '[data-inbox-integration-run="' + run.id + '"]',
    )
    await expect(
      otherHistory
        .locator('dt')
        .filter({ hasText: /^Original execution actor$/ })
        .first()
        .locator('..')
        .locator('dd'),
    ).toHaveText(profile.full_name)
    await expect(otherHistory).not.toContainText('Former user')
    await otherHistory
      .locator('dt')
      .filter({ hasText: /^Original execution actor$/ })
      .first()
      .locator('..')
      .locator('dd')
      .evaluate((element) =>
        element.scrollIntoView({ block: 'center', behavior: 'auto' }),
      )
    await snapshot('provider-other-viewer-actor-dark-390')
    await api.loginAsAdmin()
    await page.context().addCookies((await request.storageState()).cookies)
    await page.evaluate(
      (user) => localStorage.setItem('user', JSON.stringify(user)),
      profile,
    )
    await page.reload()

    const ruleBody = {
      name: 'Synthetic ordered provider rule ' + suffix,
      kind: 'rule',
      trigger: 'case_opened',
      position: 1,
      conditions: { account_id: account.id },
      actions: [
        {
          type: 'integration',
          value: once.id,
          integration_inputs: { customer: '{{contact_name}}' },
        },
        { type: 'note', value: 'Synthetic rule provider completed ' + suffix },
      ],
    }
    ruleDefinition = ruleBody
    rule = (await data(await api.post(path, ruleBody))).workflow
    expect(rule.enabled_at).toBeFalsy()
    await data(
      await api.put(path + '/' + rule.id, { ...ruleBody, enabled: true }),
    )
    const ruleContact = await api.createContact(
      '+12025550102',
      'Synthetic positive rule customer',
    )
    const ruleThread =
      '/api/inbox/accounts/' + account.id + '/contacts/' + ruleContact.id
    const ruleCase = (await data(await api.put(ruleThread, { status: 'open' })))
      .case
    await expect
      .poll(
        async () =>
          (
            await db.query(
              'SELECT state FROM inbox_workflow_runs WHERE organization_id=$1 AND workflow_id=$2 AND case_id=$3',
              [org, rule.id, ruleCase.id],
            )
          ).rows[0]?.state,
      )
      .toBe('applied')
    const ruleRun = (
      await db.query(
        'SELECT id,event_id,sequence_send_reserved FROM inbox_workflow_runs WHERE organization_id=$1 AND workflow_id=$2 AND case_id=$3',
        [org, rule.id, ruleCase.id],
      )
    ).rows[0]
    expect(ruleRun.event_id).toBeTruthy()
    expect(ruleRun.sequence_send_reserved).toBe(false)
    const ruleExecution = await execution(ruleRun.id)
    expect(ruleExecution.state).toBe('succeeded')
    expect(ruleExecution.attempt_count).toBe(1)
    expect(await provider(ruleExecution.id)).toEqual([
      { operation_key: ruleExecution.id, calls: 1 },
    ])
    expect(await notes(ruleCase.id, ruleBody.actions[1]!.value)).toBe(1)
    await checkpoint(
      'rule-completed',
      ruleRun.id,
      ruleCase.id,
      ruleBody.actions[1]!.value,
    )
    await restart(3)
    // Redeliver the original durable event after a process restart. Preserve its
    // identity and authored context; do not fabricate another business event.
    await db.query(
      'UPDATE inbox_case_activities SET workflow_processed_at=NULL WHERE organization_id=$1 AND id=$2',
      [org, ruleRun.event_id],
    )
    await expect
      .poll(
        async () =>
          (
            await db.query(
              'SELECT workflow_processed_at IS NOT NULL AS done FROM inbox_case_activities WHERE organization_id=$1 AND id=$2',
              [org, ruleRun.event_id],
            )
          ).rows[0].done,
      )
      .toBe(true)
    expect(
      (
        await db.query(
          'SELECT count(*)::int AS n FROM inbox_workflow_runs WHERE organization_id=$1 AND event_id=$2',
          [org, ruleRun.event_id],
        )
      ).rows[0].n,
    ).toBe(1)
    expect(await provider(ruleExecution.id)).toEqual([
      { operation_key: ruleExecution.id, calls: 1 },
    ])
    expect(await notes(ruleCase.id, ruleBody.actions[1]!.value)).toBe(1)
    expect(await provider(initial.id)).toEqual([
      { operation_key: initial.id, calls: 2 },
    ])
    await data(
      await api.put(path + '/' + rule.id, { ...ruleBody, enabled: false }),
    )
    rule = undefined
    expect(
      (
        await db.query(
          "SELECT count(*)::int AS n FROM messages WHERE organization_id=$1 AND direction='outgoing'",
          [org],
        )
      ).rows[0].n,
    ).toBe(0)
    expect(
      (
        await db.query(
          "SELECT count(*)::int AS n FROM inbox_workflows WHERE organization_id=$1 AND kind='rule' AND enabled_at IS NOT NULL",
          [org],
        )
      ).rows[0].n,
    ).toBe(0)
    expect(await data(await api.get('/api/inbox/automation'))).toEqual(before)
    await data(
      await api.put('/api/inbox/integration-settings', { enabled: false }),
    )
    await data(
      await api.put('/api/inbox/workflows', { enabled: featureBefore.enabled }),
    )
    writeFileSync(
      '/artifacts/acceptance-evidence.json',
      JSON.stringify(
        {
          synthetic: true,
          macro_run: run.id,
          macro_execution: initial.id,
          provider_objects_for_macro: 1,
          macro_provider_calls: 2,
          macro_application_attempts: 2,
          retry_decisions: 1,
          note_applications: 1,
          original_request_unchanged: true,
          retry_expiry_unchanged: true,
          rule_run: ruleRun.id,
          rule_execution: ruleExecution.id,
          rule_provider_objects: 1,
          rule_provider_calls: 1,
          rule_event_redelivery_runs: 1,
          process_replacements: 3,
          outgoing_messages: 0,
          rules_enabled: 0,
          automatic_replies: false,
        },
        null,
        2,
      ),
      { flag: 'wx', mode: 0o600 },
    )
  } finally {
    if (rule && ruleDefinition)
      await api.put(path + '/' + rule.id, { ...ruleDefinition, enabled: false })
    await db.end()
  }
})
