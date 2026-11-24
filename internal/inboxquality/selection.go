package inboxquality

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/inboxpredicate"
	"github.com/shridarpatil/whatomate/internal/models"
	"gorm.io/gorm"
)

const MaxResponsePolicies = 20

var ErrPolicyConditions = errors.New("invalid or unavailable response policy conditions")

type ResponseCandidate struct {
	models.InboxResponsePolicy
	Definition models.InboxResponseDefinition `json:"definition"`
	Targets    models.InboxSLAPolicy          `json:"targets"`
}

// ConditionOutcome exposes facts about a match, never customer field values.
type ConditionOutcome struct {
	Field        string     `json:"field"`
	DefinitionID *uuid.UUID `json:"definition_id,omitempty"`
	Label        string     `json:"label,omitempty"`
	Matched      bool       `json:"matched"`
}

type ResponseEvaluation struct {
	PolicyID   uuid.UUID          `json:"policy_id"`
	VersionID  uuid.UUID          `json:"version_id"`
	Name       string             `json:"name"`
	Position   int                `json:"position"`
	Enabled    bool               `json:"enabled"`
	Matched    bool               `json:"matched"`
	Selected   bool               `json:"selected"`
	Reason     string             `json:"reason"`
	Conditions []ConditionOutcome `json:"conditions"`
}

func ValidatePolicyConditions(conditions models.InboxPolicyConditions) error {
	if len(conditions.Predicates) > 20 || (conditions.Match != "" && conditions.Match != "all" && conditions.Match != "any") ||
		(conditions.TeamID != nil && *conditions.TeamID == uuid.Nil) {
		return ErrPolicyConditions
	}
	encoded, err := json.Marshal(conditions)
	if err != nil || len(encoded) > 65536 {
		return ErrPolicyConditions
	}
	for _, predicate := range conditions.Predicates {
		if predicate.Field != "priority" && predicate.Field != "custom_field" {
			return ErrPolicyConditions
		}
	}
	return nil
}

// LoadResponsePolicies retains disabled candidates for configuration/preview,
// but runtime selection never lets an invalid disabled draft poison queue entry.
func LoadResponsePolicies(db *gorm.DB, org, account uuid.UUID, includeDisabled bool) ([]ResponseCandidate, error) {
	return loadResponsePolicies(db, org, account, includeDisabled, true)
}

func loadResponsePolicies(db *gorm.DB, org, account uuid.UUID, includeDisabled, includeTargets bool) ([]ResponseCandidate, error) {
	var rows []models.InboxResponsePolicy
	query := db.Where("organization_id=? AND account_id=?", org, account)
	if !includeDisabled {
		query = query.Where("enabled")
	}
	if err := query.Order("position,id").Limit(MaxResponsePolicies + 1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) > MaxResponsePolicies {
		return nil, errors.New("response policy count exceeds the supported limit")
	}
	result := make([]ResponseCandidate, 0, len(rows))
	if len(rows) == 0 {
		return result, nil
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.VersionID)
	}
	var versions []models.InboxSLAVersion
	versionQuery := db.Where("organization_id=? AND account_id=? AND id IN ?", org, account, ids)
	if !includeTargets {
		// Queue entry needs the criteria, not every candidate's full calendar.
		// Sync reads only the selected immutable target version afterwards.
		versionQuery = versionQuery.Select("id", "organization_id", "account_id", "selection")
	}
	if err := versionQuery.Find(&versions).Error; err != nil {
		return nil, err
	}
	byID := make(map[uuid.UUID]models.InboxSLAVersion, len(versions))
	for _, version := range versions {
		byID[version.ID] = version
	}
	for _, row := range rows {
		version, found := byID[row.VersionID]
		if !found || version.Selection == nil || version.Selection.PolicyID != row.ID || (includeTargets && version.Policy.AccountID != account) {
			return nil, errors.New("response policy version is unavailable")
		}
		if err := ValidatePolicyConditions(version.Selection.Conditions); err != nil {
			return nil, err
		}
		result = append(result, ResponseCandidate{InboxResponsePolicy: row, Definition: *version.Selection, Targets: version.Policy})
	}
	return result, nil
}

// EvaluateResponsePolicies is read-only. Callers authorise and load the sample
// case before calling it; runtime callers hold its row and account policy locks.
// All candidate conditions are evaluated in one scoped query plus one batched
// definition lookup, rather than one database round trip per condition/policy.
func EvaluateResponsePolicies(db *gorm.DB, c models.InboxCase, candidates []ResponseCandidate) ([]ResponseEvaluation, error) {
	if c.ID == uuid.Nil || c.OrganizationID == uuid.Nil || c.AccountID == uuid.Nil || len(candidates) > MaxResponsePolicies {
		return nil, ErrPolicyConditions
	}
	if len(candidates) == 0 {
		return []ResponseEvaluation{}, nil
	}
	candidates = slices.Clone(candidates)
	slices.SortFunc(candidates, func(a, b ResponseCandidate) int {
		if a.Position != b.Position {
			return cmp.Compare(a.Position, b.Position)
		}
		return strings.Compare(a.ID.String(), b.ID.String())
	})
	predicates := []models.InboxPredicate{}
	for _, candidate := range candidates {
		if candidate.OrganizationID != c.OrganizationID || candidate.AccountID != c.AccountID || candidate.Definition.PolicyID != candidate.ID {
			return nil, ErrPolicyConditions
		}
		if err := ValidatePolicyConditions(candidate.Definition.Conditions); err != nil {
			return nil, err
		}
		predicates = append(predicates, candidate.Definition.Conditions.Predicates...)
	}
	definitions, err := inboxpredicate.Definitions(db, c.OrganizationID, predicates, true)
	if err != nil {
		return nil, err
	}
	selections := []string{"inbox_cases.id AS present"}
	args := []any{}
	for i, candidate := range candidates {
		if candidate.Definition.Conditions.TeamID != nil {
			selections = append(selections, fmt.Sprintf("EXISTS (SELECT 1 FROM teams t WHERE t.id=?::uuid AND t.organization_id=inbox_cases.organization_id AND t.id=inbox_cases.team_id AND t.is_active AND t.deleted_at IS NULL) AS t%d", i))
			args = append(args, *candidate.Definition.Conditions.TeamID)
		}
		for j, predicate := range candidate.Definition.Conditions.Predicates {
			expression, values, err := inboxpredicate.Compile(predicate, definitions)
			if err != nil {
				return nil, err
			}
			selections = append(selections, fmt.Sprintf("(%s) AS p%d_%d", expression, i, j))
			args = append(args, values...)
		}
	}
	flags := map[string]any{}
	query := db.Model(&models.InboxCase{}).
		Joins("JOIN contacts c ON c.id=inbox_cases.contact_id AND c.organization_id=inbox_cases.organization_id AND c.deleted_at IS NULL").
		Joins("JOIN whatsapp_accounts wa ON wa.id=inbox_cases.account_id AND wa.organization_id=inbox_cases.organization_id AND wa.deleted_at IS NULL").
		Where("inbox_cases.organization_id=? AND inbox_cases.account_id=? AND inbox_cases.id=?", c.OrganizationID, c.AccountID, c.ID).
		Select(strings.Join(selections, ","), args...).Scan(&flags)
	if query.Error != nil {
		return nil, query.Error
	}
	if query.RowsAffected != 1 {
		return nil, gorm.ErrRecordNotFound
	}
	result := make([]ResponseEvaluation, 0, len(candidates))
	selected := false
	for i, candidate := range candidates {
		conditions := candidate.Definition.Conditions
		out := ResponseEvaluation{PolicyID: candidate.ID, VersionID: candidate.VersionID, Name: candidate.Definition.Name, Position: candidate.Position, Enabled: candidate.Enabled, Conditions: []ConditionOutcome{}}
		teamMatches := true
		if conditions.TeamID != nil {
			var ok bool
			teamMatches, ok = flags[fmt.Sprintf("t%d", i)].(bool)
			if !ok {
				return nil, errors.New("response policy team result is unavailable")
			}
			out.Conditions = append(out.Conditions, ConditionOutcome{Field: "team", Matched: teamMatches})
		}
		groupMatches := conditions.Match != "any" || len(conditions.Predicates) == 0
		for j, predicate := range conditions.Predicates {
			matched, ok := flags[fmt.Sprintf("p%d_%d", i, j)].(bool)
			if !ok {
				return nil, errors.New("response policy condition result is unavailable")
			}
			outcome := ConditionOutcome{Field: predicate.Field, DefinitionID: predicate.DefinitionID, Matched: matched}
			if predicate.DefinitionID != nil {
				outcome.Label = definitions[*predicate.DefinitionID].Label
			}
			out.Conditions = append(out.Conditions, outcome)
			if conditions.Match == "any" {
				groupMatches = groupMatches || matched
			} else {
				groupMatches = groupMatches && matched
			}
		}
		out.Matched = teamMatches && groupMatches
		switch {
		case !candidate.Enabled:
			out.Reason = "disabled"
		case !out.Matched:
			out.Reason = "conditions_not_matched"
		case selected:
			out.Reason = "earlier_policy_selected"
		default:
			out.Selected, selected, out.Reason = true, true, "first_matching_policy"
		}
		result = append(result, out)
	}
	return result, nil
}
