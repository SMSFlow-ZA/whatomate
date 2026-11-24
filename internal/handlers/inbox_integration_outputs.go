package handlers

import (
	"encoding/json"
	"time"

	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/inboxintegration"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// A provider receipt cannot overwrite a newer edit or a subsequent case. The
// full field batch and its marker commit together; no rule-triggering activity
// is emitted by integration results.
func (a *App) applyInboxIntegrationOutputs(tx *gorm.DB, execution models.InboxIntegrationExecution, now time.Time, review ...inboxIntegrationOutputReview) (bool, error) {
	tx = privateInboxIntegrationDB(tx)
	if len(review) > 1 {
		return false, errInboxTransition
	}
	if len(review) == 1 {
		// A signed reviewer acts with their current rights/context. The copy is
		// never saved back over the original external actor/request snapshot.
		execution.ActorID, execution.CaseDigest = review[0].ActorID, review[0].CaseDigest
	}
	if err := lockInboxFieldDefinitions(tx, execution.OrganizationID); err != nil {
		return false, err
	}
	thread := inboxThread{execution.OrganizationID, execution.AccountID, execution.ContactID}
	if err := thread.lock(tx); err != nil {
		return false, nil
	}
	c, err := thread.latest(tx.Clauses(clause.Locking{Strength: "UPDATE"}))
	if err != nil || c.ID != execution.CaseID {
		return false, nil
	}
	var scoped models.InboxCase
	if err := a.scopeInboxCases(tx.Model(&scoped), execution.ActorID, execution.OrganizationID).First(&scoped, "inbox_cases.id=?", c.ID).Error; err != nil {
		return false, nil
	}
	if !a.workflowActorActive(tx, execution.OrganizationID, execution.ActorID) || !a.HasPermission(execution.ActorID, models.ResourceChat, models.ActionWrite, execution.OrganizationID) || !a.HasPermission(execution.ActorID, models.ResourceCustomActions, models.ActionWrite, execution.OrganizationID) {
		return false, nil
	}
	digest, err := inboxSendCaseDigest(tx, c)
	if err != nil {
		return false, err
	}
	if digest != execution.CaseDigest {
		return false, nil
	}
	var run models.InboxWorkflowRun
	if err := tx.First(&run, "id=? AND organization_id=?", execution.RunID, execution.OrganizationID).Error; err != nil {
		return false, err
	}
	manual := len(review) == 1
	if !manual && run.State != inboxRunApplied && run.State != "integrating" {
		return false, nil
	}
	if manual && (!run.Sequenced || run.NextAction != execution.ActionIndex || (run.State != inboxSequenceRunning && run.State != inboxSequenceReview && run.State != inboxSequenceUnknown && run.State != inboxSequenceBlocked)) {
		return false, nil
	}
	var workflow models.InboxWorkflow
	if err := a.visibleInboxWorkflows(tx, execution.OrganizationID, execution.ActorID).First(&workflow, "id=?", run.WorkflowID).Error; err != nil {
		return false, nil
	}
	if !workflow.UpdatedAt.Equal(run.WorkflowUpdatedAt) || (workflow.Kind == "rule" && (workflow.EnabledAt == nil || !a.HasPermission(execution.ActorID, models.ResourceSettingsGeneral, models.ActionWrite, execution.OrganizationID))) {
		return false, nil
	}
	var definition models.InboxIntegration
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&definition, "id=? AND organization_id=?", execution.IntegrationID, execution.OrganizationID).Error; err != nil {
		return false, nil
	}
	endpoint, err := a.integrationEndpoint(definition.EndpointKey, execution.OrganizationID)
	if err != nil || integrationDefinitionDigest(definition, endpoint) != execution.DefinitionDigest {
		return false, nil
	}
	fields, err := a.integrationOutputFields(tx, definition, execution.ActorID)
	if err != nil {
		return false, nil
	}
	key, err := a.integrationEncryptionKey()
	if err != nil || !crypto.IsEncrypted(execution.EncryptedResult) {
		return false, nil
	}
	raw, err := crypto.Decrypt(execution.EncryptedResult, key)
	if err != nil || len(raw) > inboxintegration.MaxBody {
		return false, nil
	}
	var results map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &results) != nil {
		return false, nil
	}
	// Validate everything before the first write.
	values := make([]models.InboxFieldValue, 0, len(definition.Outputs))
	for _, output := range definition.Outputs {
		value, ok := results[output.Name]
		field := fields[output.DefinitionID]
		if !ok || field.ValidateValue(value) != nil || emptyInboxField(value) {
			return false, nil
		}
		subject := c.ID
		if field.Scope == "contact" {
			subject = c.ContactID
		}
		values = append(values, models.InboxFieldValue{OrganizationID: c.OrganizationID, DefinitionID: field.ID, SubjectID: subject, Value: value})
	}
	for _, value := range values {
		if err := tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&value).Error; err != nil {
			return false, err
		}
	}
	if len(values) > 0 {
		c.Version++
		if err := tx.Model(&c).Update("version", c.Version).Error; err != nil {
			return false, err
		}
	}
	if err := appendInboxActivity(tx, c, models.InboxCaseActivity{Kind: "workflow_integration_result", ActorID: &execution.ActorID, Detail: definition.Name, OccurredAt: &now}); err != nil {
		return false, err
	}
	digest, err = inboxSendCaseDigest(tx, c)
	if err != nil {
		return false, err
	}
	if err := tx.Model(&run).Update("case_digest", digest).Error; err != nil {
		return false, err
	}
	return true, nil
}
