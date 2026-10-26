package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
)

type ACLGrant struct {
	Grantor   string `json:"grantor"`
	Grantee   string `json:"grantee"`
	Privilege string `json:"privilege"`
	Grantable bool   `json:"grantable"`
}
type ACLRecord struct {
	Kind     string     `json:"kind"`
	Object   string     `json:"object"`
	Owner    string     `json:"owner"`
	Explicit bool       `json:"explicit_acl"`
	Grants   []ACLGrant `json:"grants"`
	Detail   string     `json:"detail"`
}
type Observation struct {
	Version int         `json:"version"`
	Records []ACLRecord `json:"records"`
}

func recordKey(r ACLRecord) string { return r.Kind + "\x00" + r.Object }
func grantKey(g ACLGrant) string   { b, _ := json.Marshal(g); return string(b) }
func (o Observation) Validate() error {
	if o.Version != 1 || len(o.Records) == 0 || len(o.Records) > 100000 {
		return denied
	}
	previous := ""
	for _, r := range o.Records {
		k := recordKey(r)
		if r.Kind == "" || r.Object == "" || k <= previous || len(r.Detail) > 65536 || len(r.Grants) > 10000 || r.Grants == nil {
			return denied
		}
		previous = k
		gprev := ""
		for _, g := range r.Grants {
			k := grantKey(g)
			if g.Grantor == "" || g.Grantee == "" || g.Privilege == "" || k <= gprev {
				return denied
			}
			gprev = k
		}
	}
	return nil
}
func normalize(o *Observation) {
	for i := range o.Records {
		r := &o.Records[i]
		if r.Grants == nil {
			r.Grants = []ACLGrant{}
		}
		sort.Slice(r.Grants, func(i, j int) bool { return grantKey(r.Grants[i]) < grantKey(r.Grants[j]) })
		if r.Detail != "" {
			var d any
			if json.Unmarshal([]byte(r.Detail), &d) == nil {
				b, _ := json.Marshal(d)
				r.Detail = string(b)
			}
		}
	}
	sort.Slice(o.Records, func(i, j int) bool { return recordKey(o.Records[i]) < recordKey(o.Records[j]) })
}
func observationHash(o Observation) string    { b, _ := json.Marshal(o); return digest(b) }
func observationsEqual(a, b Observation) bool { return observationHash(a) == observationHash(b) }

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// A public, generic ACL projection. It is independent of deployment/provisioning
// code and contains no passwords. Explicit ACL nullness is retained; privileges
// are normalized instead of accepting a caller's opaque expected-after hash.
const ACLObservationSQL = `WITH objects AS (
 SELECT 'namespace' kind,nspname::text object,pg_get_userbyid(nspowner) owner,nspacl acl,'' detail FROM pg_namespace WHERE nspname='public'
 UNION ALL SELECT 'database',datname::text,pg_get_userbyid(datdba),datacl,'' FROM pg_database WHERE datname=current_database()
 UNION ALL SELECT 'relation',n.nspname||'.'||c.relname,pg_get_userbyid(c.relowner),c.relacl,'' FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public'
 UNION ALL SELECT 'column',n.nspname||'.'||c.relname||'.'||a.attname,pg_get_userbyid(c.relowner),a.attacl,'' FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND a.attnum>0 AND NOT a.attisdropped
 UNION ALL SELECT 'routine',n.nspname||'.'||p.proname||'('||replace(oidvectortypes(p.proargtypes),', ', ',')||')',pg_get_userbyid(p.proowner),p.proacl,'' FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public' OR (n.nspname='pg_catalog' AND p.proacl IS NOT NULL)
 UNION ALL SELECT 'default',d.defaclrole::text||':'||d.defaclnamespace::text||':'||d.defaclobjtype::text,pg_get_userbyid(d.defaclrole),d.defaclacl,'' FROM pg_default_acl d
)
SELECT * FROM (SELECT kind,object,owner,acl IS NOT NULL,COALESCE((SELECT json_agg(json_build_object('grantor',pg_get_userbyid(x.grantor),'grantee',CASE WHEN x.grantee=0 THEN 'PUBLIC' ELSE pg_get_userbyid(x.grantee) END,'privilege',x.privilege_type,'grantable',x.is_grantable)) FROM aclexplode(acl) x),'[]'::json)::text,detail FROM objects
UNION ALL SELECT 'role',rolname::text,'',false,'[]',json_build_object('super',rolsuper,'inherit',rolinherit,'createrole',rolcreaterole,'createdb',rolcreatedb,'login',rolcanlogin,'replication',rolreplication,'bypassrls',rolbypassrls,'connlimit',rolconnlimit,'validuntil',rolvaliduntil::text,'config',rolconfig)::text FROM pg_roles
UNION ALL SELECT 'membership',roleid::text||':'||member::text||':'||grantor::text,'',false,'[]',json_build_object('role',pg_get_userbyid(roleid),'member',pg_get_userbyid(member),'grantor',pg_get_userbyid(grantor),'admin',admin_option,'inherit',inherit_option,'set',set_option)::text FROM pg_auth_members
UNION ALL SELECT 'setting',setdatabase::text||':'||setrole::text,'',false,'[]',to_json(setconfig)::text FROM pg_db_role_setting) observed ORDER BY kind COLLATE "C",object COLLATE "C"`

func captureACL(ctx context.Context, q queryer) (Observation, error) {
	rows, e := q.QueryContext(ctx, ACLObservationSQL)
	if e != nil {
		return Observation{}, denied
	}
	defer rows.Close()
	o := Observation{Version: 1}
	for rows.Next() {
		var r ACLRecord
		var grants string
		if rows.Scan(&r.Kind, &r.Object, &r.Owner, &r.Explicit, &grants, &r.Detail) != nil || json.Unmarshal([]byte(grants), &r.Grants) != nil {
			return Observation{}, denied
		}
		o.Records = append(o.Records, r)
	}
	if rows.Err() != nil {
		return Observation{}, denied
	}
	normalize(&o)
	return o, o.Validate()
}

type columnGrant struct {
	Table  string
	Select []string
	Update string
}

var metadata = []columnGrant{{"whatomate_schema_seal", []string{"generation", "release", "catalog_sha256"}, ""}, {"whatomate_schema_attempt", []string{"release", "catalog_sha256"}, ""}, {"whatomate_protection_state", []string{"id", "protected"}, ""}, {"extension_schema", []string{"id", "checksum"}, ""}, {"extension_security_schema", []string{"id", "checksum"}, ""}, {"addon_cases_schema", []string{"version"}, ""}}
var ownerColumns = []columnGrant{{"whatomate_protection_state", []string{"id", "protected"}, "id"}, {"organizations", []string{"id", "deleted_at"}, "id"}, {"users", []string{"id", "organization_id", "role_id", "is_active", "is_super_admin", "deleted_at"}, "id"}, {"custom_roles", []string{"id", "organization_id", "deleted_at", "is_system", "is_default"}, "id"}, {"role_permissions", []string{"custom_role_id", "permission_id"}, "custom_role_id"}, {"permissions", []string{"id", "resource", "action", "deleted_at"}, "id"}, {"addon_api_access_versions", []string{"organization_id", "user_id", "version"}, "organization_id"}, {"addon_api_access_audit", []string{"organization_id", "user_id", "new_version", "action", "metadata"}, ""}, {"addon_api_grants", []string{"organization_id", "user_id", "permission", "unit_id", "audience"}, "organization_id"}}

var runtimeJobSelect = []string{"id", "tenant", "actor", "input", "input_hash", "metadata", "metadata_hash", "status", "created_at", "version", "lease_until", "lease_token"}
var runtimeJobUpdate = []string{"status", "version", "failure", "updated_at", "worker", "lease_token", "lease_until", "document", "evidence", "receipt"}

func runtimeSQL(p Plan) []string {
	database, _, worker, _ := p.names()
	return []string{"GRANT CONNECT ON DATABASE " + database + " TO " + worker, "GRANT SELECT(" + strings.Join(runtimeJobSelect, ",") + ") ON public.addon_knowledge_jobs TO " + worker, "GRANT UPDATE(" + strings.Join(runtimeJobUpdate, ",") + ") ON public.addon_knowledge_jobs TO " + worker}
}

const signature = "public.er_processing_authorize_v1(uuid,uuid,text,text[])"

func metadataSQL(p Plan) []string {
	_, _, worker, _ := p.names()
	var out []string
	for _, g := range metadata {
		out = append(out, "GRANT SELECT("+strings.Join(g.Select, ",")+") ON public."+g.Table+" TO "+worker)
	}
	return out
}
func provisioningSQL(p Plan) []string {
	_, _, worker, owner := p.names()
	out := metadataSQL(p)
	out = append(out, "CREATE ROLE "+owner+" NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS", "GRANT USAGE,CREATE ON SCHEMA public TO "+owner)
	for _, g := range ownerColumns {
		out = append(out, "GRANT SELECT("+strings.Join(g.Select, ",")+") ON public."+g.Table+" TO "+owner)
		if g.Update != "" {
			out = append(out, "GRANT UPDATE("+g.Update+") ON public."+g.Table+" TO "+owner)
		}
	}
	out = append(out, "GRANT SELECT ON public.er_processing_authorization_bindings TO "+owner, "ALTER FUNCTION "+signature+" OWNER TO "+owner, "REVOKE CREATE ON SCHEMA public FROM "+owner, "REVOKE ALL ON FUNCTION "+signature+" FROM PUBLIC", "GRANT EXECUTE ON FUNCTION "+signature+" TO "+worker)
	return append(out, runtimeSQL(p)...)
}

func expectedACL(before Observation, p Plan) (Observation, error) {
	if before.Validate() != nil {
		return Observation{}, denied
	}
	raw, _ := json.Marshal(before)
	var after Observation
	_ = json.Unmarshal(raw, &after)
	database, dbowner, worker, owner := p.names()
	index := map[string]*ACLRecord{}
	for i := range after.Records {
		r := &after.Records[i]
		index[recordKey(*r)] = r
		if r.Kind == "role" && r.Object == owner {
			return Observation{}, denied
		}
	}
	add := func(kind, obj, grantee, priv string) error {
		r := index[kind+"\x00"+obj]
		if r == nil {
			return denied
		}
		grantor := dbowner
		if kind == "namespace" && obj == "public" {
			if r.Owner != dbowner && r.Owner != "pg_database_owner" {
				return denied
			}
			grantor = r.Owner
		} else if r.Owner != dbowner {
			return denied
		}
		for _, g := range r.Grants {
			if g.Grantee == grantee {
				return denied
			}
		}
		r.Grants = append(r.Grants, ACLGrant{grantor, grantee, priv, false})
		r.Explicit = true
		return nil
	}
	// Multiple reviewed privileges for one grantee on one column are permitted;
	// preexisting grants to either new owner or processing role remain a refusal.
	column := func(table, col, grantee, priv string) error {
		r := index["column\x00public."+table+"."+col]
		if r == nil || r.Owner != dbowner {
			return denied
		}
		for _, g := range r.Grants {
			if g.Grantee == grantee && g.Privilege == priv {
				return denied
			}
		}
		r.Grants = append(r.Grants, ACLGrant{dbowner, grantee, priv, false})
		r.Explicit = true
		return nil
	}
	for _, g := range metadata {
		for _, c := range g.Select {
			r := index["column\x00public."+g.Table+"."+c]
			if r == nil || r.Explicit {
				return Observation{}, denied
			}
			if column(g.Table, c, worker, "SELECT") != nil {
				return Observation{}, denied
			}
		}
	}
	for _, g := range ownerColumns {
		for _, c := range g.Select {
			if column(g.Table, c, owner, "SELECT") != nil {
				return Observation{}, denied
			}
		}
		if g.Update != "" {
			if column(g.Table, g.Update, owner, "UPDATE") != nil {
				return Observation{}, denied
			}
		}
	}
	if add("namespace", "public", owner, "USAGE") != nil || add("relation", "public.er_processing_authorization_bindings", owner, "SELECT") != nil {
		return Observation{}, denied
	}
	fn := index["routine\x00"+signature]
	if fn == nil || fn.Owner != dbowner || !fn.Explicit {
		return Observation{}, denied
	}
	for _, g := range fn.Grants {
		if g.Grantee != dbowner && g.Grantee != "PUBLIC" {
			return Observation{}, denied
		}
		if g.Grantor != dbowner || g.Privilege != "EXECUTE" {
			return Observation{}, denied
		}
	}
	fn.Owner = owner
	fn.Grants = []ACLGrant{{owner, owner, "EXECUTE", false}, {owner, worker, "EXECUTE", false}}
	fn.Explicit = true
	// GRANT materializes the built-in database ACL when datacl was NULL.
	// Observation retains explicit nullness, so reproduce those default entries.
	databaseACL := index["database\x00"+database]
	if databaseACL == nil {
		return Observation{}, denied
	}
	if !databaseACL.Explicit {
		databaseACL.Grants = []ACLGrant{{dbowner, dbowner, "CREATE", false}, {dbowner, dbowner, "CONNECT", false}, {dbowner, dbowner, "TEMPORARY", false}, {dbowner, "PUBLIC", "CONNECT", false}, {dbowner, "PUBLIC", "TEMPORARY", false}}
	}
	if add("database", database, worker, "CONNECT") != nil {
		return Observation{}, denied
	}
	for _, c := range runtimeJobSelect {
		if column("addon_knowledge_jobs", c, worker, "SELECT") != nil {
			return Observation{}, denied
		}
	}
	for _, c := range runtimeJobUpdate {
		if column("addon_knowledge_jobs", c, worker, "UPDATE") != nil {
			return Observation{}, denied
		}
	}
	detail := `{"bypassrls":false,"config":null,"connlimit":-1,"createdb":false,"createrole":false,"inherit":true,"login":false,"replication":false,"super":false,"validuntil":null}`
	after.Records = append(after.Records, ACLRecord{Kind: "role", Object: owner, Grants: []ACLGrant{}, Detail: detail})
	normalize(&after)
	return after, after.Validate()
}
