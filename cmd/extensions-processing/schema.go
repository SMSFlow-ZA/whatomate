//go:build linux

package main

import (
	"context"
	"database/sql"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/protectedschema"
)

func processingSchemaConfig(c configuration) *config.Config {
	result := &config.Config{}
	result.Extensions.ProtectedMode = true
	result.Database.SchemaManagedExternally = true
	result.Database.SchemaRelease = protectedschema.ReleaseIdentity
	result.Database.SchemaSHA256 = protectedschema.ExpectedCatalogSHA256
	result.Database.Name, result.Database.User = c.DatabaseName, c.DatabaseRole
	result.Database.MaxOpenConns, result.Database.MaxIdleConns = 3, 3
	return result
}

func openProcessingDatabase(c configuration, pc *pgx.ConnConfig) (*sql.DB, *config.Config, error) {
	schema := processingSchemaConfig(c)
	schema.Database.Host, schema.Database.Port, schema.Database.Password = pc.Host, int(pc.Port), pc.Password
	schema.Database.SSLMode = "verify-full"
	guarded, err := protectedschema.OpenRuntimeConfig(schema, pc)
	if err != nil {
		return nil, nil, denied
	}
	db, err := guarded.DB()
	if err != nil {
		return nil, nil, denied
	}
	return db, schema, nil
}

type schemaLease interface {
	Check(context.Context) error
	Close() error
}

// The worker callback is synchronous: cancellation cannot release the lease
// while the supervisor is still stopping/verifying its owned Docker child.
func withSchemaLease(ctx context.Context, lease schemaLease, interval time.Duration, work func(context.Context) error) error {
	defer lease.Close()
	check := func() error {
		bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return lease.Check(bounded)
	}
	if check() != nil {
		return denied
	}
	job, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	finished, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-finished:
				return
			case <-job.Done():
				return
			case <-ticker.C:
				if check() != nil {
					cancel(denied)
					return
				}
			}
		}
	}()
	err := work(job)
	close(finished)
	<-stopped
	if context.Cause(job) != nil || check() != nil {
		return denied
	}
	return err
}
