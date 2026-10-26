package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/url"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/shridarpatil/whatomate/internal/protectedschema"
)

type Connection struct {
	Version        int    `json:"version"`
	Host           string `json:"host"`
	Port           int    `json:"port"`
	Database       string `json:"database"`
	User           string `json:"user"`
	SSLMode        string `json:"sslmode"`
	CAFile         string `json:"ca_file"`
	PasswordFile   string `json:"password_file"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

func openOwner(ctx context.Context, c Connection, p Plan) (*Session, error) {
	database, owner, _, _ := p.names()
	if c.Version != 1 || !exactText(c.Host, 253) || c.Port < 1 || c.Port > 65535 || c.Database != database || c.User != owner || c.SSLMode != "verify-full" || c.TimeoutSeconds < 1 || c.TimeoutSeconds > 120 {
		return nil, denied
	}
	for _, key := range []string{"PGSERVICE", "PGSERVICEFILE", "PGPASSFILE", "PGSSLCERT", "PGSSLKEY", "PGSSLROOTCERT", "PGTARGETSESSIONATTRS"} {
		if _, ok := os.LookupEnv(key); ok {
			return nil, denied
		}
	}
	ca, e := privateBytes(c.CAFile, 1024*1024)
	if e != nil {
		return nil, denied
	}
	password, e := privateBytes(c.PasswordFile, 4096)
	if e != nil || len(password) == 0 {
		return nil, denied
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, denied
	}
	// All security-relevant fields override PG* defaults. No service/passfile or
	// fallback TLS mode is accepted, and no password enters argv or diagnostics.
	u := url.URL{Scheme: "postgres", Host: net.JoinHostPort(c.Host, strconv.Itoa(c.Port)), Path: "/" + c.Database, User: url.UserPassword(c.User, string(password))}
	u.RawQuery = "sslmode=verify-full&connect_timeout=10"
	cfg, e := pgx.ParseConfig(u.String())
	if e != nil {
		return nil, denied
	}
	cfg.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: c.Host}
	cfg.Fallbacks = nil
	cfg.ConnectTimeout = 10 * time.Second
	cfg.RuntimeParams = map[string]string{"search_path": "public", "application_name": "extensions-worker-bootstrap", "statement_timeout": strconv.Itoa(c.TimeoutSeconds * 1000), "lock_timeout": "3000"}
	var first atomic.Int64
	db := stdlib.OpenDB(*cfg, stdlib.OptionAfterConnect(func(ctx context.Context, conn *pgx.Conn) error {
		// A connection replacement after any loss cannot inherit the session lease.
		pid := int64(conn.PgConn().PID())
		if !first.CompareAndSwap(0, pid) {
			return denied
		}
		return nil
	}))
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)
	fail := func() (*Session, error) { db.Close(); return nil, denied }
	var pid int
	var held, ssl bool
	if db.QueryRowContext(ctx, `SELECT pg_backend_pid(),pg_try_advisory_lock($1)`, protectedschema.LockID).Scan(&pid, &held) != nil || !held {
		return fail()
	}
	if db.QueryRowContext(ctx, `SELECT ssl FROM pg_stat_ssl WHERE pid=pg_backend_pid()`).Scan(&ssl) != nil || !ssl {
		return fail()
	}
	s := &Session{DB: db, PID: pid, Owner: owner, Database: database}
	if s.guard(ctx, db) != nil {
		return fail()
	}
	return s, nil
}
