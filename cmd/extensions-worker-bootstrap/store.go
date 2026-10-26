package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/shridarpatil/whatomate/internal/protectedschema"
	"github.com/shridarpatil/whatomate/pkg/extensions/processingauth"
)

var errCommitUnknown = api.ErrAccessCommitUnknown

type Result struct {
	Version        int             `json:"version"`
	Mode           string          `json:"mode"`
	Outcome        string          `json:"outcome"`
	Code           string          `json:"code"`
	PlanSHA256     string          `json:"plan_sha256"`
	Nonce          string          `json:"attempt_nonce"`
	WorkerID       string          `json:"worker_id"`
	AccessVersion  int64           `json:"access_version"`
	BeforeACL      Observation     `json:"before_acl"`
	AfterACL       Observation     `json:"after_acl"`
	CatalogSHA256  string          `json:"catalog_sha256"`
	FunctionSHA256 string          `json:"function_sha256"`
	DataState      json.RawMessage `json:"data_state,omitempty"`
	StateSHA256    string          `json:"state_sha256,omitempty"`
}

// A Session owns an otherwise unused pool with exactly one nonexpiring physical
// connection. No caller may share it. The exclusive session lease spans the
// AccessStore transaction and all postcommit observations; reconnects refuse.
type Session struct {
	DB       *sql.DB
	PID      int
	Owner    string
	Database string
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToUpper(s), strings.ToUpper(sub))
}
func (s *Session) result(p Plan) Result {
	return Result{Version: 1, PlanSHA256: planHash(p), Nonce: p.Nonce, WorkerID: p.WorkerID, BeforeACL: p.BeforeACL}
}
func (s *Session) failedResult(p Plan, e error) Result {
	r := s.result(p)
	r.Outcome = "refused"
	r.Code = "precondition_or_transaction_refused"
	var stage *stageRefusal
	if errors.As(e, &stage) {
		r.Code = stage.code
	}
	if errors.Is(e, errCommitUnknown) {
		r.Outcome = "unknown-held"
		r.Code = "commit_unknown"
	}
	return r
}
func (s *Session) guard(ctx context.Context, q queryer) error {
	var ok bool
	e := q.QueryRowContext(ctx, `SELECT pg_backend_pid()=$1 AND session_user=current_user AND current_user=$2 AND current_database()=$3
 AND EXISTS(SELECT 1 FROM pg_roles r JOIN pg_database d ON d.datdba=r.oid WHERE r.rolname=current_user AND r.rolsuper AND d.datname=current_database())
 AND EXISTS(SELECT 1 FROM pg_locks WHERE pid=pg_backend_pid() AND locktype='advisory' AND granted AND mode='ExclusiveLock' AND classid=($4::bigint>>32)::oid AND objid=($4::bigint&4294967295)::oid AND objsubid=1)
 AND NOT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND backend_type='client backend')`, s.PID, s.Owner, s.Database, protectedschema.LockID).Scan(&ok)
	if e != nil || !ok {
		return refuseStage("session_pid_lease_owner_or_clients_refused")
	}
	return nil
}
func (s *Session) schema(ctx context.Context, p Plan) error {
	if e := s.guard(ctx, s.DB); e != nil {
		return e
	}
	var generation int64
	var release, shape string
	var protected, pending bool
	if s.DB.QueryRowContext(ctx, `SELECT generation,release,catalog_sha256 FROM public.whatomate_schema_seal ORDER BY generation DESC LIMIT 1`).Scan(&generation, &release, &shape) != nil || generation != p.Generation || release != p.Release || shape != p.CatalogSHA256 {
		return refuseStage("latest_seal_refused")
	}
	if s.DB.QueryRowContext(ctx, `SELECT protected FROM public.whatomate_protection_state WHERE id=1`).Scan(&protected) != nil || !protected {
		return refuseStage("protected_state_refused")
	}
	if s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM public.whatomate_schema_attempt a WHERE NOT EXISTS(SELECT 1 FROM public.whatomate_schema_seal s WHERE s.release=a.release AND s.catalog_sha256=a.catalog_sha256))`).Scan(&pending) != nil || pending {
		return refuseStage("pending_installation_refused")
	}
	m, e := protectedschema.Catalog(ctx, s.DB, "public")
	if e != nil || m.SHA256 != p.CatalogSHA256 {
		return refuseStage("actual_catalog_refused")
	}
	return s.guard(ctx, s.DB)
}
func functionState(ctx context.Context, q queryer, p Plan, committed bool) error {
	var definition, body, owner, configuration string
	var valid bool
	e := q.QueryRowContext(ctx, `SELECT pg_get_functiondef(p.oid),p.prosrc,pg_get_userbyid(p.proowner),COALESCE(array_to_string(p.proconfig,','),''),p.prosecdef AND NOT p.proleakproof AND p.prokind='f' AND p.prorettype='boolean'::regtype FROM pg_proc p WHERE p.oid=to_regprocedure($1)`, signature).Scan(&definition, &body, &owner, &configuration, &valid)
	_, dbowner, _, fnowner := p.names()
	want := dbowner
	if committed {
		want = fnowner
	}
	sourceBody := strings.Split(processingauth.DDL, "$er_owner$")[1]
	if e != nil || !valid || owner != want || configuration != "search_path=pg_catalog" || body != sourceBody || digest([]byte(definition)) != p.FunctionSHA256 {
		return denied
	}
	return nil
}
func absent(ctx context.Context, q queryer, p Plan) error {
	db, _, worker, fnowner := p.names()
	var ok bool
	e := q.QueryRowContext(ctx, `SELECT
 NOT EXISTS(SELECT 1 FROM public.users WHERE id=$1 OR email=$2)
 AND NOT EXISTS(SELECT 1 FROM public.custom_roles WHERE id=$3)
 AND NOT EXISTS(SELECT 1 FROM public.user_organizations WHERE id=$4 OR user_id=$1)
 AND NOT EXISTS(SELECT 1 FROM public.api_keys WHERE user_id=$1)
 AND NOT EXISTS(SELECT 1 FROM public.addon_api_identity_links WHERE user_id=$1::text)
 AND NOT EXISTS(SELECT 1 FROM public.extension_security_mfa WHERE user_id=$1::text)
 AND NOT EXISTS(SELECT 1 FROM public.addon_api_access_versions WHERE user_id=$1::text)
 AND NOT EXISTS(SELECT 1 FROM public.addon_api_grants WHERE user_id=$1::text)
 AND NOT EXISTS(SELECT 1 FROM public.addon_api_access_audit WHERE user_id=$1::text OR metadata->>'attempt_nonce'=$5)
 AND NOT EXISTS(SELECT 1 FROM public.er_processing_authorization_bindings WHERE database_name=$6 OR worker_role=$7 OR worker_user_id=$1)
 AND NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$8)`, p.WorkerID, p.Email, p.RoleID, p.MembershipID, p.Nonce, db, worker, fnowner).Scan(&ok)
	if e != nil || !ok {
		return denied
	}
	return nil
}
func workerRole(ctx context.Context, q queryer, p Plan) error {
	_, _, worker, _ := p.names()
	var ok bool
	e := q.QueryRowContext(ctx, `SELECT rolcanlogin AND NOT(rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls)
 AND NOT EXISTS(SELECT 1 FROM pg_auth_members WHERE member=r.oid OR roleid=r.oid)
 AND NOT EXISTS(SELECT 1 FROM pg_namespace WHERE nspowner=r.oid)
 AND NOT EXISTS(SELECT 1 FROM pg_class WHERE relowner=r.oid)
 AND NOT EXISTS(SELECT 1 FROM pg_proc WHERE proowner=r.oid)
 AND NOT EXISTS(SELECT 1 FROM pg_database WHERE datdba=r.oid)
 FROM pg_roles r WHERE rolname=$1`, worker).Scan(&ok)
	if e != nil || !ok {
		return denied
	}
	return nil
}
func operator(ctx context.Context, q queryer, p Plan) error {
	var ok bool
	e := q.QueryRowContext(ctx, `SELECT true FROM public.organizations o JOIN public.users u ON u.organization_id=o.id WHERE o.id=$1 AND u.id=$2 AND o.deleted_at IS NULL AND u.deleted_at IS NULL AND u.is_active=true FOR SHARE OF o,u`, p.OrganizationID, p.OperatorID).Scan(&ok)
	if e != nil || !ok {
		return denied
	}
	return nil
}
func metadataFor(p Plan) []byte {
	b, _ := json.Marshal(map[string]any{"kind": "knowledge-worker", "profile": p.Profile, "attempt_nonce": p.Nonce, "approval_reference": p.ApprovalReference, "bootstrap_plan_sha256": planHash(p), "source": "extensions-worker-bootstrap-v1", "new_role_id": p.RoleID, "permissions": []string{"knowledge:job"}})
	return b
}
func grantsFor(p Plan) []api.Grant {
	g := make([]api.Grant, 0, len(p.Audiences))
	for _, a := range p.Audiences {
		g = append(g, api.Grant{Permission: "knowledge:job", Audience: a})
	}
	return g
}
func (s *Session) committed(ctx context.Context, p Plan) error {
	if s.guard(ctx, s.DB) != nil || functionState(ctx, s.DB, p, true) != nil {
		return denied
	}
	db, _, worker, fnowner := p.names()
	var ok bool
	e := s.DB.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM public.users WHERE id=$1 AND organization_id=$2 AND email=$3 AND auth_stamp=$4 AND password_hash='' AND full_name=$5 AND role_id=$6 AND is_active AND NOT is_super_admin AND sso_provider='' AND sso_provider_id='' AND sso_binding='' AND deleted_at IS NULL)
 AND EXISTS(SELECT 1 FROM public.custom_roles WHERE id=$6 AND organization_id=$2 AND name=$7 AND description=$8 AND NOT is_system AND NOT is_default AND deleted_at IS NULL)
 AND (SELECT count(*) FROM public.user_organizations WHERE user_id=$1)=1
 AND EXISTS(SELECT 1 FROM public.user_organizations WHERE id=$9 AND user_id=$1 AND organization_id=$2 AND role_id=$6 AND is_default AND deleted_at IS NULL)
 AND (SELECT count(*) FROM public.role_permissions WHERE custom_role_id=$6)=1
 AND EXISTS(SELECT 1 FROM public.role_permissions rp JOIN public.permissions p ON p.id=rp.permission_id WHERE rp.custom_role_id=$6 AND p.id=$10 AND p.resource='knowledge' AND p.action='job' AND p.deleted_at IS NULL)
 AND NOT EXISTS(SELECT 1 FROM public.users WHERE role_id=$6 AND id<>$1)
 AND NOT EXISTS(SELECT 1 FROM public.api_keys WHERE user_id=$1)
 AND NOT EXISTS(SELECT 1 FROM public.addon_api_identity_links WHERE user_id=$1::text)
 AND NOT EXISTS(SELECT 1 FROM public.extension_security_mfa WHERE user_id=$1::text)
 AND (SELECT count(*) FROM public.addon_api_access_versions WHERE user_id=$1::text)=1
 AND EXISTS(SELECT 1 FROM public.addon_api_access_versions WHERE organization_id=$2::text AND user_id=$1::text AND version=1)
 AND NOT EXISTS(SELECT 1 FROM public.addon_api_grants WHERE user_id=$1::text AND organization_id<>$2::text)
 AND (SELECT count(*) FROM public.er_processing_authorization_bindings WHERE worker_user_id=$1 OR database_name=$11 OR worker_role=$12)=1
 AND EXISTS(SELECT 1 FROM public.er_processing_authorization_bindings WHERE database_name=$11 AND worker_role=$12 AND organization_id=$2 AND worker_user_id=$1 AND profile_id=$13 AND audiences=$14::text[])
 AND EXISTS(SELECT 1 FROM pg_roles r WHERE rolname=$15 AND NOT(rolcanlogin OR rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls) AND NOT EXISTS(SELECT 1 FROM pg_auth_members WHERE roleid=r.oid OR member=r.oid))
 AND NOT has_schema_privilege($15,'public','CREATE')
 AND NOT has_table_privilege($12,'public.er_processing_authorization_bindings','INSERT,UPDATE,DELETE,TRUNCATE')
 AND NOT has_table_privilege($15,'public.er_processing_authorization_bindings','INSERT,UPDATE,DELETE,TRUNCATE')`, p.WorkerID, p.OrganizationID, p.Email, p.AuthStamp, p.DisplayName, p.RoleID, "Service/"+p.WorkerID, "Explicit knowledge-worker profile: "+p.Profile, p.MembershipID, p.PermissionID, db, worker, p.Profile, p.Audiences, fnowner).Scan(&ok)
	if e != nil || !ok {
		return denied
	}
	want, _ := json.Marshal(grantsFor(p))
	var raw string
	var count int
	// Compare the full set, not just the requested job audience subset.
	e = s.DB.QueryRowContext(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_object('permission',permission,'unit_id',unit_id,'audience',audience) ORDER BY audience),'[]'::jsonb)::text FROM public.addon_api_grants WHERE user_id=$1`, p.WorkerID).Scan(&raw)
	if e != nil {
		return denied
	}
	var observed []api.Grant
	if json.Unmarshal([]byte(raw), &observed) != nil {
		return denied
	}
	expected := grantsFor(p)
	sort.Slice(expected, func(i, j int) bool { return expected[i].Audience < expected[j].Audience })
	b, _ := json.Marshal(expected)
	a, _ := json.Marshal(observed)
	if string(a) != string(b) {
		return denied
	}
	e = s.DB.QueryRowContext(ctx, `SELECT count(*) FROM public.addon_api_access_audit WHERE user_id=$1`, p.WorkerID).Scan(&count)
	if e != nil || count != 1 {
		return denied
	}
	e = s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM public.addon_api_access_audit WHERE organization_id=$1 AND user_id=$2 AND operator_id=$3 AND action='replace' AND reason=$4 AND previous_version=0 AND new_version=1 AND before_grants='[]'::jsonb AND after_grants=$5::jsonb AND metadata=$6::jsonb)`, p.OrganizationID, p.WorkerID, p.OperatorID, p.Reason, string(want), string(metadataFor(p))).Scan(&ok)
	if e != nil || !ok {
		return denied
	}
	return s.guard(ctx, s.DB)
}
func (s *Session) state(ctx context.Context, p Plan) (json.RawMessage, error) {
	if s.guard(ctx, s.DB) != nil {
		return nil, denied
	}
	var raw string
	e := s.DB.QueryRowContext(ctx, `SELECT jsonb_build_object(
 'worker_id',u.id,'organization_id',u.organization_id,'email',u.email,'display_name',u.full_name,'custom_role_id',u.role_id,'auth_stamp',u.auth_stamp,'is_active',u.is_active,'is_super_admin',u.is_super_admin,
 'credentialless',u.password_hash='' AND u.sso_provider='' AND u.sso_provider_id='' AND u.sso_binding='',
 'membership_id',m.id,'permission_id',$3::text,'version',v.version,'audit_id',a.id,'audit_metadata',a.metadata,'binding_profile',b.profile_id,'binding_audiences',b.audiences,
 'grants',(SELECT COALESCE(jsonb_agg(jsonb_build_object('permission',g.permission,'unit_id',g.unit_id,'audience',g.audience) ORDER BY g.audience COLLATE "C"),'[]'::jsonb) FROM public.addon_api_grants g WHERE g.organization_id=$2::text AND g.user_id=$1::text))::text
 FROM public.users u JOIN public.user_organizations m ON m.user_id=u.id JOIN public.addon_api_access_versions v ON v.user_id=u.id::text AND v.organization_id=u.organization_id::text
 JOIN public.addon_api_access_audit a ON a.user_id=v.user_id AND a.organization_id=v.organization_id AND a.new_version=v.version
 JOIN public.er_processing_authorization_bindings b ON b.worker_user_id=u.id
 WHERE u.id=$1::uuid AND u.organization_id=$2::uuid`, p.WorkerID, p.OrganizationID, p.PermissionID).Scan(&raw)
	if e != nil {
		return nil, denied
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return nil, denied
	}
	b, e := json.Marshal(value)
	return b, e
}
func (s *Session) complete(ctx context.Context, p Plan, mode string, actual Observation) Result {
	state, e := s.state(ctx, p)
	if e != nil {
		r := s.failedResult(p, errCommitUnknown)
		r.Mode = mode
		return r
	}
	r := s.result(p)
	r.Mode = mode
	r.Outcome = "committed-observed"
	r.AccessVersion = 1
	r.AfterACL = actual
	r.CatalogSHA256 = p.CatalogSHA256
	r.FunctionSHA256 = p.FunctionSHA256
	r.DataState = state
	r.StateSHA256 = digest(state)
	return r
}
func writeHost(ctx context.Context, tx *sql.Tx, p Plan) error {
	statements := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO public.custom_roles(id,organization_id,name,description,is_system,is_default,created_at,updated_at) VALUES($1,$2,$3,$4,false,false,clock_timestamp(),clock_timestamp())`, []any{p.RoleID, p.OrganizationID, "Service/" + p.WorkerID, "Explicit knowledge-worker profile: " + p.Profile}},
		{`INSERT INTO public.users(id,organization_id,email,auth_stamp,password_hash,full_name,role_id,is_active,is_super_admin,sso_provider,sso_provider_id,sso_binding,created_at,updated_at) VALUES($1,$2,$3,$4,'',$5,$6,true,false,'','','',clock_timestamp(),clock_timestamp())`, []any{p.WorkerID, p.OrganizationID, p.Email, p.AuthStamp, p.DisplayName, p.RoleID}},
		{`INSERT INTO public.user_organizations(id,user_id,organization_id,role_id,is_default,created_at,updated_at) VALUES($1,$2,$3,$4,true,clock_timestamp(),clock_timestamp())`, []any{p.MembershipID, p.WorkerID, p.OrganizationID, p.RoleID}},
		{`INSERT INTO public.permissions(id,resource,action,description,created_at,updated_at) VALUES($1,'knowledge','job','Explicit knowledge-worker permission',clock_timestamp(),clock_timestamp()) ON CONFLICT(resource,action) DO NOTHING`, []any{p.PermissionID}},
	}
	for _, st := range statements {
		if _, e := tx.ExecContext(ctx, st.sql, st.args...); e != nil {
			return denied
		}
	}
	var id string
	if tx.QueryRowContext(ctx, `SELECT id::text FROM public.permissions WHERE resource='knowledge' AND action='job' AND deleted_at IS NULL FOR SHARE`).Scan(&id) != nil || id != p.PermissionID {
		return denied
	}
	if _, e := tx.ExecContext(ctx, `INSERT INTO public.role_permissions(custom_role_id,permission_id) VALUES($1,$2)`, p.RoleID, p.PermissionID); e != nil {
		return denied
	}
	for _, st := range provisioningSQL(p) {
		if _, e := tx.ExecContext(ctx, st); e != nil {
			return denied
		}
	}
	db, _, worker, _ := p.names()
	if _, e := tx.ExecContext(ctx, `INSERT INTO public.er_processing_authorization_bindings(database_name,worker_role,organization_id,worker_user_id,profile_id,audiences) VALUES($1,$2,$3,$4,$5,$6::text[])`, db, worker, p.OrganizationID, p.WorkerID, p.Profile, p.Audiences); e != nil {
		return denied
	}
	return nil
}
func (s *Session) Execute(ctx context.Context, p Plan, mode string) Result {
	fail := func(e error) Result { r := s.failedResult(p, e); r.Mode = mode; return r }
	if p.Validate() != nil || s.DB == nil {
		return fail(refuseStage("plan_or_session_refused"))
	}
	if e := s.schema(ctx, p); e != nil {
		return fail(e)
	}
	if e := workerRole(ctx, s.DB, p); e != nil {
		return fail(refuseStage("processing_role_refused"))
	}
	actual, e := captureACL(ctx, s.DB)
	if e != nil {
		return fail(refuseStage("acl_observation_refused"))
	}
	if mode == "inspect" {
		var definition string
		if s.DB.QueryRowContext(ctx, `SELECT pg_get_functiondef(to_regprocedure($1))`, signature).Scan(&definition) != nil {
			return fail(refuseStage("fixed_function_observation_refused"))
		}
		if e := s.guard(ctx, s.DB); e != nil {
			return fail(e)
		}
		r := s.result(p)
		r.Mode = mode
		r.Outcome = "observed"
		r.AfterACL = actual
		r.CatalogSHA256 = p.CatalogSHA256
		r.FunctionSHA256 = digest([]byte(definition))
		return r
	}
	expected, e := expectedACL(p.BeforeACL, p)
	if e != nil {
		return fail(e)
	}
	if mode == "reconcile" {
		if observationsEqual(actual, p.BeforeACL) && absent(ctx, s.DB, p) == nil && functionState(ctx, s.DB, p, false) == nil {
			r := s.result(p)
			r.Mode = mode
			r.Outcome = "not-committed-observed"
			r.AfterACL = actual
			return r
		}
		if observationsEqual(actual, expected) && s.committed(ctx, p) == nil {
			return s.complete(ctx, p, mode, actual)
		}
		r := fail(denied)
		r.Outcome = "unresolved-held"
		return r
	}
	if mode != "preview" && mode != "apply" || !observationsEqual(actual, p.BeforeACL) {
		return fail(denied)
	}
	change := api.AccessReplacement{OrganizationID: p.OrganizationID, UserID: p.WorkerID, OperatorID: p.OperatorID, Action: "replace", Reason: p.Reason, ExpectedVersion: 0, Grants: grantsFor(p), Metadata: metadataFor(p)}
	_, e = (api.AccessStore{DB: s.DB}).ReplaceVersioned(ctx, change, mode == "apply", func(ctx context.Context, tx *sql.Tx, write bool) error {
		if s.guard(ctx, tx) != nil || operator(ctx, tx, p) != nil || absent(ctx, tx, p) != nil || functionState(ctx, tx, p, false) != nil {
			return denied
		}
		now, e := captureACL(ctx, tx)
		if e != nil || !observationsEqual(now, p.BeforeACL) {
			return denied
		}
		if !write {
			return nil
		}
		if e := writeHost(ctx, tx, p); e != nil {
			return e
		}
		now, e = captureACL(ctx, tx)
		if e != nil || !observationsEqual(now, expected) || functionState(ctx, tx, p, true) != nil {
			return denied
		}
		return s.guard(ctx, tx)
	})
	if e != nil {
		return fail(e)
	}
	if mode == "preview" {
		r := s.result(p)
		r.Mode = mode
		r.Outcome = "preview"
		r.AfterACL = expected
		return r
	}
	// A commit succeeded but incomplete/lost readback remains unknown and held.
	if s.schema(ctx, p) != nil || s.committed(ctx, p) != nil {
		return fail(errCommitUnknown)
	}
	actual, e = captureACL(ctx, s.DB)
	if e != nil || !observationsEqual(actual, expected) || s.guard(ctx, s.DB) != nil {
		return fail(errCommitUnknown)
	}
	return s.complete(ctx, p, mode, actual)
}
