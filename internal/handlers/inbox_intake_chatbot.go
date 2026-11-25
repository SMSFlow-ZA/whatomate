package handlers

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

var errIntakeStorage = errors.New("intake storage unavailable")

func (a *App) intakeRuntimeError(err error, session uuid.UUID) error {
	var problem inboxIntakeProblem
	if errors.As(err, &problem) {
		a.Log.Warn("Intake capture unavailable", "reason", problem.Reason, "session", session)
		return nil // Preserve the existing bot path; no field write is authorised.
	}
	if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, errInboxDisabled) {
		return nil
	}
	a.Log.Error("Intake storage unavailable", "code", audit.ErrorCode(err), "session", session)
	return errIntakeStorage
}

func (a *App) sessionHasIntakeStep(session *models.ChatbotSession) bool {
	if session.CurrentFlowID == nil || session.CurrentStep == "" {
		return false
	}
	var flow models.ChatbotFlow
	if err := privateInboxIntegrationDB(a.DB).Select("id,graph").First(&flow, "id=? AND organization_id=?", *session.CurrentFlowID, session.OrganizationID).Error; err != nil {
		return true // On lookup failure, prefer a fixed private-history marker.
	}
	graph, err := parseChatGraph(flow.Graph)
	return err == nil && graph != nil && graph.Node(session.CurrentStep) != nil && stringFromConfig(graph.Node(session.CurrentStep).Config, "intake_mapping_id") != ""
}

func (a *App) prepareChatIntake(node *ChatNode, ctx *chatNodeCtx) error {
	ctx.intakeBindingID = uuid.Nil
	raw := stringFromConfig(node.Config, "intake_mapping_id")
	if raw == "" {
		return nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return a.intakeRuntimeError(inboxIntakeProblem{"mapping_unavailable"}, ctx.session.ID)
	}
	if ctx.flow == nil || ctx.account == nil || ctx.contact == nil || ctx.session.Channel == "sms" {
		return a.intakeRuntimeError(inboxIntakeProblem{"unsupported_step"}, ctx.session.ID)
	}
	// Do not bind a transport switch, SMS fallback, or multi-card CTA as a
	// single WhatsApp response. Ordinary steps retain their existing options.
	if delivery, ok := node.Config["delivery_options"].(map[string]any); ok {
		channel, _ := delivery["channel"].(string)
		fallback, _ := delivery["fallback_enabled"].(bool)
		if (channel != "" && channel != "whatsapp") || fallback {
			return a.intakeRuntimeError(inboxIntakeProblem{"whatsapp_prompt_required"}, ctx.session.ID)
		}
	}
	entering := ctx.consumed || (ctx.userInput == "" && ctx.buttonID == "" && len(ctx.flowResponseData) == 0)
	if node.Type == ChatNodeButtons && ctx.buttonID == "" {
		entering = true
	}
	if !entering {
		return nil
	}
	binding, err := a.bindInboxIntake(*ctx.account, *ctx.contact, *ctx.session, *ctx.flow, *node, id, time.Now().UTC())
	if err != nil {
		return a.intakeRuntimeError(err, ctx.session.ID)
	}
	ctx.intakeBindingID = binding.ID
	return nil
}

func (a *App) captureChatIntake(node *ChatNode, ctx *chatNodeCtx) error {
	if stringFromConfig(node.Config, "intake_mapping_id") == "" || ctx.flow == nil || ctx.session.Channel == "sms" || ctx.session.ChannelInboundMessageID == nil {
		return nil
	}
	var binding models.InboxIntakeBinding
	db := privateInboxIntegrationDB(a.DB)
	err := db.Where("organization_id=? AND account_id=? AND contact_id=? AND session_id=? AND flow_id=? AND node_id=? AND state='pending'", ctx.account.OrganizationID, ctx.account.ID, ctx.contact.ID, ctx.session.ID, ctx.flow.ID, node.ID).Order("issued_at DESC,id DESC").First(&binding).Error
	if err != nil {
		return a.intakeRuntimeError(err, ctx.session.ID)
	}
	inputs := ctx.flowResponseData
	if node.Type != ChatNodeWhatsAppFlow {
		answer := ctx.userInput
		if ctx.buttonID != "" {
			valid := false
			for _, button := range buttonsFromConfig(node.Config) {
				if id, _ := button["id"].(string); id == ctx.buttonID {
					valid = true
					break
				}
			}
			if !valid {
				return a.intakeRuntimeError(inboxIntakeProblem{"response_mismatch"}, ctx.session.ID)
			}
			answer = ctx.buttonID
		}
		inputs = map[string]any{"answer": answer}
		var mapping models.InboxIntakeMapping
		if err := db.Select("consent_key").First(&mapping, "id=? AND organization_id=?", binding.MappingID, binding.OrganizationID).Error; err != nil {
			return a.intakeRuntimeError(err, ctx.session.ID)
		}
		if value, ok := ctx.session.SessionData[mapping.ConsentKey].(bool); ok {
			inputs[mapping.ConsentKey] = value
		}
	}
	response := inboxIntakeResponse{BindingID: binding.ID, OrganizationID: binding.OrganizationID, AccountID: binding.AccountID, ContactID: binding.ContactID, SessionID: binding.SessionID, EventKey: *ctx.session.ChannelInboundMessageID, ReplyToWAMID: ctx.session.IntakeReplyToWAMID, Inputs: inputs}
	if value, ok := ctx.flowResponseData["flow_token"].(string); ok {
		response.FlowToken = value
	}
	_, err = a.receiveInboxIntake(response, time.Now().UTC())
	if err != nil {
		return a.intakeRuntimeError(err, ctx.session.ID)
	}
	return nil
}
