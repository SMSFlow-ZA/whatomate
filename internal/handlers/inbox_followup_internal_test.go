package handlers

import (
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestInboxFollowupHistoricalIdentityFieldsAndActivityCursor(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	user := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	a := &App{DB: db, Log: testutil.NopLogger()}
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	thread := inboxThread{org.ID, account.ID, contact.ID}
	a.recordInboxInbound(account, contact, "followup-old", time.Now().Add(-time.Hour), nil)
	old, err := thread.latest(db)
	require.NoError(t, err)
	d := models.InboxFieldDefinition{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Scope: "case", Key: "reference", Label: "Reference", Kind: "text"}
	require.NoError(t, db.Create(&d).Error)
	require.NoError(t, db.Create(&models.InboxFieldValue{OrganizationID: org.ID, DefinitionID: d.ID, SubjectID: old.ID, Value: json.RawMessage(`"older"`)}).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return applyInboxTransition(tx, &old, inboxCaseUpdate{Status: models.InboxCaseResolved}, &user.ID, time.Now().Add(-time.Minute), "updated")
	}))
	a.recordInboxInbound(account, contact, "followup-current", time.Now(), nil)
	current, err := thread.latest(db)
	require.NoError(t, err)
	require.NotEqual(t, old.ID, current.ID)
	require.NoError(t, db.Model(&d).Update("archived_at", time.Now()).Error)
	at := time.Now().Add(-time.Minute)
	for i := 0; i < 55; i++ {
		require.NoError(t, db.Create(&models.InboxCaseActivity{BaseModel: models.BaseModel{ID: uuid.New(), CreatedAt: at}, OrganizationID: org.ID, AccountID: account.ID, CaseID: old.ID, Kind: "workflow_note", Detail: fmt.Sprintf("Synthetic %d", i)}).Error)
	}
	request := func(caseID uuid.UUID, cursor string) *struct {
		Case          models.InboxCase
		CurrentCaseID uuid.UUID `json:"current_case_id"`
		ReadOnly      bool      `json:"read_only"`
		Activity      []models.InboxCaseActivity
		More          bool   `json:"activity_has_more"`
		Cursor        string `json:"activity_next_cursor"`
	} {
		r := testutil.NewGETRequest(t)
		testutil.SetAuthContext(r, org.ID, user.ID)
		testutil.SetPathParam(r, "id", contact.ID.String())
		testutil.SetPathParam(r, "account_id", account.ID.String())
		testutil.SetQueryParam(r, "case_id", caseID.String())
		if cursor != "" {
			testutil.SetQueryParam(r, "activity_before", cursor)
		}
		require.NoError(t, a.GetInboxConversation(r))
		require.Equal(t, 200, testutil.GetResponseStatusCode(r))
		var reply struct {
			Case          models.InboxCase
			CurrentCaseID uuid.UUID `json:"current_case_id"`
			ReadOnly      bool      `json:"read_only"`
			Activity      []models.InboxCaseActivity
			More          bool   `json:"activity_has_more"`
			Cursor        string `json:"activity_next_cursor"`
		}
		testutil.ParseEnvelopeResponse(t, r, &reply)
		return &reply
	}
	first := request(old.ID, "")
	require.Equal(t, old.ID, first.Case.ID)
	require.Equal(t, current.ID, first.CurrentCaseID)
	require.True(t, first.ReadOnly)
	require.Len(t, first.Activity, 50)
	require.True(t, first.More)
	second := request(old.ID, first.Cursor)
	require.False(t, second.More)
	ids := map[uuid.UUID]bool{}
	for _, page := range [][]models.InboxCaseActivity{first.Activity, second.Activity} {
		for _, e := range page {
			require.False(t, ids[e.ID])
			ids[e.ID] = true
		}
	}
	require.Len(t, ids, 57)
	fields := testutil.NewGETRequest(t)
	testutil.SetAuthContext(fields, org.ID, user.ID)
	testutil.SetPathParam(fields, "id", contact.ID.String())
	testutil.SetPathParam(fields, "account_id", account.ID.String())
	testutil.SetQueryParam(fields, "case_id", old.ID.String())
	require.NoError(t, a.ListInboxCaseFields(fields))
	var values struct {
		Subject uuid.UUID `json:"subject_id"`
		Values  []models.InboxFieldValue
	}
	testutil.ParseEnvelopeResponse(t, fields, &values)
	require.Equal(t, old.ID, values.Subject)
	require.Len(t, values.Values, 1)
	require.JSONEq(t, `"older"`, string(values.Values[0].Value))
	stale := testutil.NewJSONRequest(t, map[string]any{"case_id": old.ID, "version": old.Version, "status": "pending"})
	testutil.SetAuthContext(stale, org.ID, user.ID)
	testutil.SetPathParam(stale, "id", contact.ID.String())
	testutil.SetPathParam(stale, "account_id", account.ID.String())
	require.NoError(t, a.UpdateInboxConversation(stale))
	require.Equal(t, 409, testutil.GetResponseStatusCode(stale))
	foreign := testutil.NewGETRequest(t)
	testutil.SetAuthContext(foreign, org.ID, user.ID)
	testutil.SetPathParam(foreign, "id", contact.ID.String())
	testutil.SetPathParam(foreign, "account_id", account.ID.String())
	testutil.SetQueryParam(foreign, "case_id", uuid.NewString())
	require.NoError(t, a.GetInboxConversation(foreign))
	require.Equal(t, 404, testutil.GetResponseStatusCode(foreign))
	_, _, err = parseInboxActivityCursor("invalid")
	require.Error(t, err)
}

func TestInboxFollowupStableHistoryFacets(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	user := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	other := testutil.CreateTestUser(t, db, org.ID)
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	a := &App{DB: db, Log: testutil.NopLogger()}
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	a.recordInboxInbound(account, contact, "facet-case", time.Now().Add(-time.Hour), nil)
	c, err := (inboxThread{org.ID, account.ID, contact.ID}).latest(db)
	require.NoError(t, err)
	now := time.Now().Add(-time.Minute)
	var policies []uuid.UUID
	for i, agent := range []uuid.UUID{user.ID, other.ID} {
		v := models.InboxSLAVersion{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, AccountID: account.ID, Policy: models.InboxSLAPolicy{FirstReplyMinutes: 30, Timezone: "UTC"}}
		require.NoError(t, db.Create(&v).Error)
		policies = append(policies, v.ID)
		require.NoError(t, db.Create(&models.InboxSLAObligation{OrganizationID: org.ID, AccountID: account.ID, CaseID: c.ID, PolicyID: v.ID, AgentID: &agent, Kind: "first_reply", StartedAt: now.Add(time.Duration(i) * time.Second), CompletedAt: &now, BudgetMinutes: 30, Outcome: "met"}).Error)
	}
	for _, key := range []string{"agent_id", "policy_id"} {
		r := testutil.NewGETRequest(t)
		testutil.SetAuthContext(r, org.ID, user.ID)
		id := user.ID
		if key == "policy_id" {
			id = policies[0]
		}
		testutil.SetQueryParam(r, key, id.String())
		require.NoError(t, a.GetInboxSLAHistory(r))
		var reply struct {
			Results []inboxSLAHistoryRow
			Filters struct {
				Agents   []struct{ ID uuid.UUID }
				Policies []struct{ ID uuid.UUID } `json:"policy_versions"`
			}
		}
		testutil.ParseEnvelopeResponse(t, r, &reply)
		require.Len(t, reply.Results, 1)
		require.Len(t, reply.Filters.Agents, 2)
		require.Len(t, reply.Filters.Policies, 2)
	}
}

func TestInboxFollowupResolutionErrorsRollbackAndContext(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	user := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	a := &App{DB: db, Log: testutil.NopLogger()}
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	a.recordInboxInbound(account, contact, "validation-case", time.Now(), nil)
	c, err := (inboxThread{org.ID, account.ID, contact.ID}).latest(db)
	require.NoError(t, err)
	d := models.InboxFieldDefinition{OrganizationID: org.ID, Scope: "case", Key: "due", Label: "Due date", Kind: "date", RequiredOnResolve: true}
	require.NoError(t, db.Create(&d).Error)
	r := testutil.NewJSONRequest(t, map[string]any{"case_id": c.ID, "version": c.Version, "values": []map[string]any{{"definition_id": d.ID, "value": "not-a-date"}}})
	testutil.SetAuthContext(r, org.ID, user.ID)
	testutil.SetPathParam(r, "id", contact.ID.String())
	testutil.SetPathParam(r, "account_id", account.ID.String())
	require.NoError(t, a.CompleteAndResolveInboxCase(r))
	require.Equal(t, 400, testutil.GetResponseStatusCode(r))
	var problem struct {
		Data struct {
			Code   string
			Errors []inboxResolutionFieldError `json:"field_errors"`
		}
	}
	require.NoError(t, json.Unmarshal(r.RequestCtx.Response.Body(), &problem))
	require.Equal(t, "invalid_values", problem.Data.Code)
	require.Len(t, problem.Data.Errors, 1)
	require.Equal(t, d.ID, problem.Data.Errors[0].DefinitionID)
	var stored models.InboxCase
	require.NoError(t, db.First(&stored, "id=?", c.ID).Error)
	require.Equal(t, models.InboxCaseOpen, stored.Status)
	var count int64
	require.NoError(t, db.Model(&models.InboxFieldValue{}).Where("subject_id=?", c.ID).Count(&count).Error)
	require.Zero(t, count)
}

func TestInboxFollowupPredicateBatchCostAndSemantics(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	user := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	a := &App{DB: db, Log: testutil.NopLogger()}
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	a.recordInboxInbound(account, contact, "batch-case", time.Now(), nil)
	c, err := (inboxThread{org.ID, account.ID, contact.ID}).latest(db)
	require.NoError(t, err)
	var predicates []models.InboxPredicate
	for i := 0; i < 20; i++ {
		d := models.InboxFieldDefinition{OrganizationID: org.ID, Scope: "case", Key: fmt.Sprintf("field_%d", i), Label: "Synthetic", Kind: "text"}
		require.NoError(t, db.Create(&d).Error)
		id := d.ID
		predicates = append(predicates, models.InboxPredicate{Field: "custom_field", DefinitionID: &id, Operator: "is_empty"})
	}
	var queries atomic.Int64
	callback := func(*gorm.DB) { queries.Add(1) }
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register("followup_query_count", callback))
	require.NoError(t, db.Callback().Row().Before("gorm:row").Register("followup_row_count", callback))
	defer func() { require.NoError(t, db.Callback().Query().Remove("followup_query_count")) }()
	defer func() { require.NoError(t, db.Callback().Row().Remove("followup_row_count")) }()
	w := models.InboxWorkflow{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Kind: "macro", Actions: []models.InboxAction{{Type: "note", Value: "Synthetic preview"}}}
	baseline, err := a.evaluateInboxWorkflow(db, w, c, *account, *contact, user.ID, time.Now())
	require.NoError(t, err)
	require.True(t, baseline.Matches)
	plain := queries.Swap(0)
	w.Conditions.Predicates = predicates
	preview, err := a.evaluateInboxWorkflow(db, w, c, *account, *contact, user.ID, time.Now())
	require.NoError(t, err)
	require.True(t, preview.Matches)
	require.Len(t, preview.ConditionOutcomes, 20)
	require.LessOrEqual(t, queries.Load(), plain+3)
	w.Conditions.Match = "any"
	w.Conditions.Predicates[0] = models.InboxPredicate{Field: "status", Operator: "eq", Value: json.RawMessage(`"pending"`)}
	preview, err = a.evaluateInboxWorkflow(db, w, c, *account, *contact, user.ID, time.Now())
	require.NoError(t, err)
	require.True(t, preview.Matches)
	require.False(t, preview.ConditionOutcomes[0].Matched)
	w.Conditions.Match = "all"
	preview, err = a.evaluateInboxWorkflow(db, w, c, *account, *contact, user.ID, time.Now())
	require.NoError(t, err)
	require.False(t, preview.Matches)
	t.Logf("Query callbacks: plain=%d, twenty conditions <=%d", plain, plain+3)
}

func TestInboxFollowupSearchLiteralMaskingAndScope(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	user := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	a := &App{DB: db, Log: testutil.NopLogger()}
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	require.NoError(t, db.Model(contact).Update("profile_name", "Example%_reference").Error)
	a.recordInboxInbound(account, contact, "search-case", time.Now(), nil)
	c, err := (inboxThread{org.ID, account.ID, contact.ID}).latest(db)
	require.NoError(t, err)
	query := func(search string) []inboxQueueRow {
		r := testutil.NewGETRequest(t)
		testutil.SetAuthContext(r, org.ID, user.ID)
		filters, _ := json.Marshal(models.InboxQueueFilter{Search: search})
		testutil.SetQueryParam(r, "filters", string(filters))
		require.NoError(t, a.ListInboxQueue(r))
		require.Equal(t, 200, testutil.GetResponseStatusCode(r))
		var data struct{ Cases []inboxQueueRow }
		testutil.ParseEnvelopeResponse(t, r, &data)
		return data.Cases
	}
	require.Len(t, query("%_ref"), 1)
	require.Empty(t, query("EXAMPLE___"))
	require.Len(t, query(c.ID.String()[:8]), 1)
	require.NoError(t, db.Model(org).Update("settings", models.JSONB{"mask_phone_numbers": true}).Error)
	for _, name := range []string{"27847344172", "27847344172 x", "+27 847344172"} {
		require.NoError(t, db.Model(contact).Update("profile_name", name).Error)
		require.Empty(t, query("847344"))
	}
	require.Len(t, query(c.ID.String()), 1)
	require.False(t, validInboxWorkflow(models.InboxWorkflow{Name: "Synthetic", Kind: "macro", Conditions: models.InboxQueueFilter{Search: "anything"}, Actions: []models.InboxAction{{Type: "note", Value: "Synthetic"}}}))
}
