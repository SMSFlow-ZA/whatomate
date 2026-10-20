package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/channels"
	"github.com/zerodha/fastglue"
)

func (a *App) validateCampaignSMS(org, connection uuid.UUID, text string, consent bool) error {
	if a.Channels == nil || a.channelConnectionProtected(connection) || !consent {
		return channelsError()
	}
	var c models.ChannelConnection
	if err := a.DB.Where("id = ? AND organization_id = ? AND enabled = ?", connection, org, true).First(&c).Error; err != nil {
		return channelsError()
	}
	_, err := channels.PreviewSMS(text, c.MaxSegments)
	return err
}
func channelsError() error { return &channels.ProviderError{Kind: "channel_unavailable"} }
func (a *App) createSMSCampaign(r *fastglue.Request, org, user uuid.UUID, req CampaignRequest) error {
	if err := a.validateCampaignSMS(org, req.ChannelConnectionID, req.SMSBody, req.ConsentConfirmed); err != nil {
		return r.SendErrorEnvelope(422, "Select an enabled SMS connection, valid text and recipient consent", nil, "sms_campaign_invalid")
	}
	if req.SMSFallback != nil && req.SMSFallback.Enabled {
		return r.SendErrorEnvelope(400, "SMS campaigns cannot have another SMS fallback", nil, "")
	}
	campaign := models.BulkMessageCampaign{OrganizationID: org, Channel: "sms", ChannelConnectionID: &req.ChannelConnectionID, SMSBody: req.SMSBody, SMSConsentConfirmed: true, Name: req.Name, Status: models.CampaignStatusDraft, ScheduledAt: req.ScheduledAt, CreatedBy: user, UpdatedByID: &user}
	if err := a.DB.Create(&campaign).Error; err != nil {
		return r.SendErrorEnvelope(503, "SMS campaign could not be saved", nil, "")
	}
	a.logAudit(org, user, "campaign", campaign.ID, models.AuditActionCreated, nil, &campaign)
	return r.SendEnvelope(campaign)
}
func (a *App) updateSMSCampaign(r *fastglue.Request, org, user uuid.UUID, campaign *models.BulkMessageCampaign, req CampaignRequest) error {
	if req.Channel != "" && req.Channel != "sms" {
		return r.SendErrorEnvelope(400, "Create a new campaign to change delivery channels", nil, "")
	}
	if req.ChannelConnectionID == uuid.Nil && campaign.ChannelConnectionID != nil {
		req.ChannelConnectionID = *campaign.ChannelConnectionID
	}
	if err := a.validateCampaignSMS(org, req.ChannelConnectionID, req.SMSBody, req.ConsentConfirmed); err != nil {
		return r.SendErrorEnvelope(422, "Invalid SMS text, connection or recipient consent", nil, "")
	}
	if err := a.DB.Model(campaign).Updates(map[string]any{"name": req.Name, "sms_body": req.SMSBody, "channel_connection_id": req.ChannelConnectionID, "sms_consent_confirmed": req.ConsentConfirmed, "scheduled_at": req.ScheduledAt, "updated_by_id": user}).Error; err != nil {
		return r.SendErrorEnvelope(503, "SMS campaign could not be updated", nil, "")
	}
	if err := a.DB.First(campaign, "id = ?", campaign.ID).Error; err != nil {
		return r.SendErrorEnvelope(503, "SMS campaign reload failed", nil, "")
	}
	return r.SendEnvelope(campaign)
}
