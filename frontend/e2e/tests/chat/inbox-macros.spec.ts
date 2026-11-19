import { expect, test } from '@playwright/test'
import { createInboxTestContext } from '../../helpers/inbox'
import { createTestScope } from '../../framework'

test('reviews disabled rules and manual macros without enabling automatic replies', async ({ page, request }) => {
  page.setDefaultTimeout(15000)
  const scope = createTestScope('inbox-macros')
  const { api } = await createInboxTestContext(request, scope)
  const enabledBefore = (await (await api.get('/api/inbox/workflows')).json()).data.enabled
  const account = await api.createWhatsAppAccount({ name: scope.name('support'), phone_id: scope.phone().replace(/\D/g, ''), business_id: '100000000008', access_token: 'synthetic-no-send' })
  const contact = await api.createContact('+12025550195', scope.name('customer'))
  const path = `/api/inbox/accounts/${account.id}/contacts/${contact.id}`
  const definitions: string[] = []
  try {
    expect((await api.put('/api/inbox/workflows', { enabled: true })).ok()).toBeTruthy()
    expect((await (await api.get('/api/inbox/automation')).json()).data.enabled).toBe(false)
    const opened = await api.put(path, { status: 'open', priority: 'normal' })
    const current = (await opened.json()).data.case
    const profile = (await (await api.get('/api/me')).json()).data
    await page.context().addCookies((await request.storageState()).cookies)
    await page.addInitScript(user => localStorage.setItem('user', JSON.stringify(user)), profile)
    await page.goto('/settings/inbox-workflows')
    await page.getByRole('button', { name: 'New rule', exact: true }).first().click()
    const form = page.locator('#inbox-workflow-editor')
    await form.getByLabel('Workflow name').fill(scope.name('triage'))
    await form.getByRole('combobox', { name: 'WhatsApp account', exact: true }).selectOption(account.id)
    await form.getByRole('combobox', { name: 'Action', exact: true }).selectOption('priority')
    await form.getByRole('combobox', { name: 'Action value', exact: true }).selectOption('high')
    const enableControl = form.getByRole('checkbox', { name: 'Enable this rule' })
    if (await enableControl.count()) await expect(enableControl).not.toBeChecked()
    const [saved] = await Promise.all([
      page.waitForResponse(r => new URL(r.url()).pathname === '/api/inbox/workflow-definitions' && r.request().method() === 'POST'),
      form.getByRole('button', { name: 'Save', exact: true }).click(),
    ])
    expect(saved.ok()).toBeTruthy()
    const rule = (await saved.json()).data.workflow
    definitions.push(rule.id)
    expect(rule.enabled_at).toBeFalsy()
    const row = page.locator('li').filter({ has: page.getByText(rule.name, { exact: true }) })
    await row.getByRole('button', { name: 'Dry run', exact: true }).click()
    const preview = page.getByRole('region', { name: 'Dry run', exact: true })
    await preview.getByRole('combobox', { name: 'Choose a case', exact: true }).selectOption(current.id)
    await preview.getByRole('button', { name: 'Review actions', exact: true }).click()
    await expect(preview.getByText('This case matches the workflow.', { exact: true })).toBeVisible()
    expect((await (await api.get(path)).json()).data.case.priority).toBe('normal')
    const name = scope.name('handover')
    const created = await api.post('/api/inbox/workflow-definitions', { kind: 'macro', name, trigger: '', position: 1, conditions: { account_id: account.id }, actions: [{ type: 'note', value: 'Example private handover note' }, { type: 'status', value: 'waiting_customer' }] })
    expect(created.ok()).toBeTruthy()
    definitions.push((await created.json()).data.workflow.id)
    await page.goto(`/chat/${contact.id}?inbox_account=${encodeURIComponent(account.name)}&inbox_account_id=${account.id}`)
    await page.getByRole('button', { name: 'Macros', exact: true }).click()
    const dialog = page.getByRole('dialog', { name: 'Macros', exact: true })
    await dialog.getByRole('button', { name, exact: true }).click()
    await expect(dialog.getByText('Example private handover note', { exact: true })).toBeVisible()
    expect((await (await api.get(path)).json()).data.case.status).toBe('open')
    await dialog.getByRole('button', { name: 'Apply macro', exact: true }).click()
    await expect(dialog).not.toBeVisible()
    expect((await (await api.get(path)).json()).data.case.status).toBe('waiting_customer')
    const replyName = scope.name('closed-window-reply')
    const reply = await api.post('/api/inbox/workflow-definitions', { kind: 'macro', name: replyName, trigger: '', position: 2, conditions: { account_id: account.id }, actions: [{ type: 'send_text', value: 'Synthetic reply preview only' }] })
    definitions.push((await reply.json()).data.workflow.id)
    await page.getByRole('button', { name: 'Macros', exact: true }).click()
    await dialog.getByRole('button', { name: replyName, exact: true }).click()
    await expect(dialog.getByRole('alert')).toContainText('currently ineligible')
    await expect(dialog.getByRole('button', { name: 'Apply macro', exact: true })).toBeDisabled()
    await page.keyboard.press('Escape')
    expect((await (await api.get('/api/inbox/automation')).json()).data.enabled).toBe(false)
  } finally {
    for (const id of definitions) await api.del('/api/inbox/workflow-definitions/' + id)
    await api.del('/api/contacts/' + contact.id)
    await api.del('/api/accounts/' + account.id)
    await api.put('/api/inbox/workflows', { enabled: enabledBefore })
  }
})
