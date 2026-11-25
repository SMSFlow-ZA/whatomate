package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/inboxpredicate"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

type inboxFieldDefinitionResponse struct {
	models.InboxFieldDefinition
	ConditionalRequired bool                          `json:"conditional_required"`
	RequiredWhen        *models.InboxFieldRequirement `json:"required_when,omitempty"`
}

func inboxFieldDefinitionResult(d models.InboxFieldDefinition, authoring bool) inboxFieldDefinitionResponse {
	response := inboxFieldDefinitionResponse{InboxFieldDefinition: d, ConditionalRequired: d.RequiredWhen != nil}
	if authoring {
		response.RequiredWhen = d.RequiredWhen
	}
	return response
}

func validateInboxFieldRequirement(db *gorm.DB, d models.InboxFieldDefinition) error {
	if d.RequiredWhen == nil {
		return nil
	}
	r := d.RequiredWhen
	if r.Validate() != nil {
		return errInboxTransition
	}
	if r.AccountID != nil {
		var account models.WhatsAppAccount
		if err := db.Select("id").First(&account, "id=? AND organization_id=?", r.AccountID, d.OrganizationID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errInboxTransition
			}
			return err
		}
	}
	if r.TeamID != nil {
		var team models.Team
		if err := db.Select("id").First(&team, "id=? AND organization_id=? AND is_active", r.TeamID, d.OrganizationID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errInboxTransition
			}
			return err
		}
	}
	definitions, err := inboxpredicate.Definitions(db, d.OrganizationID, r.Predicates, true)
	if err != nil {
		return inboxPredicateError(err)
	}
	for _, p := range r.Predicates {
		if p.DefinitionID != nil && definitions[*p.DefinitionID].ArchivedAt != nil {
			return errInboxTransition
		}
		if _, _, err := inboxpredicate.Compile(p, definitions); err != nil {
			return inboxPredicateError(err)
		}
	}
	return nil
}

// Definitions/values are read inside the caller's existing organisation-first
// transaction when resolving. The read-only completion screen may be refreshed;
// execution always rechecks. False/zero remain filled values. No history is read.
func inboxMissingDefinitions(db *gorm.DB, c models.InboxCase) ([]models.InboxFieldDefinition, error) {
	db = privateInboxIntegrationDB(db)
	ctx, cancel := context.WithTimeout(db.Statement.Context, 5*time.Second)
	defer cancel()
	query := db.Model(&models.InboxFieldDefinition{}).Where("organization_id=? AND archived_at IS NULL AND (required_on_resolve OR required_when IS NOT NULL)", c.OrganizationID).
		Where("NOT EXISTS (SELECT 1 FROM inbox_field_values v WHERE v.organization_id=inbox_field_definitions.organization_id AND v.definition_id=inbox_field_definitions.id AND v.subject_id=CASE WHEN inbox_field_definitions.scope='case' THEN ?::uuid ELSE ?::uuid END AND v.value IS NOT NULL AND v.value <> 'null'::jsonb AND (jsonb_typeof(v.value) <> 'string' OR btrim(v.value #>> '{}') <> ''))", c.ID, c.ContactID)
	var candidates []models.InboxFieldDefinition
	if err := query.Session(&gorm.Session{}).WithContext(ctx).Order("scope,label,id").Find(&candidates).Error; err != nil {
		return nil, err
	}
	matched := map[uuid.UUID]bool{}
	conditional := []models.InboxFieldDefinition{}
	predicates := []models.InboxPredicate{}
	for _, d := range candidates {
		if d.RequiredOnResolve {
			matched[d.ID] = true
			continue
		}
		r := d.RequiredWhen
		if r == nil {
			continue
		}
		if r.Validate() != nil {
			return nil, errInboxTransition
		}
		if (r.AccountID != nil && *r.AccountID != c.AccountID) || (r.TeamID != nil && (c.TeamID == nil || *r.TeamID != *c.TeamID)) {
			continue
		}
		if len(r.Predicates) == 0 {
			matched[d.ID] = true
			continue
		}
		conditional = append(conditional, d)
		predicates = append(predicates, r.Predicates...)
	}
	definitions, err := inboxpredicate.Definitions(db.WithContext(ctx), c.OrganizationID, predicates, true)
	if err != nil {
		return nil, inboxPredicateError(err)
	}
	//32 rules means at most64 jsonb_build_object arguments. Predicate values are
	// bound, schema is loaded once, and SQL work is bounded per batch.
	for start := 0; start < len(conditional); start += 32 {
		end := min(start+32, len(conditional))
		parts := []string{}
		args := []any{}
		for _, d := range conditional[start:end] {
			conditions := []string{}
			values := []any{}
			for _, p := range d.RequiredWhen.Predicates {
				clause, bound, err := inboxpredicate.Compile(p, definitions)
				if err != nil {
					return nil, inboxPredicateError(err)
				}
				conditions = append(conditions, "("+clause+")")
				values = append(values, bound...)
			}
			join := " AND "
			if d.RequiredWhen.Match == "any" {
				join = " OR "
			}
			parts = append(parts, "?::text,COALESCE(("+strings.Join(conditions, join)+"),false)")
			args = append(args, d.ID.String())
			args = append(args, values...)
		}
		// Project the proposed state, not a stale stored priority during an
		// atomic priority-plus-resolve transition. Custom values stay scoped to
		// this exact case/contact/organisation inside their existing SQL compiler.
		projection := db.Raw("SELECT ?::uuid AS id,?::uuid AS organization_id,?::uuid AS contact_id,?::text AS priority", c.ID, c.OrganizationID, c.ContactID, c.Priority)
		var raw []byte
		if err := db.WithContext(ctx).Table("(?) AS inbox_cases", projection).Select("jsonb_build_object("+strings.Join(parts, ",")+") AS matches", args...).Row().Scan(&raw); err != nil {
			return nil, err
		}
		matches := map[string]bool{}
		if err := json.Unmarshal(raw, &matches); err != nil {
			return nil, err
		}
		for _, d := range conditional[start:end] {
			if matches[d.ID.String()] {
				matched[d.ID] = true
			}
		}
	}
	result := make([]models.InboxFieldDefinition, 0, len(matched))
	for _, d := range candidates {
		if matched[d.ID] {
			result = append(result, d)
		}
	}
	return result, nil
}
