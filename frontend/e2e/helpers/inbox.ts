import type { APIRequestContext } from '@playwright/test'
import { ApiHelper } from './api'
import type { TestScope } from '../framework/scope'

/** Isolate organization settings, required fields and user preferences per test. */
export async function createInboxTestContext(request: APIRequestContext, scope: TestScope) {
  const api = new ApiHelper(request)
  await api.login('admin@admin.com', 'admin')
  const organization = await api.createOrganization(scope.name())
  await api.switchOrg(organization.id)
  const response = await api.get('/api/roles')
  if (!response.ok()) throw new Error('Could not load isolated inbox roles')
  const roles: { id: string; name: string }[] = (await response.json()).data.roles
  const role = roles.find(item => item.name === 'admin')
  if (!role) throw new Error('Isolated inbox administrator role missing')
  const admin = { email: scope.email(), password: 'InboxTest123!' }
  await api.createUser({ ...admin, full_name: scope.name('admin'), role_id: role.id })
  await api.login(admin.email, admin.password)
  return { api, admin, organization, roles }
}
