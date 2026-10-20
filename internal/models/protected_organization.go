package models

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// GenericOrganizationAllowed covers background dispatch as well as HTTP. It
// intentionally includes deleted rows and denies on storage uncertainty.
func GenericOrganizationAllowed(ctx context.Context, db *gorm.DB, org uuid.UUID) bool {
	if db == nil || org == uuid.Nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var exists bool
	err := db.WithContext(ctx).Raw(`SELECT EXISTS(
 SELECT 1 FROM contacts WHERE organization_id=? AND
 (protection_organization_id<>'' OR protection_module<>'' OR protection_resource<>'' OR protection_classification<>'')
 UNION ALL SELECT 1 FROM messages WHERE organization_id=? AND
 (protection_organization_id<>'' OR protection_module<>'' OR protection_resource<>'' OR protection_classification<>''))`, org, org).Scan(&exists).Error
	return err == nil && !exists
}
