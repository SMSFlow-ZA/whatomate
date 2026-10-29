package worker

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/channelapp"
	"github.com/shridarpatil/whatomate/internal/contactutil"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/queue"
	"github.com/shridarpatil/whatomate/internal/templateutil"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

func (w *Worker) handleChannelRecipient(ctx context.Context, job *queue.RecipientJob, campaign *models.BulkMessageCampaign) error {
	if w.Channels == nil {
		return errors.New("channel service unavailable")
	}
	var recipient models.BulkMessageRecipient
	if err := w.DB.First(&recipient, "id = ? AND campaign_id = ?", job.RecipientID, campaign.ID).Error; err != nil {
		return err
	}
	if recipient.Status == models.MessageStatusFailed {
		return nil
	}
	contact, _, err := contactutil.GetOrCreateContact(w.DB, campaign.OrganizationID, recipient.PhoneNumber, recipient.RecipientName)
	if err != nil {
		return err
	}
	id := uuid.NewSHA1(campaign.ID, []byte(recipient.ID.String()))
	metadata := models.JSONB{"campaign_id": campaign.ID.String(), "recipient_name": recipient.RecipientName}
	if campaign.Channel == "sms" {
		if campaign.ChannelConnectionID == nil {
			return channelapp.ErrInvalid
		}
		text := templateutil.ReplaceWithJSONBParams(campaign.SMSBody, campaign.SMSBody, recipient.TemplateParams)
		if strings.Contains(text, "{{") {
			return channelapp.ErrInvalid
		}
		return w.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			svc := *w.Channels
			svc.DB = tx
			message, err := svc.EnqueueSMSWithID(ctx, campaign.OrganizationID, contact.ID, *campaign.ChannelConnectionID, id, text, &campaign.CreatedBy, campaign.SMSConsentConfirmed, "campaign")
			if err != nil {
				return err
			}
			if err = tx.Model(&models.Message{}).Where("id = ?", message.ID).Update("metadata", metadata).Error; err != nil {
				return err
			}
			if campaign.ScheduledAt != nil && campaign.ScheduledAt.After(time.Now()) {
				if err = tx.Model(&models.ChannelAttempt{}).Where("message_id = ? AND status = ?", message.ID, "queued").Update("due_at", campaign.ScheduledAt).Error; err != nil {
					return err
				}
			}
			return tx.Model(&models.BulkMessageRecipient{}).Where("id = ? AND message_id IS NULL", recipient.ID).Updates(map[string]any{"message_id": message.ID, "status": "queued"}).Error
		})
	}
	var policy struct {
		Enabled          bool
		ConnectionID     uuid.UUID `json:"connection_id"`
		Text             string
		ConsentConfirmed bool `json:"consent_confirmed"`
		TimeoutEnabled   bool `json:"timeout_enabled"`
		TimeoutMinutes   int  `json:"timeout_minutes"`
	}
	data, _ := json.Marshal(campaign.SMSFallback)
	if err = json.Unmarshal(data, &policy); err != nil || !policy.Enabled || !policy.ConsentConfirmed || policy.TimeoutMinutes < 0 || policy.TimeoutMinutes > 1440 {
		return channelapp.ErrInvalid
	}
	if campaign.Template == nil {
		return errors.New("WhatsApp template required")
	}
	var account models.WhatsAppAccount
	if err = w.DB.First(&account, "organization_id = ? AND name = ?", campaign.OrganizationID, campaign.WhatsAppAccount).Error; err != nil {
		return err
	}
	w.decryptAccountSecrets(&account)
	if contact.MarketingOptOut {
		return channelapp.ErrUnavailable
	}
	created := false
	message := models.Message{BaseModel: models.BaseModel{ID: id}, OrganizationID: campaign.OrganizationID, ContactID: contact.ID, Channel: "whatsapp", WhatsAppAccount: account.Name, Direction: models.DirectionOutgoing, MessageType: models.MessageTypeTemplate, TemplateName: campaign.Template.Name, TemplateParams: recipient.TemplateParams, Content: templateutil.ReplaceWithJSONBParams(campaign.Template.BodyContent, campaign.Template.BodyContent, recipient.TemplateParams), Metadata: metadata, Status: models.MessageStatusPending, SentByUserID: &campaign.CreatedBy}
	err = w.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", id.String()).Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&models.Message{}).Where("id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
		if err := tx.Create(&message).Error; err != nil {
			return err
		}
		svc := *w.Channels
		svc.DB = tx
		var timeout time.Duration
		if policy.TimeoutEnabled {
			minutes := policy.TimeoutMinutes
			if minutes == 0 {
				minutes = 10
			}
			timeout = time.Duration(minutes) * time.Minute
		}
		text := templateutil.ReplaceWithJSONBParams(policy.Text, policy.Text, recipient.TemplateParams)
		if strings.Contains(text, "{{") {
			return channelapp.ErrInvalid
		}
		if err := svc.AttachFallback(ctx, &message, policy.ConnectionID, text, policy.ConsentConfirmed, timeout); err != nil {
			return err
		}
		if err := tx.Model(&models.ChannelAttempt{}).Where("message_id = ? AND channel = ?", id, "sms").Update("source", "campaign_fallback").Error; err != nil {
			return err
		}
		if err := tx.Model(&models.BulkMessageRecipient{}).Where("id = ?", recipient.ID).Update("message_id", id).Error; err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil || !created {
		return err
	}
	waID, sendErr := w.sendTemplateMessage(ctx, &account, campaign.Template, &recipient, campaign.HeaderMediaID, campaign.HeaderMediaFilename)
	return w.recordChannelPrimary(ctx, &message, &recipient, waID, sendErr)
}

func (w *Worker) recordChannelPrimary(ctx context.Context, message *models.Message, recipient *models.BulkMessageRecipient, waID string, sendErr error) error {
	status, code := "sent", 0
	messageStatus := models.MessageStatusSent
	if sendErr != nil {
		status = "unknown"
		messageStatus = models.MessageStatusPending
		var apiErr *whatsapp.CodedAPIError
		if errors.As(sendErr, &apiErr) {
			status = "failed"
			code = apiErr.Code
			messageStatus = models.MessageStatusFailed
		}
	}
	if err := w.Channels.RecordWhatsApp(ctx, message.ID, status, code); err != nil {
		return err
	}
	err := w.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Match dispatch's campaign-first order before updating message rows.
		var campaign models.BulkMessageCampaign
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&campaign, "id = ?", recipient.CampaignID).Error; err != nil {
			return err
		}
		updates := map[string]any{"status": messageStatus, "whats_app_message_id": waID}
		if status == "unknown" {
			updates["error_message"] = "WhatsApp submission outcome unknown; awaiting provider delivery evidence"
		}
		if status == "failed" {
			updates["error_message"] = "WhatsApp submission rejected"
		}
		if err := tx.Model(&models.Message{}).Where("id = ? AND status NOT IN ('delivered','read')", message.ID).Updates(updates).Error; err != nil {
			return err
		}
		var stored models.Message
		if err := tx.Select("id,status").First(&stored, "id = ?", message.ID).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.BulkMessageRecipient{}).Where("id = ? AND status NOT IN ('delivered','read')", recipient.ID).Updates(map[string]any{"status": stored.Status, "whats_app_message_id": waID}).Error; err != nil {
			return err
		}
		var counts struct{ Sent, Delivered, Read, Failed, Pending int }
		if err := tx.Model(&models.BulkMessageRecipient{}).Select("count(*) FILTER (WHERE status IN ('sent','delivered','read')) AS sent,count(*) FILTER (WHERE status IN ('delivered','read')) AS delivered,count(*) FILTER (WHERE status='read') AS read,count(*) FILTER (WHERE status='failed') AS failed,count(*) FILTER (WHERE status IN ('pending','queued')) AS pending").Where("campaign_id = ?", campaign.ID).Scan(&counts).Error; err != nil {
			return err
		}
		values := map[string]any{"sent_count": counts.Sent, "delivered_count": counts.Delivered, "read_count": counts.Read, "failed_count": counts.Failed}
		if counts.Pending == 0 && campaign.Status == models.CampaignStatusProcessing {
			values["status"] = models.CampaignStatusCompleted
			values["completed_at"] = time.Now()
		}
		return tx.Model(&campaign).Updates(values).Error
	})
	if err == nil && w.Publisher != nil {
		w.publishCampaignStats(ctx, recipient.CampaignID, message.OrganizationID)
	}
	return err
}

// Invalid input is terminal, unlike a transient database failure. Never ACK a
// queue item while leaving its recipient pending forever, and never count a
// redelivered validation failure twice. Existing submissions remain untouched.
func (w *Worker) failChannelRecipient(ctx context.Context, job *queue.RecipientJob) error {
	return w.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		q := tx.Model(&models.BulkMessageRecipient{}).Where("id = ? AND campaign_id = ? AND status='pending' AND message_id IS NULL", job.RecipientID, job.CampaignID).Updates(map[string]any{"status": models.MessageStatusFailed, "error_message": "SMS consent, connection, recipient or text validation failed"})
		if q.Error != nil || q.RowsAffected == 0 {
			return q.Error
		}
		var campaign models.BulkMessageCampaign
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&campaign, "id = ? AND organization_id = ?", job.CampaignID, job.OrganizationID).Error; err != nil {
			return err
		}
		updates := map[string]any{"failed_count": gorm.Expr("failed_count+1")}
		var pending int64
		if err := tx.Model(&models.BulkMessageRecipient{}).Where("campaign_id = ? AND status IN ('pending','queued')", job.CampaignID).Count(&pending).Error; err != nil {
			return err
		}
		if pending == 0 && campaign.Status == models.CampaignStatusProcessing {
			updates["status"] = models.CampaignStatusCompleted
			updates["completed_at"] = time.Now()
		}
		return tx.Model(&campaign).Updates(updates).Error
	})
}
