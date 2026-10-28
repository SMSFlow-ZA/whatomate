package handlers

import (
	"context"
	"database/sql"

	"github.com/SMSFlow-ZA/whatomate-addons/ai"
	"github.com/SMSFlow-ZA/whatomate-addons/carrier"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/conversation"
	"github.com/SMSFlow-ZA/whatomate-addons/directory"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"github.com/SMSFlow-ZA/whatomate-addons/notifications"
	"github.com/SMSFlow-ZA/whatomate-addons/reports"
	ext "github.com/shridarpatil/whatomate/pkg/extensions/v1"
)

func (a *App) initializeGovernance(ctx context.Context, db *sql.DB) error {
	if e := a.extensionSchemaMigration(ctx, db, func() error { return notifications.Migrate(ctx, db) }); e != nil {
		return e
	}
	if e := a.extensionSchemaMigration(ctx, db, func() error { return carrier.Migrate(ctx, db) }); e != nil {
		return e
	}
	if e := a.extensionSchemaMigration(ctx, db, func() error { return governance.MigrateHTTP(ctx, db) }); e != nil {
		return e
	}
	a.ExtensionAPI["governance"] = governance.NewHTTPHandler(db, governance.HTTPConfig{Enabled: true, RefreshActor: a.refreshExtensionActor, SubjectAliases: a.ExtensionAccess.EmployeeAliases, Adapters: []governance.PhysicalAdapter{notifications.PrivacyAdapter{}, cases.PrivacyAdapter{}, knowledge.PrivacyAdapter{}, directory.PrivacyAdapter{}, conversation.PrivacyAdapter{}, reports.PrivacyAdapter{}, ai.PrivacyAdapter{}, carrier.PrivacyAdapter{}}})
	return a.ExtensionRegistry.Register(ext.Descriptor{ID: "governance", Version: "0.1.0", APIVersion: ext.APIVersion, Dependencies: []string{"security", "directory", "cases", "knowledge", "conversation", "reports", "ai"}, Capabilities: []string{"owner-inventory", "physical-lifecycle", "legal-holds", "offline-restore-journal"}})
}
