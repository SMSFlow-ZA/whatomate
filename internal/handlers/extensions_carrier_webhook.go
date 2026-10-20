package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/SMSFlow-ZA/whatomate-addons/carrier"
	"github.com/SMSFlow-ZA/whatomate-addons/conversation"
	"github.com/shridarpatil/whatomate/internal/models"
	ext "github.com/shridarpatil/whatomate/pkg/extensions/v1"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

// protectedCarrierWebhook exclusively consumes bound account traffic before any
// generic logging, contact writes, chatbot, call, echo or transcript processing.
// Authentication covers the complete batch; mixed or unsupported batches deny.
func (a *App) protectedCarrierWebhook(r *fastglue.Request, p WebhookPayload, body, signature []byte) (bool, error) {
	if a.ExtensionCarrier == nil {
		if a.Config != nil && a.Config.Extensions.CarrierEnabled {
			return true, r.SendErrorEnvelope(fasthttp.StatusForbidden, "Protected carrier unavailable", nil, "")
		}
		return false, nil
	}
	deny := func() (bool, error) {
		return true, r.SendErrorEnvelope(fasthttp.StatusForbidden, "Protected carrier denied", nil, "")
	}
	if len(signature) == 0 || len(body) > 1024*1024 || len(p.Entry) == 0 {
		return deny()
	}
	// Classify the complete payload before allowing any legacy processing.
	// Unknown accounts cannot use the ordinary-account compatibility path.
	boundCount, ordinaryCount := 0, 0
	for _, entry := range p.Entry {
		if len(entry.Changes) == 0 {
			return deny()
		}
		for _, change := range entry.Changes {
			phone := change.Value.Metadata.PhoneNumberID
			if phone == "" {
				return deny()
			}
			if _, ok := a.ExtensionCarrier.Binding(phone); ok {
				boundCount++
				continue
			}
			if a.DB == nil || a.Config == nil {
				return deny()
			}
			var accounts []models.WhatsAppAccount
			if e := a.DB.WithContext(context.Background()).Where("phone_id=? AND status=?", phone, "active").Find(&accounts).Error; e != nil || len(accounts) != 1 {
				return deny()
			}
			account := accounts[0]
			// A protected immutable account ID can never become ordinary through phone
			// reassignment, organization movement or another mutable account field.
			for _, binding := range a.ExtensionCarrier.Bindings {
				if binding.Account == account.ID.String() {
					return deny()
				}
			}
			if account.BusinessID != entry.ID {
				return deny()
			}
			account.DecryptSecrets(a.Config.App.EncryptionKey)
			if account.AppSecret == "" || !verifyWebhookSignature(body, signature, []byte(account.AppSecret)) {
				return deny()
			}
			ordinaryCount++
		}
	}
	if boundCount > 0 && ordinaryCount > 0 {
		return deny()
	}
	if boundCount == 0 && ordinaryCount > 0 {
		return false, nil
	}
	var incoming []conversation.Inbound
	var statuses []carrier.Status
	digest := sha256.Sum256(body)
	evidence := hex.EncodeToString(digest[:])
	for _, entry := range p.Entry {
		if len(entry.Changes) == 0 {
			return deny()
		}
		for _, change := range entry.Changes {
			b, ok := a.ExtensionCarrier.Binding(change.Value.Metadata.PhoneNumberID)
			if !ok || !verifyWebhookSignature(body, signature, []byte(b.AppSecret)) {
				return deny()
			}
			if a.DB == nil {
				return deny()
			}
			var account models.WhatsAppAccount
			// Resolve current account identity directly; cached credentials must not revive
			// an inactive, replaced or moved account.
			if e := a.DB.WithContext(context.Background()).Where("id = ? AND organization_id = ? AND phone_id = ? AND status = ?", b.Account, b.Tenant, b.PhoneID, "active").First(&account).Error; e != nil {
				return deny()
			}
			if entry.ID != account.BusinessID {
				return deny()
			}
			if change.Field != "messages" || len(change.Value.Calls) > 0 || len(change.Value.MessageEchoes) > 0 || len(change.Value.UserPreferences) > 0 {
				return deny()
			}
			for _, m := range change.Value.Messages {
				if m.Type != "text" || m.Text == nil {
					return deny()
				}
				ts, e := strconv.ParseInt(m.Timestamp, 10, 64)
				if e != nil || ts <= 0 {
					return deny()
				}
				canonical, e := carrier.CanonicalNumber(m.From)
				if e != nil {
					return deny()
				}
				if a.ExtensionGuard == nil {
					return deny()
				}
				if e = a.ExtensionGuard.CheckInbound(context.Background(), ext.Inbound{OrganizationID: b.Tenant, AccountID: b.Account, ProviderMessageID: m.ID, Sender: canonical, Kind: "text"}, extensionIDs(a.Config.Extensions.RequiredInbound)); e != nil {
					return deny()
				}
				incoming = append(incoming, conversation.Inbound{Tenant: b.Tenant, Account: b.Account, Sender: canonical, ProviderMessageID: m.ID, Text: m.Text.Body, ProviderTimestamp: time.Unix(ts, 0).UTC()})
			}
			for _, st := range change.Value.Statuses {
				outcome := st.Status
				if outcome == "read" {
					outcome = "delivered"
				}
				if outcome == "sent" {
					outcome = "accepted"
				}
				if outcome != "delivered" && outcome != "failed" && outcome != "accepted" {
					return deny()
				}
				ts, e := strconv.ParseInt(st.Timestamp, 10, 64)
				if e != nil || ts <= 0 {
					return deny()
				}
				statuses = append(statuses, carrier.Status{Tenant: b.Tenant, Account: b.Account, ProviderID: st.ID, IntentID: st.BizOpaqueCallbackData, ReceiptID: st.ID + ":" + outcome + ":" + st.Timestamp, Outcome: outcome, ObservedAt: time.Unix(ts, 0).UTC(), Evidence: evidence})
			}
		}
	}
	if len(incoming)+len(statuses) == 0 {
		return deny()
	}
	if len(incoming)+len(statuses) > 0 {
		if e := a.ExtensionCarrier.AcceptBatch(context.Background(), incoming, statuses); e != nil {
			return true, r.SendErrorEnvelope(fasthttp.StatusServiceUnavailable, "Protected persistence unavailable", nil, "")
		}
	}
	return true, r.SendEnvelope(map[string]string{"status": "ok"})
}
