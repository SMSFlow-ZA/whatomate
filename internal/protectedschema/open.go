package protectedschema

import (
	"context"
	"crypto/tls"
	"database/sql"
	"net"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/shridarpatil/whatomate/internal/config"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var guardedPools sync.Map

func copyRuntimeConfig(supplied *pgx.ConnConfig) *pgx.ConnConfig {
	cfg := supplied.Copy()
	copyTLS := func(value *tls.Config) *tls.Config {
		if value == nil {
			return nil
		}
		value = value.Clone()
		if value.RootCAs != nil {
			value.RootCAs = value.RootCAs.Clone()
		}
		if value.ClientCAs != nil {
			value.ClientCAs = value.ClientCAs.Clone()
		}
		return value
	}
	cfg.TLSConfig = copyTLS(cfg.TLSConfig)
	for _, fallback := range cfg.Fallbacks {
		fallback.TLSConfig = copyTLS(fallback.TLSConfig)
	}
	return cfg
}

// OpenRuntime is the only external-mode runtime constructor. Each physical
// connection, including reconnects after network failure, must hold the shared
// migration fence before the pool can hand it to any query or transaction.
// InitializeExtensions/Acquire reject pools lacking this construction evidence.
func OpenRuntime(c *config.Config) (*gorm.DB, error) {
	if Validate(c) != nil || c.Database.Host == "" || c.Database.Name == "" || c.Database.User == "" || c.Database.Password == "" || c.Database.Port < 1 || c.Database.Port > 65535 {
		return nil, ErrDenied
	}
	u := url.URL{Scheme: "postgres", User: url.UserPassword(c.Database.User, c.Database.Password), Host: net.JoinHostPort(c.Database.Host, strconv.Itoa(c.Database.Port)), Path: "/" + c.Database.Name}
	q := url.Values{"sslmode": []string{c.Database.SSLMode}, "search_path": []string{"public"}, "connect_timeout": []string{"10"}}
	u.RawQuery = q.Encode()
	cfg, e := pgx.ParseConfig(u.String())
	if e != nil {
		return nil, ErrDenied
	}
	return OpenRuntimeConfig(c, cfg)
}

// OpenRuntimeConfig retains explicitly parsed TLS roots and client settings for
// trusted controllers. It uses the same physical-connection fence as OpenRuntime.
// The caller's configuration is copied before any connection or callback uses it.
func OpenRuntimeConfig(c *config.Config, supplied *pgx.ConnConfig) (*gorm.DB, error) {
	if Validate(c) != nil || supplied == nil || supplied.Host != c.Database.Host || supplied.Port != uint16(c.Database.Port) || supplied.Database != c.Database.Name || supplied.User != c.Database.User || supplied.Password != c.Database.Password || c.Database.Host == "" || c.Database.Password == "" || c.Database.Port < 1 || c.Database.Port > 65535 {
		return nil, ErrDenied
	}
	cfg := copyRuntimeConfig(supplied)
	// Capture immutable values: later callers cannot alter a live pool's fence by
	// mutating the configuration struct or a request-scoped setting.
	release, shape := ReleaseIdentity, ExpectedCatalogSHA256
	databaseUser, databaseName := c.Database.User, c.Database.Name
	pool := stdlib.OpenDB(*cfg, stdlib.OptionAfterConnect(func(ctx context.Context, conn *pgx.Conn) error {
		var identity bool
		if e := conn.QueryRow(ctx, "SELECT session_user=current_user AND current_user=$1 AND current_database()=$2", databaseUser, databaseName).Scan(&identity); e != nil || !identity {
			return ErrDenied
		}
		var held bool
		if e := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock_shared($1)", LockID).Scan(&held); e != nil || !held {
			return ErrDenied
		}
		var actualRelease, actualShape string
		if e := conn.QueryRow(ctx, "SELECT release,catalog_sha256 FROM whatomate_schema_seal ORDER BY generation DESC LIMIT 1").Scan(&actualRelease, &actualShape); e != nil || actualRelease != release || actualShape != shape {
			return ErrDenied
		}
		var pending, protected bool
		if e := conn.QueryRow(ctx, pendingSQL).Scan(&pending); e != nil || pending {
			return ErrDenied
		}
		if e := conn.QueryRow(ctx, "SELECT protected FROM whatomate_protection_state WHERE id=1").Scan(&protected); e != nil || !protected {
			return ErrDenied
		}
		return nil
	}))
	maximum := c.Database.MaxOpenConns
	if maximum == 0 {
		maximum = 5
	}
	if maximum < 3 {
		pool.Close()
		return nil, ErrDenied
	}
	pool.SetMaxOpenConns(maximum)
	idle := c.Database.MaxIdleConns
	if idle == 0 {
		idle = maximum
	}
	pool.SetMaxIdleConns(idle)
	pool.SetConnMaxLifetime(time.Duration(c.Database.ConnMaxLifetime) * time.Second)
	db, e := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if e != nil {
		pool.Close()
		return nil, ErrDenied
	}
	guardedPools.Store(pool, struct{}{})
	return db, nil
}

func guarded(db *sql.DB) bool { _, ok := guardedPools.Load(db); return ok }
