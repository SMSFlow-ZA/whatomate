package handlers

import (
	"context"
	"database/sql"
	addonapi "github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/cases"
	"github.com/SMSFlow-ZA/whatomate-addons/delivery"
	"github.com/SMSFlow-ZA/whatomate-addons/notifications"
	"github.com/google/uuid"
	ext "github.com/shridarpatil/whatomate/pkg/extensions/v1"
	"strings"
	"time"
)

// A notification uses current background membership, never a fabricated session.
func extensionNotificationPrincipal(member addonapi.Actor) (cases.Principal, error) {
	org, err := uuid.Parse(member.OrganizationID)
	if err != nil || org == uuid.Nil || org.String() != member.OrganizationID {
		return cases.Principal{}, cases.ErrDenied
	}
	user, err := uuid.Parse(member.UserID)
	if err != nil || user == uuid.Nil || user.String() != member.UserID || !member.Has(string(cases.WorkAssigned)) {
		return cases.Principal{}, cases.ErrDenied
	}
	p := cases.Principal{ID: member.UserID, Aliases: append([]string(nil), member.Aliases...)}
	for _, grant := range member.Grants {
		if grant.Permission != string(cases.WorkAssigned) {
			continue
		}
		if grant.UnitID == "" || len(grant.UnitID) > 256 || strings.TrimSpace(grant.UnitID) != grant.UnitID || strings.ContainsAny(grant.UnitID, "*\x00\r\n") {
			return cases.Principal{}, cases.ErrDenied
		}
		p.Grants = append(p.Grants, cases.Grant{OrganizationID: member.OrganizationID, UnitID: grant.UnitID, Permission: cases.WorkAssigned})
	}
	if len(p.Grants) == 0 {
		return cases.Principal{}, cases.ErrDenied
	}
	return p, nil
}

func (a *App) handlerNotificationsEnabled() bool {
	if a.Config == nil {
		return false
	}
	for _, id := range extensionIDs(a.Config.Extensions.Modules) {
		if id == "notifications" {
			return true
		}
	}
	return false
}

func (a *App) initializeNotificationModule(ctx context.Context, db *sql.DB) error {
	if err := a.extensionSchemaMigration(ctx, db, func() error { return notifications.Migrate(ctx, db) }); err != nil {
		return err
	}
	return a.ExtensionRegistry.Register(ext.Descriptor{ID: "notifications", Version: "0.1.0", APIVersion: ext.APIVersion, Dependencies: []string{"security", "directory", "cases", "conversation"}, Capabilities: []string{"protected-handler-notices", "current-recipient-authorization", "durable-event-deduplication"}})
}

func (a *App) initializeHandlerNotifications(ctx context.Context, db *sql.DB) error {
	a.ExtensionNotifications = nil
	if !a.handlerNotificationsEnabled() {
		if len(a.Config.Extensions.HandlerNotificationUnits) > 0 {
			return errCarrierComposition
		}
		return nil
	}
	c := a.Config.Extensions
	if a.ExtensionCarrier == nil || a.ExtensionAccess == nil || db == nil || len(c.HandlerNotificationUnits) == 0 || len(c.HandlerNotificationUnits) > 100 {
		return errCarrierComposition
	}
	seen := map[string]bool{}
	for _, unit := range c.HandlerNotificationUnits {
		if unit == "" || len(unit) > 256 || strings.TrimSpace(unit) != unit || strings.ContainsAny(unit, "*\x00\r\n") || seen[unit] {
			return errCarrierComposition
		}
		seen[unit] = true
	}
	services := map[string]*notifications.Service{}
	for _, binding := range a.ExtensionCarrier.Bindings {
		if services[binding.Tenant] != nil {
			return errCarrierComposition
		}
		s := &notifications.Service{DB: db, Tenant: binding.Tenant, Account: binding.Account, Units: append([]string(nil), c.HandlerNotificationUnits...), Clock: time.Now, Freshness: time.Duration(c.RosterFreshnessSeconds) * time.Second, Resolve: a.resolveHandlerNotificationIdentity}
		if c.CarrierDispatchEnabled {
			if err := s.Activate(ctx); err != nil {
				return err
			}
		}
		services[binding.Tenant] = s
	}
	a.ExtensionNotifications = services
	a.ExtensionCarrier.AuthorizeHandler = func(ctx context.Context, m delivery.Message) error {
		service := a.ExtensionNotifications[m.Tenant]
		if service == nil || !extensionCarrierRecipientAllowed(a.Config.Extensions, m.Sender) {
			return notifications.ErrDenied
		}
		return service.Authorize(ctx, m)
	}
	return nil
}

func (a *App) resolveHandlerNotificationIdentity(ctx context.Context, org, user string) (notifications.Identity, error) {
	member, err := a.extensionMember(ctx, org, user)
	if err != nil {
		return notifications.Identity{}, err
	}
	db, err := a.DB.DB()
	if err != nil {
		return notifications.Identity{}, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return notifications.Identity{}, err
	}
	defer tx.Rollback()
	aliases, err := a.ExtensionAccess.IdentityLinksTx(ctx, tx, org, user)
	if err != nil {
		return notifications.Identity{}, err
	}
	member.Aliases = aliases
	p, err := extensionNotificationPrincipal(member)
	if err != nil {
		return notifications.Identity{}, notifications.ErrDenied
	}
	if err = tx.Commit(); err != nil {
		return notifications.Identity{}, err
	}
	return notifications.Identity{OrganizationID: org, UserID: user, Active: true, Principal: p, EmployeeIDs: append([]string(nil), aliases...)}, nil
}

func (a *App) authorizeProtectedDelivery(ctx context.Context, m delivery.Message) error {
	switch m.Purpose {
	case "":
		if m.RecipientUserID != "" || m.EventVersion != 0 || a.ExtensionConversation == nil {
			return errCarrierComposition
		}
		return a.ExtensionConversation.AuthorizeDelivery(ctx, m)
	case delivery.HandlerEscalation:
		if a.ExtensionCarrier == nil || a.ExtensionCarrier.AuthorizeHandler == nil || m.Channel == "sms" {
			return errCarrierComposition
		}
		return a.ExtensionCarrier.AuthorizeHandler(ctx, m)
	default:
		return errCarrierComposition
	}
}
