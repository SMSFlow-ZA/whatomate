package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

func (a *App) channelConnectionProtected(id uuid.UUID) bool {
	if a.Channels != nil && a.Channels.IsConnectionProtected(id) {
		return true
	}
	for _, protected := range a.configuredProtectedChannelIDs() {
		if protected == id {
			return true
		}
	}
	return false
}
func (a *App) genericChannelConnections(db *gorm.DB) *gorm.DB {
	if a.Channels != nil {
		db = a.Channels.GenericConnections(db)
	}
	if ids := a.configuredProtectedChannelIDs(); len(ids) > 0 {
		db = db.Where("id NOT IN ?", ids)
	}
	return db
}
func (a *App) configuredProtectedChannelIDs() []uuid.UUID {
	var ids []uuid.UUID
	if a.Config == nil {
		return ids
	}
	for _, binding := range a.Config.Extensions.CarrierSMSBindings {
		if id, err := uuid.Parse(binding.ConnectionID); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}
func (a *App) visibleChannelConnections(rows []models.ChannelConnection) []models.ChannelConnection {
	out := make([]models.ChannelConnection, 0, len(rows))
	for _, row := range rows {
		if !a.channelConnectionProtected(row.ID) {
			out = append(out, row)
		}
	}
	return out
}
