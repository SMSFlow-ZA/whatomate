package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SMS bots run only transactional graph nodes. External side effects must be
// represented by durable jobs before they can be supported here.
func validateSMSBotGraph(flow *models.ChatbotFlow) error {
	graph, err := parseChatGraph(flow.Graph)
	if err != nil || graph == nil {
		return fmt.Errorf("SMS bots require a valid graph")
	}
	for _, node := range graph.Nodes {
		switch node.Type {
		case ChatNodeStart, ChatNodeMessage, ChatNodePrompt, ChatNodeCondition, ChatNodeTiming, ChatNodeSetVariable, ChatNodeEnd:
		case ChatNodeButtons:
			if stringFromConfig(node.Config, "sms_text") == "" {
				return errors.New("SMS choices require an authored SMS version")
			}
			for _, b := range buttonsFromConfig(node.Config) {
				if kind, _ := b["type"].(string); kind != "" && kind != "reply" {
					return errors.New("SMS choices require text replies")
				}
			}
		default:
			return fmt.Errorf("node %s is unavailable for SMS bots", node.Type)
		}
		// A connection defines the SMS bot transport. A per-node transport switch
		// would escape the transaction and is therefore not allowed.
		if delivery, ok := node.Config["delivery_options"].(map[string]any); ok && len(delivery) > 0 {
			return fmt.Errorf("SMS bot nodes use the connection transport")
		}
	}
	return nil
}

func (a *App) sendBotText(node *ChatNode, state *chatNodeCtx, text string) error {
	if state.session.Channel != "sms" {
		return a.sendWhatsAppBotText(node, state, text)
	}
	if state.session.ChannelConnectionID == nil || state.session.ChannelInboundMessageID == nil {
		return errors.New("SMS bot connection and inbound message required")
	}
	id := uuid.NewSHA1(state.session.ID, []byte(state.session.ChannelInboundMessageID.String()+":"+node.ID))
	_, err := a.SendOutgoingMessage(context.Background(), OutgoingMessageRequest{Channel: "sms", ChannelConnectionID: *state.session.ChannelConnectionID, IdempotencyKey: id, Source: "bot", Contact: state.contact, Type: models.MessageTypeText, Content: text, ConsentConfirmed: state.session.SMSConsentConfirmed}, ChatbotSendOptions())
	return err
}

func (a *App) sendWhatsAppBotText(node *ChatNode, state *chatNodeCtx, text string) error {
	req := OutgoingMessageRequest{Account: state.account, Contact: state.contact, Type: models.MessageTypeText, Content: text, Source: "bot"}
	if err := prepareBotDelivery(node, state, &req); err != nil {
		return err
	}
	_, err := a.SendOutgoingMessage(context.Background(), req, ChatbotSendOptions())
	return err
}

func prepareBotDelivery(node *ChatNode, state *chatNodeCtx, req *OutgoingMessageRequest) error {
	raw, ok := node.Config["delivery_options"]
	if !ok {
		return nil
	}
	var options struct {
		Channel          string
		ConnectionID     uuid.UUID `json:"connection_id"`
		FallbackEnabled  bool      `json:"fallback_enabled"`
		ConsentConfirmed bool      `json:"consent_confirmed"`
		Text             string    `json:"sms_text"`
		TimeoutEnabled   bool      `json:"timeout_enabled"`
		TimeoutMinutes   int       `json:"timeout_minutes"`
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(data, &options); err != nil {
		return err
	}
	req.Source = "bot"
	if options.Channel != "" && options.Channel != "whatsapp" && options.Channel != "sms" {
		return errors.New("unsupported bot delivery channel")
	}
	if options.Channel == "sms" || options.FallbackEnabled {
		if state.session.ChannelInboundMessageID == nil || state.session.CurrentFlowID == nil {
			return errors.New("SMS bot actions require a stable inbound and flow identifier")
		}
		req.IdempotencyKey = uuid.NewSHA1(*state.session.CurrentFlowID, []byte(state.session.ChannelInboundMessageID.String()+":"+node.ID))
	}
	if options.Channel == "sms" {
		if req.Type != models.MessageTypeText {
			return errors.New("rich bot actions use WhatsApp with an authored SMS fallback")
		}
		req.Channel = "sms"
		req.ChannelConnectionID = options.ConnectionID
		req.ConsentConfirmed = options.ConsentConfirmed
	}
	if options.FallbackEnabled && req.Channel != "sms" {
		req.Fallback = &SMSFallbackOptions{Enabled: true, ConnectionID: options.ConnectionID, Text: processTemplate(options.Text, state.session.SessionData), ConsentConfirmed: options.ConsentConfirmed, TimeoutEnabled: options.TimeoutEnabled, TimeoutMinutes: options.TimeoutMinutes}
	}
	return nil
}

// ProcessSMSBotJobs commits the input, session transition, and every outbound
// enqueue in one transaction. It never invokes a provider or external HTTP.
func (a *App) ProcessSMSBotJobs(ctx context.Context, now time.Time) error {
	if a.Channels == nil || a.Channels.StopDispatch {
		return nil
	}
	for range 20 {
		var job models.ChannelBotJob
		changed := map[uuid.UUID]uuid.UUID{}
		err := a.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			// Only one bot transition per database at a time, including across
			// server processes. Preserve inbound order before taking a job lock.
			var claimed bool
			if err := tx.Raw("SELECT pg_try_advisory_xact_lock(hashtextextended('channel-bots-v1', 0))").Scan(&claimed).Error; err != nil {
				return err
			}
			if !claimed {
				return gorm.ErrRecordNotFound
			}
			if err := tx.Where("status = ?", "pending").Order("created_at, id").First(&job).Error; err != nil {
				return err
			}
			var conn models.ChannelConnection
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).First(&conn, "id = ? AND organization_id = ?", job.ConnectionID, job.OrganizationID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					job.ID = uuid.Nil
				}
				return err
			}
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&job, "id = ? AND status = 'pending'", job.ID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					job.ID = uuid.Nil
				}
				return err
			}
			if !conn.Enabled || conn.BotFlowID == nil || *conn.BotFlowID != job.FlowID || !conn.BotConsentConfirmed {
				return tx.Model(&job).Updates(map[string]any{"status": "blocked", "error_code": "bot_not_enabled"}).Error
			}
			var incoming models.Message
			if err := tx.First(&incoming, "id = ? AND organization_id = ? AND channel_connection_id = ? AND channel = 'sms' AND direction = 'incoming'", job.IncomingMessageID, job.OrganizationID, conn.ID).Error; err != nil {
				return err
			}
			var contact models.Contact
			if err := tx.First(&contact, "id = ? AND organization_id = ?", incoming.ContactID, job.OrganizationID).Error; err != nil {
				return err
			}
			if contact.SMSOptOut || contact.MarketingOptOut {
				return tx.Model(&job).Updates(map[string]any{"status": "blocked", "error_code": "recipient_suppressed"}).Error
			}
			var flow models.ChatbotFlow
			if err := tx.First(&flow, "id = ? AND organization_id = ? AND is_enabled = true", job.FlowID, job.OrganizationID).Error; err != nil {
				return err
			}
			if err := validateSMSBotGraph(&flow); err != nil {
				return err
			}
			var session models.ChatbotSession
			err := tx.Where("organization_id = ? AND contact_id = ? AND channel = 'sms' AND channel_connection_id = ? AND status = ? AND last_activity_at > ?", job.OrganizationID, contact.ID, conn.ID, models.SessionStatusActive, now.Add(-30*time.Minute)).Order("created_at DESC").First(&session).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				session = models.ChatbotSession{BaseModel: models.BaseModel{ID: uuid.NewSHA1(conn.ID, []byte(contact.ID.String()+":"+flow.ID.String()+":"+incoming.ID.String()))}, OrganizationID: job.OrganizationID, ContactID: contact.ID, Channel: "sms", ChannelConnectionID: &conn.ID, CurrentFlowID: &flow.ID, PhoneNumber: contact.PhoneNumber, Status: models.SessionStatusActive, SessionData: models.JSONB{}, StartedAt: now, LastActivityAt: now}
				if err := tx.Create(&session).Error; err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
			if session.CurrentFlowID == nil || *session.CurrentFlowID != flow.ID {
				return errors.New("SMS bot flow changed during session")
			}
			session.ChannelInboundMessageID = &incoming.ID
			session.SMSConsentConfirmed = true
			svc := *a.Channels
			svc.DB = tx
			svc.Notify = func(org, id uuid.UUID) { changed[id] = org }
			bot := &App{DB: tx, Log: a.Log, Channels: &svc}
			if err := bot.runChatGraph(nil, &contact, &session, &flow, incoming.Content, "", nil); err != nil {
				return err
			}
			bot.logSessionMessage(session.ID, models.DirectionIncoming, incoming.Content, "")
			return tx.Model(&job).Updates(map[string]any{"status": "done", "error_code": ""}).Error
		})
		if errors.Is(err, gorm.ErrRecordNotFound) && job.ID == uuid.Nil {
			return nil
		}
		if err != nil {
			if job.ID == uuid.Nil {
				return err
			}
			// Rollback preserves the old session and removes all outgoing queues.
			if updateErr := a.DB.WithContext(ctx).Model(&models.ChannelBotJob{}).Where("id = ? AND status = 'pending'", job.ID).Updates(map[string]any{"status": "blocked", "error_code": "bot_execution_failed"}).Error; updateErr != nil {
				return updateErr
			}
			continue
		}
		if a.Channels.Notify != nil {
			for id, org := range changed {
				a.Channels.Notify(org, id)
			}
		}
	}
	return nil
}
