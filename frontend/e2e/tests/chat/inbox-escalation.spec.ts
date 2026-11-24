import {
  expect,
  test,
  type APIResponse,
  type BrowserContext,
  type Page,
} from '@playwright/test'
import { Client } from 'pg'
import { ApiHelper } from '../../helpers/api'
import { createTestScope } from '../../framework'
import { inboxContrast } from '../../helpers/inbox-contrast'

test('configures and reviews durable internal risk and breach notices without sending', async ({
  request,
  page,
}, testInfo) => {
  test.setTimeout(300000)
  page.setDefaultTimeout(15000)
  const database = process.env.TEST_DATABASE_URL
  test.skip(!database, 'An explicitly owned disposable is required')
  const db = new URL(database!),
    host = new URL(process.env.BASE_URL!).hostname
  expect(['fixture', 'whatomate_test']).toContain(db.pathname.slice(1))
  for (const address of [db.hostname, host])
    expect(
      address.startsWith('whatomate-inbox-') ||
        ['localhost', '127.0.0.1'].includes(address),
    ).toBe(true)
  const api = new ApiHelper(request),
    scope = createTestScope('internal-notices')
  const data = async (response: APIResponse) => {
    expect(
      response.ok(),
      'HTTP ' + response.status() + ' ' + new URL(response.url()).pathname,
    ).toBe(true)
    return (await response.json()).data
  }
  await api.loginAsAdmin()
  const feature = (await data(await api.get('/api/inbox/workflows'))).enabled
  await data(await api.put('/api/inbox/workflows', { enabled: true }))
  const automatic = await data(await api.get('/api/inbox/automation'))
  expect(automatic.dispatch_available).toBe(false)
  const profile = await data(await api.get('/api/me'))
  const account = await api.createWhatsAppAccount({
    name: scope.name('internal targets'),
    phone_id: scope.phone().replace(/\D/g, ''),
    business_id: '100000000054',
    access_token: 'synthetic-never-send',
  })
  const contact = await api.createContact(
    scope.phone(),
    'Synthetic internal notice enquiry',
  )
  const base = '/api/inbox/sla/escalations/' + account.id
  const thread = '/api/inbox/accounts/' + account.id + '/contacts/' + contact.id
  let agentContext: BrowserContext | undefined
  async function captures(
    name: string,
    heading?: string,
    surface: Page = page,
  ) {
    for (const width of [1366, 390])
      for (const theme of ['light', 'dark']) {
        await surface.setViewportSize({
          width,
          height: width === 390 ? 844 : 1000,
        })
        await surface.evaluate(async (theme) => {
          document.documentElement.classList.remove('light', 'dark')
          document.documentElement.classList.add(theme)
          await new Promise<void>((done) =>
            requestAnimationFrame(() => requestAnimationFrame(() => done())),
          )
          await Promise.all(
            document
              .getAnimations()
              .filter(
                (animation) =>
                  animation.effect?.getComputedTiming().iterations !== Infinity,
              )
              .map((animation) => animation.finished.catch(() => {})),
          )
        }, theme)
        if (heading)
          await page
            .getByRole('heading', { name: heading, exact: true })
            .evaluate((el) => {
              const container = el.closest<HTMLElement>('.inbox-page-scroll')
              if (!container)
                throw new Error('Missing application scrolling surface')
              for (
                let parent = container.parentElement;
                parent;
                parent = parent.parentElement
              )
                parent.scrollTop = 0
              window.scrollTo(0, 0)
              container.scrollTop +=
                el.getBoundingClientRect().top -
                container.getBoundingClientRect().top -
                16
            })
        expect(
          await surface.evaluate(
            () => document.documentElement.scrollWidth <= innerWidth + 1,
          ),
        ).toBe(true)
        const ratios = await inboxContrast(page)
        expect(
          ratios.filter((item) => item.ratio + 0.01 < item.minimum),
        ).toEqual([])
        if (width === 390) {
          const small = await surface
            .locator('.inbox-ui')
            .evaluateAll((roots) => {
              const controls = new Set(
                roots.flatMap((root) => [
                  ...root.querySelectorAll<HTMLElement>(
                    'button,input:not([type=checkbox]),a,summary,label.inbox-check',
                  ),
                ]),
              )
              return [...controls]
                .filter(
                  (el) =>
                    !el.matches(':disabled') &&
                    el.getClientRects().length &&
                    !el.closest('[inert],[aria-hidden=true]') &&
                    el.getBoundingClientRect().height < 43.5,
                )
                .map((el) => ({
                  text:
                    el.getAttribute('aria-label') ||
                    el.textContent?.trim().slice(0, 50),
                  height: el.getBoundingClientRect().height,
                }))
            })
          expect(small).toEqual([])
        }
        if (process.env.INBOX_CAPTURE)
          await surface.screenshot({
            path: testInfo.outputPath(
              name + '-' + theme + '-' + width + '.png',
            ),
            fullPage: false,
            animations: 'disabled',
          })
      }
    await surface.setViewportSize({ width: 1366, height: 1000 })
  }
  try {
    const initial = await data(await api.get(base))
    expect(initial.settings.enabled).toBe(false)
    expect(initial.settings.revision).toBe(0)
    expect(initial.delivery).toBe('in_app')
    const directory = await data(await api.get(base + '/recipients'))
    expect(
      directory.recipients.some(
        (person: { id: string }) => person.id === profile.id,
      ),
    ).toBe(true)
    await data(
      await api.put('/api/inbox/sla/policies/' + account.id, {
        first_reply_minutes: 1,
        next_reply_minutes: 1,
        resolution_minutes: 120,
        business_hours: false,
        timezone: 'UTC',
        shifts: [],
        date_exceptions: [],
      }),
    )
    await page.context().addCookies((await request.storageState()).cookies)
    await page.addInitScript(
      (user) => localStorage.setItem('user', JSON.stringify(user)),
      profile,
    )
    await page.goto('/settings/inbox-workflows')
    await page
      .getByRole('tab', { name: 'Response targets', exact: true })
      .click()
    await page
      .getByRole('combobox', { name: 'WhatsApp account', exact: true })
      .selectOption(account.id)
    await page
      .getByRole('tab', { name: 'Internal notifications', exact: true })
      .click()
    const editor = page.getByRole('region', {
      name: 'Internal notifications',
      exact: true,
    })
    await expect(
      editor.getByRole('checkbox', {
        name: 'Enable internal notifications',
        exact: true,
      }),
    ).not.toBeChecked()
    await editor
      .getByRole('checkbox', {
        name: 'Enable internal notifications',
        exact: true,
      })
      .check()
    await expect(
      editor.getByRole('button', { name: 'Save', exact: true }),
    ).toBeDisabled()
    const recipient = directory.recipients.find(
      (person: { id: string }) => person.id === profile.id,
    )
    await editor
      .getByRole('checkbox', { name: recipient.name, exact: true })
      .check()
    const saved = page.waitForResponse(
      (response) =>
        response.url().endsWith(base) && response.request().method() === 'PUT',
    )
    await editor.getByRole('button', { name: 'Save', exact: true }).click()
    expect((await saved).status()).toBe(200)
    await expect(editor.getByText(/Notification routing saved/)).toBeVisible()
    const settings = (await data(await api.get(base))).settings
    expect(settings.recipient_ids).toEqual([profile.id])
    await expect(
      page.getByText(/Saving creates a new policy version for future cases/),
    ).toHaveCount(0)
    await editor
      .getByRole('checkbox', { name: 'Breached', exact: true })
      .uncheck()
    await page
      .getByRole('combobox', { name: 'WhatsApp account', exact: true })
      .selectOption('')
    await expect(page.getByRole('alertdialog')).toBeVisible()
    await page
      .getByRole('alertdialog')
      .getByRole('button', { name: 'Stay', exact: true })
      .click()
    await expect(
      page.getByRole('combobox', { name: 'WhatsApp account', exact: true }),
    ).toHaveValue(account.id)
    await expect(
      editor.getByRole('checkbox', { name: 'Breached', exact: true }),
    ).not.toBeChecked()
    await editor
      .getByRole('checkbox', { name: 'Breached', exact: true })
      .check()
    await captures('internal-notification-settings', 'Inbox response targets')
    await captures('internal-notification-recipients', 'Staff recipients')
    // A failed directory lookup must retain the exact selected recipient.
    const recipientRoute =
      '**/api/inbox/sla/escalations/' + account.id + '/recipients*'
    await page.route(recipientRoute, (route) =>
      route.fulfill({
        status: 503,
        contentType: 'application/json',
        body: JSON.stringify({ message: 'Synthetic staff lookup failure' }),
      }),
    )
    await editor.getByRole('button', { name: 'Search', exact: true }).click()
    await expect(editor.getByText(/Staff lookup failed/)).toBeVisible()
    await expect(
      editor.getByRole('button', {
        name: 'Remove ' + recipient.name + ' from recipients',
        exact: true,
      }),
    ).toBeVisible()
    await captures(
      'internal-notification-directory-failure',
      'Staff recipients',
    )
    await page.unroute(recipientRoute)
    await editor.getByRole('button', { name: 'Retry', exact: true }).click()
    await expect(
      editor.getByRole('checkbox', { name: recipient.name, exact: true }),
    ).toBeVisible()
    await page.route(recipientRoute, (route) =>
      route.fulfill({
        status: 403,
        contentType: 'application/json',
        body: JSON.stringify({
          message: 'Synthetic directory permission change',
        }),
      }),
    )
    await editor.getByRole('button', { name: 'Search', exact: true }).click()
    await expect(
      editor.getByText(/User-directory read access is required/),
    ).toBeVisible()
    await expect(editor.getByText(recipient.name, { exact: true })).toHaveCount(
      0,
    )
    await expect(editor.getByText(profile.id, { exact: true })).toBeVisible()
    await captures('internal-notification-directory-denied', 'Staff recipients')
    await page.unroute(recipientRoute)
    await editor.getByRole('button', { name: 'Retry', exact: true }).click()
    await expect(
      editor.getByRole('checkbox', { name: recipient.name, exact: true }),
    ).toBeVisible()
    const opened = (
      await data(await api.put(thread, { status: 'open', priority: 'high' }))
    ).case
    const listURL = '/api/inbox/sla/notices?account_id=' + account.id
    await page.goto('/chat')
    await page.getByRole('button', { name: 'Queues', exact: true }).click()
    await expect(
      page.getByRole('link', { name: 'Response alerts', exact: true }),
    ).toBeVisible()
    await captures('internal-alert-entry-inbox')
    await page
      .getByRole('link', { name: 'Response alerts', exact: true })
      .click()
    await expect(
      page.getByRole('heading', { name: 'Response alerts', exact: true }),
    ).toBeVisible()
    let notices: {
      id: string
      threshold: string
      case_id: string
      policy_version_id: string
      read_at?: string
    }[] = []
    // Observe the real existing worker and clock, not a public test endpoint or fabricated delivery event.
    await expect
      .poll(
        async () => {
          notices = (await data(await api.get(listURL))).notices
          return [...new Set(notices.map((notice) => notice.threshold))].sort()
        },
        { timeout: 125000, intervals: [2000] },
      )
      .toEqual(['at_risk', 'breach'])
    expect(notices).toHaveLength(2)
    for (const notice of notices) {
      expect(notice.case_id).toBe(opened.id)
      expect(notice.policy_version_id).toBe(opened.sla_policy_id)
    }
    const risk = notices.find((notice) => notice.threshold === 'at_risk')!
    const noticeList = page.getByRole('list', {
      name: 'Response alerts',
      exact: true,
    })
    await expect(noticeList.locator(':scope > li')).toHaveCount(2)
    await expect(
      noticeList
        .getByRole('link', {
          name: 'Synthetic internal notice enquiry',
          exact: true,
        })
        .first(),
    ).toHaveAttribute('href', new RegExp('case_id=' + opened.id))
    await captures('internal-response-alerts')
    const acknowledged = page.waitForResponse(
      (response) =>
        response
          .url()
          .endsWith('/api/inbox/sla/notices/' + risk.id + '/read') &&
        response.request().method() === 'PUT',
    )
    await noticeList
      .locator(':scope > li')
      .filter({ hasText: 'At risk' })
      .getByRole('button', { name: 'Acknowledge', exact: true })
      .click()
    expect((await acknowledged).status()).toBe(200)
    await expect(
      page.getByText(
        'Notice acknowledged. Customer messages and case status are unchanged.',
        { exact: true },
      ),
    ).toBeFocused()
    const afterRead = (await data(await api.get(listURL))).notices.find(
      (notice: { id: string }) => notice.id === risk.id,
    )
    expect(afterRead.read_at).toBeTruthy()
    await data(await api.put('/api/inbox/sla/notices/' + risk.id + '/read', {}))
    expect(
      (await data(await api.get(listURL))).notices.find(
        (notice: { id: string }) => notice.id === risk.id,
      ).read_at,
    ).toBe(afterRead.read_at)
    const unread = await data(await api.get(listURL + '&unread=true'))
    expect(unread.notices).toHaveLength(1)
    const history = await data(await api.get(base + '/history'))
    expect(history.events).toHaveLength(2)
    expect(
      history.events.every(
        (event: { available_count: number; suppressed_count: number }) =>
          event.available_count === 1 && event.suppressed_count === 0,
      ),
    ).toBe(true)
    await page.goto('/settings/inbox-workflows?tab=sla')
    await page
      .getByRole('combobox', { name: 'WhatsApp account', exact: true })
      .selectOption(account.id)
    await page
      .getByRole('tab', { name: 'Internal notifications', exact: true })
      .click()
    await page
      .getByRole('button', { name: 'Observation history', exact: true })
      .click()
    await expect(
      page
        .getByRole('list', { name: 'Observation history', exact: true })
        .locator(':scope > li'),
    ).toHaveCount(2)
    await captures('internal-observation-history', 'Observation history')
    await page.goto('/analytics/agents')
    await page.getByRole('tab', { name: 'Live inbox', exact: true }).click()
    await expect(
      page.getByRole('link', { name: 'Response alerts', exact: true }),
    ).toBeVisible()
    await captures('internal-alert-entry-supervision')
    const unchanged = (await data(await api.get(thread))).case
    expect(unchanged.version).toBe(opened.version)
    expect(unchanged.sla_policy_id).toBe(opened.sla_policy_id)
    const stale = await api.put(base, {
      revision: 0,
      enabled: false,
      at_risk: false,
      breach: false,
      recipient_ids: [],
    })
    expect(stale.status()).toBe(409)
    await api.login('agent@test.com', 'password')
    expect((await data(await api.get(listURL))).notices).toHaveLength(0)
    expect(
      (
        await api.put('/api/inbox/sla/notices/' + risk.id + '/read', {})
      ).status(),
    ).toBe(404)
    expect((await api.get(base)).status()).toBe(403)
    const agentProfile = await data(await api.get('/api/me'))
    expect(agentProfile.id).not.toBe(profile.id)
    agentContext = await page
      .context()
      .browser()!
      .newContext({ baseURL: process.env.BASE_URL })
    await agentContext.addCookies((await request.storageState()).cookies)
    await agentContext.addInitScript(
      (user) => localStorage.setItem('user', JSON.stringify(user)),
      agentProfile,
    )
    const agentPage = await agentContext.newPage()
    await agentPage.goto('/inbox/notices')
    await expect(
      agentPage.getByText(/No unacknowledged notices on this page/),
    ).toBeVisible()
    await expect(
      agentPage.getByText('Synthetic internal notice enquiry', { exact: true }),
    ).toHaveCount(0)
    await captures('internal-response-alerts-agent-empty', undefined, agentPage)
    await api.loginAsAdmin()
    await data(
      await api.put(base, {
        revision: settings.revision,
        enabled: false,
        at_risk: true,
        breach: true,
        recipient_ids: [profile.id],
      }),
    )
    expect(await data(await api.get('/api/inbox/automation'))).toEqual(
      automatic,
    )
    const client = new Client({ connectionString: database })
    await client.connect()
    try {
      const rows = await client.query(
        'SELECT threshold,count(*)::int AS total FROM inbox_sla_escalation_events WHERE organization_id=$1 AND account_id=$2 GROUP BY threshold ORDER BY threshold',
        [profile.organization_id, account.id],
      )
      expect(rows.rows).toEqual([
        { threshold: 'at_risk', total: 1 },
        { threshold: 'breach', total: 1 },
      ])
      const outgoing = await client.query(
        "SELECT count(*)::int AS total FROM messages WHERE organization_id=$1 AND whats_app_account=$2 AND direction='outgoing'",
        [profile.organization_id, account.name],
      )
      expect(outgoing.rows[0].total).toBe(0)
    } finally {
      await client.end()
    }
  } finally {
    await agentContext?.close()
    await api.loginAsAdmin()
    const current = await api.get(base)
    if (current.ok()) {
      const settings = (await current.json()).data.settings
      await api.put(base, {
        revision: settings.revision,
        enabled: false,
        at_risk: false,
        breach: false,
        recipient_ids: [],
      })
    }
    await api.del('/api/inbox/sla/policies/' + account.id)
    await api.del('/api/contacts/' + contact.id)
    await api.del('/api/accounts/' + account.id)
    await api.put('/api/inbox/workflows', { enabled: feature })
  }
})
