package channelapp

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

// currentChannelIdentity rechecks active organization membership immediately
// before queued dispatch. It does not change the host authentication middleware.
func currentChannelIdentity(db *gorm.DB, org, id uuid.UUID) (*models.User, error) {
	var user models.User
	if err := db.Where("id = ? AND is_active = true", id).First(&user).Error; err != nil {
		return nil, err
	}
	var organization models.Organization
	if err := db.First(&organization, "id = ?", org).Error; err != nil {
		return nil, err
	}
	if !user.IsSuperAdmin && user.OrganizationID != org {
		var membership models.UserOrganization
		if err := db.First(&membership, "user_id = ? AND organization_id = ?", id, org).Error; err != nil {
			return nil, err
		}
		user.RoleID = membership.RoleID
	}
	return &user, nil
}
