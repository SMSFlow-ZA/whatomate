package handlers

import (
	"context"
	"database/sql"
	"github.com/shridarpatil/whatomate/internal/protectedschema"
)

// Migration bypass is available only after independent compiled-release,
// catalog, protected-marker and SELECT/DML-only role verification succeeds.
func (a *App) extensionSchemaMigration(ctx context.Context, db *sql.DB, install func() error) error {
	if a.Config.Database.SchemaManagedExternally {
		return protectedschema.Verify(ctx, db, a.Config)
	}
	return install()
}
