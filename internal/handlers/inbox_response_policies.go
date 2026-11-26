package handlers

import (
	"database/sql"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/inboxpredicate"
	"github.com/shridarpatil/whatomate/internal/inboxquality"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

type responsePolicyInput struct {
	Revision    int64                        `json:"revision"`
	Name        string                       `json:"name"`
	Description string                       `json:"description"`
	Enabled     bool                         `json:"enabled"`
	Conditions  models.InboxPolicyConditions `json:"conditions"`
	Targets     models.InboxSLAPolicy        `json:"targets"`
}

var errResponsePolicyLimit = errors.New("response policy limit reached")

func (a *App) responsePolicyError(r *fastglue.Request, err error) error {
	switch {
	case errors.Is(err, errResponsePolicyLimit):
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "This inbox has reached its conditional policy limit. Archive a policy before adding another.", nil, "response_policy_limit")
	case errors.Is(err, errInboxTransition):
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Check the policy name, target durations, calendar and matching conditions.", nil, "invalid_response_policy")
	case errors.Is(err, errInboxConflict):
		return r.SendErrorEnvelope(fasthttp.StatusConflict, "The policy or its order has changed. Refresh the list before trying again.", nil, "response_policy_conflict")
	case errors.Is(err, gorm.ErrRecordNotFound):
		return r.SendErrorEnvelope(fasthttp.StatusNotFound, "Response policy or sample case not found in this inbox.", nil, "response_policy_not_found")
	default:
		return a.inboxError(r, err)
	}
}

// Account-wide policy configuration requires the existing full inbox-read
// permission as well as settings access; it never expands an agent's case scope.
func (a *App) responsePolicyContext(r *fastglue.Request, action string) (uuid.UUID, uuid.UUID, uuid.UUID, bool) {
	viewer, ok := a.freshInboxAuthority(r, models.ResourceSettingsGeneral, action)
	if !ok {
		return uuid.Nil, uuid.Nil, uuid.Nil, false
	}
	org, actor := viewer.Org, viewer.User
	if !viewer.has(models.ResourceChat, models.ActionRead) || !viewer.has(models.ResourceContacts, models.ActionRead) {
		_ = r.SendErrorEnvelope(fasthttp.StatusForbidden, "Response policy configuration requires full inbox read access", nil, "")
		return uuid.Nil, uuid.Nil, uuid.Nil, false
	}
	account, err := parsePathUUID(r, "account_id", "WhatsApp account")
	if err != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, false
	}
	if _, err := findByIDAndOrg[models.WhatsAppAccount](a.DB, r, account, org, "WhatsApp account"); err != nil {
		return uuid.Nil, uuid.Nil, uuid.Nil, false
	}
	return org, actor, account, true
}

func validateResponseDefinition(db *gorm.DB, org uuid.UUID, input responsePolicyInput) error {
	if strings.TrimSpace(input.Name) == "" || utf8.RuneCountInString(input.Name) > 100 || strings.IndexFunc(input.Name, unicode.IsControl) >= 0 || utf8.RuneCountInString(input.Description) > 1000 {
		return errInboxTransition
	}
	if inboxquality.ValidatePolicyConditions(input.Conditions) != nil || validateInboxPolicy(input.Targets) != nil {
		return errInboxTransition
	}
	if input.Conditions.TeamID != nil {
		var team models.Team
		if err := db.First(&team, "id=? AND organization_id=? AND is_active", input.Conditions.TeamID, org).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errInboxTransition
			}
			return err
		}
	}
	definitions, err := inboxpredicate.Definitions(db, org, input.Conditions.Predicates, true)
	if err != nil {
		return inboxPredicateError(err)
	}
	for _, predicate := range input.Conditions.Predicates {
		if _, _, err := inboxpredicate.Compile(predicate, definitions); err != nil {
			return inboxPredicateError(err)
		}
	}
	return nil
}

func saveResponseVersion(tx *gorm.DB, row *models.InboxResponsePolicy, input responsePolicyInput) error {
	input.Targets.OrganizationID, input.Targets.AccountID, input.Targets.VersionID = row.OrganizationID, row.AccountID, nil
	definition := models.InboxResponseDefinition{PolicyID: row.ID, Name: strings.TrimSpace(input.Name), Description: input.Description, Conditions: input.Conditions}
	version := models.InboxSLAVersion{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: row.OrganizationID, AccountID: row.AccountID, Policy: input.Targets, Selection: &definition}
	if err := tx.Create(&version).Error; err != nil {
		return err
	}
	row.VersionID = version.ID
	return nil
}

func (a *App) ListInboxResponsePolicies(r *fastglue.Request) error {
	org, _, account, ok := a.responsePolicyContext(r, models.ActionRead)
	if !ok {
		return nil
	}
	policies, err := inboxquality.LoadResponsePolicies(a.DB, org, account, true)
	if err != nil {
		return a.responsePolicyError(r, err)
	}
	return r.SendEnvelope(map[string]any{"policies": policies, "maximum": inboxquality.MaxResponsePolicies})
}

func (a *App) CreateInboxResponsePolicy(r *fastglue.Request) error {
	org, _, account, ok := a.responsePolicyContext(r, models.ActionWrite)
	if !ok {
		return nil
	}
	var input responsePolicyInput
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	row := models.InboxResponsePolicy{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org, AccountID: account, Revision: 1}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := inboxquality.PolicyLock(tx, org, account); err != nil {
			return err
		}
		if err := validateResponseDefinition(tx, org, input); err != nil {
			return err
		}
		var count int64
		if err := tx.Model(&models.InboxResponsePolicy{}).Where("organization_id=? AND account_id=?", org, account).Count(&count).Error; err != nil {
			return err
		}
		if count >= inboxquality.MaxResponsePolicies {
			return errResponsePolicyLimit
		}
		row.Position = int(count)
		if err := saveResponseVersion(tx, &row, input); err != nil {
			return err
		}
		return tx.Create(&row).Error // All new definitions start disabled.
	})
	if err != nil {
		return a.responsePolicyError(r, err)
	}
	return r.SendEnvelope(map[string]any{"policy": row})
}

func (a *App) UpdateInboxResponsePolicy(r *fastglue.Request) error {
	org, _, account, ok := a.responsePolicyContext(r, models.ActionWrite)
	if !ok {
		return nil
	}
	id, err := parsePathUUID(r, "id", "response policy")
	if err != nil {
		return nil
	}
	var input responsePolicyInput
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	var row models.InboxResponsePolicy
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if err := inboxquality.PolicyLock(tx, org, account); err != nil {
			return err
		}
		if err := tx.First(&row, "id=? AND organization_id=? AND account_id=?", id, org, account).Error; err != nil {
			return err
		}
		if row.Revision != input.Revision {
			return errInboxConflict
		}
		if err := validateResponseDefinition(tx, org, input); err != nil {
			return err
		}
		if err := saveResponseVersion(tx, &row, input); err != nil {
			return err
		}
		row.Revision++
		row.Enabled = input.Enabled
		return tx.Save(&row).Error
	})
	if err != nil {
		return a.responsePolicyError(r, err)
	}
	return r.SendEnvelope(map[string]any{"policy": row})
}

type responsePolicyRevision struct {
	ID       uuid.UUID `json:"id"`
	Revision int64     `json:"revision"`
}

func (a *App) ReorderInboxResponsePolicies(r *fastglue.Request) error {
	org, _, account, ok := a.responsePolicyContext(r, models.ActionWrite)
	if !ok {
		return nil
	}
	var input struct {
		Order []responsePolicyRevision `json:"order"`
	}
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	if len(input.Order) > inboxquality.MaxResponsePolicies {
		return a.responsePolicyError(r, errInboxTransition)
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := inboxquality.PolicyLock(tx, org, account); err != nil {
			return err
		}
		var rows []models.InboxResponsePolicy
		if err := tx.Where("organization_id=? AND account_id=?", org, account).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) != len(input.Order) {
			return errInboxConflict
		}
		byID := map[uuid.UUID]models.InboxResponsePolicy{}
		for _, row := range rows {
			byID[row.ID] = row
		}
		for position, item := range input.Order {
			row, exists := byID[item.ID]
			if !exists || row.Revision != item.Revision {
				return errInboxConflict
			}
			delete(byID, item.ID)
			if row.Position != position {
				if err := tx.Model(&row).Updates(map[string]any{"position": position, "revision": gorm.Expr("revision+1")}).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return a.responsePolicyError(r, err)
	}
	return r.SendEnvelope(map[string]any{"reordered": true})
}

func (a *App) CloneInboxResponsePolicy(r *fastglue.Request) error {
	org, _, account, ok := a.responsePolicyContext(r, models.ActionWrite)
	if !ok {
		return nil
	}
	id, err := parsePathUUID(r, "id", "response policy")
	if err != nil {
		return nil
	}
	var input responsePolicyRevision
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	var clone models.InboxResponsePolicy
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if err := inboxquality.PolicyLock(tx, org, account); err != nil {
			return err
		}
		candidates, err := inboxquality.LoadResponsePolicies(tx, org, account, true)
		if err != nil {
			return err
		}
		if len(candidates) >= inboxquality.MaxResponsePolicies {
			return errResponsePolicyLimit
		}
		for _, source := range candidates {
			if source.ID != id {
				continue
			}
			if source.Revision != input.Revision {
				return errInboxConflict
			}
			clone = models.InboxResponsePolicy{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org, AccountID: account, Position: len(candidates), Revision: 1}
			name := []rune(source.Definition.Name)
			if len(name) > 93 {
				name = name[:93]
			}
			copy := responsePolicyInput{Name: string(name) + " (copy)", Description: source.Definition.Description, Conditions: source.Definition.Conditions, Targets: source.Targets}
			if err := validateResponseDefinition(tx, org, copy); err != nil {
				return err
			}
			if err := saveResponseVersion(tx, &clone, copy); err != nil {
				return err
			}
			return tx.Create(&clone).Error
		}
		return gorm.ErrRecordNotFound
	})
	if err != nil {
		return a.responsePolicyError(r, err)
	}
	return r.SendEnvelope(map[string]any{"policy": clone})
}

func (a *App) DeleteInboxResponsePolicy(r *fastglue.Request) error {
	org, _, account, ok := a.responsePolicyContext(r, models.ActionWrite)
	if !ok {
		return nil
	}
	id, err := parsePathUUID(r, "id", "response policy")
	if err != nil {
		return nil
	}
	var input responsePolicyRevision
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	err = a.DB.Transaction(func(tx *gorm.DB) error {
		if err := inboxquality.PolicyLock(tx, org, account); err != nil {
			return err
		}
		var row models.InboxResponsePolicy
		if err := tx.First(&row, "id=? AND organization_id=? AND account_id=?", id, org, account).Error; err != nil {
			return err
		}
		if row.Revision != input.Revision {
			return errInboxConflict
		}
		if err := tx.Delete(&row).Error; err != nil {
			return err
		}
		var remaining []models.InboxResponsePolicy
		if err := tx.Where("organization_id=? AND account_id=?", org, account).Order("position,id").Find(&remaining).Error; err != nil {
			return err
		}
		for position, item := range remaining {
			if item.Position != position {
				if err := tx.Model(&item).Updates(map[string]any{"position": position, "revision": gorm.Expr("revision+1")}).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return a.responsePolicyError(r, err)
	}
	return r.SendEnvelope(map[string]any{"archived": true})
}

func (a *App) PreviewInboxResponsePolicies(r *fastglue.Request) error {
	org, actor, account, ok := a.responsePolicyContext(r, models.ActionRead)
	if !ok {
		return nil
	}
	var input struct {
		CaseID uuid.UUID `json:"case_id"`
	}
	if a.decodeRequest(r, &input) != nil {
		return nil
	}
	var sample models.InboxCase
	var evaluations []inboxquality.ResponseEvaluation
	var fallback *models.InboxSLAPolicy
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := a.scopeInboxCases(tx.Model(&models.InboxCase{}), actor, org).Select("inbox_cases.*").First(&sample, "inbox_cases.id=? AND inbox_cases.account_id=?", input.CaseID, account).Error; err != nil {
			return err
		}
		candidates, err := inboxquality.LoadResponsePolicies(tx, org, account, true)
		if err != nil {
			return err
		}
		evaluations, err = inboxquality.EvaluateResponsePolicies(tx, sample, candidates)
		if err != nil {
			return err
		}
		var active models.InboxSLAPolicy
		err = tx.First(&active, "organization_id=? AND account_id=?", org, account).Error
		if err == nil {
			fallback = &active
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return a.responsePolicyError(r, err)
	}
	return r.SendEnvelope(map[string]any{"case_id": sample.ID, "account_id": account, "existing_policy_version": sample.SLAPolicyID, "prospective_only": true, "evaluations": evaluations, "default_policy": fallback})
}
