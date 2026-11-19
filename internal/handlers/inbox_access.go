package handlers

import (
	"errors"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

// latestInboxAssignment limits case-based access to the current case for one
// contact and WhatsApp account. Previous assignees cannot use historical cases
// to regain access after assignment changes.
func latestInboxAssignment(db *gorm.DB, org, user uuid.UUID) *gorm.DB {
	return latestInboxCases(db, org).Where("ic.assignee_id = ?", user)
}

func latestInboxCases(db *gorm.DB, org uuid.UUID) *gorm.DB {
	return db.Table("inbox_cases AS ic").
		Where("ic.organization_id = ? AND ic.deleted_at IS NULL", org).
		Where("EXISTS (SELECT 1 FROM organizations o WHERE o.id = ic.organization_id AND o.inbox_workflows_enabled AND o.deleted_at IS NULL)").
		Where("NOT EXISTS (SELECT 1 FROM inbox_cases newer WHERE newer.organization_id = ic.organization_id AND newer.account_id = ic.account_id AND newer.contact_id = ic.contact_id AND newer.deleted_at IS NULL AND (newer.opened_at, newer.id) > (ic.opened_at, ic.id))")
}

func inboxAssignedElsewhere(db *gorm.DB, org, user, contact uuid.UUID, account string) *gorm.DB {
	return latestInboxCases(db, org).
		Joins("JOIN whatsapp_accounts wa ON wa.id = ic.account_id AND wa.organization_id = ic.organization_id AND wa.deleted_at IS NULL").
		Where("wa.name = ? AND ic.contact_id = ? AND ic.assignee_id IS NOT NULL AND ic.assignee_id <> ?", account, contact, user).Select("1")
}

// findInboxChatContact grants the existing contact access first. An agent who
// relies only on a case assignment must explicitly select the assigned account.
func (a *App) findInboxChatContact(org, user, contactID uuid.UUID, account string) (models.Contact, bool, error) {
	var contact models.Contact
	query := a.DB.Where("id = ? AND organization_id = ?", contactID, org)
	if !a.HasPermission(user, models.ResourceContacts, models.ActionRead, org) {
		query = query.Where("assigned_user_id = ?", user)
	}
	err := query.First(&contact).Error
	if !errors.Is(err, gorm.ErrRecordNotFound) || account == "" {
		return contact, false, err
	}
	grant := latestInboxAssignment(a.DB, org, user).
		Joins("JOIN whatsapp_accounts wa ON wa.id = ic.account_id AND wa.organization_id = ic.organization_id AND wa.deleted_at IS NULL").
		Where("wa.name = ? AND ic.contact_id = contacts.id", account).Select("1")
	transfer := a.DB.Model(&models.AgentTransfer{}).Select("1").
		Where("organization_id = ? AND contact_id = contacts.id AND agent_id = ? AND whats_app_account = ? AND status = ?", org, user, account, models.TransferStatusActive)
	// A legacy transfer must not restore access revoked by the current case.
	assignedElsewhere := inboxAssignedElsewhere(a.DB, org, user, contactID, account)
	transfer = transfer.Where("NOT EXISTS (?)", assignedElsewhere)
	err = a.DB.Where("contacts.id = ? AND contacts.organization_id = ? AND (EXISTS (?) OR EXISTS (?))", contactID, org, grant, transfer).
		First(&contact).Error
	if err == nil {
		contact.WhatsAppAccount = account
	}
	return contact, err == nil, err
}
