package handlers

import (
	"errors"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errInboxAgentAtCapacity = errors.New("agent is at capacity for this WhatsApp account")

// reserveInboxAgent permits assignment while holding the configured agent and
// inbox limit row until the enclosing case update commits. The caller must
// keep the check and assignment in the same transaction.
func reserveInboxAgent(tx *gorm.DB, c models.InboxCase, userID uuid.UUID) error {
	if c.Status != models.InboxCaseOpen && c.Status != models.InboxCasePending {
		return nil
	}
	var limit models.InboxAgentCapacity
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("organization_id = ? AND account_id = ? AND agent_id = ?", c.OrganizationID, c.AccountID, userID).
		First(&limit).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil || limit.MaxActive == 0 {
		return err
	}
	var count int64
	err = tx.Model(&models.InboxCase{}).
		Where("organization_id = ? AND account_id = ? AND assignee_id = ? AND id <> ?",
			c.OrganizationID, c.AccountID, userID, c.ID).
		Where("status IN ?", []models.InboxCaseStatus{models.InboxCaseOpen, models.InboxCasePending}).
		Count(&count).Error
	if err != nil {
		return err
	}
	if count >= int64(limit.MaxActive) {
		return errInboxAgentAtCapacity
	}
	return nil
}
