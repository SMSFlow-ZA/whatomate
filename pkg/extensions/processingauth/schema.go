// Package processingauth owns the fixed, deny-by-default processing authorization
// schema. Only an offline owner writes per-deployment binding rows.
package processingauth

import (
	"context"
	"database/sql"
)

const Signature = "public.er_processing_authorize_v1(uuid,uuid,text,text[])"
const BindingTable = "public.er_processing_authorization_bindings"

const DDL = `CREATE TABLE IF NOT EXISTS public.er_processing_authorization_bindings(
 database_name text NOT NULL,worker_role text NOT NULL,organization_id uuid NOT NULL,
 worker_user_id uuid NOT NULL,profile_id text NOT NULL,audiences text[] NOT NULL,
 PRIMARY KEY(database_name,worker_role),CHECK(profile_id<>''),CHECK(cardinality(audiences)>0),CHECK(array_position(audiences,NULL) IS NULL));
REVOKE ALL ON public.er_processing_authorization_bindings FROM PUBLIC;
CREATE OR REPLACE FUNCTION public.er_processing_authorize_v1(org uuid,worker uuid,profile text,audiences text[])
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $er_owner$
DECLARE role_id uuid; permission text; grant_row record; found_job boolean:=false;
 accepted text[]:=ARRAY[]::text[]; configured text[]; audience text; version bigint;
BEGIN
 SELECT b.audiences INTO configured FROM public.er_processing_authorization_bindings b
 WHERE b.database_name=current_database() AND b.worker_role=session_user
 AND b.organization_id=org AND b.worker_user_id=worker AND b.profile_id=profile;
 IF NOT FOUND OR audiences IS NULL OR cardinality(audiences)=0 THEN RETURN false; END IF;
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
REVOKE ALL ON FUNCTION public.er_processing_authorize_v1(uuid,uuid,text,text[]) FROM PUBLIC;
`

func Migrate(ctx context.Context, db *sql.DB) error { _, e := db.ExecContext(ctx, DDL); return e }
