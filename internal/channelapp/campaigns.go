package channelapp

import (
	"errors"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"strings"
	"time"
)

func stopAttempt(tx *gorm.DB, attempt *models.ChannelAttempt, status, code string) error {
	if err := tx.Model(attempt).Updates(map[string]any{"status": status, "error_code": code, "due_at": nil}).Error; err != nil {
		return err
	}
	if !attempt.IsFallback {
		return updateSMSMessage(tx, attempt.MessageID, status)
	}
	return nil
}

// CancelPending is called while holding the connection row. Claimed or
// uncertain submissions remain available for reconciliation and are not resent.
func CancelPending(tx *gorm.DB, connectionID uuid.UUID) error {
	var rows []models.ChannelAttempt
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("connection_id = ? AND channel = 'sms' AND status IN ?", connectionID, []string{"waiting", "queued", "blocked"}).Order("id").Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		code := row.ErrorCode
		if code == "" {
			code = "connection_disabled"
		}
		if err := stopAttempt(tx, &row, "cancelled", code); err != nil {
			return err
		}
	}
	return tx.Model(&models.ChannelBotJob{}).Where("connection_id = ? AND status IN ?", connectionID, []string{"pending", "blocked"}).Updates(map[string]any{"status": "cancelled", "error_code": "connection_disabled"}).Error
}

func campaignDispatchAllowed(tx *gorm.DB, attempt *models.ChannelAttempt, now time.Time) (bool, error) {
	if !strings.HasPrefix(attempt.Source, "campaign") {
		return true, nil
	}
	var recipient models.BulkMessageRecipient
	if err := tx.First(&recipient, "message_id = ?", attempt.MessageID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, stopAttempt(tx, attempt, "cancelled", "campaign_missing")
		}
		return false, err
	}
	var campaign models.BulkMessageCampaign
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&campaign, "id = ? AND organization_id = ?", recipient.CampaignID, attempt.OrganizationID).Error; err != nil {
		return false, err
	}
	if campaign.Status == models.CampaignStatusCancelled || campaign.Status == models.CampaignStatusFailed {
		return false, stopAttempt(tx, attempt, "cancelled", "campaign_cancelled")
	}
	if campaign.Status == models.CampaignStatusPaused || campaign.Status == models.CampaignStatusDraft || (campaign.ScheduledAt != nil && campaign.ScheduledAt.After(now)) {
		due := now.Add(30 * time.Second)
		if campaign.ScheduledAt != nil && campaign.ScheduledAt.After(due) {
			due = *campaign.ScheduledAt
		}
		return false, tx.Model(attempt).Update("due_at", due).Error
	}
	return true, nil
}

func syncSMSCampaign(tx *gorm.DB, messageID uuid.UUID, status models.MessageStatus) error {
	var recipient models.BulkMessageRecipient
	if err := tx.First(&recipient, "message_id = ?", messageID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	updates := map[string]any{"status": status}
	now := time.Now()
	if status == models.MessageStatusSent || status == models.MessageStatusDelivered {
		updates["sent_at"] = gorm.Expr("COALESCE(sent_at, ?)", now)
	}
	if status == models.MessageStatusDelivered {
		updates["delivered_at"] = gorm.Expr("COALESCE(delivered_at, ?)", now)
	}
	if err := tx.Model(&recipient).Updates(updates).Error; err != nil {
		return err
	}
	var campaign models.BulkMessageCampaign
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&campaign, "id = ?", recipient.CampaignID).Error; err != nil {
		return err
	}
	var counts struct{ Sent, Delivered, Failed, Pending int }
	if err := tx.Model(&models.BulkMessageRecipient{}).Select("COUNT(*) FILTER (WHERE status IN ('sent','delivered','read')) AS sent, COUNT(*) FILTER (WHERE status IN ('delivered','read')) AS delivered, COUNT(*) FILTER (WHERE status = 'failed') AS failed, COUNT(*) FILTER (WHERE status NOT IN ('sent','delivered','read','failed')) AS pending").Where("campaign_id = ?", campaign.ID).Scan(&counts).Error; err != nil {
		return err
	}
	values := map[string]any{"sent_count": counts.Sent, "delivered_count": counts.Delivered, "failed_count": counts.Failed, "read_count": 0}
	if counts.Pending == 0 && campaign.Status != models.CampaignStatusCancelled && campaign.Status != models.CampaignStatusPaused {
		values["status"] = models.CampaignStatusCompleted
		values["completed_at"] = now
	}
	return tx.Model(&campaign).Updates(values).Error
}
