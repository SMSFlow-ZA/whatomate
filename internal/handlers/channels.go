package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/channelapp"
	"strings"
	"time"

	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/channels"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (a *App) ListChannelConnections(r *fastglue.Request) error {
	org, _, err := a.requireAuth(r, models.ResourceSettingsChannels, models.ActionRead)
	if err != nil {
		return nil
	}
	var rows []models.ChannelConnection
	if err = a.DB.Where("organization_id = ?", org).Order("created_at").Find(&rows).Error; err != nil {
		return r.SendErrorEnvelope(503, "Channel settings unavailable", nil, "")
	}
	return r.SendEnvelope(map[string]any{"connections": rows})
}
func (a *App) AvailableChannels(r *fastglue.Request) error {
	org, _, err := a.requireChannelConsumer(r, models.ActionRead)
	if err != nil {
		return nil
	}
	var rows []models.ChannelConnection
	if err = a.DB.Where("organization_id = ? AND enabled = ?", org, true).Find(&rows).Error; err != nil {
		return r.SendErrorEnvelope(503, "Channels unavailable", nil, "")
	}
	return r.SendEnvelope(map[string]any{"connections": rows})
}
func (a *App) ChannelProviders(r *fastglue.Request) error {
	if _, _, err := a.requireAuth(r, models.ResourceSettingsChannels, models.ActionRead); err != nil {
		return nil
	}
	if a.Channels == nil || a.Channels.Providers == nil {
		return r.SendEnvelope(map[string]any{"providers": []channels.Descriptor{}})
	}
	return r.SendEnvelope(map[string]any{"providers": a.Channels.Providers.Descriptors(), "credentials_configured": len(a.Channels.EncryptionKey) >= 32})
}
func (a *App) CreateChannelConnection(r *fastglue.Request) error {
	org, user, err := a.requireAuth(r, models.ResourceSettingsChannels, models.ActionWrite)
	if err != nil {
		return nil
	}
	if a.Channels == nil {
		return r.SendErrorEnvelope(503, "Channels unavailable", nil, "")
	}
	var req struct {
		Name, Provider string
		Credentials    channels.Credentials
		MaxSegments    int `json:"max_segments"`
	}
	if err = a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.MaxSegments == 0 {
		req.MaxSegments = 3
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := a.Channels.CreateConnection(ctx, org, req.Name, req.Provider, req.Credentials, req.MaxSegments)
	if err != nil {
		return r.SendErrorEnvelope(422, "Connection validation failed; check credentials and API compatibility. A disabled connection is retained for revalidation.", nil, "channel_validation_failed")
	}
	a.logAudit(org, user, "channel_connection", c.ID, models.AuditActionCreated, nil, map[string]any{"provider": c.Provider, "name": c.Name})
	return r.SendEnvelope(c)
}
func (a *App) UpdateChannelConnection(r *fastglue.Request) error {
	org, user, err := a.requireAuth(r, models.ResourceSettingsChannels, models.ActionWrite)
	if err != nil {
		return nil
	}
	id, err := parsePathUUID(r, "id", "channel connection")
	if err != nil {
		return nil
	}
	var c models.ChannelConnection
	if err = a.DB.Where("id = ? AND organization_id = ?", id, org).First(&c).Error; err != nil {
		return r.SendErrorEnvelope(404, "Channel connection not found", nil, "")
	}
	var req struct {
		Name                string
		Enabled             bool
		MaxSegments         int `json:"max_segments"`
		Credentials         channels.Credentials
		BotFlowID           *string `json:"bot_flow_id"`
		BotConsentConfirmed bool    `json:"bot_consent_confirmed"`
		Revalidate          bool    `json:"revalidate"`
		BalanceThreshold    *string `json:"balance_threshold"`
	}
	if err = a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if strings.TrimSpace(req.Name) == "" || len(req.Name) > 100 || req.MaxSegments < 1 || req.MaxSegments > 10 {
		return r.SendErrorEnvelope(400, "Name and a segment limit from 1 to 10 are required", nil, "")
	}
	updates := map[string]any{"name": strings.TrimSpace(req.Name), "enabled": req.Enabled, "max_segments": req.MaxSegments}
	threshold := c.BalanceThreshold
	if req.BalanceThreshold != nil {
		if !channelapp.ValidBalanceThreshold(*req.BalanceThreshold) {
			return r.SendErrorEnvelope(422, "Enter a non-negative balance threshold", nil, "")
		}
		threshold = *req.BalanceThreshold
		updates["balance_threshold"] = threshold
	}
	if req.BotFlowID != nil {
		updates["bot_flow_id"] = nil
		updates["bot_consent_confirmed"] = false
		if *req.BotFlowID != "" {
			if c.Capabilities["inbound"] != true {
				return r.SendErrorEnvelope(422, "A verified reply-capable route is required for SMS bots", nil, "")
			}
			id, err := uuid.Parse(*req.BotFlowID)
			if err != nil || !req.BotConsentConfirmed {
				return r.SendErrorEnvelope(422, "An SMS bot flow and recipient consent are required", nil, "")
			}
			var flow models.ChatbotFlow
			if err = a.DB.First(&flow, "id = ? AND organization_id = ? AND channel = 'sms' AND is_enabled = true", id, org).Error; err != nil {
				return r.SendErrorEnvelope(422, "Choose an enabled SMS flow in this organization", nil, "")
			}
			if err = validateSMSBotGraph(&flow); err != nil {
				return r.SendErrorEnvelope(422, err.Error(), nil, "")
			}
			updates["bot_flow_id"] = id
			updates["bot_consent_confirmed"] = true
		}
	}
	if len(req.Credentials) > 0 || req.Revalidate || (req.Enabled && c.ValidatedAt == nil) {
		if a.Channels == nil || a.Channels.Providers == nil || len(a.Channels.EncryptionKey) < 32 {
			return r.SendErrorEnvelope(503, "Credential encryption unavailable", nil, "")
		}
		p, ok := a.Channels.Providers.Get(c.Provider)
		if !ok {
			return r.SendErrorEnvelope(503, "Provider unavailable", nil, "")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// Keep pending work and report cursors attached to their original account.
		// The existing SMPP API does not enforce the retired V2 server binding.
		if !crypto.IsEncrypted(c.Credentials) {
			return r.SendErrorEnvelope(503, "Credential encryption unavailable", nil, "")
		}
		var existingCredentials channels.Credentials
		raw, err := crypto.Decrypt(c.Credentials, a.Channels.EncryptionKey)
		if err != nil || json.Unmarshal([]byte(raw), &existingCredentials) != nil {
			return r.SendErrorEnvelope(503, "Credential encryption unavailable", nil, "")
		}
		credentials := req.Credentials
		if len(credentials) == 0 {
			credentials = existingCredentials
		}
		if original := existingCredentials["client_id"]; original != "" && credentials["client_id"] != original {
			return r.SendErrorEnvelope(422, "Create a new connection for a different client account", nil, "")
		}
		info, err := p.Validate(ctx, channels.Connection{ID: id.String(), Credentials: credentials})
		if err != nil || !channelapp.ValidCapabilities(c.Provider, info.Capabilities) {
			return r.SendErrorEnvelope(422, "Credential rotation requires the same registered SMSFlow connection", nil, "")
		}
		data, err := json.Marshal(credentials)
		if err != nil {
			return r.SendErrorEnvelope(400, "Invalid credentials", nil, "")
		}
		enc, err := crypto.Encrypt(string(data), a.Channels.EncryptionKey)
		if err != nil {
			return r.SendErrorEnvelope(503, "Credential encryption unavailable", nil, "")
		}
		updates["credentials"] = enc
		caps, _ := json.Marshal(info.Capabilities)
		var capabilities models.JSONB
		if json.Unmarshal(caps, &capabilities) != nil {
			return r.SendErrorEnvelope(422, "Invalid provider capabilities", nil, "")
		}
		updates["capabilities"] = capabilities
		updates["validated_at"] = time.Now()
		updates["error_code"] = ""
		for key, value := range channelapp.BalanceUpdates(info.Balance, threshold, time.Now()) {
			updates[key] = value
		}
	}
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		var locked models.ChannelConnection
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, "id = ? AND organization_id = ?", id, org).Error; err != nil {
			return err
		}
		if !locked.UpdatedAt.Equal(c.UpdatedAt) {
			return errors.New("connection_changed")
		}
		if err := tx.Model(&locked).Updates(updates).Error; err != nil {
			return err
		}
		if !req.Enabled {
			return channelapp.CancelPending(tx, id)
		}
		return nil
	})
	if err != nil {
		return r.SendErrorEnvelope(409, "Connection changed; reload before saving", nil, "")
	}
	a.logAudit(org, user, "channel_connection", id, models.AuditActionUpdated, nil, map[string]any{"enabled": req.Enabled, "max_segments": req.MaxSegments, "credentials_rotated": len(req.Credentials) > 0})
	if err = a.DB.First(&c, "id = ?", id).Error; err != nil {
		return r.SendErrorEnvelope(503, "Reload failed", nil, "")
	}
	return r.SendEnvelope(c)
}
func (a *App) DeleteChannelConnection(r *fastglue.Request) error {
	org, user, err := a.requireAuth(r, models.ResourceSettingsChannels, models.ActionWrite)
	if err != nil {
		return nil
	}
	id, err := parsePathUUID(r, "id", "channel connection")
	if err != nil {
		return nil
	}
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		var c models.ChannelConnection
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND organization_id = ?", id, org).First(&c).Error; err != nil {
			return err
		}
		if c.Enabled {
			return errors.New("enabled")
		}
		var pending int64
		if err := tx.Model(&models.ChannelAttempt{}).Where("connection_id = ? AND status NOT IN ?", id, []string{"delivered", "failed", "cancelled"}).Count(&pending).Error; err != nil {
			return err
		}
		if pending > 0 {
			return errors.New("pending")
		}
		return tx.Delete(&c).Error
	})
	if err != nil {
		return r.SendErrorEnvelope(409, "Disable the connection first; unresolved deliveries must remain available for reconciliation", nil, "channel_in_use")
	}
	a.logAudit(org, user, "channel_connection", id, models.AuditActionDeleted, nil, nil)
	return r.SendEnvelope(map[string]bool{"deleted": true})
}
func (a *App) PreviewSMS(r *fastglue.Request) error {
	if _, _, err := a.requireChannelConsumer(r, models.ActionWrite); err != nil {
		return nil
	}
	var req struct {
		Text        string
		MaxSegments int `json:"max_segments"`
	}
	if err := a.decodeRequest(r, &req); err != nil {
		return nil
	}
	if req.MaxSegments == 0 {
		req.MaxSegments = 3
	}
	preview, err := channels.PreviewSMS(req.Text, req.MaxSegments)
	if err != nil {
		return r.SendErrorEnvelope(422, "SMS is empty or exceeds the configured segment limit", preview, "sms_segment_limit")
	}
	return r.SendEnvelope(preview)
}
func (a *App) ChannelHealth(r *fastglue.Request) error {
	org, _, err := a.requireAuth(r, models.ResourceSettingsChannels, models.ActionRead)
	if err != nil {
		return nil
	}
	var unresolved, blocked, late int64
	if err = a.DB.Model(&models.ChannelAttempt{}).Where("organization_id = ? AND status = ?", org, "unknown").Count(&unresolved).Error; err != nil {
		return r.SendErrorEnvelope(503, "Monitoring unavailable", nil, "")
	}
	if err = a.DB.Model(&models.ChannelAttempt{}).Where("organization_id = ? AND status = ?", org, "blocked").Count(&blocked).Error; err != nil {
		return r.SendErrorEnvelope(503, "Monitoring unavailable", nil, "")
	}
	if err = a.DB.Model(&models.ChannelConnection{}).Where("organization_id = ? AND enabled = true AND (poll_failures > 0 OR next_poll_at < ?)", org, time.Now().Add(-2*time.Minute)).Count(&late).Error; err != nil {
		return r.SendErrorEnvelope(503, "Monitoring unavailable", nil, "")
	}
	var balances struct{ Low, Failed int64 }
	if err = a.DB.Model(&models.ChannelConnection{}).Select("count(*) FILTER (WHERE low_balance) AS low, count(*) FILTER (WHERE balance_error_code <> '') AS failed").Where("organization_id = ? AND enabled", org).Scan(&balances).Error; err != nil {
		return r.SendErrorEnvelope(503, "Monitoring unavailable", nil, "")
	}
	var bots int64
	if err = a.DB.Model(&models.ChannelBotJob{}).Where("organization_id = ? AND (status='blocked' OR (status='pending' AND created_at < ?))", org, time.Now().Add(-2*time.Minute)).Count(&bots).Error; err != nil {
		return r.SendErrorEnvelope(503, "Monitoring unavailable", nil, "")
	}
	var volume int64
	if err = a.DB.Model(&models.ChannelAttempt{}).Where("organization_id = ? AND channel='sms' AND is_fallback AND dispatched_at > ?", org, time.Now().Add(-time.Hour)).Count(&volume).Error; err != nil {
		return r.SendErrorEnvelope(503, "Monitoring unavailable", nil, "")
	}
	return r.SendEnvelope(map[string]any{"healthy": unresolved+blocked+late+balances.Low+balances.Failed+bots == 0 && volume <= 50, "unresolved_submissions": unresolved, "blocked_sends": blocked, "unhealthy_connections": late, "low_balance_connections": balances.Low, "balance_failures": balances.Failed, "unhealthy_bot_jobs": bots, "fallbacks_last_hour": volume, "dispatch_stopped": a.Channels != nil && a.Channels.StopDispatch})
}

func (a *App) canConsumeChannel(org, user uuid.UUID, action string) bool {
	for _, resource := range []string{models.ResourceChat, models.ResourceCampaigns, models.ResourceFlowsChatbot} {
		if a.HasPermission(user, resource, action, org) {
			return true
		}
	}
	return action == models.ActionRead && a.HasPermission(user, models.ResourceSettingsChannels, models.ActionRead, org)
}
func (a *App) requireChannelConsumer(r *fastglue.Request, action string) (uuid.UUID, uuid.UUID, error) {
	org, user, err := a.getOrgAndUserID(r)
	if err != nil {
		_ = r.SendErrorEnvelope(401, "Unauthorized", nil, "")
		return org, user, err
	}
	if !a.canConsumeChannel(org, user, action) {
		_ = r.SendErrorEnvelope(403, "Permission denied", nil, "")
		return org, user, errors.New("permission denied")
	}
	return org, user, nil
}
