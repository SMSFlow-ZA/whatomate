//go:build linux

package main

import (
	"fmt"
	"github.com/shridarpatil/whatomate/pkg/extensions/processingauth"
	"strings"
	"time"
)

// Emits a per-instance static migration for independent privileged review. This
// SQL is never executed by worker startup. Identifiers are UUID-derived and all
// data literals are quoted; the function contains no dynamic SQL.
func authorizationMigration(c configuration) (string, error) {
	if c.validateBindings(time.Now().UTC(), false) != nil {
		return "", denied
	}
	owner := "er_processing_owner_" + strings.ReplaceAll(c.InstanceID, "-", "")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	audiences := make([]string, len(c.Audiences))
	for i, a := range c.Audiences {
		if strings.Contains(a, "$er_owner$") {
			return "", denied
		}
		audiences[i] = quote(a)
	}
	body := processingauth.DDL

	grants := fmt.Sprintf(`CREATE ROLE %s NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
GRANT USAGE,CREATE ON SCHEMA public TO %s;
GRANT SELECT(id,protected),UPDATE(id) ON public.whatomate_protection_state TO %s;
GRANT SELECT(id,deleted_at),UPDATE(id) ON public.organizations TO %s;
GRANT SELECT(id,organization_id,role_id,is_active,is_super_admin,deleted_at),UPDATE(id) ON public.users TO %s;
GRANT SELECT(id,organization_id,deleted_at,is_system,is_default),UPDATE(id) ON public.custom_roles TO %s;
GRANT SELECT(custom_role_id,permission_id),UPDATE(custom_role_id) ON public.role_permissions TO %s;
GRANT SELECT(id,resource,action,deleted_at),UPDATE(id) ON public.permissions TO %s;
GRANT SELECT(organization_id,user_id,version),UPDATE(organization_id) ON public.addon_api_access_versions TO %s;
GRANT SELECT(organization_id,user_id,new_version,action,metadata) ON public.addon_api_access_audit TO %s;
GRANT SELECT(organization_id,user_id,permission,unit_id,audience),UPDATE(organization_id) ON public.addon_api_grants TO %s;
`, owner, owner, owner, owner, owner, owner, owner, owner, owner, owner, owner)
	binding := fmt.Sprintf("INSERT INTO public.er_processing_authorization_bindings(database_name,worker_role,organization_id,worker_user_id,profile_id,audiences) VALUES(%s,%s,%s::uuid,%s::uuid,%s,ARRAY[%s]::text[]);\n", quote(c.DatabaseName), quote(c.DatabaseRole), quote(c.OrganizationID), quote(c.WorkerUserID), quote(c.ProfileID), strings.Join(audiences, ","))
	return "BEGIN;\n" + body + grants + "GRANT SELECT ON public.er_processing_authorization_bindings TO " + owner + ";\n" + binding + fmt.Sprintf(`ALTER FUNCTION %s OWNER TO %s;
REVOKE CREATE ON SCHEMA public FROM %s;
REVOKE ALL ON FUNCTION %s FROM PUBLIC;
GRANT EXECUTE ON FUNCTION %s TO %s;
COMMIT;
`, authorizationSignature, owner, owner, authorizationSignature, authorizationSignature, c.DatabaseRole), nil
}
