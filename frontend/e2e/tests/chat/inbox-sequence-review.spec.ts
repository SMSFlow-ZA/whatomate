import { test, expect, type Locator } from '@playwright/test'
import { Client } from 'pg'
import { createCipheriv, randomBytes, randomUUID } from 'node:crypto'
import { ApiHelper } from '../../helpers/api'
import { createTestScope } from '../../framework'
import { inboxContrast } from '../../helpers/inbox-contrast'

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

test('reviews a synthetic remainder and recovers its committed resume receipt without dispatch', async ({
  page,
  request,
}, testInfo) => {
  test.setTimeout(120000)
  const database = process.env.TEST_DATABASE_URL
  test.skip(!database, 'Requires an explicitly disposable database')
  const address = new URL(database!)
  expect(['fixture', 'whatomate_test']).toContain(address.pathname.slice(1))
  expect(
    ['127.0.0.1', 'localhost'].includes(address.hostname) ||
      address.hostname.startsWith('whatomate-inbox-'),
  ).toBe(true)
  const fixtureKey = process.env.INBOX_TEST_ENCRYPTION_KEY
  expect(
    fixtureKey,
    'Isolated test configuration must supply its own encryption key',
  ).toBeTruthy()
  const key = Buffer.alloc(32)
  Buffer.from(fixtureKey!).copy(key, 0, 0, 32)
  const nonce = randomBytes(12),
    cipher = createCipheriv('aes-256-gcm', key, nonce)
  const encrypted =
    'enc:' +
    Buffer.concat([
      nonce,
      cipher.update(
        JSON.stringify({
          integrations: {},
          reply_digest: '',
          send_skip_reason: 'synthetic_no_reply_reserved',
        }),
      ),
      cipher.final(),
      cipher.getAuthTag(),
    ]).toString('base64')
  const api = new ApiHelper(request),
    scope = createTestScope('sequence-review')
  await api.loginAsAdmin()
  const before = (await (await api.get('/api/inbox/workflows')).json()).data
    .enabled
  await api.put('/api/inbox/workflows', { enabled: true })
  const automation = (await (await api.get('/api/inbox/automation')).json())
    .data
  const integration = (
    await (await api.get('/api/inbox/integration-settings')).json()
  ).data
  expect(automation.dispatch_available).toBe(false)
  expect(integration.dispatch_available).toBe(false)
  const profile = (await (await api.get('/api/me')).json()).data
  const account = await api.createWhatsAppAccount({
    name: scope.name('Synthetic account'),
    phone_id: scope.phone().replace(/\D/g, ''),
    business_id: '100000000009',
    access_token: 'synthetic-do-not-send',
  })
  const contact = await api.createContact(
    scope.phone(),
    'Synthetic remaining-work customer',
  )
  const current = (
    await (
      await api.put(
        '/api/inbox/accounts/' + account.id + '/contacts/' + contact.id,
        { status: 'open' },
      )
    ).json()
  ).data.case
  const response = await api.post('/api/inbox/workflow-definitions', {
    kind: 'macro',
    name: scope.name('Synthetic remainder review'),
    description: 'Synthetic UI fixture; no provider work or message submission',
    visibility: 'organization',
    trigger: '',
    position: 0,
    conditions: {},
    actions: [
      { type: 'note', value: 'Synthetic earlier action' },
      { type: 'note', value: 'Synthetic remaining action' },
      { type: 'send_text', value: 'Synthetic authored reply for context only' },
    ],
  })
  expect(response.ok()).toBe(true)
  const macro = (await response.json()).data.workflow,
    runId = randomUUID()
  const client = new Client({ connectionString: database })
  await client.connect()
  try {
    // Explicit synthetic held plan: this is not evidence of a provider operation.
    // The normal handlers review and record it; host dispatch remains disabled.
    await client.query(
      `INSERT INTO inbox_workflow_runs(id,created_at,updated_at,organization_id,workflow_id,case_id,account_id,contact_id,actor_id,kind,workflow_name,workflow_updated_at,state,sequenced,next_action,sequence_send_reserved,encrypted_sequence_plan,action_outcomes)
      VALUES($1,NOW(),NOW(),$2,$3,$4,$5,$6,$7,'macro',$8,(SELECT updated_at FROM inbox_workflows WHERE id=$3),'sequence_review',true,1,false,$9,$10::jsonb)`,
      [
        runId,
        profile.organization_id,
        macro.id,
        current.id,
        account.id,
        contact.id,
        profile.id,
        macro.name,
        encrypted,
        JSON.stringify([
          {
            index: 0,
            type: 'note',
            status: 'applied',
            reason: 'synthetic_fixture',
          },
          { index: 1, type: 'note', status: 'pending', reason: '' },
          { index: 2, type: 'send_text', status: 'pending', reason: '' },
        ]),
      ],
    )
    await page.context().addCookies((await request.storageState()).cookies)
    await page.addInitScript((user) => {
      if (!localStorage.getItem('user'))
        localStorage.setItem('user', JSON.stringify(user))
      localStorage.setItem('locale', 'en')
    }, profile)
    await page.goto('/settings/inbox-workflows?tab=history')
    let activityPath = '/settings/inbox-workflows?tab=history'
    const open = async () => {
      await page.goto(activityPath)
      const row = page
        .locator('.inbox-list-row')
        .filter({ hasText: macro.name })
      await row
        .locator('summary')
        .filter({ hasText: /^Integration operations$/ })
        .click()
      await row
        .getByRole('button', { name: 'Review remaining actions', exact: true })
        .click()
      const review = page.getByRole('dialog')
      await expect(
        review.getByRole('heading', {
          name: 'Review remaining workflow actions',
          exact: true,
        }),
      ).toBeFocused()
      return review
    }
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
        const review = await open()
        await expect(review).toContainText('Synthetic remaining action')
        await expect(review).toContainText(
          'Synthetic authored reply for context only',
        )
        await expect(review).toContainText('No customer reply is reserved')
        await expect(review.locator('.inbox-action-number').first()).toHaveText(
          '2',
        )
        await expect(review.locator('ol.inbox-list').first()).toHaveAttribute(
          'start',
          '2',
        )
        expect(
          await review.locator('ol.inbox-list').first().ariaSnapshot(),
        ).toContain('Action 2')
        await expect(
          review.getByRole('textbox', {
            name: 'Investigation or operational reason',
            exact: true,
          }),
        ).toHaveAttribute('aria-required', 'true')
        await expect(
          review.getByRole('button', {
            name: 'Authorise remaining actions',
            exact: true,
          }),
        ).toBeDisabled()
        await settle(review)
        await expect
          .poll(async () =>
            (await inboxContrast(page)).filter(
              (item) => item.ratio < item.minimum,
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
            path: testInfo.outputPath(
              'sequence-review-' + theme + '-' + viewport.width + '.png',
            ),
            fullPage: true,
            animations: 'disabled',
          })
        await review
          .getByRole('button', { name: 'Cancel', exact: true })
          .click()
      }
    }
    const users = (await (await api.get('/api/users')).json()).data.users
    const agent = users.find(
      (user: { email: string }) => user.email === 'agent@test.com',
    )
    expect(agent).toBeTruthy()
    await client.query(
      'UPDATE inbox_workflow_runs SET actor_id=$2,initiated_by_id=$2 WHERE id=$1',
      [runId, agent.id],
    )
    const assigned = await api.post('/api/inbox/cases/bulk', {
      action: 'assign',
      items: [{ id: current.id, version: current.version }],
      assignee_id: agent.id,
    })
    expect((await assigned.json()).data.results[0].result).toBe('updated')
    await api.login('agent@test.com', 'password')
    const agentProfile = (await (await api.get('/api/me')).json()).data
    expect((await api.get('/api/inbox/integrations')).status()).toBe(403)
    await page.context().addCookies((await request.storageState()).cookies)
    await page.evaluate(
      (user) => localStorage.setItem('user', JSON.stringify(user)),
      agentProfile,
    )
    await page.goto(
      '/chat/' +
        contact.id +
        '?inbox_account=' +
        encodeURIComponent(account.name) +
        '&inbox_account_id=' +
        account.id,
    )
    await page.getByRole('button', { name: 'Macros', exact: true }).click()
    const historyLink = page.getByRole('link', {
      name: 'Workflow activity',
      exact: true,
    })
    await expect(historyLink).toBeVisible()
    await settle(page.getByRole('dialog', { name: 'Macros', exact: true }))
    expect((await historyLink.boundingBox())!.height).toBeGreaterThanOrEqual(44)
    if (process.env.INBOX_CAPTURE)
      await page.screenshot({
        path: testInfo.outputPath('sequence-agent-macro-entry-dark-390.png'),
        fullPage: true,
        animations: 'disabled',
      })
    await historyLink.click()
    activityPath = '/inbox/cases/' + current.id + '/workflows'
    await expect(page).toHaveURL(
      new RegExp('/inbox/cases/' + current.id + '/workflows'),
    )
    await expect(
      page.getByRole('heading', { name: 'Workflow activity', exact: true }),
    ).toBeVisible()
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
        await page.goto(activityPath)
        const row = page
          .locator('.inbox-list-row')
          .filter({ hasText: macro.name })
        await row
          .locator('summary')
          .filter({ hasText: /^Integration operations$/ })
          .click()
        await expect(
          row.getByRole('button', {
            name: 'Review remaining actions',
            exact: true,
          }),
        ).toBeVisible()
        await expect(
          row.getByRole('button', {
            name: 'Review stopping remaining actions',
            exact: true,
          }),
        ).toHaveCount(0)
        await expect
          .poll(async () =>
            (await inboxContrast(page)).filter(
              (item) => item.ratio < item.minimum,
            ),
          )
          .toEqual([])
        expect(
          await page.evaluate(
            () => document.documentElement.scrollWidth <= window.innerWidth,
          ),
        ).toBe(true)
        if (process.env.INBOX_CAPTURE)
          await page.screenshot({
            path: testInfo.outputPath(
              'sequence-agent-activity-' +
                theme +
                '-' +
                viewport.width +
                '.png',
            ),
            fullPage: true,
            animations: 'disabled',
          })
      }
    }
    const previewPath = '**/api/inbox/workflow-runs/' + runId + '/preview'
    await page.route(previewPath, (route) =>
      route.fulfill({
        status: 503,
        contentType: 'application/json',
        body: JSON.stringify({ error: 'SYNTHETIC_PRIVATE_FAILURE' }),
      }),
    )
    const review = await open()
    await expect(review).toContainText('The current review could not be loaded')
    await expect(review).not.toContainText('SYNTHETIC_PRIVATE_FAILURE')
    await settle(review)
    if (process.env.INBOX_CAPTURE)
      await page.screenshot({
        path: testInfo.outputPath('sequence-review-error-dark-390.png'),
        fullPage: true,
        animations: 'disabled',
      })
    await page.unroute(previewPath)
    await review.getByRole('button', { name: 'Retry', exact: true }).click()
    await expect(review).toContainText('Synthetic remaining action')
    let submitted: Record<string, unknown> | undefined
    const resumePath = '**/api/inbox/workflow-runs/' + runId + '/resume'
    await page.route(resumePath, async (route) => {
      submitted = route.request().postDataJSON()
      const accepted = await route.fetch()
      expect(accepted.ok()).toBe(true)
      expect((await accepted.json()).data.recovery.kind).toBe('resume_sequence')
      await route.abort('failed')
    })
    await review
      .getByRole('textbox', {
        name: 'Investigation or operational reason',
        exact: true,
      })
      .fill('Synthetic remainder reviewed; no provider work or sends')
    await review
      .getByRole('button', { name: 'Authorise remaining actions', exact: true })
      .click()
    await expect(review).toContainText('Decision acknowledgement unconfirmed')
    await expect(
      review.getByRole('heading', {
        name: 'Decision acknowledgement unconfirmed',
        exact: true,
      }),
    ).toBeFocused()
    await expect(review).toContainText(submitted!.operation_id as string)
    await settle(review)
    if (process.env.INBOX_CAPTURE)
      await page.screenshot({
        path: testInfo.outputPath('sequence-review-unconfirmed-dark-390.png'),
        fullPage: true,
        animations: 'disabled',
      })
    await page.unroute(resumePath)
    await review
      .getByRole('button', { name: 'Check recorded decision', exact: true })
      .click()
    await expect(review).toHaveCount(0)
    const replay = await api.post(
      '/api/inbox/workflow-runs/' + runId + '/resume',
      submitted!,
    )
    expect(replay.ok()).toBe(true)
    expect((await replay.json()).data.recovery.id).toBe(submitted!.operation_id)
    expect(
      (
        await client.query(
          "SELECT count(*)::int AS n FROM inbox_integration_recoveries WHERE run_id=$1 AND kind='resume_sequence'",
          [runId],
        )
      ).rows[0].n,
    ).toBe(1)
    expect(
      (
        await client.query(
          'SELECT state,next_action::int AS next_action FROM inbox_workflow_runs WHERE id=$1',
          [runId],
        )
      ).rows[0],
    ).toEqual({ state: 'integrating', next_action: 1 })
    expect(
      (
        await client.query(
          "SELECT count(*)::int AS n FROM messages WHERE organization_id=$1 AND whats_app_account=$2 AND direction='outgoing'",
          [profile.organization_id, account.name],
        )
      ).rows[0].n,
    ).toBe(0)
    await api.loginAsAdmin()
    expect(
      (await (await api.get('/api/inbox/automation')).json()).data,
    ).toEqual(automation)
    expect(
      (await (await api.get('/api/inbox/integration-settings')).json()).data,
    ).toEqual(integration)
  } finally {
    await api.loginAsAdmin()
    await client.query(
      'DELETE FROM inbox_integration_recoveries WHERE run_id=$1',
      [runId],
    )
    await client.query('DELETE FROM inbox_workflow_runs WHERE id=$1', [runId])
    await client.end()
    await api.del('/api/inbox/workflow-definitions/' + macro.id)
    await api.del('/api/contacts/' + contact.id)
    await api.del('/api/accounts/' + account.id)
    await api.put('/api/inbox/workflows', { enabled: before })
  }
})
