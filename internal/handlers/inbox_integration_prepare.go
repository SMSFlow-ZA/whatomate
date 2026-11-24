package handlers

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/inboxintegration"
	"github.com/shridarpatil/whatomate/internal/inboxtext"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

type inboxIntegrationProblem struct{ Reason string }

func (e inboxIntegrationProblem) Error() string { return "integration unavailable: " + e.Reason }

type inboxIntegrationFieldPreview struct {
	ID    uuid.UUID `json:"id"`
	Label string    `json:"label"`
	Scope string    `json:"scope"`
	Key   string    `json:"key"`
}

type inboxIntegrationIntent struct {
	ID             uuid.UUID                      `json:"id"`
	Name           string                         `json:"name"`
	Destination    string                         `json:"destination"`
	Method         string                         `json:"method"`
	Inputs         map[string]json.RawMessage     `json:"inputs"`
	DeferredInputs map[string][]string            `json:"deferred_inputs,omitempty"`
	Outputs        []inboxIntegrationFieldPreview `json:"outputs"`
	Available      bool                           `json:"available"`
	Reason         string                         `json:"reason,omitempty"`
}

type preparedInboxIntegration struct {
	CaseID, AccountID, ContactID, ActorID uuid.UUID
	Intent                                inboxIntegrationIntent
	Definition                            models.InboxIntegration
	Endpoint                              inboxintegration.Endpoint
	Body                                  []byte
	DefinitionDigest                      string
	RequestDigest                         string
}

func (a *App) integrationOutputFields(db *gorm.DB, d models.InboxIntegration, actor uuid.UUID) (map[uuid.UUID]models.InboxFieldDefinition, error) {
	ids := make([]uuid.UUID, 0, len(d.Outputs))
	for _, output := range d.Outputs {
		ids = append(ids, output.DefinitionID)
	}
	fields := map[uuid.UUID]models.InboxFieldDefinition{}
	if len(ids) == 0 {
		return fields, nil
	}
	var definitions []models.InboxFieldDefinition
	if err := db.Where("organization_id=? AND id IN ? AND archived_at IS NULL", d.OrganizationID, ids).Find(&definitions).Error; err != nil {
		return nil, err
	}
	for _, field := range definitions {
		if field.Scope == "contact" && (!a.HasPermission(actor, models.ResourceContacts, models.ActionRead, d.OrganizationID) || !a.HasPermission(actor, models.ResourceContacts, models.ActionWrite, d.OrganizationID)) {
			return nil, inboxIntegrationProblem{"contact_field_permission_required"}
		}
		fields[field.ID] = field
	}
	if len(fields) != len(ids) {
		return nil, inboxIntegrationProblem{"output_fields_unavailable"}
	}
	return fields, nil
}

// Preparing is read-only and uses exactly the same bounded value resolver as
// message personalisation. No credential or network request enters the preview.
func (a *App) prepareInboxIntegration(db *gorm.DB, c models.InboxCase, actor, id uuid.UUID, mappings map[string]string, deferred ...map[string]bool) (preparedInboxIntegration, error) {
	var prepared preparedInboxIntegration
	prepared.CaseID, prepared.AccountID, prepared.ContactID, prepared.ActorID = c.ID, c.AccountID, c.ContactID, actor
	if !a.workflowActorActive(db, c.OrganizationID, actor) || !a.HasPermission(actor, models.ResourceChat, models.ActionWrite, c.OrganizationID) || !a.HasPermission(actor, models.ResourceCustomActions, models.ActionWrite, c.OrganizationID) {
		return prepared, inboxIntegrationProblem{"permission_required"}
	}
	var scoped models.InboxCase
	if err := a.scopeInboxCases(db.Model(&scoped), actor, c.OrganizationID).First(&scoped, "inbox_cases.id=?", c.ID).Error; err != nil {
		return prepared, err
	}
	if err := db.First(&prepared.Definition, "id=? AND organization_id=?", id, c.OrganizationID).Error; err != nil {
		return prepared, err
	}
	d := prepared.Definition
	endpoint, err := a.integrationEndpoint(d.EndpointKey, c.OrganizationID)
	if err != nil {
		return prepared, inboxIntegrationProblem{"endpoint_unavailable"}
	}
	prepared.Endpoint = endpoint
	prepared.DefinitionDigest = integrationDefinitionDigest(d, endpoint)
	prepared.Intent = inboxIntegrationIntent{ID: d.ID, Name: d.Name, Destination: endpoint.DisplayDestination(), Method: endpoint.Method, Inputs: map[string]json.RawMessage{}, Outputs: []inboxIntegrationFieldPreview{}}
	fields, err := a.integrationOutputFields(db, d, actor)
	if err != nil {
		return prepared, err
	}
	for _, output := range d.Outputs {
		field := fields[output.DefinitionID]
		prepared.Intent.Outputs = append(prepared.Intent.Outputs, inboxIntegrationFieldPreview{field.ID, field.Label, field.Scope, field.Scope + ".field." + field.Key})
	}
	if len(mappings) > 32 {
		return prepared, inboxIntegrationProblem{"inputs_invalid"}
	}
	inputByName := map[string]models.InboxIntegrationInput{}
	for _, input := range d.Inputs {
		inputByName[input.Name] = input
	}
	keys := make([]string, 0, len(mappings))
	for name := range mappings {
		if _, ok := inputByName[name]; !ok {
			return prepared, inboxIntegrationProblem{"inputs_invalid"}
		}
		keys = append(keys, name)
	}
	sort.Strings(keys)
	texts := make([]string, 0, len(keys))
	total := 0
	for _, name := range keys {
		total += len(mappings[name])
		texts = append(texts, mappings[name])
	}
	if total > inboxintegration.MaxBody {
		return prepared, inboxIntegrationProblem{"inputs_invalid"}
	}
	variables, err := inboxtext.KeysForTexts(texts)
	if err != nil {
		return prepared, inboxIntegrationProblem{"inputs_invalid"}
	}
	account, contact, err := a.inboxWorkflowContext(db, c)
	if err != nil {
		return prepared, err
	}
	values, err := a.inboxMessageValues(db, c, account, contact, actor, variables, deferred...)
	if err != nil {
		return prepared, err
	}
	for _, input := range d.Inputs {
		source, exists := mappings[input.Name]
		if !exists {
			if input.Required {
				return prepared, inboxIntegrationProblem{"inputs_missing"}
			}
			continue
		}
		rendered, err := inboxtext.Render(source, values)
		if err != nil {
			return prepared, inboxIntegrationProblem{"inputs_invalid"}
		}
		if len(rendered.Missing) > 0 {
			return prepared, inboxIntegrationProblem{"inputs_missing"}
		}
		pending, err := inboxDeferredDependencies([]string{source}, deferred...)
		if err != nil {
			return prepared, inboxIntegrationProblem{"inputs_invalid"}
		}
		if len(pending) != 0 {
			if prepared.Intent.DeferredInputs == nil {
				prepared.Intent.DeferredInputs = map[string][]string{}
			}
			// Never display or approve a stale field value as the future request.
			// Known independent inputs still undergo ordinary validation below.
			prepared.Intent.DeferredInputs[input.Name] = pending
			continue
		}
		if strings.TrimSpace(rendered.Text) == "" {
			if input.Required {
				return prepared, inboxIntegrationProblem{"inputs_missing"}
			}
			continue
		}
		var raw json.RawMessage
		if input.Kind == "number" || input.Kind == "boolean" {
			raw = json.RawMessage(strings.TrimSpace(rendered.Text))
		} else {
			raw, err = json.Marshal(rendered.Text)
			if err != nil {
				return prepared, err
			}
		}
		if (models.InboxFieldDefinition{Kind: input.Kind}).ValidateValue(raw) != nil || string(raw) == "null" {
			return prepared, inboxIntegrationProblem{"inputs_invalid"}
		}
		prepared.Intent.Inputs[input.Name] = raw
	}
	prepared.Body, err = json.Marshal(prepared.Intent.Inputs)
	if err != nil {
		return prepared, err
	}
	if len(prepared.Body) > inboxintegration.MaxBody {
		return prepared, inboxIntegrationProblem{"inputs_invalid"}
	}
	prepared.RequestDigest = workflowDigest([]any{prepared.DefinitionDigest, json.RawMessage(prepared.Body)})
	if len(prepared.Intent.DeferredInputs) != 0 {
		// The intent can be reviewed, but no exact request exists to authorise.
		prepared.Body, prepared.RequestDigest = nil, ""
	}
	prepared.Intent.Available = true
	if !d.Enabled {
		prepared.Intent.Available = false
		prepared.Intent.Reason = "integration_disabled"
	}
	if _, err := a.integrationEncryptionKey(); err != nil {
		prepared.Intent.Available = false
		prepared.Intent.Reason = "encryption_unavailable"
	}
	var organization models.Organization
	if err := db.Select("id,settings,inbox_workflows_enabled").First(&organization, "id=?", c.OrganizationID).Error; err != nil {
		return prepared, err
	}
	orgEnabled, _ := organization.Settings["inbox_integrations_enabled"].(bool)
	if !organization.InboxWorkflowsEnabled || !orgEnabled || a.Config == nil || !a.Config.InboxAutomation.IntegrationDispatchEnabled {
		prepared.Intent.Available = false
		prepared.Intent.Reason = "dispatch_disabled"
	}
	return prepared, nil
}

func integrationProblemReason(err error) string {
	var problem inboxIntegrationProblem
	if errors.As(err, &problem) {
		return problem.Reason
	}
	return "integration_unavailable"
}
