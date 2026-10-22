package channelapp

import (
	"context"

	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

func genericPollingHostAllowed(ctx context.Context, db *gorm.DB, lock bool) error {
	if db == nil {
		return ErrUnavailable
	}
	if !lock {
		protected, err := models.ProtectedHostEnabled(db.WithContext(ctx))
		if err != nil || protected {
			return ErrUnavailable
		}
		return nil
	}
	// Fence a concurrent host activation until the generic receipt transaction
	// commits. A marker that changed during the network request denies all writes.
	var state struct{ Protected bool }
	result := db.WithContext(ctx).Raw(`SELECT protected FROM whatomate_protection_state WHERE id=1 FOR SHARE`).Scan(&state)
	if result.Error != nil || result.RowsAffected != 1 || state.Protected {
		return ErrUnavailable
	}
	return nil
}
