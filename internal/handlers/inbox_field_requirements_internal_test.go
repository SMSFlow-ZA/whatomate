package handlers

import (
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func intakeCase(t *testing.T, f responsePolicyFixture, account, contact uuid.UUID) models.InboxCase {
	t.Helper()
	c := newInboxCase(inboxThread{f.org.ID, account, contact}, time.Now().UTC(), &f.admin.ID, nil)
	require.NoError(t, f.db.Create(&c).Error)
	return c
}
func intakeField(t *testing.T, f responsePolicyFixture, key, scope, kind string, rule *models.InboxFieldRequirement) models.InboxFieldDefinition {
	t.Helper()
	d := models.InboxFieldDefinition{OrganizationID: f.org.ID, Key: key, Label: key, Scope: scope, Kind: kind, RequiredWhen: rule}
	require.NoError(t, d.Validate())
	require.NoError(t, f.db.Create(&d).Error)
	return d
}
func intakeMissing(t *testing.T, f responsePolicyFixture, c models.InboxCase) []models.InboxFieldDefinition {
	t.Helper()
	fields, err := inboxMissingDefinitions(f.db, c)
	require.NoError(t, err)
	return fields
}

func TestInboxConditionalRequirementsKeepAccountAndTeamOutsideAnyGroup(t *testing.T) {
	f := newResponsePolicyFixture(t)
	contact := testutil.CreateTestContact(t, f.db, f.org.ID)
	c := intakeCase(t, f, f.account.ID, contact.ID)
	team := models.Team{OrganizationID: f.org.ID, Name: "Synthetic intake team", IsActive: true}
	require.NoError(t, f.db.Create(&team).Error)
	c.TeamID = &team.ID
	c.Priority = models.InboxPriorityHigh
	require.NoError(t, f.db.Save(&c).Error)
	flag := intakeField(t, f, "flag", "contact", "boolean", nil)
	amount := intakeField(t, f, "amount", "contact", "number", nil)
	for _, value := range []models.InboxFieldValue{{OrganizationID: f.org.ID, DefinitionID: flag.ID, SubjectID: contact.ID, Value: json.RawMessage("false")}, {OrganizationID: f.org.ID, DefinitionID: amount.ID, SubjectID: contact.ID, Value: json.RawMessage("0")}} {
		require.NoError(t, f.db.Create(&value).Error)
	}
	rule := &models.InboxFieldRequirement{AccountID: &f.account.ID, TeamID: &team.ID, Match: "all", Predicates: []models.InboxPredicate{
		{Field: "custom_field", DefinitionID: &flag.ID, Value: json.RawMessage("false")},
		{Field: "custom_field", DefinitionID: &amount.ID, Value: json.RawMessage("0")},
		{Field: "priority", Value: json.RawMessage(`"high"`)},
	}}
	required := intakeField(t, f, "reference", "case", "text", rule)
	fields := intakeMissing(t, f, c)
	require.Len(t, fields, 1)
	require.Equal(t, required.ID, fields[0].ID)
	otherAccount := testutil.CreateTestWhatsAppAccount(t, f.db, f.org.ID)
	other := intakeCase(t, f, otherAccount.ID, contact.ID)
	other.TeamID = &team.ID
	other.Priority = models.InboxPriorityHigh
	require.NoError(t, f.db.Save(&other).Error)
	require.Empty(t, intakeMissing(t, f, other), "same contact cannot borrow requirements from another account")
	c.TeamID = nil
	require.Empty(t, intakeMissing(t, f, c), "team restriction is outside all/any")
	c.TeamID = &team.ID
	c.Priority = models.InboxPriorityNormal
	require.Empty(t, intakeMissing(t, f, c), "all conditions must match")
	rule.Match = "any"
	required.RequiredWhen = rule
	require.NoError(t, f.db.Save(&required).Error)
	require.Len(t, intakeMissing(t, f, c), 1, "false and zero are actual matching values")
	archived := time.Now().UTC()
	required.ArchivedAt = &archived
	require.NoError(t, f.db.Save(&required).Error)
	require.Empty(t, intakeMissing(t, f, c))
}

func TestInboxConditionalResolutionUsesProposedPriorityAndRollsBack(t *testing.T) {
	f := newResponsePolicyFixture(t)
	contact := testutil.CreateTestContact(t, f.db, f.org.ID)
	c := intakeCase(t, f, f.account.ID, contact.ID)
	required := intakeField(t, f, "high_priority_reference", "case", "text", &models.InboxFieldRequirement{Predicates: []models.InboxPredicate{{Field: "priority", Value: json.RawMessage(`"high"`)}}})
	update := inboxCaseUpdate{Status: models.InboxCaseResolved, Priority: models.InboxPriorityHigh}
	err := f.db.Transaction(func(tx *gorm.DB) error {
		return applyInboxTransition(tx, &c, update, &f.admin.ID, time.Now().UTC(), "updated")
	})
	require.ErrorIs(t, err, errRequiredInboxFields)
	var unchanged models.InboxCase
	require.NoError(t, f.db.First(&unchanged, "id=?", c.ID).Error)
	require.Equal(t, models.InboxCaseOpen, unchanged.Status)
	require.Equal(t, models.InboxPriorityNormal, unchanged.Priority)
	value := models.InboxFieldValue{OrganizationID: f.org.ID, SubjectID: c.ID, DefinitionID: required.ID, Value: json.RawMessage(`"Synthetic reference"`)}
	require.NoError(t, f.db.Create(&value).Error)
	require.NoError(t, f.db.Transaction(func(tx *gorm.DB) error {
		return applyInboxTransition(tx, &c, update, &f.admin.ID, time.Now().UTC(), "updated")
	}))
	require.Equal(t, models.InboxCaseResolved, c.Status)
}

func TestInboxConditionalRequirementsTreatWhitespaceAsEmptyButFalseAndZeroAsFilled(t *testing.T) {
	f := newResponsePolicyFixture(t)
	contact := testutil.CreateTestContact(t, f.db, f.org.ID)
	c := intakeCase(t, f, f.account.ID, contact.ID)
	text := intakeField(t, f, "legacy_text", "case", "text", nil)
	flag := intakeField(t, f, "flag", "case", "boolean", nil)
	amount := intakeField(t, f, "amount", "case", "number", nil)
	for _, value := range []models.InboxFieldValue{
		{OrganizationID: f.org.ID, SubjectID: c.ID, DefinitionID: text.ID, Value: json.RawMessage(`"   "`)},
		{OrganizationID: f.org.ID, SubjectID: c.ID, DefinitionID: flag.ID, Value: json.RawMessage(`false`)},
		{OrganizationID: f.org.ID, SubjectID: c.ID, DefinitionID: amount.ID, Value: json.RawMessage(`0`)},
	} {
		require.NoError(t, f.db.Create(&value).Error)
	}
	required := intakeField(t, f, "reference", "case", "text", &models.InboxFieldRequirement{Predicates: []models.InboxPredicate{
		{Field: "custom_field", DefinitionID: &text.ID, Operator: "is_empty"},
		{Field: "custom_field", DefinitionID: &flag.ID, Operator: "not_empty"},
		{Field: "custom_field", DefinitionID: &amount.ID, Operator: "not_empty"},
	}})
	fields := intakeMissing(t, f, c)
	require.Len(t, fields, 1)
	require.Equal(t, required.ID, fields[0].ID)
	archived := time.Now().UTC()
	flag.ArchivedAt = &archived
	require.NoError(t, f.db.Save(&flag).Error)
	require.Len(t, intakeMissing(t, f, c), 1, "archived values keep their recorded comparison meaning")
	require.NoError(t, f.db.Where("organization_id=? AND subject_id=? AND definition_id=?", f.org.ID, c.ID, flag.ID).Delete(&models.InboxFieldValue{}).Error)
	require.Empty(t, intakeMissing(t, f, c), "missing is different from false")
}

func TestInboxConditionalRequirementCreationRejectsForeignReferencesAndHidesLiterals(t *testing.T) {
	f := newResponsePolicyFixture(t)
	foreign := testutil.CreateTestOrganization(t, f.db)
	foreignAccount := testutil.CreateTestWhatsAppAccount(t, f.db, foreign.ID)
	foreignField := models.InboxFieldDefinition{OrganizationID: foreign.ID, Scope: "case", Key: "foreign", Label: "Foreign", Kind: "text"}
	require.NoError(t, f.db.Create(&foreignField).Error)
	for _, rule := range []models.InboxFieldRequirement{{AccountID: &foreignAccount.ID}, {Predicates: []models.InboxPredicate{{Field: "custom_field", DefinitionID: &foreignField.ID, Value: json.RawMessage(`"private"`)}}}} {
		r := testutil.NewJSONRequest(t, map[string]any{"scope": "case", "key": "example", "label": "Example", "kind": "text", "required_when": rule})
		testutil.SetAuthContext(r, f.org.ID, f.admin.ID)
		require.NoError(t, f.a.CreateInboxFieldDefinition(r))
		require.Equal(t, 400, r.RequestCtx.Response.StatusCode())
	}
	condition := intakeField(t, f, "condition", "case", "text", nil)
	const literal = "synthetic-private-condition-literal"
	rule := models.InboxFieldRequirement{AccountID: &f.account.ID, Predicates: []models.InboxPredicate{{Field: "custom_field", DefinitionID: &condition.ID, Value: json.RawMessage(`"` + literal + `"`)}}}
	r := testutil.NewJSONRequest(t, map[string]any{"scope": "case", "key": "example", "label": "Example", "kind": "text", "required_when": rule})
	testutil.SetAuthContext(r, f.org.ID, f.admin.ID)
	require.NoError(t, f.a.CreateInboxFieldDefinition(r))
	require.Equal(t, 200, r.RequestCtx.Response.StatusCode(), string(r.RequestCtx.Response.Body()))
	require.Contains(t, string(r.RequestCtx.Response.Body()), literal)
	var stored models.InboxFieldDefinition
	require.NoError(t, f.db.First(&stored, "organization_id=? AND key='example'", f.org.ID).Error)
	require.NotNil(t, stored.RequiredWhen)
	role := testutil.CreateTestRoleWithKeys(t, f.db, f.org.ID, "intake-reader", []string{"chat:read"})
	agent := testutil.CreateTestUser(t, f.db, f.org.ID, testutil.WithRoleID(&role.ID))
	read := testutil.NewGETRequest(t)
	testutil.SetFullAuthContext(read, f.org.ID, agent.ID, &role.ID, false)
	require.NoError(t, f.a.ListInboxFieldDefinitions(read))
	require.Equal(t, 200, read.RequestCtx.Response.StatusCode())
	require.Contains(t, string(read.RequestCtx.Response.Body()), `"conditional_required":true`)
	require.NotContains(t, string(read.RequestCtx.Response.Body()), literal)
	require.NotContains(t, string(read.RequestCtx.Response.Body()), `"required_when"`)
}

func TestInboxConditionalRequirementsUseBoundedSQLBatches(t *testing.T) {
	f := newResponsePolicyFixture(t)
	contact := testutil.CreateTestContact(t, f.db, f.org.ID)
	c := intakeCase(t, f, f.account.ID, contact.ID)
	definitions := make([]models.InboxFieldDefinition, 65)
	for i := range definitions {
		definitions[i] = models.InboxFieldDefinition{OrganizationID: f.org.ID, Scope: "case", Key: "required_" + uuid.NewString()[:8], Label: "Synthetic requirement", Kind: "text", RequiredWhen: &models.InboxFieldRequirement{Predicates: []models.InboxPredicate{{Field: "priority", Value: json.RawMessage(`"normal"`)}}}}
	}
	require.NoError(t, f.db.Create(&definitions).Error)
	var queries atomic.Int32
	name := "intake_queries_" + uuid.NewString()
	require.NoError(t, f.db.Callback().Query().Before("gorm:query").Register(name, func(*gorm.DB) { queries.Add(1) }))
	require.NoError(t, f.db.Callback().Row().Before("gorm:row").Register(name, func(*gorm.DB) { queries.Add(1) }))
	t.Cleanup(func() {
		require.NoError(t, f.db.Callback().Query().Remove(name))
		require.NoError(t, f.db.Callback().Row().Remove(name))
	})
	require.Len(t, intakeMissing(t, f, c), 65)
	require.LessOrEqual(t, queries.Load(), int32(6), "schema/values must not be fetched per predicate")
}

func TestInboxFieldDefinitionRejectsStaleConditionalEditsWithoutChangingLegacyRequests(t *testing.T) {
	f := newResponsePolicyFixture(t)
	d := intakeField(t, f, "conditional_revision", "case", "text", nil)
	require.EqualValues(t, 1, d.Revision)
	update := func(body map[string]any) int {
		r := testutil.NewJSONRequest(t, body)
		testutil.SetAuthContext(r, f.org.ID, f.admin.ID)
		testutil.SetPathParam(r, "definition_id", d.ID.String())
		require.NoError(t, f.a.UpdateInboxFieldDefinition(r))
		return r.RequestCtx.Response.StatusCode()
	}
	rule := models.InboxFieldRequirement{AccountID: &f.account.ID}
	require.Equal(t, 409, update(map[string]any{"required_when": rule}))
	require.Equal(t, 200, update(map[string]any{"required_when": rule, "revision": 1}))
	require.Equal(t, 409, update(map[string]any{"required_when": nil, "revision": 1}))
	require.NoError(t, f.db.First(&d, "id=?", d.ID).Error)
	require.EqualValues(t, 2, d.Revision)
	require.NotNil(t, d.RequiredWhen)
	require.Equal(t, 200, update(map[string]any{"label": "Legacy label update"}))
	require.Equal(t, 409, update(map[string]any{"label": "Stale label", "revision": 2}))
	require.Equal(t, 200, update(map[string]any{"required_when": nil, "revision": 3}))
	var fresh models.InboxFieldDefinition
	require.NoError(t, f.db.First(&fresh, "id=?", d.ID).Error)
	require.EqualValues(t, 4, fresh.Revision)
	require.Nil(t, fresh.RequiredWhen)
}
