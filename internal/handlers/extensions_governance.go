package handlers

import (
	"context"
	"database/sql"

	"github.com/SMSFlow-ZA/whatomate-addons/ai"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/conversation"
	"github.com/SMSFlow-ZA/whatomate-addons/directory"
	"github.com/SMSFlow-ZA/whatomate-addons/governance"
	"github.com/SMSFlow-ZA/whatomate-addons/knowledge"
	"github.com/SMSFlow-ZA/whatomate-addons/reports"
	ext "github.com/shridarpatil/whatomate/pkg/extensions/v1"
)

func (a *App) initializeGovernance(ctx context.Context, db *sql.DB) error {
	if e := governance.MigrateHTTP(ctx, db); e != nil {
		return e
	}
	a.ExtensionAPI["governance"] = governance.NewHTTPHandler(db, governance.HTTPConfig{Enabled: true, RefreshActor: a.refreshExtensionActor, SubjectAliases: a.ExtensionAccess.EmployeeAliases, Adapters: []governance.PhysicalAdapter{cases.PrivacyAdapter{}, knowledge.PrivacyAdapter{}, directory.PrivacyAdapter{}, conversation.PrivacyAdapter{}, reports.PrivacyAdapter{}, ai.PrivacyAdapter{}}})
	return a.ExtensionRegistry.Register(ext.Descriptor{ID: "governance", Version: "0.1.0", APIVersion: ext.APIVersion, Dependencies: []string{"security", "directory", "cases", "knowledge", "conversation", "reports", "ai"}, Capabilities: []string{"owner-inventory", "physical-lifecycle", "legal-holds", "offline-restore-journal"}})
}
