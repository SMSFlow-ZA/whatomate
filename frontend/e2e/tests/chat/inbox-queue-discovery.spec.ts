import { expect, test, type APIResponse } from '@playwright/test'
import { Client } from 'pg'
import { ApiHelper } from '../../helpers/api'
import { createTestScope } from '../../framework'
import { inboxContrast } from '../../helpers/inbox-contrast'

test('qualifies queue discovery through normal HTTP while preserving legacy queues', async ({
  page,
  request,
}, testInfo) => {
  test.setTimeout(180000)
  const database = process.env.TEST_DATABASE_URL
  test.skip(!database, 'An explicitly owned disposable is required')
  const dbURL = new URL(database!)
  expect(['fixture', 'whatomate_test']).toContain(dbURL.pathname.slice(1))
  for (const host of [dbURL.hostname, new URL(process.env.BASE_URL!).hostname])
    expect(
      host.startsWith('whatomate-inbox-') ||
        ['localhost', '127.0.0.1'].includes(host),
    ).toBe(true)
  const api = new ApiHelper(request)
  const scope = createTestScope('queue-discovery')
  const data = async (response: APIResponse) => {
    expect(
      response.ok(),
      'HTTP ' + response.status() + ' ' + new URL(response.url()).pathname,
    ).toBe(true)
    return (await response.json()).data
  }
  await api.loginAsAdmin()
  const enabledBefore = (await data(await api.get('/api/inbox/workflows')))
    .enabled
  await data(await api.put('/api/inbox/workflows', { enabled: true }))
  const sendsBefore = await data(await api.get('/api/inbox/automation'))
  expect(sendsBefore.dispatch_available).toBe(false)
  const profile = await data(await api.get('/api/me'))
  const account = await api.createWhatsAppAccount({
    name: scope.name('queue discovery'),
    phone_id: scope.phone().replace(/\D/g, ''),
    business_id: '100000000055',
    access_token: 'synthetic-never-send',
  })
  const contacts: { id: string; profile_name: string }[] = []
  const cases: string[] = []
  const client = new Client({ connectionString: database })
  let viewID = ''
  let uiViewID = ''
  async function captures(name: string, heading?: string) {
    for (const width of [1366, 390])
      for (const theme of ['light', 'dark']) {
        await page.setViewportSize({
          width,
          height: width === 390 ? 844 : 1000,
        })
        await page.evaluate(async (theme) => {
          document.documentElement.classList.remove('light', 'dark')
          document.documentElement.classList.add(theme)
          await new Promise<void>((done) =>
            requestAnimationFrame(() => requestAnimationFrame(() => done())),
          )
          await Promise.all(
            document
              .getAnimations()
              .filter(
                (a) => a.effect?.getComputedTiming().iterations !== Infinity,
              )
              .map((a) => a.finished.catch(() => {})),
          )
        }, theme)
        if (heading) {
          await page
            .getByRole('heading', { name: heading, exact: true })
            .evaluate((element) => {
              let parent = element.parentElement
              while (parent) {
                if (
                  parent.scrollHeight > parent.clientHeight &&
                  ['auto', 'scroll'].includes(
                    getComputedStyle(parent).overflowY,
                  )
                ) {
                  parent.scrollTop +=
                    element.getBoundingClientRect().top -
                    parent.getBoundingClientRect().top -
                    12
                  break
                }
                parent = parent.parentElement
              }
            })
          if (name === 'queue-date-error')
            await page
              .getByRole('alert')
              .filter({ hasText: 'Through must be on or after From' })
              .scrollIntoViewIfNeeded()
          await expect(
            page.locator('input[aria-label="Entered human queue — From"]'),
          ).toHaveCSS('color-scheme', theme)
        }
        const panel = page.getByRole('dialog')
        if (await panel.count()) {
          const bounds = await panel.last().boundingBox()
          expect(bounds).not.toBeNull()
          expect(bounds!.y).toBeGreaterThanOrEqual(0)
          expect(bounds!.y + bounds!.height).toBeLessThanOrEqual(
            width === 390 ? 845 : 1001,
          )
        }
        expect(
          await page.evaluate(
            () => document.documentElement.scrollWidth <= innerWidth + 1,
          ),
        ).toBe(true)
        // The queue clips horizontal overflow independently of the document.
        // Check its actual controls so a narrow pane cannot hide Refresh or
        // response alerts while the page-wide overflow check still passes.
        expect(
          await page.locator('.inbox-queue-tools').evaluate((root) => {
            const bounds = root.getBoundingClientRect()
            const refresh = root.querySelector('[data-queue-refresh]')
            const row = refresh?.parentElement
            if (!row) throw new Error('Queue action controls are missing')
            return [...row.querySelectorAll<HTMLElement>('button,a')]
              .filter((element) => element.getClientRects().length)
              .filter((element) => {
                const box = element.getBoundingClientRect()
                return (
                  box.left < bounds.left - 1 || box.right > bounds.right + 1
                )
              })
              .map(
                (element) =>
                  element.getAttribute('aria-label') || element.textContent,
              )
          }),
        ).toEqual([])
        expect(
          (await inboxContrast(page, true)).filter(
            (item) => item.ratio + 0.01 < item.minimum,
          ),
        ).toEqual([])
        if (width === 390) {
          const small = await page.locator('.inbox-ui').evaluateAll((roots) =>
            [
              ...new Set(
                roots.flatMap((root) => [
                  ...root.querySelectorAll<HTMLElement>(
                    'button,input:not([type=checkbox]),select,a,summary,label.inbox-check',
                  ),
                ]),
              ),
            ]
              .filter(
                (el) =>
                  el.getClientRects().length &&
                  !el.matches(':disabled') &&
                  !el.closest('[inert],[aria-hidden=true]') &&
                  el.getBoundingClientRect().height < 43.5,
              )
              .map((el) => ({
                text:
                  el.getAttribute('aria-label') ||
                  el.textContent?.trim().slice(0, 45),
                height: el.getBoundingClientRect().height,
              })),
          )
          expect(small).toEqual([])
        }
        if (process.env.INBOX_CAPTURE)
          await page.screenshot({
            path: testInfo.outputPath(
              name + '-' + theme + '-' + width + '.png',
            ),
            animations: 'disabled',
          })
      }
    await page.setViewportSize({ width: 1366, height: 1000 })
  }
  const filters = { account_id: account.id, queue: 'unassigned' }
  const queue = (discovery?: object, cursor?: string) => {
    const query = new URLSearchParams({
      filters: JSON.stringify(filters),
      include_counts: 'true',
    })
    if (discovery) query.set('discovery', JSON.stringify(discovery))
    if (cursor) query.set('cursor', cursor)
    return '/api/inbox/queue?' + query
  }
  await client.connect()
  try {
    const indexes = await client.query(
      "SELECT indexname FROM pg_indexes WHERE tablename='inbox_cases' AND indexname IN ('idx_inbox_discovery_opened','idx_inbox_discovery_activity') ORDER BY indexname",
    )
    expect(indexes.rows.map((row) => row.indexname)).toEqual([
      'idx_inbox_discovery_activity',
      'idx_inbox_discovery_opened',
    ])
    for (let i = 0; i < 55; i++) {
      const contact = await api.createContact(
        scope.phone(),
        'Synthetic queue discovery ' + String(i).padStart(2, '0'),
      )
      contacts.push(contact)
      const result = await data(
        await api.put(
          '/api/inbox/accounts/' + account.id + '/contacts/' + contact.id,
          { status: 'open', priority: 'normal' },
        ),
      )
      cases.push(result.case.id)
    }
    // Clearly owned timestamp fixtures; no webhook, provider or outbound calls.
    await client.query(
      "UPDATE inbox_cases c SET opened_at='2026-11-01T04:00:00Z'::timestamptz+(s.n-1)*interval '1 minute',last_inbound_at='2026-11-01T04:00:00Z'::timestamptz+(s.n-1)*interval '1 minute' FROM unnest($1::uuid[]) WITH ORDINALITY s(id,n) WHERE c.id=s.id AND c.organization_id=$2 AND c.account_id=$3",
      [cases, profile.organization_id, account.id],
    )
    const discovery = { order: 'oldest' }
    const first = await data(await api.get(queue(discovery)))
    expect(first.cases.map((row: { id: string }) => row.id)).toEqual(
      cases.slice(0, 50),
    )
    expect(first.counts.unassigned).toBe(55)
    expect(first.paging).toBe('keyset')
    const second = await data(
      await api.get(queue(discovery, first.next_cursor)),
    )
    expect(second.cases.map((row: { id: string }) => row.id)).toEqual(
      cases.slice(50),
    )
    expect(second.has_more).toBe(false)
    const range = {
      order: 'latest_activity',
      timezone: 'America/New_York',
      opened_from: '2026-11-01T00:10:00-04:00',
      opened_before: '2026-11-01T00:37:00-04:00',
    }
    const ranged = await data(await api.get(queue(range)))
    expect(ranged.counts.all).toBe(27)
    expect(ranged.cases.map((row: { id: string }) => row.id)).toEqual(
      cases.slice(10, 37).reverse(),
    )
    const saved = await data(
      await api.post('/api/inbox/views', {
        name: scope.name('saved discovery'),
        filters,
        discovery: range,
      }),
    )
    viewID = saved.view.id
    await data(
      await api.put('/api/inbox/views/' + viewID, {
        name: scope.name('legacy rename'),
        filters,
      }),
    )
    const views = await data(await api.get('/api/inbox/views'))
    expect(views.discovery_version).toBe(1)
    expect(
      views.views.find((view: { id: string }) => view.id === viewID).discovery
        .order,
    ).toBe('latest_activity')
    await data(
      await api.put(
        '/api/inbox/accounts/' + account.id + '/contacts/' + contacts[0].id,
        {
          case_id: first.cases[0].id,
          priority: 'high',
          version: first.cases[0].version,
        },
      ),
    )
    const stale = await api.get(queue(discovery, first.next_cursor))
    expect(stale.status()).toBe(409)
    expect(await stale.text()).toContain('inbox_queue_changed')
    expect((await api.get(queue({ order: 'unsupported' }))).status()).toBe(400)
    const refreshed = await data(await api.get(queue(discovery)))
    expect(refreshed.cases).toHaveLength(50)
    const legacy = await data(await api.get(queue() + '&offset=50'))
    expect(legacy.paging).toBe('offset')
    expect(legacy.cases).toHaveLength(5)
    expect(legacy.next_cursor).toBe('')

    // The normal Vue queue uses the same authenticated API; all provider sends
    // remain held throughout calendar, saved-view and cursor acceptance.
    await page.context().addCookies((await request.storageState()).cookies)
    await page.addInitScript(
      (user) => localStorage.setItem('user', JSON.stringify(user)),
      profile,
    )
    await page.goto('/chat')
    await page.getByRole('button', { name: 'Queues', exact: true }).click()
    await page.getByRole('button', { name: 'Unassigned', exact: true }).click()
    await page.getByRole('button', { name: /^Filters(?: \(\d+\))?$/ }).click()
    await page
      .getByRole('combobox', { name: 'WhatsApp account', exact: true })
      .selectOption(account.id)
    await page.keyboard.press('Escape')
    await expect(
      page.getByRole('checkbox', {
        name: 'Select case for Synthetic queue discovery 00',
        exact: true,
      }),
    ).toBeVisible()
    const sort = page.getByRole('combobox', { name: 'Sort', exact: true })
    const queueResponse = (
      predicate: (query: Record<string, string>) => boolean,
    ) =>
      page.waitForResponse((response) => {
        const url = new URL(response.url())
        if (
          url.pathname !== '/api/inbox/queue' ||
          response.request().method() !== 'GET'
        )
          return false
        const filter = JSON.parse(url.searchParams.get('filters') || '{}')
        return (
          filter.account_id === account.id &&
          predicate(JSON.parse(url.searchParams.get('discovery') || '{}'))
        )
      })
    for (const order of [
      'newest',
      'oldest',
      'latest_activity',
      'longest_unanswered',
      'nearest_deadline',
      'priority_waiting',
    ]) {
      const response = queueResponse((query) => query.order === order)
      await sort.selectOption(order)
      const result = await data(await response)
      await expect(page.locator('.inbox-queue-row').first()).toContainText(
        result.cases[0].contact_name,
      )
    }
    const about = page.getByRole('button', {
      name: 'About this order',
      exact: true,
    })
    await about.focus()
    await page.keyboard.press('Enter')
    await expect(
      page.getByText(
        'Urgent to low priority, then earliest entry into the human queue.',
        { exact: true },
      ),
    ).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(about).toBeFocused()
    await sort.focus()
    await page.keyboard.press('ArrowDown')
    await page.keyboard.press('Enter')
    await expect(sort).toHaveValue('latest_activity')
    await sort.selectOption('oldest')
    await expect(page.locator('.inbox-queue-row').first()).toContainText(
      'Synthetic queue discovery 00',
    )
    await captures('queue-sort')
    await page.getByRole('button', { name: /^Filters(?: \(\d+\))?$/ }).click()
    await page
      .getByRole('combobox', { name: 'Date timezone', exact: true })
      .fill('America/New_York')
    // Native date controls are not exposed as textboxes in every Chromium build.
    const fromInput = page.locator(
      'input[aria-label="Entered human queue — From"]',
    )
    const throughInput = page.locator(
      'input[aria-label="Entered human queue — Through"]',
    )
    await fromInput.fill('2026-11-02')
    await throughInput.fill('2026-11-01')
    await page.getByRole('button', { name: 'Apply dates', exact: true }).click()
    await expect(
      page
        .getByRole('alert')
        .filter({ hasText: 'Through must be on or after From' }),
    ).toBeVisible()
    await captures('queue-date-error', 'Date ranges')
    await fromInput.fill('2026-11-01')
    const dated = queueResponse(
      (query) => query.opened_before === '2026-11-02T05:00:00.000Z',
    )
    await page.getByRole('button', { name: 'Apply dates', exact: true }).click()
    expect((await data(await dated)).counts.all).toBe(55)
    await captures('queue-dates', 'Date ranges')
    await page.keyboard.press('Escape')
    await page.getByRole('button', { name: 'Saved views', exact: true }).click()
    const uiViewName = scope.name('calendar queue')
    await page
      .getByRole('textbox', { name: 'View name', exact: true })
      .fill(uiViewName)
    const savedResponse = page.waitForResponse(
      (response) =>
        new URL(response.url()).pathname === '/api/inbox/views' &&
        response.request().method() === 'POST',
    )
    await page.getByRole('button', { name: 'Save view', exact: true }).click()
    uiViewID = (await data(await savedResponse)).view.id
    await expect(
      page
        .getByRole('combobox', { name: 'Choose a saved view' })
        .locator('option:checked'),
    ).toHaveText(uiViewName)
    await captures('queue-saved-dates')
    await page.keyboard.press('Escape')
    await page.reload()
    await expect(
      page.getByRole('combobox', { name: 'Sort', exact: true }),
    ).toHaveValue('oldest')
    await expect(
      page.getByText('Date filters applied · America/New_York'),
    ).toBeVisible()
    await page.getByRole('button', { name: 'Next', exact: true }).click()
    await expect(page.getByText('Page 2', { exact: true })).toBeVisible()
    await expect(page.locator('.inbox-queue-row')).toHaveCount(5)
    const current = (
      await data(
        await api.get(
          '/api/inbox/accounts/' + account.id + '/contacts/' + contacts[0].id,
        ),
      )
    ).case
    await data(
      await api.put(
        '/api/inbox/accounts/' + account.id + '/contacts/' + contacts[0].id,
        { case_id: current.id, version: current.version, priority: 'urgent' },
      ),
    )
    await page.getByRole('button', { name: 'Previous', exact: true }).click()
    await expect(
      page.getByText('Matching work changed.', { exact: false }),
    ).toBeVisible()
    await expect(page.getByText('Page 2', { exact: true })).toBeVisible()
    await expect(page.locator('.inbox-queue-row')).toHaveCount(5)
    await captures('queue-stale-page')
    await page
      .getByRole('button', { name: 'Refresh from first page', exact: true })
      .click()
    await expect(page.getByText('Page 1', { exact: true })).toBeVisible()
    await expect(
      page.getByText('Matching work changed.', { exact: false }),
    ).toHaveCount(0)
    await expect(page.locator('[data-queue-refresh]')).toBeFocused()
    await page.getByRole('button', { name: /^Filters(?: \(\d+\))?$/ }).click()
    await fromInput.fill('2026-10-01')
    await throughInput.fill('2026-10-01')
    await page.getByRole('button', { name: 'Apply dates', exact: true }).click()
    await page.keyboard.press('Escape')
    await expect(page.getByText('No cases match these filters.')).toBeVisible()
    await captures('queue-empty-dates')
    expect(await data(await api.get('/api/inbox/automation'))).toEqual(
      sendsBefore,
    )
    const outgoing = await client.query(
      "SELECT count(*)::int AS total FROM messages WHERE organization_id=$1 AND whats_app_account=$2 AND direction='outgoing'",
      [profile.organization_id, account.name],
    )
    expect(outgoing.rows[0].total).toBe(0)
  } finally {
    if (uiViewID) await api.del('/api/inbox/views/' + uiViewID)
    if (viewID) await api.del('/api/inbox/views/' + viewID)
    for (const contact of contacts) await api.del('/api/contacts/' + contact.id)
    await api.del('/api/accounts/' + account.id)
    await api.put('/api/inbox/workflows', { enabled: enabledBefore })
    await client.end()
  }
})
