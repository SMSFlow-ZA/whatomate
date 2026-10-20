package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/channelapp"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/models"
	"testing"
)

func TestProtectedChannelNilRuntimePreservesOrdinarySettings(t *testing.T) {
	private, ordinary := uuid.New(), uuid.New()
	a := App{Config: &config.Config{}}
	if a.channelConnectionProtected(ordinary) {
		t.Fatal("disabled carrier must not hide ordinary settings without a runtime")
	}
	rows := []models.ChannelConnection{{BaseModel: models.BaseModel{ID: private}}, {BaseModel: models.BaseModel{ID: ordinary}}}
	if len(a.visibleChannelConnections(rows)) != 2 {
		t.Fatal("ordinary settings disappeared without runtime")
	}
	a.Config.Extensions.CarrierSMSBindings = []config.ExtensionCarrierSMSBinding{{ConnectionID: private.String()}}
	if !a.channelConnectionProtected(private) || a.channelConnectionProtected(ordinary) {
		t.Fatal("trusted configured binding lost without runtime")
	}
	visible := a.visibleChannelConnections(rows)
	if len(visible) != 1 || visible[0].ID != ordinary {
		t.Fatal("configured protected connection disclosed or ordinary settings hidden")
	}
	if e := a.validateCampaignSMS(uuid.New(), ordinary, "synthetic", true); e == nil {
		t.Fatal("nil runtime enabled campaign sending")
	}
}

func TestProtectedChannelHiddenAndCampaignDeniedBeforeStorage(t *testing.T) {
	private, ordinary := uuid.New(), uuid.New()
	a := App{Channels: &channelapp.Service{ProtectedConnectionIDs: []uuid.UUID{private}}}
	rows := a.visibleChannelConnections([]models.ChannelConnection{{BaseModel: models.BaseModel{ID: private}}, {BaseModel: models.BaseModel{ID: ordinary}}})
	if len(rows) != 1 || rows[0].ID != ordinary {
		t.Fatal("protected connection disclosed or generic connection hidden")
	}
	if e := a.validateCampaignSMS(uuid.New(), private, "synthetic", true); e == nil {
		t.Fatal("protected campaign accepted")
	}
	a.Channels.ConnectionProtected = func(id uuid.UUID) bool { return id == ordinary }
	if len(a.visibleChannelConnections(rows)) != 0 {
		t.Fatal("dynamic protected binding disclosed")
	}
}
