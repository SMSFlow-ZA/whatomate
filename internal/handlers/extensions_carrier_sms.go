package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/SMSFlow-ZA/whatomate-addons/carrier"
	"github.com/SMSFlow-ZA/whatomate-addons/delivery"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/protectedcarrier"
	"github.com/shridarpatil/whatomate/pkg/channels"
	"strconv"
)

func (a *App) initializeProtectedSMS(ctx context.Context) error {
	if a.Config == nil || len(a.Config.Extensions.CarrierSMSBindings) == 0 {
		return nil
	}
	if a.ExtensionCarrier == nil || a.Channels == nil || a.Channels.Providers == nil {
		return errCarrierComposition
	}
	seen := map[string]bool{}
	for _, b := range a.Config.Extensions.CarrierSMSBindings {
		org, e := uuid.Parse(b.OrganizationID)
		if e != nil || seen[org.String()] {
			return errCarrierComposition
		}
		connection, e := uuid.Parse(b.ConnectionID)
		if e != nil {
			return errCarrierComposition
		}
		wa, e := uuid.Parse(b.WhatsAppAccountID)
		if e != nil {
			return errCarrierComposition
		}
		valid := false
		for _, w := range a.ExtensionCarrier.Bindings {
			if w.Tenant == org.String() && w.Account == wa.String() {
				valid = true
			}
		}
		if !valid {
			return errCarrierComposition
		}
		if _, _, e = a.protectedSMSRuntime(ctx, org.String(), connection.String(), false); e != nil {
			return e
		}
		seen[org.String()] = true
		a.ExtensionCarrier.SMSRoutes = append(a.ExtensionCarrier.SMSRoutes, carrier.SMSRoute{Tenant: org.String(), Account: connection.String(), WhatsAppAccount: wa.String()})
	}
	a.ExtensionCarrier.PollSMS = a.pollProtectedSMS
	if a.Config.Extensions.CarrierDispatchEnabled {
		a.ExtensionCarrier.SendSMSObserved = (protectedcarrier.SMSSender{Resolve: func(ctx context.Context, m delivery.Message) (channels.Provider, channels.Connection, error) {
			if a.Config.Extensions.SyntheticDevelopment {
				allowed := false
				for _, number := range a.Config.Extensions.CarrierAllowedRecipients {
					if number == m.Sender {
						allowed = true
					}
				}
				if !allowed {
					return nil, channels.Connection{}, errCarrierComposition
				}
			}
			if a.ExtensionConversation == nil || a.ExtensionConversation.AuthorizeDelivery(ctx, m) != nil {
				return nil, channels.Connection{}, errCarrierComposition
			}
			provider, connection, err := a.protectedSMSRuntime(ctx, m.Tenant, m.Account, true)
			if err != nil {
				return nil, channels.Connection{}, err
			}
			if err = a.ExtensionCarrier.BindSMSCorrelation(ctx, m, protectedcarrier.SMSClientID(m)); err != nil {
				return nil, channels.Connection{}, errCarrierComposition
			}
			return provider, connection, nil
		}}).Send
	}
	return nil
}

// Current validated connection credentials are resolved for each operation.
// StopDispatch blocks submissions while receipt polling remains available.
func (a *App) protectedSMSRuntime(ctx context.Context, tenant, account string, dispatch bool) (channels.Provider, channels.Connection, error) {
	deny := func() (channels.Provider, channels.Connection, error) {
		return nil, channels.Connection{}, errCarrierComposition
	}
	if a.Config == nil || !a.Config.Extensions.CarrierEnabled || a.Channels == nil || a.Channels.Providers == nil || a.DB == nil {
		return deny()
	}
	if dispatch && (!a.Config.Extensions.CarrierDispatchEnabled || a.Channels.StopDispatch) {
		return deny()
	}
	bound := false
	for _, b := range a.Config.Extensions.CarrierSMSBindings {
		org, e1 := uuid.Parse(b.OrganizationID)
		id, e2 := uuid.Parse(b.ConnectionID)
		if e1 == nil && e2 == nil && org.String() == tenant && id.String() == account {
			bound = true
		}
	}
	id, parseErr := uuid.Parse(account)
	if !bound || parseErr != nil || !a.Channels.IsConnectionProtected(id) {
		return deny()
	}
	var c models.ChannelConnection
	if e := a.DB.WithContext(ctx).Where("id=? AND organization_id=? AND enabled=?", account, tenant, true).First(&c).Error; e != nil || c.Provider != "smsflow" || c.ValidatedAt == nil || c.MaxSegments < 1 || !crypto.IsEncrypted(c.Credentials) || len(a.Config.App.EncryptionKey) < 32 {
		return deny()
	}
	if c.Capabilities["text"] != true || (c.Capabilities["delivery_reports"] != true && c.Capabilities["reconciliation"] != true) {
		return deny()
	}
	raw, e := crypto.Decrypt(c.Credentials, a.Config.App.EncryptionKey)
	if e != nil {
		return deny()
	}
	var creds channels.Credentials
	if json.Unmarshal([]byte(raw), &creds) != nil || len(creds) == 0 {
		return deny()
	}
	p, ok := a.Channels.Providers.Get(c.Provider)
	if !ok {
		return deny()
	}
	return p, channels.Connection{ID: c.ID.String(), Credentials: creds, Config: map[string]string{"max_segments": strconv.Itoa(c.MaxSegments)}}, nil
}
func (a *App) pollProtectedSMS(ctx context.Context, tenant, account, cursor string) (carrier.ReceiptPage, error) {
	p, c, e := a.protectedSMSRuntime(ctx, tenant, account, false)
	if e != nil {
		return carrier.ReceiptPage{}, e
	}
	page, e := p.Poll(ctx, c, cursor)
	if e != nil {
		return carrier.ReceiptPage{}, errCarrierComposition
	}
	result := carrier.ReceiptPage{NextCursor: page.NextCursor, HasMore: page.HasMore}
	for _, event := range page.Events {
		// SMS fallback is outbound only. Inbound texts and phone fields are neither
		// copied into generic transcripts nor retained in the private receipt queue.
		if event.Kind != "status" {
			continue
		}
		outcome := ""
		switch event.Status {
		case channels.StatusDelivered:
			outcome = "delivered"
		case channels.StatusFailed:
			outcome = "failed"
		case channels.StatusSubmitted:
			outcome = "accepted"
		default:
			continue
		}
		proof, _ := json.Marshal(struct{ Event, Intent, Provider, Outcome string }{event.ID, event.ClientMessageID, event.ProviderMessageID, outcome})
		hash := sha256.Sum256(proof)
		intent := event.ClientMessageID
		if a.ExtensionCarrier != nil {
			resolved, resolveErr := a.ExtensionCarrier.ResolveSMSCorrelation(ctx, tenant, account, event.ClientMessageID)
			if resolveErr == nil {
				intent = resolved
			} else {
				intent = ""
			}
		} else {
			intent = ""
		}
		result.Receipts = append(result.Receipts, carrier.Status{Tenant: tenant, Account: account, ProviderID: event.ProviderMessageID, ProviderClientID: event.ClientMessageID, IntentID: intent, ReceiptID: event.ID, Outcome: outcome, ObservedAt: event.OccurredAt, Evidence: "sms-poll:" + hex.EncodeToString(hash[:])})
	}
	return result, nil
}
