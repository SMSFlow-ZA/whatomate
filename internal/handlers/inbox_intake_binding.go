package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/middleware"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type inboxIntakeProblem struct{ Reason string }

func (e inboxIntakeProblem) Error() string { return "intake unavailable: " + e.Reason }

// Reuse the fresh database identity loader. Its name reflects its first caller;
// no audit permission is implied or required by loading an intake owner.
func intakeAuthority(db *gorm.DB, org, owner uuid.UUID, claims ...*middleware.JWTClaims) error {
	var current *middleware.JWTClaims
	if len(claims) == 1 {
		current = claims[0]
	}
	v, err := loadAuditViewer(db, org, owner, current)
	if errors.Is(err, errAuditUnauthenticated) || errors.Is(err, errAuditForbidden) {
		return inboxIntakeProblem{"permission_required"}
	}
	if err != nil {
		return err
	}
	for _, pair := range [][2]string{{models.ResourceSettingsGeneral, models.ActionWrite}, {models.ResourceFlowsChatbot, models.ActionWrite}, {models.ResourceChat, models.ActionWrite}, {models.ResourceContacts, models.ActionRead}, {models.ResourceContacts, models.ActionWrite}} {
		if !v.has(pair[0], pair[1]) {
			return inboxIntakeProblem{"permission_required"}
		}
	}
	return nil
}

func boundedIntakeDB(db *gorm.DB) (*gorm.DB, context.CancelFunc) {
	parent := db.Statement.Context
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	return privateInboxIntegrationDB(db).WithContext(ctx), cancel
}

func intakeDefinitions(db *gorm.DB, mapping models.InboxIntakeMapping) (map[uuid.UUID]models.InboxFieldDefinition, error) {
	ids := make([]uuid.UUID, len(mapping.Fields))
	for i, field := range mapping.Fields {
		ids[i] = field.DefinitionID
	}
	var rows []models.InboxFieldDefinition
	if err := db.Where("organization_id=? AND id IN ?", mapping.OrganizationID, ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make(map[uuid.UUID]models.InboxFieldDefinition, len(rows))
	for _, row := range rows {
		result[row.ID] = row
	}
	return result, nil
}

func intakeFieldStamp(db *gorm.DB, org, definition, subject uuid.UUID) (int64, error) {
	if subject == uuid.Nil {
		return 0, nil
	}
	var row models.InboxFieldStamp
	err := db.First(&row, "organization_id=? AND definition_id=? AND subject_id=?", org, definition, subject).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	return row.Revision, err
}

// bindInboxIntake runs BEFORE a prompt can be dispatched. All later input must
// identify this binding through a quoted message or opaque WhatsApp Flow token.
// There is intentionally no public callback or caller-supplied field ID here.
func (a *App) bindInboxIntake(account models.WhatsAppAccount, contact models.Contact, session models.ChatbotSession, flow models.ChatbotFlow, node ChatNode, mappingID uuid.UUID, now time.Time) (models.InboxIntakeBinding, error) {
	var binding models.InboxIntakeBinding
	if mappingID == uuid.Nil || session.ID == uuid.Nil || (session.Channel != "" && session.Channel != "whatsapp") || session.OrganizationID != account.OrganizationID || contact.OrganizationID != account.OrganizationID || session.ContactID != contact.ID || session.WhatsAppAccount != account.Name || len(node.ID) > 100 {
		return binding, inboxIntakeProblem{"context_unavailable"}
	}
	if node.Type != ChatNodePrompt && node.Type != ChatNodeButtons && node.Type != ChatNodeWhatsAppFlow {
		return binding, inboxIntakeProblem{"unsupported_step"}
	}
	if _, err := a.integrationEncryptionKey(); err != nil {
		return binding, inboxIntakeProblem{"encryption_unavailable"}
	}
	db, cancel := boundedIntakeDB(a.DB)
	defer cancel()
	err := db.Transaction(func(tx *gorm.DB) error {
		enabled, err := inboxWorkflowEnabled(tx, account.OrganizationID, true)
		if err != nil {
			return err
		}
		if !enabled {
			return errInboxDisabled
		}
		thread := inboxThread{account.OrganizationID, account.ID, contact.ID}
		if err := thread.lock(tx); err != nil {
			return err
		}
		var ownedAccount models.WhatsAppAccount
		if err := tx.Select("id,name").First(&ownedAccount, "id=? AND organization_id=?", account.ID, account.OrganizationID).Error; err != nil {
			return err
		}
		if ownedAccount.Name != account.Name {
			return inboxIntakeProblem{"context_unavailable"}
		}
		var active models.ChatbotSession
		if err := tx.Select("id").First(&active, "id=? AND organization_id=? AND contact_id=? AND whats_app_account=? AND channel='whatsapp' AND status=?", session.ID, account.OrganizationID, contact.ID, account.Name, models.SessionStatusActive).Error; err != nil {
			return err
		}
		var actualFlow models.ChatbotFlow
		if err := tx.First(&actualFlow, "id=? AND organization_id=? AND is_enabled AND channel='whatsapp'", flow.ID, account.OrganizationID).Error; err != nil {
			return err
		}
		if workflowDigest(actualFlow.Graph) != workflowDigest(flow.Graph) || (actualFlow.WhatsAppAccount != "" && actualFlow.WhatsAppAccount != account.Name) {
			return inboxIntakeProblem{"flow_changed"}
		}
		graph, err := parseChatGraph(actualFlow.Graph)
		if err != nil || graph == nil || graph.Node(node.ID) == nil || graph.Node(node.ID).Type != node.Type || stringFromConfig(graph.Node(node.ID).Config, "intake_mapping_id") != mappingID.String() {
			return inboxIntakeProblem{"mapping_unavailable"}
		}
		var mapping models.InboxIntakeMapping
		if err := tx.First(&mapping, "id=? AND organization_id=?", mappingID, account.OrganizationID).Error; err != nil {
			return err
		}
		if !mapping.Enabled || mapping.Validate() != nil {
			return inboxIntakeProblem{"mapping_disabled"}
		}
		if err := intakeAuthority(tx, mapping.OrganizationID, mapping.OwnerID); err != nil {
			return err
		}
		definitions, err := intakeDefinitions(tx, mapping)
		if err != nil {
			return err
		}
		var staged int64
		if err := tx.Model(&models.InboxIntakeBinding{}).Where("organization_id=? AND session_id=? AND state='staged'", account.OrganizationID, session.ID).Count(&staged).Error; err != nil {
			return err
		}
		if staged >= 20 {
			return inboxIntakeProblem{"handoff_required"}
		}
		binding = models.InboxIntakeBinding{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: account.OrganizationID, AccountID: account.ID, ContactID: contact.ID, SessionID: session.ID, FlowID: flow.ID, FlowDigest: workflowDigest(actualFlow.Graph), NodeID: node.ID, MappingID: mapping.ID, MappingRevision: mapping.Revision, State: "pending", IssuedAt: now.UTC(), ExpiresAt: now.UTC().Add(24 * time.Hour)}
		current, err := thread.latest(tx)
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if current.ID != uuid.Nil {
			binding.PreviousCaseID, binding.PreviousCaseVersion, binding.PreviousCaseResolved = &current.ID, current.Version, current.Status == models.InboxCaseResolved
		}
		for _, input := range mapping.Fields {
			d, exists := definitions[input.DefinitionID]
			if !exists || d.ArchivedAt != nil {
				return inboxIntakeProblem{"field_unavailable"}
			}
			subject := contact.ID
			if d.Scope == "case" {
				subject = uuid.Nil
				if current.ID != uuid.Nil && current.Status != models.InboxCaseResolved {
					subject = current.ID
				}
			}
			stamp, err := intakeFieldStamp(tx, mapping.OrganizationID, d.ID, subject)
			if err != nil {
				return err
			}
			binding.Snapshots = append(binding.Snapshots, models.InboxIntakeFieldSnapshot{DefinitionID: d.ID, DefinitionRevision: d.Revision, DefinitionUpdatedAt: d.UpdatedAt, Scope: d.Scope, Stamp: stamp})
		}
		// A fresh prompt invalidates older unanswered bindings at this exact
		// session/node. Already staged answers retain their independent receipt.
		if err := tx.Model(&models.InboxIntakeBinding{}).Where("organization_id=? AND session_id=? AND node_id=? AND state='pending'", binding.OrganizationID, binding.SessionID, binding.NodeID).Updates(map[string]any{"state": "superseded", "encrypted_answers": ""}).Error; err != nil {
			return err
		}
		return tx.Create(&binding).Error
	})
	return binding, err
}

// intakeCaseUnchanged permits only the ordinary inbound increments that
// happened while gathering answers. Human/automation edits cannot be disguised
// by a later inbound event, and a later case cannot borrow an earlier binding.
func intakeCaseUnchanged(tx *gorm.DB, binding models.InboxIntakeBinding, before models.InboxCase) (bool, error) {
	if before.ID == uuid.Nil {
		return binding.PreviousCaseID == nil, nil
	}
	if binding.PreviousCaseID != nil && before.ID == *binding.PreviousCaseID {
		if binding.PreviousCaseResolved {
			return before.Status == models.InboxCaseResolved && before.Version == binding.PreviousCaseVersion, nil
		}
		if before.Status == models.InboxCaseResolved {
			return false, nil
		}
		var received int64
		err := tx.Model(&models.InboxCaseActivity{}).Where("organization_id=? AND account_id=? AND case_id=? AND case_version>? AND kind IN ?", binding.OrganizationID, binding.AccountID, before.ID, binding.PreviousCaseVersion, []string{"incoming_message", "customer_replied"}).Distinct("case_version").Count(&received).Error
		return before.Version == binding.PreviousCaseVersion+received, err
	}
	if before.OpenedAt.Before(binding.IssuedAt) || before.Status == models.InboxCaseResolved || (binding.PreviousCaseID != nil && !binding.PreviousCaseResolved) {
		return false, nil
	}
	var previous models.InboxCase
	err := (inboxThread{binding.OrganizationID, binding.AccountID, binding.ContactID}).query(tx).
		Where("(opened_at,id)<(?,?)", before.OpenedAt, before.ID).Order("opened_at DESC,id DESC").First(&previous).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, err
	}
	if binding.PreviousCaseID == nil && previous.ID != uuid.Nil {
		return false, nil
	}
	if binding.PreviousCaseID != nil && previous.ID != *binding.PreviousCaseID {
		return false, nil
	}
	var received int64
	err = tx.Model(&models.InboxCaseActivity{}).Where("organization_id=? AND account_id=? AND case_id=? AND case_version>1 AND kind IN ?", binding.OrganizationID, binding.AccountID, before.ID, []string{"incoming_message", "customer_replied"}).Distinct("case_version").Count(&received).Error
	return before.Version == 1+received, err
}

// applyStagedInboxIntake participates in the existing organisation/contact/case
// transaction at an explicit chatbot handoff. It never manufactures a case,
// emits a rule trigger, clears a field, or sends a message.
func (a *App) applyStagedInboxIntake(tx *gorm.DB, c, before models.InboxCase, sessionID uuid.UUID, now time.Time) error {
	if sessionID == uuid.Nil {
		return nil
	}
	tx, cancel := boundedIntakeDB(tx)
	defer cancel()
	if c.ID == uuid.Nil || c.Status == models.InboxCaseResolved || (before.ID != uuid.Nil && (before.OrganizationID != c.OrganizationID || before.AccountID != c.AccountID || before.ContactID != c.ContactID)) {
		return inboxIntakeProblem{"context_unavailable"}
	}
	current, err := (inboxThread{c.OrganizationID, c.AccountID, c.ContactID}).latest(tx)
	if err != nil {
		return err
	}
	if current.ID != c.ID || current.Version != c.Version {
		return inboxIntakeProblem{"case_changed"}
	}
	var session models.ChatbotSession
	if err := tx.Select("id").First(&session, "id=? AND organization_id=? AND contact_id=? AND whats_app_account=(SELECT name FROM whatsapp_accounts WHERE id=? AND organization_id=? AND deleted_at IS NULL) AND channel='whatsapp' AND status=?", sessionID, c.OrganizationID, c.ContactID, c.AccountID, c.OrganizationID, models.SessionStatusActive).Error; err != nil {
		return err
	}
	var bindings []models.InboxIntakeBinding
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("organization_id=? AND account_id=? AND contact_id=? AND session_id=? AND case_id IS NULL", c.OrganizationID, c.AccountID, c.ContactID, sessionID).Order("(state='staged') DESC,issued_at,id").Limit(200).Find(&bindings).Error; err != nil {
		return err
	}
	for i := range bindings {
		binding := &bindings[i]
		finish := func(state string) error {
			binding.State, binding.EncryptedAnswers, binding.CaseID = state, "", &c.ID
			if state != "applied" {
				for i := range binding.Outcomes {
					if binding.Outcomes[i].Status == "pending_handoff" {
						binding.Outcomes[i].Status = state
					}
				}
			}
			return tx.Save(binding).Error
		}
		if binding.State != "staged" {
			state := binding.State
			if state == "pending" {
				state = "response_unverified"
			}
			if err := finish(state); err != nil {
				return err
			}
			continue
		}
		if !now.Before(binding.ExpiresAt) {
			if err := finish("expired"); err != nil {
				return err
			}
			continue
		}
		unchanged, err := intakeCaseUnchanged(tx, *binding, before)
		if err != nil {
			return err
		}
		if !unchanged {
			if err := finish("case_changed"); err != nil {
				return err
			}
			continue
		}
		if !binding.ConsentConfirmed || binding.ResponseAt == nil {
			if err := finish("consent_required"); err != nil {
				return err
			}
			continue
		}
		var revoked int64
		if err := tx.Model(&models.InboxIntakeBinding{}).Where("organization_id=? AND account_id=? AND contact_id=? AND session_id=? AND mapping_id=? AND mapping_revision=? AND response_at>=? AND id<>? AND NOT consent_confirmed", binding.OrganizationID, binding.AccountID, binding.ContactID, binding.SessionID, binding.MappingID, binding.MappingRevision, *binding.ResponseAt, binding.ID).Count(&revoked).Error; err != nil {
			return err
		}
		if revoked > 0 {
			if err := finish("consent_required"); err != nil {
				return err
			}
			continue
		}
		var mapping models.InboxIntakeMapping
		err = tx.First(&mapping, "id=? AND organization_id=?", binding.MappingID, binding.OrganizationID).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err != nil || !mapping.Enabled || mapping.Revision != binding.MappingRevision || mapping.Validate() != nil {
			if err := finish("mapping_changed"); err != nil {
				return err
			}
			continue
		}
		if err := intakeAuthority(tx, mapping.OrganizationID, mapping.OwnerID); err != nil {
			var problem inboxIntakeProblem
			if !errors.As(err, &problem) {
				return err
			}
			if err := finish("permission_required"); err != nil {
				return err
			}
			continue
		}
		var flow models.ChatbotFlow
		err = tx.First(&flow, "id=? AND organization_id=? AND is_enabled AND channel='whatsapp'", binding.FlowID, binding.OrganizationID).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err != nil || workflowDigest(flow.Graph) != binding.FlowDigest {
			if err := finish("flow_changed"); err != nil {
				return err
			}
			continue
		}
		key, err := a.integrationEncryptionKey()
		if err != nil {
			return inboxIntakeProblem{"encryption_unavailable"}
		}
		plain, err := crypto.Decrypt(binding.EncryptedAnswers, key)
		if err != nil {
			return inboxIntakeProblem{"staging_unavailable"}
		}
		answers := map[string]json.RawMessage{}
		if len(plain) > 65536 || json.Unmarshal([]byte(plain), &answers) != nil {
			return inboxIntakeProblem{"staging_unavailable"}
		}
		definitions, err := intakeDefinitions(tx, mapping)
		if err != nil {
			return err
		}
		snapshots := map[uuid.UUID]models.InboxIntakeFieldSnapshot{}
		for _, snapshot := range binding.Snapshots {
			snapshots[snapshot.DefinitionID] = snapshot
		}
		for j := range binding.Outcomes {
			outcome := &binding.Outcomes[j]
			if outcome.Status != "pending_handoff" {
				continue
			}
			d, exists := definitions[outcome.DefinitionID]
			snapshot, snapExists := snapshots[outcome.DefinitionID]
			value, hasAnswer := answers[outcome.DefinitionID.String()]
			if !exists || !snapExists || !hasAnswer || d.ArchivedAt != nil || d.Scope != snapshot.Scope || d.Revision != snapshot.DefinitionRevision || !d.UpdatedAt.Equal(snapshot.DefinitionUpdatedAt) {
				outcome.Status = "definition_changed"
				continue
			}
			subject := c.ID
			if d.Scope == "contact" {
				subject = c.ContactID
			}
			stamp, err := intakeFieldStamp(tx, c.OrganizationID, d.ID, subject)
			if err != nil {
				return err
			}
			if stamp != snapshot.Stamp {
				outcome.Status = "field_changed"
				continue
			}
			if d.ValidateValue(value) != nil || emptyInboxField(value) {
				outcome.Status = "invalid_value"
				continue
			}
			field := models.InboxFieldValue{OrganizationID: c.OrganizationID, DefinitionID: d.ID, SubjectID: subject, Value: value}
			if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&field).Error; err != nil {
				return err
			}
			outcome.Status = "saved"
		}
		state := "applied"
		saved, warnings := 0, false
		for _, outcome := range binding.Outcomes {
			if outcome.Status == "saved" {
				saved++
			} else if outcome.Status != "optional_missing" {
				warnings = true
			}
		}
		if warnings {
			state = "applied_with_warnings"
			if saved == 0 {
				state = "needs_review"
			}
		}
		if err := finish(state); err != nil {
			return err
		}
	}
	return nil
}

type inboxIntakeResponse struct {
	BindingID, OrganizationID, AccountID, ContactID, SessionID, EventKey uuid.UUID
	FlowToken, ReplyToWAMID                                              string
	Inputs                                                               map[string]any
}

// receiveInboxIntake is called only by the signed inbound webhook's graph path.
// It stages validated answers; human-queue entry performs a second fresh check.
func (a *App) receiveInboxIntake(response inboxIntakeResponse, now time.Time) (models.InboxIntakeBinding, error) {
	var binding models.InboxIntakeBinding
	if response.EventKey == uuid.Nil || response.BindingID == uuid.Nil {
		return binding, inboxIntakeProblem{"response_unavailable"}
	}
	db, cancel := boundedIntakeDB(a.DB)
	defer cancel()
	err := db.Transaction(func(tx *gorm.DB) error {
		enabled, err := inboxWorkflowEnabled(tx, response.OrganizationID, true)
		if err != nil {
			return err
		}
		if !enabled {
			return errInboxDisabled
		}
		if err := (inboxThread{response.OrganizationID, response.AccountID, response.ContactID}).lock(tx); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&binding, "id=? AND organization_id=? AND account_id=? AND contact_id=? AND session_id=?", response.BindingID, response.OrganizationID, response.AccountID, response.ContactID, response.SessionID).Error; err != nil {
			return err
		}
		if binding.State != "pending" {
			return nil
		}
		finish := func(reason string) error {
			binding.State, binding.EncryptedAnswers = reason, ""
			binding.ResponseKey = &response.EventKey
			return tx.Save(&binding).Error
		}
		if now.Before(binding.IssuedAt) || !now.Before(binding.ExpiresAt) {
			return finish("expired")
		}
		var session models.ChatbotSession
		if err := tx.Select("id,current_flow_id,current_step").First(&session, "id=? AND organization_id=? AND contact_id=? AND whats_app_account=(SELECT name FROM whatsapp_accounts WHERE id=? AND organization_id=? AND deleted_at IS NULL) AND channel='whatsapp' AND status=?", binding.SessionID, binding.OrganizationID, binding.ContactID, binding.AccountID, binding.OrganizationID, models.SessionStatusActive).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return finish("session_changed")
			}
			return err
		}
		if session.CurrentFlowID == nil || *session.CurrentFlowID != binding.FlowID || session.CurrentStep != binding.NodeID {
			return finish("session_changed")
		}
		var flow models.ChatbotFlow
		if err := tx.First(&flow, "id=? AND organization_id=? AND is_enabled AND channel='whatsapp'", binding.FlowID, binding.OrganizationID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return finish("flow_changed")
			}
			return err
		}
		if workflowDigest(flow.Graph) != binding.FlowDigest {
			return finish("flow_changed")
		}
		graph, err := parseChatGraph(flow.Graph)
		if err != nil || graph == nil || graph.Node(binding.NodeID) == nil {
			return finish("flow_changed")
		}
		if graph.Node(binding.NodeID).Type == ChatNodeWhatsAppFlow {
			if response.FlowToken != binding.ID.String() {
				return inboxIntakeProblem{"response_mismatch"}
			}
		} else {
			var quoted int64
			if response.ReplyToWAMID == "" {
				return inboxIntakeProblem{"quoted_reply_required"}
			}
			if err := tx.Model(&models.Message{}).Where("id=? AND organization_id=? AND contact_id=? AND whats_app_account=? AND direction=? AND whats_app_message_id=?", binding.ID, binding.OrganizationID, binding.ContactID, flow.WhatsAppAccount, models.DirectionOutgoing, response.ReplyToWAMID).Count(&quoted).Error; err != nil {
				return err
			}
			// Flows may be organisation-wide; the bound account's current name
			// is authoritative rather than the optional flow account restriction.
			if flow.WhatsAppAccount == "" {
				if err := tx.Model(&models.Message{}).Where("id=? AND organization_id=? AND contact_id=? AND whats_app_account=(SELECT name FROM whatsapp_accounts WHERE id=? AND organization_id=? AND deleted_at IS NULL) AND direction=? AND whats_app_message_id=?", binding.ID, binding.OrganizationID, binding.ContactID, binding.AccountID, binding.OrganizationID, models.DirectionOutgoing, response.ReplyToWAMID).Count(&quoted).Error; err != nil {
					return err
				}
			}
			if quoted != 1 {
				return inboxIntakeProblem{"response_mismatch"}
			}
		}
		var mapping models.InboxIntakeMapping
		if err := tx.First(&mapping, "id=? AND organization_id=?", binding.MappingID, binding.OrganizationID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return finish("mapping_changed")
			}
			return err
		}
		if !mapping.Enabled || mapping.Revision != binding.MappingRevision || mapping.Validate() != nil {
			return finish("mapping_changed")
		}
		if err := intakeAuthority(tx, mapping.OrganizationID, mapping.OwnerID); err != nil {
			return err
		}
		binding.ResponseAt = &now
		binding.ConsentConfirmed, _ = response.Inputs[mapping.ConsentKey].(bool)
		if !binding.ConsentConfirmed {
			return finish("consent_required")
		}
		definitions, err := intakeDefinitions(tx, mapping)
		if err != nil {
			return err
		}
		answers := map[string]json.RawMessage{}
		for _, input := range mapping.Fields {
			outcome := models.InboxIntakeOutcome{DefinitionID: input.DefinitionID, Status: "optional_missing"}
			d, exists := definitions[input.DefinitionID]
			value, present := response.Inputs[input.Input]
			raw, marshalError := intakeAnswerValue(d, value)
			switch {
			case !exists || d.ArchivedAt != nil:
				outcome.Status = "definition_unavailable"
			case !present || (marshalError == nil && emptyInboxField(raw)):
				if input.Required {
					outcome.Status = "required_missing"
				}
			case marshalError != nil || len(raw) > 32768 || d.ValidateValue(raw) != nil:
				outcome.Status = "invalid_value"
			default:
				outcome.Status = "pending_handoff"
				answers[input.DefinitionID.String()] = raw
			}
			binding.Outcomes = append(binding.Outcomes, outcome)
		}
		raw, err := json.Marshal(answers)
		if err != nil {
			return err
		}
		if len(raw) > 65536 {
			return finish("answers_too_large")
		}
		key, err := a.integrationEncryptionKey()
		if err != nil {
			return inboxIntakeProblem{"encryption_unavailable"}
		}
		binding.EncryptedAnswers, err = crypto.Encrypt(string(raw), key)
		if err != nil {
			return err
		}
		binding.State, binding.ResponseKey = "staged", &response.EventKey
		return tx.Save(&binding).Error
	})
	return binding, err
}
