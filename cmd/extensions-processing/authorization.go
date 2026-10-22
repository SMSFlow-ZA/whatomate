//go:build linux

package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"strings"
)

const authorizationSignature = "public.er_processing_authorize_v1(uuid,uuid,text,text[])"

// Only the reviewed fixed owner function can lock host rows. The worker has no
// host UPDATE authority. Its definition and ACL are checked in the queue tx.
func ownerAuthorization(db *sql.DB, c configuration) func(context.Context, *sql.Tx, knowledge.ProcessingJob) error {
	return func(ctx context.Context, tx *sql.Tx, j knowledge.ProcessingJob) error {
		if j.TenantID != c.OrganizationID || len(j.Metadata.Audiences) == 0 {
			return denied
		}
		configured := map[string]bool{}
		for _, a := range c.Audiences {
			configured[a] = true
		}
		for _, a := range j.Metadata.Audiences {
			if !configured[a] {
				return denied
			}
		}
		if e := (api.AccessStore{DB: db}).LockVersionedTx(ctx, tx, c.OrganizationID, c.WorkerUserID); e != nil {
			return denied
		}
		var definition string
		var safe bool
		e := tx.QueryRowContext(ctx, `SELECT pg_catalog.pg_get_functiondef(p.oid),
   p.prosecdef AND p.prokind='f' AND p.proconfig=ARRAY['search_path=pg_catalog']::text[]
   AND p.prorettype='pg_catalog.bool'::pg_catalog.regtype AND NOT p.proleakproof
   AND o.rolname=$2 AND NOT(o.rolcanlogin OR o.rolsuper OR o.rolcreatedb OR o.rolcreaterole OR o.rolreplication OR o.rolbypassrls)
   AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_auth_members m WHERE m.member=o.oid OR m.roleid=o.oid)
   AND NOT EXISTS(SELECT 1 FROM pg_catalog.aclexplode(COALESCE(p.proacl,pg_catalog.acldefault('f',p.proowner))) a
     WHERE a.grantee NOT IN(p.proowner,(SELECT r.oid FROM pg_catalog.pg_roles r WHERE r.rolname=$3))
     OR a.privilege_type<>'EXECUTE' OR (a.is_grantable AND a.grantee<>p.proowner))
   AND pg_catalog.has_function_privilege(current_user,p.oid,'EXECUTE')
   FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_roles o ON o.oid=p.proowner
   WHERE p.oid=pg_catalog.to_regprocedure($1)`, authorizationSignature, "er_processing_owner_"+strings.ReplaceAll(c.InstanceID, "-", ""), c.DatabaseRole).Scan(&definition, &safe)
		sum := sha256.Sum256([]byte(definition))
		if e != nil || !safe || hex.EncodeToString(sum[:]) != c.AuthorizationFunctionSHA256 {
			return denied
		}
		var allowed bool
		if tx.QueryRowContext(ctx, `SELECT public.er_processing_authorize_v1($1::uuid,$2::uuid,$3,$4::text[])`, c.OrganizationID, c.WorkerUserID, c.ProfileID, j.Metadata.Audiences).Scan(&allowed) != nil || !allowed {
			return denied
		}
		return nil
	}
}
