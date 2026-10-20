package handlers

import (
	"context"
	"database/sql"
	"errors"
	"github.com/SMSFlow-ZA/whatomate-addons/carrier"
	"github.com/SMSFlow-ZA/whatomate-addons/delivery"
	"github.com/SMSFlow-ZA/whatomate-addons/directory"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/protectedcarrier"
	ext "github.com/shridarpatil/whatomate/pkg/extensions/v1"
	"time"
)

var errCarrierComposition = errors.New("protected carrier composition denied")

func (a *App) initializeProtectedCarrier(ctx context.Context, db *sql.DB) error {
	if a.Config == nil {
		return errCarrierComposition
	}
	a.ExtensionCarrier = nil
	c := a.Config.Extensions
	if !c.CarrierEnabled {
		if c.CarrierDispatchEnabled || len(c.CarrierBindings) > 0 || len(c.CarrierSMSBindings) > 0 {
			return errCarrierComposition
		}
		return nil
	}
	if db == nil || a.DB == nil || a.ExtensionGuard == nil || a.ExtensionConversation == nil || len(c.CarrierBindings) == 0 || c.RosterFreshnessSeconds <= 0 {
		return errCarrierComposition
	}
	gate := false
	for _, id := range extensionIDs(c.RequiredInbound) {
		if id == "directory" {
			gate = true
		}
	}
	if !gate {
		return errCarrierComposition
	}
	bindings := []carrier.Binding{}
	phones := map[string]bool{}
	accounts := map[string]bool{}
	organizations := map[string]bool{}
	for _, b := range c.CarrierBindings {
		org, e := uuid.Parse(b.OrganizationID)
		if e != nil {
			return errCarrierComposition
		}
		account, e := uuid.Parse(b.AccountID)
		if e != nil || b.AppSecret == "" || b.PhoneID == "" || phones[b.PhoneID] || accounts[account.String()] || organizations[org.String()] {
			return errCarrierComposition
		}
		var count int64
		if e = a.DB.WithContext(ctx).Model(&models.WhatsAppAccount{}).Where("id=? AND organization_id=? AND phone_id=? AND status=?", account, org, b.PhoneID, "active").Count(&count).Error; e != nil || count != 1 {
			return errCarrierComposition
		}
		phones[b.PhoneID] = true
		accounts[account.String()] = true
		organizations[org.String()] = true
		bindings = append(bindings, carrier.Binding{Tenant: org.String(), Account: account.String(), PhoneID: b.PhoneID, AppSecret: b.AppSecret})
	}
	if c.SyntheticDevelopment && c.CarrierDispatchEnabled && len(c.CarrierAllowedRecipients) == 0 {
		return errCarrierComposition
	}
	for _, number := range c.CarrierAllowedRecipients {
		canonical, e := carrier.CanonicalNumber(number)
		if e != nil || canonical != number {
			return errCarrierComposition
		}
	}
	if e := carrier.Migrate(ctx, db); e != nil {
		return e
	}
	service := &carrier.Service{DB: db, Bindings: bindings, Conversation: a.ExtensionConversation, Clock: time.Now}
	a.ExtensionCarrier = service
	if e := a.initializeProtectedSMS(ctx); e != nil {
		return e
	}
	if c.CarrierDispatchEnabled {
		service.SendObserved = (protectedcarrier.MetaSender{Resolve: a.protectedCarrierCredentials}).Send
	}
	return a.ExtensionGuard.RegisterInbound("directory", func(ctx context.Context, in ext.Inbound) error {
		allowed := false
		for _, b := range bindings {
			if in.AccountID == b.Account {
				if in.OrganizationID != b.Tenant {
					return ext.ErrDenied
				}
				allowed = true
			}
		}
		if !allowed {
			// This policy scopes roster eligibility to operator-bound accounts.
			// Existing ordinary accounts still require a current exact DB tuple.
			var count int64
			if err := a.DB.WithContext(ctx).Model(&models.WhatsAppAccount{}).Where("id=? AND organization_id=? AND status=?", in.AccountID, in.OrganizationID, "active").Count(&count).Error; err != nil || count != 1 {
				return ext.ErrDenied
			}
			return nil
		}
		if in.Kind != "text" {
			return ext.ErrDenied
		}
		number, e := carrier.CanonicalNumber(in.Sender)
		if e != nil {
			return ext.ErrDenied
		}
		store, e := directory.NewPostgresStore(db, in.OrganizationID, "carrier-ingress")
		if e != nil {
			return ext.ErrDenied
		}
		snapshot, e := store.Load(ctx)
		if e != nil {
			return ext.ErrDenied
		}
		_, e = directory.Match(snapshot, number, time.Now(), time.Duration(c.RosterFreshnessSeconds)*time.Second)
		if e != nil {
			return ext.ErrDenied
		}
		return nil
	})
}

// Current account, authorization, roster and signed service window are checked
// immediately before submission. No cache or generic sender logs are involved.
func (a *App) protectedCarrierCredentials(ctx context.Context, m delivery.Message) (protectedcarrier.Credentials, error) {
	if a.Config == nil || !a.Config.Extensions.CarrierEnabled || !a.Config.Extensions.CarrierDispatchEnabled || a.ExtensionCarrier == nil || a.ExtensionConversation == nil || a.DB == nil {
		return protectedcarrier.Credentials{}, errCarrierComposition
	}
	if a.Config.Extensions.SyntheticDevelopment {
		allowed := false
		for _, v := range a.Config.Extensions.CarrierAllowedRecipients {
			if v == m.Sender {
				allowed = true
			}
		}
		if !allowed {
			return protectedcarrier.Credentials{}, errCarrierComposition
		}
	}
	if e := a.ExtensionConversation.AuthorizeDelivery(ctx, m); e != nil {
		return protectedcarrier.Credentials{}, errCarrierComposition
	}
	if e := a.ExtensionCarrier.AuthorizeWindow(ctx, m); e != nil {
		return protectedcarrier.Credentials{}, errCarrierComposition
	}
	var phoneID string
	for _, b := range a.ExtensionCarrier.Bindings {
		if b.Tenant == m.Tenant && b.Account == m.Account {
			phoneID = b.PhoneID
		}
	}
	if phoneID == "" {
		return protectedcarrier.Credentials{}, errCarrierComposition
	}
	var account models.WhatsAppAccount
	if e := a.DB.WithContext(ctx).Where("id=? AND organization_id=? AND phone_id=? AND status=?", m.Account, m.Tenant, phoneID, "active").First(&account).Error; e != nil {
		return protectedcarrier.Credentials{}, errCarrierComposition
	}
	account.DecryptSecrets(a.Config.App.EncryptionKey)
	return protectedcarrier.Credentials{PhoneID: account.PhoneID, APIVersion: account.APIVersion, AccessToken: account.AccessToken}, nil
}
