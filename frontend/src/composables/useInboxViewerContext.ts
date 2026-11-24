import { computed } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { useOrganizationsStore } from '@/stores/organizations'

// A late result belongs to the viewer/permissions that requested it, including
// an organisation override. Consumers invalidate generations and private drafts.
export function useInboxViewerContext() {
  const auth = useAuthStore()
  const organizations = useOrganizationsStore()
  return computed(() =>
    JSON.stringify([
      auth.user?.id,
      auth.organizationId,
      organizations.selectedOrgId,
      auth.user?.is_super_admin,
      auth.user?.role_id,
      auth.user?.role?.permissions,
    ]),
  )
}
