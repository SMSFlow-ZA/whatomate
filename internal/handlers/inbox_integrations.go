package handlers

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/inboxintegration"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (a *App) integrationEndpoints() ([]inboxintegration.Endpoint, error) {
	endpoints := []inboxintegration.Endpoint{}
	if a.Config == nil || strings.TrimSpace(a.Config.InboxAutomation.IntegrationEndpoints) == "" {
		return endpoints, nil
	}
	raw := a.Config.InboxAutomation.IntegrationEndpoints
	if len(raw) > 16384 || json.Unmarshal([]byte(raw), &endpoints) != nil || len(endpoints) > 32 {
		return nil, errInboxTransition
	}
	seen := map[string]bool{}
	for _, endpoint := range endpoints {
		if endpoint.Validate() != nil || seen[endpoint.Key] {
			return nil, errInboxTransition
		}
		seen[endpoint.Key] = true
	}
	return endpoints, nil
}

func (a *App) integrationEndpoint(key string, org uuid.UUID) (inboxintegration.Endpoint, error) {
	endpoints, err := a.integrationEndpoints()
	if err != nil {
		return inboxintegration.Endpoint{}, err
	}
	for _, endpoint := range endpoints {
		if endpoint.Key == key && endpoint.AllowsOrganization(org) {
			return endpoint, nil
		}
	}
	return inboxintegration.Endpoint{}, errInboxTransition
}

func (a *App) integrationEncryptionKey() (string, error) {
	if a.Config == nil || len(a.Config.App.EncryptionKey) < 32 {
		return "", errors.New("integration encryption unavailable")
	}
	return a.Config.App.EncryptionKey, nil
}

func integrationDefinitionDigest(d models.InboxIntegration, endpoint inboxintegration.Endpoint) string {
	return workflowDigest([]any{d.ID, d.UpdatedAt, d.Name, d.Description, d.EndpointKey, d.AuthMode, d.Credential, d.Enabled, d.Inputs, d.Outputs, d.ReferenceKey, endpoint})
}

type inboxIntegrationRequest struct {
	UpdatedAt    *time.Time                      `json:"updated_at"`
	Name         string                          `json:"name"`
	Description  string                          `json:"description"`
	EndpointKey  string                          `json:"endpoint_key"`
	AuthMode     string                          `json:"auth_mode"`
	Credential   *string                         `json:"credential"`
	Enabled      bool                            `json:"enabled"`
	Inputs       []models.InboxIntegrationInput  `json:"inputs"`
	Outputs      []models.InboxIntegrationOutput `json:"outputs"`
	ReferenceKey string                          `json:"reference_key"`
}

func (a *App) validateInboxIntegration(db *gorm.DB, d models.InboxIntegration) error {
	if strings.TrimSpace(d.Name) == "" || utf8.RuneCountInString(d.Name) > 100 || utf8.RuneCountInString(d.Description) > 1000 || len(d.Inputs) > 32 || len(d.Outputs) > 16 || (d.ReferenceKey != "" && !inboxintegration.Identifier(d.ReferenceKey)) {
		return errInboxTransition
	}
	if _, err := a.integrationEndpoint(d.EndpointKey, d.OrganizationID); err != nil {
		return errInboxTransition
	}
	if d.AuthMode != "none" && d.AuthMode != "bearer" && d.AuthMode != "api_key" {
		return errInboxTransition
	}
	if d.AuthMode != "none" && !crypto.IsEncrypted(d.Credential) {
		return errInboxTransition
	}
	if d.AuthMode == "none" && d.Credential != "" {
		return errInboxTransition
	}
	seen := map[string]bool{}
	for _, input := range d.Inputs {
		if !inboxintegration.Identifier(input.Name) || seen[input.Name] {
			return errInboxTransition
		}
		switch input.Kind {
		case "text", "number", "date", "boolean":
		default:
			return errInboxTransition
		}
		seen[input.Name] = true
	}
	seen = map[string]bool{}
	fields := map[uuid.UUID]bool{}
	for _, output := range d.Outputs {
		if !inboxintegration.Identifier(output.Name) || seen[output.Name] || output.DefinitionID == uuid.Nil || fields[output.DefinitionID] {
			return errInboxTransition
		}
		seen[output.Name], fields[output.DefinitionID] = true, true
		var field models.InboxFieldDefinition
		if err := db.First(&field, "id=? AND organization_id=? AND archived_at IS NULL", output.DefinitionID, d.OrganizationID).Error; err != nil {
			return err
		}
	}
	return nil
}

func (a *App) validateInboxWorkflowIntegrations(db *gorm.DB, org uuid.UUID, w models.InboxWorkflow) error {
	for _, action := range w.Actions {
		if action.Type != "integration" {
			continue
		}
		var definition models.InboxIntegration
		if err := db.First(&definition, "id=? AND organization_id=?", action.Value, org).Error; err != nil {
			return err
		}
		inputs := map[string]bool{}
		for _, input := range definition.Inputs {
			inputs[input.Name] = true
			if input.Required {
				if _, ok := action.IntegrationInputs[input.Name]; !ok {
					return errInboxTransition
				}
			}
		}
		for name := range action.IntegrationInputs {
			if !inputs[name] {
				return errInboxTransition
			}
		}
	}
	return nil
}

func (a *App) ListInboxIntegrations(r *fastglue.Request) error {
	viewer, ok := a.freshInboxAuthority(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if !ok {
		return nil
	}
	org := viewer.Org
	definitions := []models.InboxIntegration{}
	if err := a.DB.Where("organization_id=?", org).Order("name,id").Limit(101).Find(&definitions).Error; err != nil {
		return a.inboxError(r, err)
	}
	views := []map[string]any{}
	for _, d := range definitions {
		views = append(views, inboxIntegrationView(d))
	}
	endpoints, err := a.integrationEndpoints()
	if err != nil {
		return a.inboxError(r, err)
	}
	endpointViews := []map[string]any{}
	for _, endpoint := range endpoints {
		if endpoint.AllowsOrganization(org) {
			endpointViews = append(endpointViews, map[string]any{"key": endpoint.Key, "name": endpoint.Name, "method": endpoint.Method, "destination": endpoint.DisplayDestination(), "supports_idempotency": endpoint.IdempotencyHeader != ""})
		}
	}
	return r.SendEnvelope(map[string]any{"integrations": views, "endpoints": endpointViews})
}

func inboxIntegrationView(d models.InboxIntegration) map[string]any {
	return map[string]any{"id": d.ID, "name": d.Name, "description": d.Description, "endpoint_key": d.EndpointKey, "auth_mode": d.AuthMode,
		"has_credential": d.Credential != "", "enabled": d.Enabled, "inputs": d.Inputs, "outputs": d.Outputs, "reference_key": d.ReferenceKey, "updated_at": d.UpdatedAt}
}

func (a *App) ListInboxIntegrationCatalogue(r *fastglue.Request) error {
	org, _, ok := a.inboxFeatureAuth(r, models.ActionRead)
	if !ok {
		return nil
	}
	definitions := []models.InboxIntegration{}
	if err := a.DB.Select("id,name,description,inputs,enabled").Where("organization_id=?", org).Order("name,id").Limit(100).Find(&definitions).Error; err != nil {
		return a.inboxError(r, err)
	}
	return r.SendEnvelope(map[string]any{"integrations": definitions})
}

func (a *App) CreateInboxIntegration(r *fastglue.Request) error {
	return a.saveInboxIntegration(r, true)
}
func (a *App) UpdateInboxIntegration(r *fastglue.Request) error {
	return a.saveInboxIntegration(r, false)
}

func (a *App) saveInboxIntegration(r *fastglue.Request, create bool) error {
	viewer, ok := a.freshInboxAuthority(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if !ok {
		return nil
	}
	org, actor := viewer.Org, viewer.User
	key, err := a.integrationEncryptionKey()
	if err != nil {
		return r.SendErrorEnvelope(503, "Secure integration storage is unavailable. Contact your operator.", nil, "integration_storage_unavailable")
	}
	var input inboxIntegrationRequest
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	id := uuid.New()
	if !create {
		id, err = parsePathUUID(r, "integration_id", "Integration")
		if err != nil {
			return nil
		}
	}
	var d models.InboxIntegration
	err = privateInboxIntegrationDB(a.DB).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?,0))", "inbox-integrations:"+org.String()).Error; err != nil {
			return err
		}
		if create {
			var count int64
			if err := tx.Model(&models.InboxIntegration{}).Where("organization_id=?", org).Count(&count).Error; err != nil {
				return err
			}
			if count >= 100 {
				return errInboxTransition
			}
			d = models.InboxIntegration{BaseModel: models.BaseModel{ID: id}, OrganizationID: org, CreatedByID: actor}
		} else if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&d, "id=? AND organization_id=?", id, org).Error; err != nil {
			return err
		}
		if !create && (input.UpdatedAt == nil || !input.UpdatedAt.UTC().Truncate(time.Microsecond).Equal(d.UpdatedAt.UTC().Truncate(time.Microsecond))) {
			return errInboxConflict
		}
		d.Name, d.Description, d.EndpointKey, d.AuthMode = strings.TrimSpace(input.Name), strings.TrimSpace(input.Description), input.EndpointKey, input.AuthMode
		d.Enabled = !create && input.Enabled // definitions start disabled
		d.Inputs, d.Outputs, d.ReferenceKey = input.Inputs, input.Outputs, input.ReferenceKey
		if d.Inputs == nil {
			d.Inputs = []models.InboxIntegrationInput{}
		}
		if d.Outputs == nil {
			d.Outputs = []models.InboxIntegrationOutput{}
		}
		if input.Credential != nil {
			if len(*input.Credential) > 8192 || strings.ContainsAny(*input.Credential, "\r\n") {
				return errInboxTransition
			}
			d.Credential, err = crypto.Encrypt(*input.Credential, key)
			if err != nil {
				return err
			}
		}
		if d.AuthMode == "none" {
			d.Credential = ""
		}
		if err := a.validateInboxIntegration(tx, d); err != nil {
			return err
		}
		reason := audit.ConfigurationChanged
		if create {
			if err := tx.Create(&d).Error; err != nil {
				return err
			}
			reason = audit.Created
		} else {
			if err := tx.Save(&d).Error; err != nil {
				return err
			}
			if !d.Enabled {
				reason = audit.Disabled
			}
			if input.Credential != nil {
				reason = audit.CredentialRotated
			}
		}
		return a.recordIntegrationSecurity(r, tx, org, actor, "inbox_integration", d.ID, audit.IntegrationChanged, reason)
	})
	if err != nil {
		if errors.Is(err, errInboxConflict) {
			return r.SendErrorEnvelope(409, "This integration changed. Cancel, refresh the integration list, then reopen it to review the latest settings before saving.", nil, "integration_changed")
		}
		return a.inboxError(r, err)
	}
	// Only safe metadata enters configuration audit; never the credential,
	// request mapping values or provider response.
	auditAction := models.AuditActionUpdated
	if create {
		auditAction = models.AuditActionCreated
	}
	a.logAudit(org, actor, "inbox_integration", d.ID, auditAction, nil, map[string]any{"name": d.Name, "enabled": d.Enabled, "endpoint_key": d.EndpointKey})
	return r.SendEnvelope(map[string]any{"integration": inboxIntegrationView(d)})
}

func (a *App) DeleteInboxIntegration(r *fastglue.Request) error {
	viewer, ok := a.freshInboxAuthority(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if !ok {
		return nil
	}
	org, actor := viewer.Org, viewer.User
	id, err := parsePathUUID(r, "integration_id", "Integration")
	if err != nil {
		return nil
	}
	err = privateInboxIntegrationDB(a.DB).Transaction(func(tx *gorm.DB) error {
		var d models.InboxIntegration
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&d, "id=? AND organization_id=?", id, org).Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&models.InboxWorkflow{}).Where("organization_id=? AND EXISTS (SELECT 1 FROM jsonb_array_elements(actions) a WHERE a->>'type'='integration' AND a->>'value'=?)", org, id.String()).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return errInboxConflict
		}
		if err := tx.Model(&models.InboxIntegrationExecution{}).Where("integration_id=? AND organization_id=? AND state IN ?", id, org, []string{"pending", "submitting", "unknown"}).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return errInboxConflict
		}
		if err := a.recordIntegrationSecurity(r, tx, org, actor, "inbox_integration", d.ID, audit.IntegrationChanged, audit.Deleted); err != nil {
			return err
		}
		return tx.Delete(&d).Error
	})
	if err != nil {
		return a.inboxError(r, err)
	}
	a.logAudit(org, actor, "inbox_integration", id, models.AuditActionDeleted, nil, nil)
	return r.SendEnvelope(map[string]bool{"deleted": true})
}

func (a *App) GetInboxIntegrationSettings(r *fastglue.Request) error {
	viewer, ok := a.freshInboxAuthority(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if !ok {
		return nil
	}
	org := viewer.Org
	var organization models.Organization
	if err := a.DB.Select("id,settings").First(&organization, "id=?", org).Error; err != nil {
		return a.inboxError(r, err)
	}
	enabled, _ := organization.Settings["inbox_integrations_enabled"].(bool)
	return r.SendEnvelope(map[string]bool{"enabled": enabled, "dispatch_available": a.Config != nil && a.Config.InboxAutomation.IntegrationDispatchEnabled})
}

func (a *App) UpdateInboxIntegrationSettings(r *fastglue.Request) error {
	viewer, ok := a.freshInboxAuthority(r, models.ResourceSettingsGeneral, models.ActionWrite)
	if !ok {
		return nil
	}
	org, actor := viewer.Org, viewer.User
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	if input.Enabled == nil {
		return a.inboxError(r, errInboxTransition)
	}
	reason := audit.ConfigurationChanged
	if !*input.Enabled {
		reason = audit.Disabled
	}
	err := a.mutateAuditedIntegration(r, org, actor, "inbox_integration_settings", &org, reason, func(tx *gorm.DB) error {
		return tx.Model(&models.Organization{}).Where("id=?", org).Update("settings", gorm.Expr("jsonb_set(COALESCE(settings,'{}'::jsonb),'{inbox_integrations_enabled}',to_jsonb(?::boolean),true)", *input.Enabled)).Error
	})
	if err != nil {
		return a.inboxError(r, err)
	}
	a.logAudit(org, actor, "inbox_integration_settings", org, models.AuditActionUpdated, nil, map[string]any{"enabled": *input.Enabled})
	return r.SendEnvelope(map[string]bool{"enabled": *input.Enabled})
}
