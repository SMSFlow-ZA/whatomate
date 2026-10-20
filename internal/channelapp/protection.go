package channelapp

import (
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// IsConnectionProtected consults trusted composition only, never persisted user settings.
func (s *Service) IsConnectionProtected(id uuid.UUID) bool {
	if s == nil {
		return true
	}
	for _, protected := range s.ProtectedConnectionIDs {
		if protected == id {
			return true
		}
	}
	return s.ConnectionProtected != nil && s.ConnectionProtected(id)
}

// GenericConnections excludes private transports before ordering and limiting.
func (s *Service) GenericConnections(db *gorm.DB) *gorm.DB {
	return s.GenericConnectionReferences(db, "id")
}

// GenericConnectionReferences is limited to trusted source-code column names.
func (s *Service) GenericConnectionReferences(db *gorm.DB, column string) *gorm.DB {
	if column != "id" && column != "connection_id" {
		return db.Where("false")
	}
	if len(s.ProtectedConnectionIDs) > 0 {
		return db.Where(column+" NOT IN ?", s.ProtectedConnectionIDs)
	}
	return db
}
