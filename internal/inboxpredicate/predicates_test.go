package inboxpredicate

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/require"
)

func TestCompilePreservesAllowedStatusAndPriorityMeanings(t *testing.T) {
	for field, allowed := range map[string][]string{
		"status":   {"open", "pending", "waiting_customer", "snoozed", "resolved"},
		"priority": {"low", "normal", "high", "urgent"},
	} {
		for _, value := range allowed {
			for _, op := range []string{"", "eq", "not_eq"} {
				encoded, err := json.Marshal(value)
				require.NoError(t, err)
				sql, args, err := Compile(models.InboxPredicate{Field: field, Operator: op, Value: encoded}, nil)
				require.NoError(t, err)
				comparison := "="
				if op == "not_eq" {
					comparison = "<>"
				}
				require.Equal(t, "inbox_cases."+field+" "+comparison+" ?", sql)
				require.Equal(t, []any{value}, args)
			}
		}
		for _, value := range []string{"", "not-a-value", "' OR true --"} {
			encoded, err := json.Marshal(value)
			require.NoError(t, err)
			_, _, err = Compile(models.InboxPredicate{Field: field, Value: encoded}, nil)
			require.ErrorIs(t, err, ErrInvalid)
		}
	}
}

func TestCompileIntakeTextTypesAllowSearchFragmentsButNotNumericOperands(t *testing.T) {
	for _, kind := range []string{"email", "url"} {
		id := uuid.New()
		definitions := map[uuid.UUID]models.InboxFieldDefinition{id: {BaseModel: models.BaseModel{ID: id}, Scope: "contact", Kind: kind}}
		sql, args, err := Compile(models.InboxPredicate{Field: "custom_field", DefinitionID: &id, Operator: "contains", Value: json.RawMessage(`"example.invalid"`)}, definitions)
		require.NoError(t, err)
		require.Contains(t, sql, "inbox_cases.contact_id")
		require.Contains(t, args, "example.invalid")
		_, _, err = Compile(models.InboxPredicate{Field: "custom_field", DefinitionID: &id, Operator: "eq", Value: json.RawMessage(`0`)}, definitions)
		require.ErrorIs(t, err, ErrInvalid)
	}
}

func TestCompileTypedFalseAndZeroAreNotMissing(t *testing.T) {
	for _, tc := range []struct{ kind, value string }{{"number", "0"}, {"boolean", "false"}} {
		t.Run(tc.kind, func(t *testing.T) {
			id := uuid.New()
			definition := models.InboxFieldDefinition{BaseModel: models.BaseModel{ID: id}, Kind: tc.kind, Scope: "case"}
			sql, args, err := Compile(models.InboxPredicate{Field: "custom_field", DefinitionID: &id, Value: json.RawMessage(tc.value)}, map[uuid.UUID]models.InboxFieldDefinition{id: definition})
			require.NoError(t, err)
			require.Contains(t, sql, "v.subject_id = inbox_cases.id")
			require.Equal(t, []any{id, tc.value}, args)
		})
	}
}

func TestCompileBindsUntrustedTextAndRejectsSQLIdentifiers(t *testing.T) {
	id := uuid.New()
	definition := models.InboxFieldDefinition{BaseModel: models.BaseModel{ID: id}, Kind: "text", Scope: "contact"}
	value := "Example%_' OR true --"
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	sql, args, err := Compile(models.InboxPredicate{Field: "custom_field", DefinitionID: &id, Operator: "contains", Value: encoded}, map[uuid.UUID]models.InboxFieldDefinition{id: definition})
	require.NoError(t, err)
	require.NotContains(t, sql, value)
	require.Contains(t, sql, "position(lower(?) in lower(v.value #>> '{}'))")
	require.Contains(t, sql, "v.subject_id = inbox_cases.contact_id")
	require.Equal(t, []any{id, value}, args)
	for _, predicate := range []models.InboxPredicate{
		{Field: "priority OR true", Value: json.RawMessage(`"high"`)},
		{Field: "custom_field", DefinitionID: &id, Operator: "contains); DROP TABLE contacts", Value: encoded},
		{Field: "custom_field", Value: encoded},
		{Field: "custom_field", DefinitionID: &id, Value: json.RawMessage(`null`)},
		{Field: "custom_field", DefinitionID: &id, Value: json.RawMessage(`"   "`)},
	} {
		_, _, err := Compile(predicate, map[uuid.UUID]models.InboxFieldDefinition{id: definition})
		require.ErrorIs(t, err, ErrInvalid)
	}
}

func TestApplyRejectsUnboundedGroupsBeforeDatabaseWork(t *testing.T) {
	for _, filter := range []models.InboxQueueFilter{
		{Predicates: make([]models.InboxPredicate, 21)},
		{Match: "any OR true"},
	} {
		_, err := Apply(nil, nil, uuid.New(), filter, false)
		require.ErrorIs(t, err, ErrInvalid)
	}
}

func TestCompilePreservesEmptyComparisonsAndTypedOperatorChecks(t *testing.T) {
	id := uuid.New()
	definition := models.InboxFieldDefinition{BaseModel: models.BaseModel{ID: id}, Kind: "boolean", Scope: "case"}
	for _, op := range []string{"is_empty", "not_empty"} {
		sql, args, err := Compile(models.InboxPredicate{Field: "custom_field", DefinitionID: &id, Operator: op}, map[uuid.UUID]models.InboxFieldDefinition{id: definition})
		require.NoError(t, err)
		require.Equal(t, op == "is_empty", strings.HasPrefix(sql, "NOT EXISTS"))
		require.Equal(t, []any{id}, args)
	}
	_, _, err := Compile(models.InboxPredicate{Field: "custom_field", DefinitionID: &id, Operator: "gt", Value: json.RawMessage("false")}, map[uuid.UUID]models.InboxFieldDefinition{id: definition})
	require.ErrorIs(t, err, ErrInvalid)
}
