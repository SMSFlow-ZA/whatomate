//go:build linux

package main

import (
	"fmt"
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
	body := fmt.Sprintf(`CREATE FUNCTION public.er_processing_authorize_v1(org uuid,worker uuid,profile text,audiences text[])
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $er_owner$
DECLARE role_id uuid; permission text; grant_row record; found_job boolean:=false;
 accepted text[]:=ARRAY[]::text[]; configured text[]:=ARRAY[%s]::text[]; audience text; version bigint;
BEGIN
 IF org<>%s::uuid OR worker<>%s::uuid OR profile<>%s
 OR session_user<>%s OR current_database()<>%s OR audiences IS NULL OR cardinality(audiences)=0 THEN RETURN false; END IF;
 PERFORM pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended('["'||org::text||'","'||worker::text||'"]',0));
 PERFORM 1 FROM public.whatomate_protection_state s WHERE s.id=1 AND s.protected=true FOR SHARE;
 IF NOT FOUND THEN RETURN false; END IF;
 SELECT u.role_id INTO role_id FROM public.organizations o JOIN public.users u ON u.organization_id=o.id
 WHERE o.id=org AND u.id=worker AND o.deleted_at IS NULL AND u.deleted_at IS NULL AND u.is_active=true AND u.is_super_admin=false FOR SHARE OF o,u;
 IF NOT FOUND OR role_id IS NULL THEN RETURN false; END IF;
 PERFORM 1 FROM public.custom_roles r WHERE r.id=role_id AND r.organization_id=org AND r.deleted_at IS NULL AND r.is_system=false AND r.is_default=false FOR SHARE;
 IF NOT FOUND THEN RETURN false; END IF;
 FOR permission IN SELECT p.resource||':'||p.action FROM public.role_permissions rp JOIN public.permissions p ON p.id=rp.permission_id WHERE rp.custom_role_id=role_id AND p.deleted_at IS NULL FOR SHARE OF rp,p LOOP
  IF permission='knowledge:job' THEN found_job:=true; ELSIF permission<>'knowledge:read' THEN RETURN false; END IF;
 END LOOP;
 IF NOT found_job THEN RETURN false; END IF;
 SELECT v.version INTO version FROM public.addon_api_access_versions v WHERE v.organization_id=org::text AND v.user_id=worker::text FOR SHARE;
 IF NOT FOUND THEN RETURN false; END IF;
 PERFORM 1 FROM public.addon_api_access_audit a WHERE a.organization_id=org::text AND a.user_id=worker::text AND a.new_version=version AND a.action='replace' AND a.metadata->>'kind'='knowledge-worker' AND a.metadata->>'profile'=profile;
 IF NOT FOUND THEN RETURN false; END IF;
 FOR grant_row IN SELECT g.permission,g.unit_id,g.audience FROM public.addon_api_grants g WHERE g.organization_id=org::text AND g.user_id=worker::text FOR SHARE LOOP
  IF grant_row.permission NOT IN('knowledge:job','knowledge:read') OR grant_row.unit_id<>'' OR grant_row.audience IN('','*') THEN RETURN false; END IF;
  IF grant_row.permission='knowledge:job' THEN accepted:=pg_catalog.array_append(accepted,grant_row.audience); END IF;
 END LOOP;
 FOREACH audience IN ARRAY audiences LOOP
  IF audience IS NULL OR NOT(audience=ANY(accepted)) OR NOT(audience=ANY(configured)) THEN RETURN false; END IF;
 END LOOP;
 RETURN true;
END $er_owner$;
`, strings.Join(audiences, ","), quote(c.OrganizationID), quote(c.WorkerUserID), quote(c.ProfileID), quote(c.DatabaseRole), quote(c.DatabaseName))
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
	return "BEGIN;\n" + grants + body + fmt.Sprintf(`ALTER FUNCTION %s OWNER TO %s;
REVOKE CREATE ON SCHEMA public FROM %s;
REVOKE ALL ON FUNCTION %s FROM PUBLIC;
GRANT EXECUTE ON FUNCTION %s TO %s;
COMMIT;
`, authorizationSignature, owner, owner, authorizationSignature, authorizationSignature, c.DatabaseRole), nil
}
