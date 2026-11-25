package database

import (
	"github.com/shridarpatil/whatomate/internal/inboxlive"
	"gorm.io/gorm"
)

// InstallInboxLiveRevisions shares the production trigger implementation with
// disposable PostgreSQL fixtures. Repeated installation preserves revisions.
func InstallInboxLiveRevisions(db *gorm.DB) error {
	return inboxlive.InstallRevisions(db)
}
