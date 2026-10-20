package handlers

import (
	"context"
	"fmt"
	"net/http"
	"time"

	addonapi "github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/directory"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	ext "github.com/shridarpatil/whatomate/pkg/extensions/v1"
)

// Only trusted compiled factories may contribute a domain API. Configuration
// selects existing modules, never an executable URL, binary or arbitrary handler.
func (a *App) initializeCompiledModules() error {
	if a.ExtensionSecurity != nil {
		found := false
		for _, d := range a.ExtensionRegistry.Manifest() {
			if d.ID == "security" {
				found = true
			}
		}
		if !found {
			if e := a.ExtensionRegistry.Register(ext.Descriptor{ID: "security", Version: "0.1.0", APIVersion: ext.APIVersion, Capabilities: []string{"mfa", "scoped-emergency"}}); e != nil {
				return e
			}
		}
	}
	enabled, e := orderedExtensionModules(extensionIDs(a.Config.Extensions.Modules))
	if e != nil {
		return e
	}
	if len(enabled) == 0 {
		return nil
	}
	if a.ExtensionSecurity == nil || a.DB == nil {
		return fmt.Errorf("compiled protected modules require configured MFA and database")
	}
	db, e := a.DB.DB()
	if e != nil {
		return e
	}
	ctx := context.Background()
	access := &addonapi.AccessStore{DB: db}
	if e = access.Migrate(ctx); e != nil {
		return e
	}
	a.ExtensionAccess = access
	a.ExtensionAPI = map[string]http.Handler{}
	for _, id := range enabled {
		if _, duplicate := a.ExtensionAPI[id]; duplicate {
			return fmt.Errorf("duplicate compiled module")
		}
		switch id {
		case "governance":
			if e = a.initializeGovernance(ctx, db); e != nil {
				return e
			}
		case "reports":
			if e = a.initializeReports(ctx, db); e != nil {
				return e
			}
		case "conversation":
			if e = a.initializeConversation(ctx, db); e != nil {
				return e
			}
		case "knowledge":
			if e = a.initializeKnowledge(ctx, db); e != nil {
				return e
			}
		case "ai":
			if e = a.initializeAI(ctx, db); e != nil {
				return e
			}
		case "cases":
			if e = (&cases.SQLRepository{DB: db}).Migrate(ctx); e != nil {
				return e
			}
			a.ExtensionAPI[id] = cases.NewHTTPHandler(db, cases.HTTPConfig{Enabled: true, ResolveCandidate: a.resolveExtensionCaseCandidate, ResolveIntake: a.resolveExtensionIntake, AuthorizeEmergency: a.authorizeEmergencyCaseRead})
			if e = a.ExtensionRegistry.Register(ext.Descriptor{ID: id, Version: "0.1.0", APIVersion: ext.APIVersion, Dependencies: []string{"security", "directory"}, Capabilities: []string{"protected-cases", "human-classification", "immutable-history"}, Routes: []ext.Route{{Method: "GET", Path: "/api/extensions/cases/", Permission: "cases:read.own"}, {Method: "POST", Path: "/api/extensions/cases/", Permission: "cases:submit.own"}}}); e != nil {
				return e
			}
			if e = a.ExtensionGuard.Register("cases", func(ctx context.Context, actor ext.Actor, p ext.Protection, action string) error {
				if !actor.MFA || action != "read" {
					return ext.ErrDenied
				}
				scoped, err := access.Load(ctx, addonapi.Actor{OrganizationID: actor.OrganizationID, UserID: actor.UserID, SessionID: actor.SessionID, MFA: actor.MFA, Permissions: actor.Permissions})
				if err != nil {
					return ext.ErrDenied
				}
				_, err = (cases.Service{Repository: &cases.SQLRepository{DB: db}}).Read(ctx, cases.PrincipalForActor(scoped), p.ResourceID())
				return err
			}); e != nil {
				return e
			}
		case "directory":
			if a.Config.Extensions.RosterFreshnessSeconds <= 0 {
				return fmt.Errorf("directory freshness policy required")
			}
			if e = directory.MigrateHTTP(ctx, db); e != nil {
				return e
			}
			a.ExtensionAPI[id] = directory.NewHTTPHandler(db, directory.HTTPConfig{Freshness: time.Duration(a.Config.Extensions.RosterFreshnessSeconds) * time.Second, Clock: time.Now})
			if e = a.ExtensionRegistry.Register(ext.Descriptor{ID: id, Version: "0.1.0", APIVersion: ext.APIVersion, Dependencies: []string{"security"}, Capabilities: []string{"roster-review", "current-eligibility"}, Routes: []ext.Route{{Method: "GET", Path: "/api/extensions/directory/status", Permission: "directory:read"}, {Method: "POST", Path: "/api/extensions/directory/preview", Permission: "directory:import"}, {Method: "POST", Path: "/api/extensions/directory/approve", Permission: "directory:approve"}}}); e != nil {
				return e
			}
		default:
			return fmt.Errorf("module not compiled: %s", id)
		}
	}
	return nil
}

// Assignment and participant inputs name an identity; its grants and active
// membership are resolved server-side. No candidate requires an active login just
// to receive an assignment, and this lookup does not impersonate their session.
func (a *App) resolveExtensionCaseCandidate(ctx context.Context, initiator addonapi.Actor, targetID, unitID string) (cases.Principal, error) {
	target, e := uuid.Parse(targetID)
	if e != nil || unitID == "" || a.DB == nil || a.ExtensionAccess == nil {
		return cases.Principal{}, cases.ErrDenied
	}
	org, e := uuid.Parse(initiator.OrganizationID)
	if e != nil {
		return cases.Principal{}, cases.ErrDenied
	}
	var user models.User
	if e = a.DB.WithContext(ctx).Where("id = ? AND organization_id = ? AND is_active = ?", target, org, true).First(&user).Error; e != nil {
		return cases.Principal{}, cases.ErrDenied
	}
	actor := addonapi.Actor{OrganizationID: org.String(), UserID: target.String()}
	if user.RoleID != nil {
		var role models.CustomRole
		if e = a.DB.WithContext(ctx).Preload("Permissions").Where("id = ? AND organization_id = ?", *user.RoleID, org).First(&role).Error; e != nil {
			return cases.Principal{}, cases.ErrDenied
		}
		for _, p := range role.Permissions {
			actor.Permissions = append(actor.Permissions, p.Resource+":"+p.Action)
		}
	}
	scoped, e := a.ExtensionAccess.Load(ctx, actor)
	if e != nil {
		return cases.Principal{}, cases.ErrDenied
	}
	return cases.PrincipalForActor(scoped), nil
}
