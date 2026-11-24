package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/inboxquality"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type responsePolicyFixture struct {
	a       *App
	db      *gorm.DB
	org     *models.Organization
	admin   *models.User
	account *models.WhatsAppAccount
}

func newResponsePolicyFixture(t *testing.T) responsePolicyFixture {
	t.Helper()
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	return responsePolicyFixture{a: &App{DB: db, Log: testutil.NopLogger()}, db: db, org: org,
		admin:   testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin()),
		account: testutil.CreateTestWhatsAppAccount(t, db, org.ID)}
}

func responseInput(name string, minutes int) responsePolicyInput {
	return responsePolicyInput{Name: name, Conditions: models.InboxPolicyConditions{Predicates: []models.InboxPredicate{}},
		Targets: models.InboxSLAPolicy{FirstReplyMinutes: minutes, NextReplyMinutes: minutes, ResolutionMinutes: 120, Timezone: "UTC"}}
}

func (f responsePolicyFixture) request(t *testing.T, body any, id uuid.UUID) *fastglue.Request {
	t.Helper()
	r := testutil.NewJSONRequest(t, body)
	testutil.SetAuthContext(r, f.org.ID, f.admin.ID)
	testutil.SetPathParam(r, "account_id", f.account.ID.String())
	if id != uuid.Nil {
		testutil.SetPathParam(r, "id", id.String())
	}
	return r
}

func (f responsePolicyFixture) create(t *testing.T, input responsePolicyInput) models.InboxResponsePolicy {
	t.Helper()
	r := f.request(t, input, uuid.Nil)
	require.NoError(t, f.a.CreateInboxResponsePolicy(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(r.RequestCtx.Response.Body()))
	var result struct {
		Policy models.InboxResponsePolicy `json:"policy"`
	}
	testutil.ParseEnvelopeResponse(t, r, &result)
	require.False(t, result.Policy.Enabled)
	return result.Policy
}

func (f responsePolicyFixture) update(t *testing.T, row models.InboxResponsePolicy, input responsePolicyInput) models.InboxResponsePolicy {
	t.Helper()
	input.Revision = row.Revision
	r := f.request(t, input, row.ID)
	require.NoError(t, f.a.UpdateInboxResponsePolicy(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(r.RequestCtx.Response.Body()))
	var result struct {
		Policy models.InboxResponsePolicy `json:"policy"`
	}
	testutil.ParseEnvelopeResponse(t, r, &result)
	return result.Policy
}

func (f responsePolicyFixture) enter(t *testing.T, priority models.InboxCasePriority, contact *models.Contact) models.InboxCase {
	t.Helper()
	if contact == nil {
		contact = testutil.CreateTestContact(t, f.db, f.org.ID)
	}
	body := map[string]any{"status": "open", "priority": priority}
	prior, err := (inboxThread{f.org.ID, f.account.ID, contact.ID}).latest(f.db)
	if err == nil {
		body["case_id"], body["version"] = prior.ID, prior.Version
	} else {
		require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	}
	r := f.request(t, body, contact.ID)
	require.NoError(t, f.a.UpdateInboxConversation(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(r.RequestCtx.Response.Body()))
	current, err := (inboxThread{f.org.ID, f.account.ID, contact.ID}).latest(f.db)
	require.NoError(t, err)
	require.True(t, current.SLAPolicyAssigned)
	return current
}

func (f responsePolicyFixture) seedDefault(t *testing.T) {
	t.Helper()
	p := responseInput("", 30).Targets
	p.OrganizationID, p.AccountID = f.org.ID, f.account.ID
	require.NoError(t, f.db.Create(&p).Error)
}

func TestInboxResponsePoliciesSelectProspectivelyAndRetainImmutableNames(t *testing.T) {
	f := newResponsePolicyFixture(t)
	f.seedDefault(t)
	before := f.enter(t, models.InboxPriorityHigh, nil)
	require.Equal(t, "default", before.SLASelection.Kind)
	input := responseInput("Urgent enquiry", 10)
	input.Enabled = true // Creation must still start disabled.
	input.Conditions.Predicates = []models.InboxPredicate{{Field: "priority", Operator: "eq", Value: json.RawMessage(`"high"`)}}
	row := f.create(t, input)
	disabled := f.enter(t, models.InboxPriorityHigh, nil)
	require.Equal(t, *before.SLAPolicyID, *disabled.SLAPolicyID)
	row = f.update(t, row, input)
	selected := f.enter(t, models.InboxPriorityHigh, nil)
	require.Equal(t, row.VersionID, *selected.SLAPolicyID)
	require.Equal(t, "conditional", selected.SLASelection.Kind)
	require.Equal(t, row.ID, *selected.SLASelection.PolicyID)
	require.Equal(t, 0, *selected.SLASelection.Position)
	unmatched := f.enter(t, models.InboxPriorityNormal, nil)
	require.Equal(t, *before.SLAPolicyID, *unmatched.SLAPolicyID)
	oldVersion := row.VersionID
	input.Name, input.Targets.FirstReplyMinutes = "Updated enquiry policy", 20
	row = f.update(t, row, input)
	newCase := f.enter(t, models.InboxPriorityHigh, nil)
	require.Equal(t, row.VersionID, *newCase.SLAPolicyID)
	require.NotEqual(t, oldVersion, row.VersionID)
	for _, item := range []struct {
		c       models.InboxCase
		name    string
		minutes int
	}{
		{selected, "Urgent enquiry", 10}, {newCase, "Updated enquiry policy", 20},
	} {
		snapshots, err := f.a.inboxSLASnapshots(f.org.ID, []models.InboxCase{item.c}, time.Now().UTC())
		require.NoError(t, err)
		require.Equal(t, item.name, snapshots[item.c.ID].PolicyName)
		require.WithinDuration(t, item.c.OpenedAt.Add(time.Duration(item.minutes)*time.Minute), *snapshots[item.c.ID].FirstReply.Deadline, time.Millisecond)
	}
	// Archiving the active candidate preserves every assigned version/name.
	r := f.request(t, responsePolicyRevision{Revision: row.Revision}, row.ID)
	require.NoError(t, f.a.DeleteInboxResponsePolicy(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	var retained models.InboxSLAVersion
	require.NoError(t, f.db.First(&retained, "id=?", oldVersion).Error)
	require.Equal(t, "Urgent enquiry", retained.Selection.Name)
	afterArchive := f.enter(t, models.InboxPriorityHigh, nil)
	require.Equal(t, "default", afterArchive.SLASelection.Kind)
}

func TestInboxResponsePolicyPreviewIsReadOnlyOrderedAndDoesNotCopyFieldValues(t *testing.T) {
	f := newResponsePolicyFixture(t)
	f.seedDefault(t)
	contact := testutil.CreateTestContact(t, f.db, f.org.ID)
	field := models.InboxFieldDefinition{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, Scope: "contact", Key: "classification", Label: "Customer classification", Kind: "text"}
	require.NoError(t, f.db.Create(&field).Error)
	privateValue := "SYNTHETIC_CUSTOMER_VALUE_NOT_FOR_PREVIEW"
	encoded, err := json.Marshal(privateValue)
	require.NoError(t, err)
	require.NoError(t, f.db.Create(&models.InboxFieldValue{OrganizationID: f.org.ID, DefinitionID: field.ID, SubjectID: contact.ID, Value: encoded}).Error)
	sample := f.enter(t, models.InboxPriorityHigh, contact)
	broadInput := responseInput("First matching policy", 20)
	broadInput.Enabled = true
	broad := f.update(t, f.create(t, broadInput), broadInput)
	narrowInput := responseInput("Matching field policy", 10)
	narrowInput.Enabled = true
	narrowInput.Conditions.Predicates = []models.InboxPredicate{{Field: "custom_field", DefinitionID: &field.ID, Operator: "eq", Value: encoded}}
	narrow := f.update(t, f.create(t, narrowInput), narrowInput)
	draft := f.create(t, responseInput("Disabled draft", 5))
	counts := func() []int64 {
		var result []int64
		for _, model := range []any{&models.InboxCase{}, &models.InboxSLAVersion{}, &models.InboxSLAObligation{}, &models.InboxCaseActivity{}, &models.InboxResponsePolicy{}} {
			var count int64
			require.NoError(t, f.db.Model(model).Where("organization_id=?", f.org.ID).Count(&count).Error)
			result = append(result, count)
		}
		return result
	}
	before := counts()
	preview := f.request(t, map[string]any{"case_id": sample.ID}, uuid.Nil)
	require.NoError(t, f.a.PreviewInboxResponsePolicies(preview))
	require.Equal(t, 200, testutil.GetResponseStatusCode(preview))
	require.NotContains(t, string(preview.RequestCtx.Response.Body()), privateValue)
	var result struct {
		Prospective bool                              `json:"prospective_only"`
		Evaluations []inboxquality.ResponseEvaluation `json:"evaluations"`
	}
	testutil.ParseEnvelopeResponse(t, preview, &result)
	require.True(t, result.Prospective)
	require.Len(t, result.Evaluations, 3)
	require.True(t, result.Evaluations[0].Selected)
	require.Equal(t, "earlier_policy_selected", result.Evaluations[1].Reason)
	require.True(t, result.Evaluations[1].Matched)
	require.Equal(t, "Customer classification", result.Evaluations[1].Conditions[0].Label)
	require.Equal(t, "disabled", result.Evaluations[2].Reason)
	require.Equal(t, before, counts())
	order := f.request(t, map[string]any{"order": []responsePolicyRevision{{ID: narrow.ID, Revision: narrow.Revision}, {ID: broad.ID, Revision: broad.Revision}, {ID: draft.ID, Revision: draft.Revision}}}, uuid.Nil)
	require.NoError(t, f.a.ReorderInboxResponsePolicies(order))
	require.Equal(t, 200, testutil.GetResponseStatusCode(order))
	current := f.enter(t, models.InboxPriorityHigh, contact)
	// The sample already has an open case: it must retain its original default.
	require.Equal(t, sample.ID, current.ID)
	require.Equal(t, *sample.SLAPolicyID, *current.SLAPolicyID)
	other := testutil.CreateTestContact(t, f.db, f.org.ID)
	require.NoError(t, f.db.Create(&models.InboxFieldValue{OrganizationID: f.org.ID, DefinitionID: field.ID, SubjectID: other.ID, Value: encoded}).Error)
	selected := f.enter(t, models.InboxPriorityHigh, other)
	require.Equal(t, narrow.VersionID, *selected.SLAPolicyID)
	// Reusing a stale ordering must fail atomically.
	require.NoError(t, f.a.ReorderInboxResponsePolicies(order))
	require.Equal(t, 409, testutil.GetResponseStatusCode(order))
	require.Contains(t, string(order.RequestCtx.Response.Body()), "response_policy_conflict")
}

func TestInboxResponsePolicyCloneAndScopeBoundaries(t *testing.T) {
	f := newResponsePolicyFixture(t)
	input := responseInput("Scoped response targets", 10)
	input.Enabled = true
	row := f.update(t, f.create(t, input), input)
	cloneReq := f.request(t, responsePolicyRevision{Revision: row.Revision}, row.ID)
	require.NoError(t, f.a.CloneInboxResponsePolicy(cloneReq))
	require.Equal(t, 200, testutil.GetResponseStatusCode(cloneReq))
	var clone struct {
		Policy models.InboxResponsePolicy `json:"policy"`
	}
	testutil.ParseEnvelopeResponse(t, cloneReq, &clone)
	require.False(t, clone.Policy.Enabled)
	require.NotEqual(t, row.ID, clone.Policy.ID)
	require.NotEqual(t, row.VersionID, clone.Policy.VersionID)
	var version models.InboxSLAVersion
	require.NoError(t, f.db.First(&version, "id=?", clone.Policy.VersionID).Error)
	require.Equal(t, clone.Policy.ID, version.Selection.PolicyID)
	require.Equal(t, input.Name+" (copy)", version.Selection.Name)
	otherAccount := testutil.CreateTestWhatsAppAccount(t, f.db, f.org.ID)
	for _, handler := range []func(*fastglue.Request) error{f.a.UpdateInboxResponsePolicy, f.a.DeleteInboxResponsePolicy, f.a.CloneInboxResponsePolicy} {
		r := f.request(t, responsePolicyRevision{Revision: row.Revision}, row.ID)
		testutil.SetPathParam(r, "account_id", otherAccount.ID.String())
		require.NoError(t, handler(r))
		require.Equal(t, 404, testutil.GetResponseStatusCode(r))
	}
	role := testutil.CreateTestRoleWithKeys(t, f.db, f.org.ID, "Partial policy scope", []string{"settings.general:read", "settings.general:write", "chat:read"})
	limited := testutil.CreateTestUser(t, f.db, f.org.ID, testutil.WithRoleID(&role.ID))
	for _, handler := range []func(*fastglue.Request) error{f.a.ListInboxResponsePolicies, f.a.CreateInboxResponsePolicy, f.a.UpdateInboxResponsePolicy, f.a.PreviewInboxResponsePolicies, f.a.DeleteInboxResponsePolicy, f.a.CloneInboxResponsePolicy, f.a.ReorderInboxResponsePolicies} {
		r := f.request(t, input, row.ID)
		testutil.SetAuthContext(r, f.org.ID, limited.ID)
		require.NoError(t, handler(r))
		require.Equal(t, 403, testutil.GetResponseStatusCode(r))
	}
	foreignOrg := testutil.CreateTestOrganization(t, f.db)
	foreignAccount := testutil.CreateTestWhatsAppAccount(t, f.db, foreignOrg.ID)
	r := f.request(t, nil, uuid.Nil)
	testutil.SetPathParam(r, "account_id", foreignAccount.ID.String())
	require.NoError(t, f.a.ListInboxResponsePolicies(r))
	require.Equal(t, 404, testutil.GetResponseStatusCode(r))
}

func TestInboxResponsePolicySelectionSerializesStaleCallersAndBackfillKeepsDefault(t *testing.T) {
	f := newResponsePolicyFixture(t)
	f.seedDefault(t)
	input := responseInput("Conditional first version", 10)
	input.Enabled = true
	row := f.update(t, f.create(t, input), input)
	contact := testutil.CreateTestContact(t, f.db, f.org.ID)
	start := time.Now().UTC()
	original := newInboxCase(inboxThread{f.org.ID, f.account.ID, contact.ID}, start, nil, nil)
	require.NoError(t, f.db.Create(&original).Error)
	var group sync.WaitGroup
	errors := make(chan error, 8)
	versions := make(chan uuid.UUID, 8)
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			c := original
			err := f.db.Transaction(func(tx *gorm.DB) error { return inboxquality.AssignPolicy(tx, &c, false) })
			errors <- err
			if c.SLAPolicyID != nil {
				versions <- *c.SLAPolicyID
			}
		}()
	}
	group.Wait()
	require.Len(t, versions, 8)
	close(errors)
	close(versions)
	for err := range errors {
		require.NoError(t, err)
	}
	for id := range versions {
		require.Equal(t, row.VersionID, id)
	}
	old := row.VersionID
	input.Name, input.Targets.FirstReplyMinutes = "Conditional replacement", 20
	row = f.update(t, row, input)
	stale := original
	require.NoError(t, f.db.Transaction(func(tx *gorm.DB) error { return inboxquality.AssignPolicy(tx, &stale, false) }))
	require.Equal(t, old, *stale.SLAPolicyID)
	legacyContact := testutil.CreateTestContact(t, f.db, f.org.ID)
	legacy := newInboxCase(inboxThread{f.org.ID, f.account.ID, legacyContact.ID}, start, nil, nil)
	require.NoError(t, f.db.Create(&legacy).Error)
	require.NoError(t, inboxquality.Backfill(f.db))
	require.NoError(t, f.db.First(&legacy, "id=?", legacy.ID).Error)
	require.Equal(t, "legacy_default", legacy.SLASelection.Kind)
	require.NotEqual(t, row.VersionID, *legacy.SLAPolicyID)
}

func TestInboxResponsePoliciesKeepTypedValuesTeamAndAccountOutsideAnyGroup(t *testing.T) {
	f := newResponsePolicyFixture(t)
	contact := testutil.CreateTestContact(t, f.db, f.org.ID)
	caseA := f.enter(t, models.InboxPriorityNormal, contact)
	other := f
	other.account = testutil.CreateTestWhatsAppAccount(t, f.db, f.org.ID)
	caseB := other.enter(t, models.InboxPriorityNormal, contact)
	field := models.InboxFieldDefinition{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, Scope: "case", Key: "rating", Label: "Enquiry rating", Kind: "number"}
	flag := models.InboxFieldDefinition{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, Scope: "contact", Key: "reviewed", Label: "Reviewed", Kind: "boolean"}
	require.NoError(t, f.db.Create(&field).Error)
	require.NoError(t, f.db.Create(&flag).Error)
	for _, value := range []models.InboxFieldValue{
		{OrganizationID: f.org.ID, DefinitionID: field.ID, SubjectID: caseA.ID, Value: json.RawMessage("0")},
		{OrganizationID: f.org.ID, DefinitionID: field.ID, SubjectID: caseB.ID, Value: json.RawMessage("1")},
		{OrganizationID: f.org.ID, DefinitionID: flag.ID, SubjectID: contact.ID, Value: json.RawMessage("false")},
	} {
		require.NoError(t, f.db.Create(&value).Error)
	}
	team := models.Team{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, Name: "Response policy team", IsActive: true}
	require.NoError(t, f.db.Create(&team).Error)
	input := responseInput("Typed enquiry match", 10)
	input.Enabled = true
	input.Conditions = models.InboxPolicyConditions{TeamID: &team.ID, Match: "any", Predicates: []models.InboxPredicate{
		{Field: "custom_field", DefinitionID: &field.ID, Operator: "eq", Value: json.RawMessage("0")},
		{Field: "custom_field", DefinitionID: &flag.ID, Operator: "eq", Value: json.RawMessage("false")},
	}}
	f.update(t, f.create(t, input), input)
	candidates, err := inboxquality.LoadResponsePolicies(f.db, f.org.ID, f.account.ID, false)
	require.NoError(t, err)
	results, err := inboxquality.EvaluateResponsePolicies(f.db, caseA, candidates)
	require.NoError(t, err)
	require.False(t, results[0].Matched, "team scope is outside the any group")
	require.True(t, results[0].Conditions[1].Matched, "zero is a recorded value")
	require.True(t, results[0].Conditions[2].Matched, "false is a recorded value")
	// The matcher uses the stored row, not a caller's stale/forged team pointer.
	caseA.TeamID = &team.ID
	results, err = inboxquality.EvaluateResponsePolicies(f.db, caseA, candidates)
	require.NoError(t, err)
	require.False(t, results[0].Matched)
	require.NoError(t, f.db.Model(&caseA).Update("team_id", team.ID).Error)
	results, err = inboxquality.EvaluateResponsePolicies(f.db, caseA, candidates)
	require.NoError(t, err)
	require.True(t, results[0].Selected)
	_, err = inboxquality.EvaluateResponsePolicies(f.db, caseB, candidates)
	require.ErrorIs(t, err, inboxquality.ErrPolicyConditions)
	// Retired fields retain their recorded matching meaning.
	require.NoError(t, f.db.Model(&field).Update("archived_at", time.Now().UTC()).Error)
	results, err = inboxquality.EvaluateResponsePolicies(f.db, caseA, candidates)
	require.NoError(t, err)
	require.True(t, results[0].Selected)
	// A deactivated team never becomes eligible through the any branch.
	require.NoError(t, f.db.Model(&team).Update("is_active", false).Error)
	results, err = inboxquality.EvaluateResponsePolicies(f.db, caseA, candidates)
	require.NoError(t, err)
	require.False(t, results[0].Matched)
}

func TestInboxResponsePolicyLimitAndRejectedReorderLeaveNoPartialWrites(t *testing.T) {
	f := newResponsePolicyFixture(t)
	var rows []models.InboxResponsePolicy
	for i := 0; i < inboxquality.MaxResponsePolicies-1; i++ {
		rows = append(rows, f.create(t, responseInput(fmt.Sprintf("Policy %d", i), 10)))
	}
	requests := []*fastglue.Request{
		f.request(t, responseInput("Final candidate A", 10), uuid.Nil),
		f.request(t, responseInput("Final candidate B", 10), uuid.Nil),
	}
	var group sync.WaitGroup
	for _, r := range requests {
		group.Add(1)
		go func() { defer group.Done(); _ = f.a.CreateInboxResponsePolicy(r) }()
	}
	group.Wait()
	statuses := map[int]int{}
	for _, r := range requests {
		statuses[testutil.GetResponseStatusCode(r)]++
		if testutil.GetResponseStatusCode(r) == 400 {
			require.Contains(t, string(r.RequestCtx.Response.Body()), "response_policy_limit")
		}
	}
	require.Equal(t, map[int]int{200: 1, 400: 1}, statuses)
	var count int64
	for _, model := range []any{&models.InboxResponsePolicy{}, &models.InboxSLAVersion{}} {
		require.NoError(t, f.db.Model(model).Where("organization_id=?", f.org.ID).Count(&count).Error)
		require.EqualValues(t, inboxquality.MaxResponsePolicies, count)
	}
	var all []models.InboxResponsePolicy
	require.NoError(t, f.db.Where("organization_id=?", f.org.ID).Order("position,id").Find(&all).Error)
	order := []responsePolicyRevision{}
	for i := len(all) - 1; i >= 0; i-- {
		order = append(order, responsePolicyRevision{ID: all[i].ID, Revision: all[i].Revision})
	}
	order[len(order)-1].ID = order[0].ID // Reject after earlier rows would have moved.
	r := f.request(t, map[string]any{"order": order}, uuid.Nil)
	require.NoError(t, f.a.ReorderInboxResponsePolicies(r))
	require.Equal(t, 409, testutil.GetResponseStatusCode(r))
	var retained []models.InboxResponsePolicy
	require.NoError(t, f.db.Where("organization_id=?", f.org.ID).Order("position,id").Find(&retained).Error)
	for i := range all {
		require.Equal(t, all[i].Position, retained[i].Position)
		require.Equal(t, all[i].Revision, retained[i].Revision)
	}
	invalid := responseInput("Invalid definition", 10)
	invalid.Revision = rows[0].Revision
	invalid.Conditions.Predicates = []models.InboxPredicate{{Field: "status", Value: json.RawMessage(`"open"`)}}
	r = f.request(t, invalid, rows[0].ID)
	require.NoError(t, f.a.UpdateInboxResponsePolicy(r))
	require.Equal(t, 400, testutil.GetResponseStatusCode(r))
	require.Contains(t, string(r.RequestCtx.Response.Body()), "invalid_response_policy")
	require.NoError(t, f.db.Model(&models.InboxSLAVersion{}).Where("organization_id=?", f.org.ID).Count(&count).Error)
	require.EqualValues(t, inboxquality.MaxResponsePolicies, count)
}

func TestInboxResponsePolicyNoneAssignmentsAndForeignCriteriaRemainIsolated(t *testing.T) {
	f := newResponsePolicyFixture(t)
	contact := testutil.CreateTestContact(t, f.db, f.org.ID)
	without := f.enter(t, models.InboxPriorityNormal, contact)
	require.Nil(t, without.SLAPolicyID)
	require.Equal(t, "none", without.SLASelection.Kind)
	f.seedDefault(t)
	input := responseInput("Future policy only", 10)
	input.Enabled = true
	row := f.update(t, f.create(t, input), input)
	existing := f.enter(t, models.InboxPriorityHigh, contact)
	require.Nil(t, existing.SLAPolicyID)
	require.Equal(t, "none", existing.SLASelection.Kind)
	foreignOrg := testutil.CreateTestOrganization(t, f.db)
	foreignField := models.InboxFieldDefinition{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: foreignOrg.ID, Scope: "case", Key: "private", Label: "Foreign definition must stay private", Kind: "number"}
	require.NoError(t, f.db.Create(&foreignField).Error)
	input.Revision = row.Revision
	input.Conditions.Predicates = []models.InboxPredicate{{Field: "custom_field", DefinitionID: &foreignField.ID, Operator: "eq", Value: json.RawMessage("0")}}
	r := f.request(t, input, row.ID)
	require.NoError(t, f.a.UpdateInboxResponsePolicy(r))
	require.Equal(t, 400, testutil.GetResponseStatusCode(r))
	require.NotContains(t, string(r.RequestCtx.Response.Body()), foreignField.Label)
	var stored models.InboxResponsePolicy
	require.NoError(t, f.db.First(&stored, "id=?", row.ID).Error)
	require.Equal(t, row.VersionID, stored.VersionID)
	require.Equal(t, row.Revision, stored.Revision)
	other := f
	other.account = testutil.CreateTestWhatsAppAccount(t, f.db, f.org.ID)
	input = responseInput("Other account definition", 10)
	input.Enabled = true
	otherRow := other.update(t, other.create(t, input), input)
	caseA := f.enter(t, models.InboxPriorityNormal, nil)
	caseA.SLAPolicyID = &otherRow.VersionID
	_, err := f.a.inboxSLASnapshots(f.org.ID, []models.InboxCase{caseA}, time.Now().UTC())
	require.Error(t, err)
	preview := other.request(t, map[string]any{"case_id": without.ID}, uuid.Nil)
	require.NoError(t, f.a.PreviewInboxResponsePolicies(preview))
	require.Equal(t, 404, testutil.GetResponseStatusCode(preview))
}

type responsePolicyQueryCounter struct {
	logger.Interface
	count atomic.Int64
}

func (counter *responsePolicyQueryCounter) Trace(context.Context, time.Time, func() (string, int64), error) {
	counter.count.Add(1) // No condition values or SQL are written to logs.
}

func TestInboxResponsePolicyMaximumMatchingIsBatchedAndDeterministic(t *testing.T) {
	f := newResponsePolicyFixture(t)
	sample := f.enter(t, models.InboxPriorityNormal, nil)
	field := models.InboxFieldDefinition{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, Scope: "case", Key: "priority_score", Label: "Priority score", Kind: "number"}
	require.NoError(t, f.db.Create(&field).Error)
	require.NoError(t, f.db.Create(&models.InboxFieldValue{OrganizationID: f.org.ID, DefinitionID: field.ID, SubjectID: sample.ID, Value: json.RawMessage("0")}).Error)
	for i := 0; i < inboxquality.MaxResponsePolicies; i++ {
		input := responseInput(fmt.Sprintf("Ordered policy %d", i), 10)
		input.Enabled = true
		for j := 0; j < 20; j++ {
			input.Conditions.Predicates = append(input.Conditions.Predicates, models.InboxPredicate{Field: "custom_field", DefinitionID: &field.ID, Operator: "eq", Value: json.RawMessage("0")})
		}
		f.update(t, f.create(t, input), input)
	}
	// Real archived definitions and their own immutable versions must not make
	// the prospective active-policy index grow with historical configuration.
	var indexDefinition string
	require.NoError(t, f.db.Raw("SELECT indexdef FROM pg_indexes WHERE schemaname=current_schema() AND indexname='idx_inbox_response_order'").Scan(&indexDefinition).Error)
	require.Contains(t, indexDefinition, "WHERE")
	require.Contains(t, indexDefinition, "deleted_at IS NULL")
	archived := []models.InboxResponsePolicy{}
	versions := []models.InboxSLAVersion{}
	for i := 0; i < 1000; i++ {
		id, versionID := uuid.New(), uuid.New()
		archived = append(archived, models.InboxResponsePolicy{BaseModel: models.BaseModel{ID: id, DeletedAt: gorm.DeletedAt{Time: time.Now().UTC(), Valid: true}}, OrganizationID: f.org.ID, AccountID: f.account.ID, VersionID: versionID, Revision: 1, Position: i})
		targets := responseInput("", 10).Targets
		targets.AccountID = f.account.ID
		versions = append(versions, models.InboxSLAVersion{BaseModel: models.BaseModel{ID: versionID}, OrganizationID: f.org.ID, AccountID: f.account.ID, Policy: targets, Selection: &models.InboxResponseDefinition{PolicyID: id, Name: "Archived synthetic policy", Conditions: models.InboxPolicyConditions{}}})
	}
	require.NoError(t, f.db.CreateInBatches(&versions, 100).Error)
	require.NoError(t, f.db.CreateInBatches(&archived, 100).Error)
	candidates, err := inboxquality.LoadResponsePolicies(f.db, f.org.ID, f.account.ID, false)
	require.NoError(t, err)
	require.Len(t, candidates, inboxquality.MaxResponsePolicies)
	for i, j := 0, len(candidates)-1; i < j; i, j = i+1, j-1 {
		candidates[i], candidates[j] = candidates[j], candidates[i]
	}
	counter := &responsePolicyQueryCounter{Interface: logger.Default.LogMode(logger.Silent)}
	start := time.Now()
	results, err := inboxquality.EvaluateResponsePolicies(f.db.Session(&gorm.Session{Logger: counter}), sample, candidates)
	require.NoError(t, err)
	t.Logf("20 policies x20 typed conditions: %d queries, elapsed=%s", counter.count.Load(), time.Since(start))
	require.LessOrEqual(t, counter.count.Load(), int64(2), "no query per policy or condition")
	require.Len(t, results, inboxquality.MaxResponsePolicies)
	require.True(t, results[0].Selected)
	require.Equal(t, 0, results[0].Position)
	for _, result := range results[1:] {
		require.True(t, result.Matched)
		require.False(t, result.Selected)
		require.Equal(t, "earlier_policy_selected", result.Reason)
	}
	require.Equal(t, inboxquality.MaxResponsePolicies-1, candidates[0].Position, "evaluation must not mutate its caller's order")
}
