//go:build linux

package main

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shridarpatil/whatomate/internal/protectedschema"
)

func TestProcessingSchemaRejectsUnlinkedBuild(t *testing.T) {
	_, worker, c := ownerFixture(t)
	release, catalog := protectedschema.ReleaseIdentity, protectedschema.ExpectedCatalogSHA256
	t.Cleanup(func() { protectedschema.ReleaseIdentity, protectedschema.ExpectedCatalogSHA256 = release, catalog })
	protectedschema.ReleaseIdentity, protectedschema.ExpectedCatalogSHA256 = "", ""
	if databaseAuthorization(context.Background(), worker, c) == nil {
		t.Fatal("unlinked production controller accepted an otherwise authorized worker database")
	}
}

func processingSchemaFixture(t *testing.T) (*sql.DB, *sql.DB, configuration, *pgx.ConnConfig) {
	t.Helper()
	owner, legacy, c := ownerFixture(t)
	legacy.Close()
	_, err := owner.Exec(`CREATE TABLE whatomate_schema_seal(generation bigint PRIMARY KEY,release text,catalog_sha256 text);
CREATE TABLE whatomate_schema_attempt(release text,catalog_sha256 text);
CREATE TABLE extension_schema(id text,checksum text);
CREATE TABLE extension_security_schema(id text,checksum text);
CREATE TABLE addon_cases_schema(version bigint);
INSERT INTO extension_schema VALUES('synthetic','source-checksum');
REVOKE ALL ON SCHEMA public FROM PUBLIC;
DO $$DECLARE f record;BEGIN FOR f IN SELECT oid::regprocedure AS signature FROM pg_proc WHERE pronamespace='pg_catalog'::regnamespace AND proname IN('pg_read_file','pg_read_binary_file','pg_ls_dir','pg_stat_file','pg_write_file','pg_file_write','pg_file_rename','pg_file_unlink','lo_import','lo_export') LOOP EXECUTE format('REVOKE EXECUTE ON FUNCTION %s FROM PUBLIC',f.signature); END LOOP;END$$;`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = owner.Exec("REVOKE ALL ON DATABASE " + c.DatabaseName + " FROM PUBLIC; GRANT CONNECT ON DATABASE " + c.DatabaseName + " TO " + c.DatabaseRole + "; GRANT SELECT ON whatomate_schema_seal,whatomate_schema_attempt,whatomate_protection_state,extension_schema,extension_security_schema,addon_cases_schema TO " + c.DatabaseRole)
	if err != nil {
		t.Fatal(err)
	}
	shape, err := protectedschema.Catalog(context.Background(), owner, "public")
	if err != nil {
		t.Fatal(err)
	}
	protectedschema.ExpectedCatalogSHA256 = shape.SHA256
	if _, err = owner.Exec("INSERT INTO whatomate_schema_seal VALUES(1,$1,$2)", protectedschema.ReleaseIdentity, shape.SHA256); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(os.Getenv("EXTENSION_PROCESSING_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + c.DatabaseName
	u.User = url.UserPassword(c.DatabaseRole, "synthetic-processing-role-only")
	pc, err := pgx.ParseConfig(u.String())
	if err != nil {
		t.Fatal(err)
	}
	// Plaintext is confined to this disposable direct-constructor fixture. The
	// production private-DSN path independently requires verified TLS.
	pc.RuntimeParams = map[string]string{"search_path": "public"}
	pool, _, err := openProcessingDatabase(c, pc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close() })
	return owner, pool, c, pc
}

func TestProcessingSchemaMetadataOnlyRoleAndLostLeaseCancelsChildBeforeReturn(t *testing.T) {
	owner, pool, c, pc := processingSchemaFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	schema := processingSchemaConfig(c)
	schema.Database.Host = pc.Host
	schema.Database.Port = int(pc.Port)
	schema.Database.Password = pc.Password
	lease, err := protectedschema.Acquire(ctx, pool, schema)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	err = owner.QueryRow(`SELECT l.pid FROM pg_locks l JOIN pg_stat_activity a ON a.pid=l.pid WHERE l.locktype='advisory' AND l.mode='ShareLock' AND a.usename=$1 LIMIT 1`, c.DatabaseRole).Scan(&pid)
	if err != nil {
		t.Fatal(err)
	}
	// A sibling physical queue connection also fences the installer while the
	// controller cancels and waits for the sandbox cleanup callback.
	sibling, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer sibling.Close()
	started, cancelled, finishCleanup := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-finishCleanup:
		default:
			close(finishCleanup)
		}
	}()
	done := make(chan error, 1)
	go func() {
		done <- withSchemaLease(ctx, lease, 5*time.Millisecond, func(job context.Context) error {
			close(started)
			<-job.Done()
			close(cancelled)
			<-finishCleanup
			return nil
		})
	}()
	<-started
	if _, err = owner.Exec("SELECT pg_terminate_backend($1)", pid); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-ctx.Done():
		t.Fatal("lost session did not cancel owned child")
	}
	select {
	case <-done:
		t.Fatal("returned before child cleanup finished")
	default:
	}
	conn, err := owner.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var acquired bool
	if err = conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", protectedschema.LockID).Scan(&acquired); err != nil || acquired {
		t.Fatal("surviving queue connection did not fence installer", acquired, err)
	}
	close(finishCleanup)
	if err = <-done; err == nil {
		t.Fatal("lease loss reported success")
	}
	sibling.Close()
	pool.Close()
	if err = conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", protectedschema.LockID).Scan(&acquired); err != nil || !acquired {
		t.Fatal("completed controller retained fence", err)
	}
	defer conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", protectedschema.LockID)
	if next, _, err := openProcessingDatabase(c, pc); err == nil {
		next.Close()
		t.Fatal("reconnect admitted while installer held lease")
	}
}

func TestProcessingSchemaRefusesTamperedSealPendingMarkerCatalogAndMetadataAccess(t *testing.T) {
	for _, kind := range []string{"seal", "pending", "marker", "catalog", "metadata"} {
		t.Run(kind, func(t *testing.T) {
			owner, pool, c, pc := processingSchemaFixture(t)
			pool.Close()
			var err error
			switch kind {
			case "seal":
				_, err = owner.Exec("INSERT INTO whatomate_schema_seal VALUES(2,'foreign','foreign')")
			case "pending":
				_, err = owner.Exec("INSERT INTO whatomate_schema_attempt VALUES('pending','pending')")
			case "marker":
				_, err = owner.Exec("UPDATE whatomate_protection_state SET protected=false")
			case "catalog":
				_, err = owner.Exec("UPDATE extension_schema SET checksum='tampered'")
			case "metadata":
				_, err = owner.Exec("REVOKE SELECT ON extension_schema FROM " + c.DatabaseRole)
			}
			if err != nil {
				t.Fatal(err)
			}
			next, schema, err := openProcessingDatabase(c, pc)
			if err == nil {
				defer next.Close()
				lease, e := protectedschema.Acquire(context.Background(), next, schema)
				if e == nil {
					lease.Close()
					t.Fatal("unsafe schema admitted", kind)
				}
			}
		})
	}
}

func TestProcessingSchemaParsedConfigurationIsCopiedAcrossReconnect(t *testing.T) {
	_, pool, _, pc := processingSchemaFixture(t)
	pc.Password = "wrong"
	pc.Host = "invalid.invalid"
	pc.RuntimeParams["search_path"] = "wrong"
	pool.SetMaxIdleConns(0)
	var schema string
	if err := pool.QueryRow("SELECT current_schema()").Scan(&schema); err != nil || schema != "public" {
		t.Fatal("caller mutation changed guarded pool", schema, err)
	}
}
