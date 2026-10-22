// Package channelapp owns durable dispatch. Provider network calls are outside
// database transactions; in-flight outcomes are reconciled, never blindly sent.
package channelapp

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/channels"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrInvalid = errors.New("invalid channel configuration, consent or content")
var ErrUnavailable = errors.New("channel unavailable or access denied")
var phonePattern = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)

type Service struct {
	StopDispatch bool
	// ConnectionProtected excludes operator-bound private transports from generic dispatch and polling.
	ConnectionProtected func(uuid.UUID) bool
	// ProtectedConnectionIDs is a trusted startup snapshot, used before bounded queries.
	ProtectedConnectionIDs []uuid.UUID
	DB                     *gorm.DB
	Providers              *channels.Registry
	EncryptionKey          string
	// AuthorizeSend rechecks the originating user's current permissions at dispatch.
	// Nil uses current active identity + organization role permissions.
	AuthorizeSend func(uuid.UUID, uuid.UUID) bool
	Notify        func(uuid.UUID, uuid.UUID) // organization, message; called after commit.
}

func Migrate(db *gorm.DB) error {
	return db.AutoMigrate(&models.ChannelConnection{}, &models.ChannelAttempt{}, &models.ChannelReceipt{}, &models.ChannelBotJob{}, &models.Contact{}, &models.Message{})
}

func (s *Service) CreateConnection(ctx context.Context, org uuid.UUID, name, providerID string, credentials channels.Credentials, maxSegments int) (*models.ChannelConnection, error) {
	if org == uuid.Nil || strings.TrimSpace(name) == "" || len(name) > 100 || maxSegments < 1 || maxSegments > 10 || len(s.EncryptionKey) < 32 || s.Providers == nil {
		return nil, ErrInvalid
	}
	p, ok := s.Providers.Get(providerID)
	if !ok {
		return nil, ErrUnavailable
	}
	c := models.ChannelConnection{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org, Name: strings.TrimSpace(name), Provider: providerID, MaxSegments: maxSegments, Enabled: false, Capabilities: models.JSONB{}, ErrorCode: "validation_required", NextPollAt: time.Now()}
	data, err := json.Marshal(credentials)
	if err != nil {
		return nil, ErrInvalid
	}
	c.Credentials, err = crypto.Encrypt(string(data), s.EncryptionKey)
	if err != nil {
		return nil, ErrUnavailable
	}
	// Persist the encrypted, disabled identity before remote validation can bind
	// it. A lost validation response can then be recovered with the same ID.
	if err = s.DB.WithContext(ctx).Create(&c).Error; err != nil {
		return nil, err
	}
	info, err := p.Validate(ctx, channels.Connection{ID: c.ID.String(), Credentials: credentials})
	if err != nil {
		return &c, err
	}
	if !ValidCapabilities(providerID, info.Capabilities) {
		return &c, ErrUnavailable
	}
	caps, _ := json.Marshal(info.Capabilities)
	if err = json.Unmarshal(caps, &c.Capabilities); err != nil {
		return nil, ErrInvalid
	}
	c.Enabled = true
	c.ErrorCode = ""
	now := time.Now()
	c.ValidatedAt = &now
	c.BalanceThreshold = "100"
	if err = s.DB.WithContext(ctx).Model(&c).Updates(map[string]any{"capabilities": c.Capabilities, "enabled": true, "error_code": "", "validated_at": now}).Error; err != nil {
		return nil, err
	}
	if err = s.DB.WithContext(ctx).Model(&c).Updates(BalanceUpdates(info.Balance, c.BalanceThreshold, now)).Error; err != nil {
		return &c, err
	}
	return &c, nil
}

func ValidCapabilities(_ string, c channels.Capabilities) bool {
	// Dispatch commits a one-shot claim before the provider call. Expired or
	// uncertain work is never resubmitted, including after process recreation.
	// A transport may recover through receipts instead of a replay/lookup API;
	// keep its advertised idempotency capability truthful.
	return c.Text && (c.Reconciliation || c.DeliveryReports)
}

func (s *Service) maySendContact(db *gorm.DB, org uuid.UUID, user *uuid.UUID, contact *models.Contact, source string) bool {
	if !models.GenericOrganizationAllowed(db.Statement.Context, db, org) {
		return false
	}
	if user == nil || strings.HasPrefix(source, "campaign") {
		return true
	}
	u, err := middleware.CurrentIdentity(db, &middleware.JWTClaims{UserID: *user, OrganizationID: org})
	if err != nil {
		return false
	}
	if u.IsSuperAdmin {
		return true
	}
	var count int64
	if u.RoleID != nil {
		err = db.Table("role_permissions rp").Joins("JOIN permissions p ON p.id = rp.permission_id AND p.deleted_at IS NULL").Joins("JOIN custom_roles r ON r.id = rp.custom_role_id AND r.deleted_at IS NULL").Where("rp.custom_role_id = ? AND r.organization_id = ? AND p.resource = ? AND p.action = ?", *u.RoleID, org, models.ResourceContacts, models.ActionRead).Count(&count).Error
		if err != nil {
			return false
		}
		if count > 0 {
			return true
		}
	}
	if contact.AssignedUserID != nil && *contact.AssignedUserID == *user {
		return true
	}
	err = db.Model(&models.AgentTransfer{}).Where("organization_id = ? AND contact_id = ? AND agent_id = ? AND status = ?", org, contact.ID, *user, models.TransferStatusActive).Count(&count).Error
	return err == nil && count > 0
}

func (s *Service) connection(db *gorm.DB, org, id uuid.UUID) (*models.ChannelConnection, error) {
	if s.IsConnectionProtected(id) {
		return nil, ErrUnavailable
	}
	var c models.ChannelConnection
	if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND organization_id = ?", id, org).First(&c).Error; err != nil || !c.Enabled {
		return nil, ErrUnavailable
	}
	return &c, nil
}
func (s *Service) runtime(c *models.ChannelConnection) (channels.Provider, channels.Connection, error) {
	if c == nil || s.IsConnectionProtected(c.ID) {
		return nil, channels.Connection{}, ErrUnavailable
	}
	if len(s.EncryptionKey) < 32 || !crypto.IsEncrypted(c.Credentials) {
		return nil, channels.Connection{}, ErrUnavailable
	}
	raw, err := crypto.Decrypt(c.Credentials, s.EncryptionKey)
	if err != nil {
		return nil, channels.Connection{}, ErrUnavailable
	}
	var creds channels.Credentials
	if err = json.Unmarshal([]byte(raw), &creds); err != nil {
		return nil, channels.Connection{}, ErrUnavailable
	}
	if s.Providers == nil {
		return nil, channels.Connection{}, ErrUnavailable
	}
	p, ok := s.Providers.Get(c.Provider)
	if !ok {
		return nil, channels.Connection{}, ErrUnavailable
	}
	return p, channels.Connection{ID: c.ID.String(), Credentials: creds, Config: map[string]string{"max_segments": formatInt(c.MaxSegments)}}, nil
}
func formatInt(n int) string {
	if n == 10 {
		return "10"
	}
	return string(rune('0' + n))
}
func normalizePhone(phone string) string {
	if !strings.HasPrefix(phone, "+") {
		phone = "+" + phone
	}
	return phone
}
func eligibleContact(db *gorm.DB, org, id uuid.UUID) (*models.Contact, error) {
	var c models.Contact
	if err := db.Where("id = ? AND organization_id = ?", id, org).First(&c).Error; err != nil || c.SMSOptOut || c.MarketingOptOut || !phonePattern.MatchString(normalizePhone(c.PhoneNumber)) {
		return nil, ErrUnavailable
	}
	return &c, nil
}
func (s *Service) maySend(db *gorm.DB, org uuid.UUID, user *uuid.UUID, source ...string) bool {
	if user == nil {
		return true
	} // Trusted host bot actions; never settable by an external caller.
	if s.AuthorizeSend != nil {
		return s.AuthorizeSend(org, *user)
	}
	u, err := middleware.CurrentIdentity(db, &middleware.JWTClaims{UserID: *user, OrganizationID: org})
	if err != nil {
		return false
	}
	if u.IsSuperAdmin {
		return true
	}
	if u.RoleID == nil {
		return false
	}
	var count int64
	resource, action := models.ResourceChat, models.ActionWrite
	if len(source) > 0 && strings.HasPrefix(source[0], "campaign") {
		resource, action = models.ResourceCampaigns, models.ActionExecute
	}
	err = db.Table("role_permissions rp").Joins("JOIN permissions p ON p.id = rp.permission_id AND p.deleted_at IS NULL").Joins("JOIN custom_roles r ON r.id = rp.custom_role_id AND r.deleted_at IS NULL").Where("rp.custom_role_id = ? AND r.organization_id = ? AND p.resource = ? AND p.action = ?", *u.RoleID, org, resource, action).Count(&count).Error
	return err == nil && count > 0
}

func (s *Service) EnqueueSMS(ctx context.Context, org, contactID, connectionID uuid.UUID, text string, user *uuid.UUID, consent bool, source string) (*models.Message, error) {
	return s.EnqueueSMSWithID(ctx, org, contactID, connectionID, uuid.New(), text, user, consent, source)
}
func (s *Service) EnqueueSMSWithID(ctx context.Context, org, contactID, connectionID, messageID uuid.UUID, text string, user *uuid.UUID, consent bool, source string) (*models.Message, error) {
	if s.IsConnectionProtected(connectionID) {
		return nil, ErrUnavailable
	}
	if !consent || len(source) > 20 {
		return nil, ErrInvalid
	}
	var msg models.Message
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if messageID == uuid.Nil {
			return ErrInvalid
		}
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", messageID.String()).Error; err != nil {
			return err
		}
		var existing models.Message
		if err := tx.First(&existing, "id = ?", messageID).Error; err == nil {
			if existing.OrganizationID != org || existing.ContactID != contactID || existing.Channel != "sms" || existing.ChannelConnectionID == nil || *existing.ChannelConnectionID != connectionID || existing.Content != text {
				return ErrInvalid
			}
			msg = existing
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		c, err := s.connection(tx, org, connectionID)
		if err != nil {
			return err
		}
		if _, err = channels.PreviewSMS(text, c.MaxSegments); err != nil {
			return ErrInvalid
		}
		contact, err := eligibleContact(tx, org, contactID)
		if err != nil {
			return err
		}
		if !s.maySend(tx, org, user, source) || !s.maySendContact(tx, org, user, contact, source) {
			return ErrUnavailable
		}
		msg = models.Message{OrganizationID: org, ContactID: contactID, Channel: "sms", ChannelConnectionID: &connectionID, Direction: models.DirectionOutgoing, MessageType: models.MessageTypeText, Content: text, Status: models.MessageStatusPending, SentByUserID: user}
		msg.ID = messageID
		if err = tx.Create(&msg).Error; err != nil {
			return err
		}
		now := time.Now()
		attempt := models.ChannelAttempt{OrganizationID: org, MessageID: msg.ID, LogicalMessageID: msg.ID, ConnectionID: &connectionID, Channel: "sms", Status: "queued", Text: text, Destination: normalizePhone(contact.PhoneNumber), DueAt: &now, Source: source, InitiatedBy: user, ConsentConfirmed: true}
		return tx.Create(&attempt).Error
	})
	if err != nil {
		return nil, err
	}
	s.notify(org, msg.ID)
	return &msg, nil
}

// AttachFallback must complete before the initial WhatsApp send. Roll back the
// caller's logical message if attachment fails; no WhatsApp send has occurred.
func (s *Service) AttachFallback(ctx context.Context, msg *models.Message, connectionID uuid.UUID, smsText string, consent bool, timeout time.Duration) error {
	if s.IsConnectionProtected(connectionID) {
		return ErrUnavailable
	}
	if !consent || timeout < 0 || timeout > 24*time.Hour {
		return ErrInvalid
	}
	if msg.MessageType == models.MessageTypeText && smsText == "" {
		smsText = msg.Content
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		c, err := s.connection(tx, msg.OrganizationID, connectionID)
		if err != nil {
			return err
		}
		if _, err = channels.PreviewSMS(smsText, c.MaxSegments); err != nil {
			return ErrInvalid
		}
		contact, err := eligibleContact(tx, msg.OrganizationID, msg.ContactID)
		if err != nil {
			return err
		}
		wa := models.ChannelAttempt{OrganizationID: msg.OrganizationID, MessageID: msg.ID, LogicalMessageID: msg.ID, Channel: "whatsapp", Status: string(msg.Status), Source: "whatsapp"}
		if err = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&wa).Error; err != nil {
			return err
		}
		var due *time.Time
		if timeout > 0 {
			t := time.Now().Add(timeout)
			due = &t
		}
		attempt := models.ChannelAttempt{OrganizationID: msg.OrganizationID, MessageID: msg.ID, LogicalMessageID: msg.ID, Channel: "sms", ConnectionID: &connectionID, Status: "waiting", Text: smsText, Destination: normalizePhone(contact.PhoneNumber), IsFallback: true, DueAt: due, Source: "fallback", InitiatedBy: msg.SentByUserID, ConsentConfirmed: true}
		return tx.Create(&attempt).Error
	})
}

func (s *Service) RecordWhatsApp(ctx context.Context, messageID uuid.UUID, status string, code int) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var wa models.ChannelAttempt
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("message_id = ? AND channel = ?", messageID, "whatsapp").First(&wa).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if wa.Status == "read" || wa.Status == "delivered" {
			if status != "read" {
				return nil
			}
		}
		if err = tx.Model(&wa).Update("status", status).Error; err != nil {
			return err
		}
		q := tx.Model(&models.ChannelAttempt{}).Where("message_id = ? AND channel = ? AND status IN ?", messageID, "sms", []string{"waiting", "queued"})
		if status == "delivered" || status == "read" {
			return q.Updates(map[string]any{"status": "cancelled", "error_code": "whatsapp_delivered"}).Error
		}
		// Recipient undeliverable. Authentication, policy, rate and ambiguous errors
		// are deliberately absent; further codes require reviewed classification.
		if status == "failed" && code == 131026 {
			now := time.Now()
			return q.Updates(map[string]any{"status": "queued", "due_at": now}).Error
		}
		if status == "failed" {
			return q.Updates(map[string]any{"status": "waiting", "due_at": nil, "error_code": "ineligible_whatsapp_failure"}).Error
		}
		return nil
	})
}

func (s *Service) Dispatch(ctx context.Context, now time.Time) error {
	if s.StopDispatch {
		return nil
	}
	// An expired lease proves only that a process stopped responding, not that
	// its POST was unsent. Recovery therefore moves it to reconciliation.
	if err := s.GenericConnectionReferences(s.DB.WithContext(ctx), "connection_id").Model(&models.ChannelAttempt{}).Where("channel = ? AND status = ? AND lease_until < ?", "sms", "sending", now).Updates(map[string]any{"status": "unknown", "claim_id": nil, "error_code": "lease_expired"}).Error; err != nil {
		return err
	}
	for range 20 {
		var attempt models.ChannelAttempt
		var conn models.ChannelConnection
		found := false
		err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			// All operations lock connection before attempts. Settings, event
			// ingestion and bot transitions use the same order.
			due := tx.Model(&models.ChannelAttempt{}).Select("connection_id").Where("channel = 'sms' AND connection_id IS NOT NULL AND status IN ? AND due_at <= ?", []string{"queued", "waiting"}, now)
			err := s.GenericConnections(tx).Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("id IN (?)", due).Order("id").First(&conn).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			err = tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("connection_id = ? AND organization_id = ? AND channel = ? AND status IN ? AND due_at <= ?", conn.ID, conn.OrganizationID, "sms", []string{"queued", "waiting"}, now).Order("due_at,id").First(&attempt).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			if attempt.ConnectionID == nil {
				return ErrInvalid
			}
			if s.IsConnectionProtected(conn.ID) {
				return nil
			}
			if !models.GenericOrganizationAllowed(ctx, tx, attempt.OrganizationID) {
				return stopAttempt(tx, &attempt, "blocked", "protected_organization")
			}
			if !conn.Enabled {
				return stopAttempt(tx, &attempt, "cancelled", "connection_disabled")
			}
			var message models.Message
			if err = tx.Where("id = ? AND organization_id = ?", attempt.MessageID, attempt.OrganizationID).First(&message).Error; err != nil {
				return err
			}
			if allowed, err := campaignDispatchAllowed(tx, &attempt, now); err != nil || !allowed {
				return err
			}
			if attempt.IsFallback {
				var wa models.ChannelAttempt
				if err = tx.Where("message_id = ? AND channel = ?", message.ID, "whatsapp").First(&wa).Error; err != nil {
					return err
				}
				if wa.Status == "delivered" || wa.Status == "read" || message.Status == models.MessageStatusDelivered || message.Status == models.MessageStatusRead {
					return tx.Model(&attempt).Updates(map[string]any{"status": "cancelled", "error_code": "whatsapp_delivered"}).Error
				}
				if attempt.Status == "waiting" && wa.Status != "sent" {
					return tx.Model(&attempt).Update("due_at", now.Add(30*time.Second)).Error
				}
			}
			contact, err := eligibleContact(tx, attempt.OrganizationID, message.ContactID)
			if err != nil || !attempt.ConsentConfirmed || !s.maySend(tx, attempt.OrganizationID, attempt.InitiatedBy, attempt.Source) || !s.maySendContact(tx, attempt.OrganizationID, attempt.InitiatedBy, contact, attempt.Source) {
				return stopAttempt(tx, &attempt, "cancelled", "suppressed_or_access_revoked")
			}
			if _, err = channels.PreviewSMS(attempt.Text, conn.MaxSegments); err != nil {
				return stopAttempt(tx, &attempt, "blocked", "segment_limit")
			}
			if normalizePhone(contact.PhoneNumber) != attempt.Destination {
				return stopAttempt(tx, &attempt, "cancelled", "destination_changed")
			}
			claim := uuid.New()
			lease := now.Add(2 * time.Minute)
			attempt.ClaimID = &claim
			if err = tx.Model(&attempt).Updates(map[string]any{"status": "sending", "claim_id": claim, "lease_until": lease, "dispatched_at": now}).Error; err != nil {
				return err
			}
			found = true
			return nil
		})
		if err != nil {
			return err
		}
		if !found {
			if attempt.ID == uuid.Nil {
				return nil
			}
			continue
		}
		p, c, err := s.runtime(&conn)
		var result channels.Submission
		if err == nil {
			sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			result, err = p.Send(sendCtx, c, channels.Message{ClientMessageID: attempt.ID.String(), Destination: attempt.Destination, Text: attempt.Text, CheckOptOuts: true})
			cancel()
		}
		status, code := result.Status, result.Code
		if err != nil {
			status = "blocked"
			code = "provider_unavailable"
			var pe *channels.ProviderError
			if errors.As(err, &pe) {
				code = pe.Kind
				if pe.Kind == "unknown" {
					status = "unknown"
				}
			}
		}
		if status != "submitted" && status != "delivered" && status != "failed" && status != "unknown" && status != "blocked" {
			status = "unknown"
			code = "invalid_provider_response"
		}
		if err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			q := tx.Model(&models.ChannelAttempt{}).Where("id = ? AND claim_id = ? AND status = ?", attempt.ID, *attempt.ClaimID, "sending").Updates(map[string]any{"status": status, "provider_message_id": result.ProviderMessageID, "error_code": code, "claim_id": nil, "lease_until": nil})
			if q.Error != nil {
				return q.Error
			}
			if q.RowsAffected == 0 {
				return nil
			}
			if !attempt.IsFallback {
				return updateSMSMessage(tx, attempt.MessageID, status)
			}
			return nil
		}); err != nil {
			return err
		}
		s.notify(attempt.OrganizationID, attempt.MessageID)
	}
	return nil
}

func updateSMSMessage(tx *gorm.DB, id uuid.UUID, status string) error {
	mapped := models.MessageStatusPending
	switch status {
	case "submitted":
		mapped = models.MessageStatusSent
	case "delivered":
		mapped = models.MessageStatusDelivered
	case "failed", "blocked", "cancelled":
		mapped = models.MessageStatusFailed
	}
	if err := tx.Model(&models.Message{}).Where("id = ? AND channel = ?", id, "sms").Update("status", mapped).Error; err != nil {
		return err
	}
	return syncSMSCampaign(tx, id, mapped)
}

func (s *Service) PollConnection(ctx context.Context, id uuid.UUID) error {
	if err := genericPollingHostAllowed(ctx, s.DB, false); err != nil {
		return err
	}
	if s.IsConnectionProtected(id) {
		return ErrUnavailable
	}
	var conn models.ChannelConnection
	if err := s.DB.WithContext(ctx).First(&conn, "id = ?", id).Error; err != nil {
		return err
	}
	p, c, err := s.runtime(&conn)
	if err != nil {
		return err
	}
	pollCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	page, err := p.Poll(pollCtx, c, conn.Cursor)
	cancel()
	if err != nil {
		delay := time.Duration(1<<min(conn.PollFailures, 5)) * 30 * time.Second
		if updateErr := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if guardErr := genericPollingHostAllowed(ctx, tx, true); guardErr != nil {
				return guardErr
			}
			return tx.Model(&conn).Updates(map[string]any{"poll_failures": conn.PollFailures + 1, "next_poll_at": time.Now().Add(delay), "error_code": "poll_failed"}).Error
		}); updateErr != nil {
			return updateErr
		}
		return err
	}
	changed := map[uuid.UUID]bool{}
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := genericPollingHostAllowed(ctx, tx, true); err != nil {
			return err
		}
		var locked models.ChannelConnection
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, "id = ?", id).Error; err != nil {
			return err
		}
		if locked.Cursor != conn.Cursor {
			return nil
		} // Another worker already committed this page.
		for _, event := range page.Events {
			if event.ID == "" || len(event.ID) > 128 || (event.Kind != "status" && event.Kind != "inbound") {
				return ErrInvalid
			}
			receipt := models.ChannelReceipt{ConnectionID: id, EventID: event.ID, Kind: event.Kind}
			added := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&receipt)
			if added.Error != nil {
				return added.Error
			}
			if added.RowsAffected == 0 {
				continue
			}
			var attempt models.ChannelAttempt
			err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND connection_id = ? AND organization_id = ?", event.ClientMessageID, id, conn.OrganizationID).First(&attempt).Error
			if err != nil {
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
				if err = tx.Model(&receipt).Update("quarantined", true).Error; err != nil {
					return err
				}
				continue
			}
			var message models.Message
			if err = tx.First(&message, "id = ? AND organization_id = ?", attempt.MessageID, conn.OrganizationID).Error; err != nil {
				return err
			}
			if event.Kind == "status" {
				if attempt.Status != "delivered" && (attempt.Status != "failed" || event.Status != "submitted") && (event.Status == "submitted" || event.Status == "delivered" || event.Status == "failed") {
					if err = tx.Model(&attempt).Updates(map[string]any{"status": event.Status, "provider_message_id": event.ProviderMessageID, "error_code": event.Code}).Error; err != nil {
						return err
					}
					if !attempt.IsFallback {
						if err = updateSMSMessage(tx, message.ID, event.Status); err != nil {
							return err
						}
					}
				}
			} else {
				if normalizePhone(event.Phone) != attempt.Destination || len(event.Text) > 10000 || event.Text == "" {
					if err = tx.Model(&receipt).Update("quarantined", true).Error; err != nil {
						return err
					}
					continue
				}
				received := event.OccurredAt
				if received.IsZero() || received.After(time.Now().Add(5*time.Minute)) {
					received = time.Now()
				}
				incoming := models.Message{OrganizationID: conn.OrganizationID, ContactID: message.ContactID, Channel: "sms", ChannelConnectionID: &id, Direction: models.DirectionIncoming, MessageType: models.MessageTypeText, Content: event.Text, Status: models.MessageStatusDelivered}
				incoming.ID = uuid.NewSHA1(id, []byte("inbound:"+event.ID))
				if err = tx.Create(&incoming).Error; err != nil {
					return err
				}
				updates := map[string]any{"sms_last_inbound_at": received, "last_message_at": received, "last_message_preview": event.Text, "is_read": false}
				if event.OptOut || strings.EqualFold(strings.TrimSpace(event.Text), "STOP") {
					updates["sms_opt_out"] = true
				} else if locked.Enabled && locked.BotFlowID != nil && locked.BotConsentConfirmed {
					job := models.ChannelBotJob{OrganizationID: conn.OrganizationID, ConnectionID: id, IncomingMessageID: incoming.ID, FlowID: *locked.BotFlowID, Status: "pending"}
					if err = tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&job).Error; err != nil {
						return err
					}
				}
				if err = tx.Model(&models.Contact{}).Where("id = ? AND organization_id = ?", message.ContactID, conn.OrganizationID).Updates(updates).Error; err != nil {
					return err
				}
				changed[incoming.ID] = true
			}
			changed[message.ID] = true
		}
		now := time.Now()
		next := now.Add(30 * time.Second)
		if page.HasMore {
			next = now
		}
		return tx.Model(&locked).Updates(map[string]any{"cursor": page.NextCursor, "last_poll_at": now, "next_poll_at": next, "poll_failures": 0, "error_code": ""}).Error
	})
	if err != nil {
		return err
	}
	for messageID := range changed {
		s.notify(conn.OrganizationID, messageID)
	}
	return nil
}

func (s *Service) ReconcileUnknown(ctx context.Context) error {
	// A restore/emergency hold stops sends, not recovery of an expired claim.
	now := time.Now()
	if err := s.GenericConnectionReferences(s.DB.WithContext(ctx), "connection_id").Model(&models.ChannelAttempt{}).Where("channel='sms' AND status='sending' AND lease_until < ?", now).Updates(map[string]any{"status": "unknown", "claim_id": nil, "lease_until": nil, "error_code": "lease_expired"}).Error; err != nil {
		return err
	}
	var attempts []models.ChannelAttempt
	if err := s.GenericConnectionReferences(s.DB.WithContext(ctx), "connection_id").Where("channel = ? AND status = ? AND (next_reconcile_at IS NULL OR next_reconcile_at <= ?)", "sms", "unknown", now).Order("next_reconcile_at NULLS FIRST, updated_at,id").Limit(20).Find(&attempts).Error; err != nil {
		return err
	}
	for _, attempt := range attempts {
		if attempt.ConnectionID != nil && s.IsConnectionProtected(*attempt.ConnectionID) {
			continue
		}
		// Schedule before every lookup, including unavailable/unknown results,
		// so permanent uncertainty cannot monopolize the bounded batch.
		delay := time.Duration(1<<min(attempt.ReconcileFailures, 5)) * 30 * time.Second
		claim := s.DB.WithContext(ctx).Model(&models.ChannelAttempt{}).Where("id = ? AND status='unknown' AND updated_at = ?", attempt.ID, attempt.UpdatedAt).Updates(map[string]any{"next_reconcile_at": now.Add(delay), "reconcile_failures": gorm.Expr("reconcile_failures+1")})
		if claim.Error != nil {
			return claim.Error
		}
		if claim.RowsAffected == 0 {
			continue
		}
		if attempt.ConnectionID == nil {
			continue
		}
		var conn models.ChannelConnection
		if err := s.DB.WithContext(ctx).First(&conn, "id = ? AND organization_id = ?", *attempt.ConnectionID, attempt.OrganizationID).Error; err != nil {
			continue
		}
		p, c, err := s.runtime(&conn)
		if err != nil {
			continue
		}
		callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		result, err := p.Reconcile(callCtx, c, attempt.ID.String())
		cancel()
		if err != nil {
			continue
		}
		if result.ClientMessageID != attempt.ID.String() || (result.Status != "submitted" && result.Status != "delivered" && result.Status != "failed") {
			continue
		}
		if err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			q := tx.Model(&models.ChannelAttempt{}).Where("id = ? AND status = ?", attempt.ID, "unknown").Updates(map[string]any{"status": result.Status, "provider_message_id": result.ProviderMessageID, "error_code": result.Code})
			if q.Error != nil {
				return q.Error
			}
			if q.RowsAffected > 0 && !attempt.IsFallback {
				return updateSMSMessage(tx, attempt.MessageID, result.Status)
			}
			return nil
		}); err != nil {
			return err
		}
		s.notify(attempt.OrganizationID, attempt.MessageID)
	}
	return nil
}

// Tick is driven by the host's cancellable worker. Disabled connections still
// receive already-sent message events; disabling stops pending dispatch only.
func (s *Service) Tick(ctx context.Context, now time.Time) error {
	if err := s.Dispatch(ctx, now); err != nil {
		return err
	}
	if err := s.RefreshBalances(ctx, now); err != nil {
		return err
	}
	if err := s.ReconcileUnknown(ctx); err != nil {
		return err
	}
	return s.PollDue(ctx, now)
}

func (s *Service) PollDue(ctx context.Context, now time.Time) error {
	var connections []models.ChannelConnection
	if err := s.GenericConnections(s.DB.WithContext(ctx)).Where("validated_at IS NOT NULL AND next_poll_at <= ?", now).Order("next_poll_at").Limit(20).Find(&connections).Error; err != nil {
		return err
	}
	for _, c := range connections {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_ = s.PollConnection(ctx, c.ID)
	}
	return nil
}
func (s *Service) notify(org, id uuid.UUID) {
	if s.Notify != nil {
		s.Notify(org, id)
	}
}
