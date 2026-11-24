package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

const inboxEscalationRecipientLimit = 20

type inboxEscalationPerson struct {
	ID              uuid.UUID
	FullName        string
	RoleID          *uuid.UUID
	IsSuperAdmin    bool
	FullContactRead bool
	SettingsRead    bool
	SettingsWrite   bool
	UsersRead       bool
}

// Load current identities and roles in batches; no Redis permission cache and
// no home-organisation role carried into a different membership. These records
// grant neither contact access nor permission to browse the staff directory.
func (a *App) inboxEscalationPeople(db *gorm.DB, org uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]inboxEscalationPerson, error) {
	result := make(map[uuid.UUID]inboxEscalationPerson)
	if len(ids) == 0 {
		return result, nil
	}
	var people []inboxEscalationPerson
	if err := db.Model(&models.User{}).
		Select("users.id,users.full_name,users.is_super_admin,COALESCE(uo.role_id,CASE WHEN users.organization_id=? THEN users.role_id END) AS role_id", org).
		Joins("LEFT JOIN user_organizations uo ON uo.user_id=users.id AND uo.organization_id=? AND uo.deleted_at IS NULL", org).
		Where("users.id IN ? AND users.is_active AND (users.organization_id=? OR uo.role_id IS NOT NULL)", ids, org).
		Find(&people).Error; err != nil {
		return nil, err
	}
	roleIDs := make([]uuid.UUID, 0, len(people))
	for _, person := range people {
		if person.RoleID != nil {
			roleIDs = append(roleIDs, *person.RoleID)
		}
	}
	var roles []models.CustomRole
	if len(roleIDs) > 0 {
		if err := db.Where("id IN ? AND organization_id=?", roleIDs, org).Find(&roles).Error; err != nil {
			return nil, err
		}
	}
	rolePointers := make([]*models.CustomRole, len(roles))
	for i := range roles {
		rolePointers[i] = &roles[i]
	}
	fresh := &App{DB: db, Log: a.Log}
	if err := fresh.loadRolePermissions(rolePointers...); err != nil {
		return nil, err
	}
	byID := make(map[uuid.UUID]models.CustomRole, len(roles))
	for _, role := range roles {
		byID[role.ID] = role
	}
	for _, person := range people {
		chatRead := person.IsSuperAdmin
		person.FullContactRead = person.IsSuperAdmin
		person.SettingsRead, person.SettingsWrite, person.UsersRead = person.IsSuperAdmin, person.IsSuperAdmin, person.IsSuperAdmin
		if person.RoleID != nil {
			for _, permission := range byID[*person.RoleID].Permissions {
				chatRead = chatRead || (permission.Resource == models.ResourceChat && permission.Action == models.ActionRead)
				person.FullContactRead = person.FullContactRead || (permission.Resource == models.ResourceContacts && permission.Action == models.ActionRead)
				person.SettingsRead = person.SettingsRead || (permission.Resource == models.ResourceSettingsGeneral && permission.Action == models.ActionRead)
				person.SettingsWrite = person.SettingsWrite || (permission.Resource == models.ResourceSettingsGeneral && permission.Action == models.ActionWrite)
				person.UsersRead = person.UsersRead || (permission.Resource == models.ResourceUsers && permission.Action == models.ActionRead)
			}
		}
		if chatRead {
			result[person.ID] = person
		}
	}
	return result, nil
}

func inboxEscalationPersonCanRead(db *gorm.DB, org uuid.UUID, person inboxEscalationPerson, caseID uuid.UUID) (bool, error) {
	var count int64
	err := scopeInboxCaseHistoryWithContactRead(db.Model(&models.InboxCase{}), person.ID, org, person.FullContactRead).
		Where("inbox_cases.id=?", caseID).Count(&count).Error
	return count > 0, err
}
