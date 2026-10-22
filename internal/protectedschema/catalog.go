// Package protectedschema separates privileged release installation from a
// SELECT/DML-only protected runtime. Neither configuration nor database rows can
// invent a compiled release identity or expected schema shape.
package protectedschema

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"

	"github.com/shridarpatil/whatomate/internal/config"
)

// Set only by the reviewed artifact build. Ordinary-host behavior does not use
// these variables. An unset production contract deliberately refuses startup.
var ReleaseIdentity string
var ExpectedCatalogSHA256 string
var ErrDenied = errors.New("protected external schema contract unavailable or mismatched")
var releasePattern = regexp.MustCompile(`^[0-9a-f]{40}:[0-9a-f]{40}(:[0-9a-f]{64})?$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var namespacePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

const LockID int64 = 8331022202

func Validate(c *config.Config) error {
	if c == nil || !c.Database.SchemaManagedExternally || !c.Extensions.ProtectedMode || c.Extensions.SyntheticDevelopment || !releasePattern.MatchString(ReleaseIdentity) || !digestPattern.MatchString(ExpectedCatalogSHA256) || c.Database.SchemaRelease != ReleaseIdentity || c.Database.SchemaSHA256 != ExpectedCatalogSHA256 {
		return ErrDenied
	}
	return nil
}

type Manifest struct {
	Format  string   `json:"format"`
	Release string   `json:"release"`
	SHA256  string   `json:"sha256"`
	Items   []string `json:"items"`
}

// Canonical metadata excludes row contents, OIDs, ownership names, ACLs and
// sequence counters. Runtime privilege/ownership checks are separate. All
// structural authorization semantics, including disabled triggers, are hashed.
const catalogSQL = `
WITH objects AS (
 SELECT c.oid,c.relname,c.relkind,c.relpersistence,c.relrowsecurity,c.relforcerowsecurity FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname=$1 AND NOT EXISTS(SELECT 1 FROM pg_depend d WHERE d.classid='pg_class'::regclass AND d.objid=c.oid AND d.deptype='e')
), routines AS (
 SELECT p.oid,p.proname FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=$1 AND NOT EXISTS(SELECT 1 FROM pg_depend d WHERE d.classid='pg_proc'::regclass AND d.objid=p.oid AND d.deptype='e')
)
SELECT json_build_array('relation',relname,relkind,relpersistence,relrowsecurity,relforcerowsecurity)::text FROM objects WHERE relkind IN('r','p','v','m','f','S','c')
UNION ALL SELECT json_build_array('column',o.relname,a.attnum,a.attname,format_type(a.atttypid,a.atttypmod),a.attnotnull,a.attidentity,a.attgenerated,pg_get_expr(d.adbin,d.adrelid))::text FROM objects o JOIN pg_attribute a ON a.attrelid=o.oid LEFT JOIN pg_attrdef d ON d.adrelid=o.oid AND d.adnum=a.attnum WHERE a.attnum>0 AND NOT a.attisdropped
UNION ALL SELECT json_build_array('constraint',c.conname,c.contype,c.condeferrable,c.condeferred,c.convalidated,pg_get_constraintdef(c.oid,false),COALESCE(r.relname,''))::text FROM pg_constraint c JOIN pg_namespace n ON n.oid=c.connamespace LEFT JOIN pg_class r ON r.oid=c.conrelid WHERE n.nspname=$1 AND NOT EXISTS(SELECT 1 FROM pg_depend d WHERE d.classid='pg_constraint'::regclass AND d.objid=c.oid AND d.deptype='e')
UNION ALL SELECT json_build_array('index',o.relname,pg_get_indexdef(i.indexrelid,0,false),i.indisvalid,i.indisready,i.indislive)::text FROM objects o JOIN pg_index i ON i.indexrelid=o.oid
UNION ALL SELECT json_build_array('trigger',o.relname,t.tgname,t.tgenabled,pg_get_triggerdef(t.oid,false))::text FROM objects o JOIN pg_trigger t ON t.tgrelid=o.oid WHERE NOT t.tgisinternal
UNION ALL SELECT json_build_array('internal-trigger',o.relname,t.tgenabled,p.proname,c.conname,t.tgtype)::text FROM objects o JOIN pg_trigger t ON t.tgrelid=o.oid JOIN pg_proc p ON p.oid=t.tgfoid LEFT JOIN pg_constraint c ON c.oid=t.tgconstraint WHERE t.tgisinternal
UNION ALL SELECT json_build_array('routine',r.proname,pg_get_functiondef(r.oid))::text FROM routines r
UNION ALL SELECT json_build_array('sequence',o.relname,s.seqtypid::regtype::text,s.seqstart,s.seqincrement,s.seqmax,s.seqmin,s.seqcache,s.seqcycle)::text FROM objects o JOIN pg_sequence s ON s.seqrelid=o.oid
UNION ALL SELECT json_build_array('view',o.relname,pg_get_viewdef(o.oid,false))::text FROM objects o WHERE o.relkind IN('v','m')
UNION ALL SELECT json_build_array('policy',o.relname,p.polname,p.polcmd,p.polpermissive,pg_get_expr(p.polqual,p.polrelid),pg_get_expr(p.polwithcheck,p.polrelid),ARRAY(SELECT rolname FROM pg_roles WHERE oid=ANY(p.polroles) ORDER BY rolname))::text FROM objects o JOIN pg_policy p ON p.polrelid=o.oid
UNION ALL SELECT json_build_array('type',t.typname,t.typtype,format_type(t.typbasetype,t.typtypmod),t.typnotnull,t.typdefault,ARRAY(SELECT e.enumlabel FROM pg_enum e WHERE e.enumtypid=t.oid ORDER BY e.enumsortorder))::text FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace WHERE n.nspname=$1 AND t.typtype IN('d','e','r','m') AND NOT EXISTS(SELECT 1 FROM pg_depend d WHERE d.classid='pg_type'::regclass AND d.objid=t.oid AND d.deptype='e')
UNION ALL SELECT json_build_array('extension',extname,extversion)::text FROM pg_extension
UNION ALL SELECT json_build_array('postgres-major',current_setting('server_version_num')::int/10000)::text`

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func capture(ctx context.Context, tx queryer, namespace string) (Manifest, error) {
	if !namespacePattern.MatchString(namespace) {
		return Manifest{}, ErrDenied
	}
	rows, e := tx.QueryContext(ctx, catalogSQL, namespace)
	if e != nil {
		return Manifest{}, e
	}
	defer rows.Close()
	items := []string{}
	for rows.Next() {
		var item string
		if e = rows.Scan(&item); e != nil {
			return Manifest{}, e
		}
		if len(item) > 1<<20 || len(items) >= 20000 {
			return Manifest{}, ErrDenied
		}
		item = strings.ReplaceAll(item, namespace+".", "public.")
		item = strings.ReplaceAll(item, `\"`+namespace+`\".`, `\"public\".`)
		items = append(items, item)
	}
	if e = rows.Err(); e != nil {
		return Manifest{}, e
	}
	rows.Close()
	// Owner migration metadata is part of the source contract, not employee or
	// application data. Omit installation timestamps and retain exact versions /
	// source checksums so deleting or changing them cannot bypass verification.
	for _, spec := range []struct{ table, columns string }{{"extension_schema", "id,checksum"}, {"extension_security_schema", "id,checksum"}, {"addon_cases_schema", "version"}} {
		qualified := namespace + "." + spec.table
		var exists bool
		// queryer includes only read-only tx instances here.
		r, e := tx.QueryContext(ctx, "SELECT to_regclass($1) IS NOT NULL", qualified)
		if e != nil {
			return Manifest{}, e
		}
		if r.Next() {
			e = r.Scan(&exists)
		}
		r.Close()
		if e != nil {
			return Manifest{}, e
		}
		if !exists {
			continue
		}
		r, e = tx.QueryContext(ctx, "SELECT json_build_array('migration-data','"+spec.table+"',"+spec.columns+")::text FROM "+qualified)
		if e != nil {
			return Manifest{}, e
		}
		for r.Next() {
			var item string
			if e = r.Scan(&item); e != nil {
				r.Close()
				return Manifest{}, e
			}
			items = append(items, item)
		}
		e = r.Err()
		r.Close()
		if e != nil {
			return Manifest{}, e
		}
	}
	sort.Strings(items)
	b, e := json.Marshal(items)
	if e != nil {
		return Manifest{}, e
	}
	sum := sha256.Sum256(b)
	return Manifest{Format: "whatomate-protected-catalog-v1", Release: ReleaseIdentity, SHA256: hex.EncodeToString(sum[:]), Items: items}, nil
}

func Catalog(ctx context.Context, db *sql.DB, namespace string) (Manifest, error) {
	tx, e := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if e != nil {
		return Manifest{}, e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, "SET LOCAL search_path=pg_catalog"); e != nil {
		return Manifest{}, e
	}
	m, e := capture(ctx, tx, namespace)
	if e != nil {
		return Manifest{}, e
	}
	if e = tx.Commit(); e != nil {
		return Manifest{}, e
	}
	return m, nil
}
