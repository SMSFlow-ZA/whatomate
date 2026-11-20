package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestInboxQualityHistoryFiltersCSVAndPausedRisk(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	a := &App{DB: db, Log: testutil.NopLogger()}
	user := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	require.NoError(t, db.Model(contact).Update("profile_name", "=SUM(1+1)").Error)
	p := models.InboxSLAPolicy{OrganizationID: org.ID, AccountID: account.ID, FirstReplyMinutes: 10, NextReplyMinutes: 15, ResolutionMinutes: 120, Timezone: "UTC"}
	require.NoError(t, db.Create(&p).Error)
	now := time.Now().UTC()
	start := now.Add(-110 * time.Minute)
	a.recordInboxInbound(account, contact, "paused-target", start, nil)
	c, err := (inboxThread{org.ID, account.ID, contact.ID}).latest(db)
	require.NoError(t, err)
	reply := start.Add(time.Minute)
	c.FirstHumanReplyAt, c.LastHumanReplyAt = &reply, &reply
	require.NoError(t, db.Save(&c).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return applyInboxTransition(tx, &c, inboxCaseUpdate{Status: models.InboxCaseWaiting}, &user.ID, now, "updated")
	}))
	live := testutil.NewGETRequest(t)
	testutil.SetAuthContext(live, org.ID, user.ID)
	require.NoError(t, a.GetInboxSLAReport(live))
	var risk struct {
		Cases   []inboxQueueRow
		Summary struct {
			AtRisk int64 `json:"at_risk"`
		}
	}
	testutil.ParseEnvelopeResponse(t, live, &risk)
	require.Empty(t, risk.Cases)
	require.Zero(t, risk.Summary.AtRisk)
	report := testutil.NewGETRequest(t)
	testutil.SetAuthContext(report, org.ID, user.ID)
	testutil.SetQueryParam(report, "account_id", account.ID.String())
	require.NoError(t, a.GetInboxSLAHistory(report))
	require.Equal(t, 200, testutil.GetResponseStatusCode(report))
	var history struct {
		Results []inboxSLAHistoryRow
		Summary struct {
			Total int64
			Met   int64
		}
	}
	testutil.ParseEnvelopeResponse(t, report, &history)
	require.Len(t, history.Results, 1)
	require.EqualValues(t, 1, history.Summary.Met)
	require.EqualValues(t, 1, history.Summary.Total)
	csv := testutil.NewGETRequest(t)
	testutil.SetAuthContext(csv, org.ID, user.ID)
	testutil.SetQueryParam(csv, "format", "csv")
	testutil.SetQueryParam(csv, "account_id", account.ID.String())
	require.NoError(t, a.GetInboxSLAHistory(csv))
	require.Equal(t, 200, testutil.GetResponseStatusCode(csv))
	require.Contains(t, string(csv.RequestCtx.Response.Body()), "'=SUM(1+1)")
	foreign := testutil.NewGETRequest(t)
	testutil.SetAuthContext(foreign, org.ID, user.ID)
	testutil.SetQueryParam(foreign, "account_id", uuid.NewString())
	require.NoError(t, a.GetInboxSLAHistory(foreign))
	testutil.ParseEnvelopeResponse(t, foreign, &history)
	require.Empty(t, history.Results)
}

func TestInboxQualityKeywordRulesUseOnlyTriggerAndSnoozeOnce(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	a := &App{DB: db, Log: testutil.NopLogger()}
	user := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	enabled := time.Now().UTC().Add(-time.Minute)
	first := models.InboxWorkflow{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, CreatedByID: user.ID, Kind: "rule", Name: "Synthetic priority", EnabledAt: &enabled, Position: 1, Trigger: "incoming_message", Actions: []models.InboxAction{{Type: "priority", Value: "high"}}}
	second := models.InboxWorkflow{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, CreatedByID: user.ID, Kind: "rule", Name: "Synthetic authored snooze", EnabledAt: &enabled, Position: 2, Trigger: "incoming_message", Conditions: models.InboxQueueFilter{Priority: models.InboxPriorityHigh, MessageContains: "orders"}, Actions: []models.InboxAction{{Type: "snooze", DurationMinutes: 15}}}
	require.NoError(t, db.Create(&first).Error)
	require.NoError(t, db.Create(&second).Error)
	message := models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, ContactID: contact.ID, WhatsAppAccount: account.Name, WhatsAppMessageID: "quality-keyword-1", Direction: models.DirectionIncoming, MessageType: models.MessageTypeText, Content: "Please check my ORDERS"}
	require.NoError(t, db.Create(&message).Error)
	a.recordInboxInbound(account, contact, message.WhatsAppMessageID, message.CreatedAt, nil)
	var event models.InboxCaseActivity
	require.NoError(t, db.First(&event, "organization_id=? AND event_key=?", org.ID, message.WhatsAppMessageID).Error)
	require.NoError(t, a.processInboxRuleEvent(context.Background(), event))
	require.NoError(t, a.processInboxRuleEvent(context.Background(), event))
	c, err := (inboxThread{org.ID, account.ID, contact.ID}).latest(db)
	require.NoError(t, err)
	require.Equal(t, models.InboxCaseSnoozed, c.Status)
	require.Equal(t, models.InboxPriorityHigh, c.Priority)
	require.NotNil(t, c.SnoozedUntil)
	var count int64
	require.NoError(t, db.Model(&models.InboxWorkflowRun{}).Where("organization_id=?", org.ID).Count(&count).Error)
	require.EqualValues(t, 2, count)
	next := models.Message{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, ContactID: contact.ID, WhatsAppAccount: account.Name, WhatsAppMessageID: "quality-keyword-2", Direction: models.DirectionIncoming, MessageType: models.MessageTypeText, Content: "Something unrelated"}
	require.NoError(t, db.Create(&next).Error)
	a.recordInboxInbound(account, contact, next.WhatsAppMessageID, next.CreatedAt, nil)
	event = models.InboxCaseActivity{}
	require.NoError(t, db.First(&event, "organization_id=? AND event_key=?", org.ID, next.WhatsAppMessageID).Error)
	require.NoError(t, a.processInboxRuleEvent(context.Background(), event))
	var skipped models.InboxWorkflowRun
	require.NoError(t, db.First(&skipped, "workflow_id=? AND event_id=?", second.ID, event.ID).Error)
	require.Equal(t, "skipped", skipped.State)
	require.NotNil(t, skipped.ConditionsMatched)
	require.False(t, *skipped.ConditionsMatched)
}

func TestInboxQualityAssignmentOptionsAndWriteStayOnSelectedAccount(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	a := &App{DB: db, Log: testutil.NopLogger()}
	user := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	other := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	a.recordInboxInbound(account, contact, "assignment-1", time.Now().UTC(), nil)
	a.recordInboxInbound(other, contact, "assignment-2", time.Now().UTC(), nil)
	c, err := (inboxThread{org.ID, account.ID, contact.ID}).latest(db)
	require.NoError(t, err)
	options := testutil.NewGETRequest(t)
	testutil.SetAuthContext(options, org.ID, user.ID)
	testutil.SetPathParam(options, "id", contact.ID.String())
	testutil.SetPathParam(options, "account_id", account.ID.String())
	require.NoError(t, a.InboxAssignmentOptions(options))
	require.Equal(t, 200, testutil.GetResponseStatusCode(options))
	assign := testutil.NewJSONRequest(t, map[string]any{"case_id": c.ID, "version": c.Version, "assignee_id": user.ID})
	testutil.SetAuthContext(assign, org.ID, user.ID)
	testutil.SetPathParam(assign, "id", contact.ID.String())
	testutil.SetPathParam(assign, "account_id", account.ID.String())
	require.NoError(t, a.AssignInboxConversation(assign))
	require.Equal(t, 200, testutil.GetResponseStatusCode(assign))
	untouched, err := (inboxThread{org.ID, other.ID, contact.ID}).latest(db)
	require.NoError(t, err)
	require.Nil(t, untouched.AssigneeID)
	require.NoError(t, db.First(contact, "id=?", contact.ID).Error)
	require.Nil(t, contact.AssignedUserID)
}

func TestInboxQualityPrivateMacroPreviewAndExecution(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	a := &App{DB: db, Log: testutil.NopLogger()}
	owner := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	outsider := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	a.recordInboxInbound(account, contact, "private-case", time.Now().UTC(), nil)
	c, err := (inboxThread{org.ID, account.ID, contact.ID}).latest(db)
	require.NoError(t, err)
	w := models.InboxWorkflow{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, CreatedByID: owner.ID, Kind: "macro", Name: "Private synthetic note", Visibility: "personal", Actions: []models.InboxAction{{Type: "note", Value: "PRIVATE_AUTHORED_TEXT"}}}
	require.NoError(t, db.Create(&w).Error)
	preview := func(user uuid.UUID) int {
		r := testutil.NewJSONRequest(t, map[string]any{"case_id": c.ID})
		testutil.SetAuthContext(r, org.ID, user)
		testutil.SetPathParam(r, "id", w.ID.String())
		require.NoError(t, a.PreviewInboxWorkflow(r))
		return testutil.GetResponseStatusCode(r)
	}
	require.Equal(t, 404, preview(outsider.ID))
	require.Equal(t, 200, preview(owner.ID))
	r := testutil.NewJSONRequest(t, map[string]any{"case_id": c.ID, "run_id": uuid.New(), "confirmation": workflowDigest("invalid")})
	testutil.SetAuthContext(r, org.ID, outsider.ID)
	testutil.SetPathParam(r, "id", w.ID.String())
	require.NoError(t, a.ExecuteInboxMacro(r))
	require.Equal(t, 404, testutil.GetResponseStatusCode(r))
	team := models.Team{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Name: "Synthetic team"}
	require.NoError(t, db.Create(&team).Error)
	require.NoError(t, db.Model(&w).Updates(map[string]any{"visibility": "team", "team_id": team.ID}).Error)
	require.Equal(t, 404, preview(outsider.ID))
	require.NoError(t, db.Create(&models.TeamMember{TeamID: team.ID, UserID: outsider.ID}).Error)
	require.Equal(t, 200, preview(outsider.ID))
	require.NoError(t, db.Model(&team).Update("is_active", false).Error)
	require.Equal(t, 404, preview(outsider.ID))
}

func TestInboxQualityAtomicResolutionRejectsPartialAndStaleValues(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	a := &App{DB: db, Log: testutil.NopLogger()}
	user := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	a.recordInboxInbound(account, contact, "resolve-case", time.Now().UTC(), nil)
	c, err := (inboxThread{org.ID, account.ID, contact.ID}).latest(db)
	require.NoError(t, err)
	fields := []models.InboxFieldDefinition{{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Scope: "case", Key: "confirmed", Label: "Confirmed", Kind: "boolean", RequiredOnResolve: true}, {BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Scope: "contact", Key: "amount", Label: "Amount", Kind: "number", RequiredOnResolve: true}}
	require.NoError(t, db.Create(&fields).Error)
	submit := func(version int64, second any) int {
		r := testutil.NewJSONRequest(t, map[string]any{"case_id": c.ID, "version": version, "values": []map[string]any{{"definition_id": fields[0].ID, "value": false}, {"definition_id": fields[1].ID, "value": second}}})
		testutil.SetAuthContext(r, org.ID, user.ID)
		testutil.SetPathParam(r, "account_id", account.ID.String())
		testutil.SetPathParam(r, "id", contact.ID.String())
		require.NoError(t, a.CompleteAndResolveInboxCase(r))
		return testutil.GetResponseStatusCode(r)
	}
	require.Equal(t, 400, submit(c.Version, "invalid number"))
	var count int64
	require.NoError(t, db.Model(&models.InboxFieldValue{}).Where("organization_id=?", org.ID).Count(&count).Error)
	require.Zero(t, count)
	require.Equal(t, 409, submit(c.Version-1, 0))
	require.Equal(t, 200, submit(c.Version, 0))
	var updated models.InboxCase
	require.NoError(t, db.First(&updated, "id=?", c.ID).Error)
	require.Equal(t, models.InboxCaseResolved, updated.Status)
	require.NoError(t, db.Model(&models.InboxFieldValue{}).Where("organization_id=?", org.ID).Count(&count).Error)
	require.EqualValues(t, 2, count)
}

func TestInboxQualityTypedAnyPredicatesCannotWidenScope(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	other := testutil.CreateTestOrganization(t, db)
	a := &App{DB: db, Log: testutil.NopLogger()}
	user := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	a.recordInboxInbound(account, contact, "predicate-case", time.Now().UTC(), nil)
	c, err := (inboxThread{org.ID, account.ID, contact.ID}).latest(db)
	require.NoError(t, err)
	d := models.InboxFieldDefinition{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Scope: "case", Key: "amount", Label: "Amount", Kind: "number"}
	require.NoError(t, db.Create(&d).Error)
	require.NoError(t, db.Create(&models.InboxFieldValue{OrganizationID: org.ID, DefinitionID: d.ID, SubjectID: c.ID, Value: json.RawMessage("0")}).Error)
	f := models.InboxQueueFilter{Match: "any", Predicates: []models.InboxPredicate{{Field: "status", Operator: "eq", Value: json.RawMessage(`"resolved"`)}, {Field: "custom_field", DefinitionID: &d.ID, Operator: "gte", Value: json.RawMessage("0")}}}
	q, err := a.inboxQueueQuery(org.ID, user.ID, f)
	require.NoError(t, err)
	var rows []models.InboxCase
	require.NoError(t, q.Select("inbox_cases.*").Find(&rows).Error)
	require.Len(t, rows, 1)
	q, err = a.inboxQueueQuery(other.ID, user.ID, f)
	require.Error(t, err)
	require.Nil(t, q)
	empty, _, err := inboxPredicateSQL(db, org.ID, models.InboxPredicate{Field: "custom_field", DefinitionID: &d.ID, Operator: "not_empty"}, true)
	require.NoError(t, err)
	require.Contains(t, empty, "EXISTS")
	require.NoError(t, db.Model(&d).Update("archived_at", time.Now().UTC()).Error)
	_, err = a.inboxQueueQuery(org.ID, user.ID, f)
	require.NoError(t, err)
}

func TestInboxQualityPresetPreviewIsReadOnlyAndRejectsStaleConfirmation(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	a := &App{DB: db, Log: testutil.NopLogger()}
	user := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	team := models.Team{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Name: "Synthetic support"}
	require.NoError(t, db.Create(&team).Error)
	require.NoError(t, db.Create(&models.TeamMember{TeamID: team.ID, UserID: user.ID}).Error)
	preset := models.InboxCapacityPreset{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Name: "Five active cases", MaxActive: 5}
	require.NoError(t, db.Create(&preset).Error)
	call := func(apply bool, confirmation string, ids []uuid.UUID) (int, string) {
		r := testutil.NewJSONRequest(t, inboxPresetApplication{TeamID: team.ID, AccountID: account.ID, AgentIDs: ids, Confirmation: confirmation})
		testutil.SetAuthContext(r, org.ID, user.ID)
		testutil.SetPathParam(r, "id", preset.ID.String())
		if apply {
			require.NoError(t, a.ApplyInboxCapacityPreset(r))
		} else {
			require.NoError(t, a.PreviewInboxCapacityPreset(r))
		}
		status := testutil.GetResponseStatusCode(r)
		var response struct{ Confirmation string }
		if status == 200 {
			testutil.ParseEnvelopeResponse(t, r, &response)
		}
		return status, response.Confirmation
	}
	status, confirmation := call(false, "", []uuid.UUID{user.ID})
	require.Equal(t, 200, status)
	var count int64
	require.NoError(t, db.Model(&models.InboxAgentCapacity{}).Where("organization_id=?", org.ID).Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, db.Model(&preset).Update("max_active", 6).Error)
	status, _ = call(true, confirmation, []uuid.UUID{user.ID})
	require.Equal(t, 409, status)
	status, confirmation = call(false, "", []uuid.UUID{user.ID})
	require.Equal(t, 200, status)
	status, _ = call(true, confirmation, []uuid.UUID{user.ID})
	require.Equal(t, 200, status)
	var limit models.InboxAgentCapacity
	require.NoError(t, db.First(&limit, "organization_id=? AND agent_id=?", org.ID, user.ID).Error)
	require.Equal(t, 6, limit.MaxActive)
	status, _ = call(false, "", []uuid.UUID{user.ID, user.ID})
	require.Equal(t, 400, status)
}

func TestInboxQualityRiskDiscoveryBeforePaginationAndPolicyRemoval(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	a := &App{DB: db, Log: testutil.NopLogger()}
	user := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	p := models.InboxSLAPolicy{OrganizationID: org.ID, AccountID: account.ID, FirstReplyMinutes: 60, NextReplyMinutes: 60, ResolutionMinutes: 120, Timezone: "UTC"}
	require.NoError(t, db.Create(&p).Error)
	now := time.Now().UTC()
	var overdue uuid.UUID
	for i := 0; i < 52; i++ {
		contact := testutil.CreateTestContact(t, db, org.ID)
		start := now
		if i == 51 {
			start = now.Add(-3 * time.Hour)
		}
		a.recordInboxInbound(account, contact, contact.ID.String(), start, nil)
		if i == 51 {
			c, err := (inboxThread{org.ID, account.ID, contact.ID}).latest(db)
			require.NoError(t, err)
			overdue = c.ID
		}
	}
	require.NoError(t, db.Delete(&p).Error)
	r := testutil.NewGETRequest(t)
	testutil.SetAuthContext(r, org.ID, user.ID)
	require.NoError(t, a.GetInboxSLAReport(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	var response struct {
		Cases   []inboxQueueRow
		Summary struct{ Breached int64 }
	}
	testutil.ParseEnvelopeResponse(t, r, &response)
	require.Len(t, response.Cases, 1)
	require.Equal(t, overdue, response.Cases[0].ID)
	require.EqualValues(t, 1, response.Summary.Breached)
}

func TestInboxQualityTerminalOrphanRunAndTimezone(t *testing.T) {
	require.Error(t, validateInboxPolicy(models.InboxSLAPolicy{FirstReplyMinutes: 1, NextReplyMinutes: 1, ResolutionMinutes: 1, Timezone: "Not/AZone"}))
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	run := models.InboxWorkflowRun{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, WorkflowID: uuid.New(), CaseID: uuid.New(), AccountID: uuid.New(), ContactID: uuid.New(), ActorID: uuid.New(), Kind: "macro", WorkflowName: "Synthetic retired case", WorkflowUpdatedAt: time.Now().UTC(), State: inboxRunPending}
	require.NoError(t, db.Create(&run).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return finishInboxSend(tx, run, inboxRunBlocked, "case_missing") }))
	require.NoError(t, db.First(&run, "id=?", run.ID).Error)
	require.Equal(t, inboxRunBlocked, run.State)
}

func TestInboxQualityHistoricalAssignmentCannotRestoreRevokedThreadAccess(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	a := &App{DB: db, Log: testutil.NopLogger()}
	agent := testutil.CreateTestUser(t, db, org.ID)
	other := testutil.CreateTestUser(t, db, org.ID)
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	now := time.Now().UTC()
	old := newInboxCase(inboxThread{org.ID, account.ID, contact.ID}, now.Add(-time.Hour), &agent.ID, nil)
	old.Status = models.InboxCaseResolved
	old.ResolvedAt = &now
	require.NoError(t, db.Create(&old).Error)
	var count int64
	require.NoError(t, a.scopeInboxCaseHistory(db.Model(&models.InboxCase{}), agent.ID, org.ID).Count(&count).Error)
	require.EqualValues(t, 1, count)
	current := newInboxCase(inboxThread{org.ID, account.ID, contact.ID}, now, &other.ID, nil)
	require.NoError(t, db.Create(&current).Error)
	require.NoError(t, a.scopeInboxCaseHistory(db.Model(&models.InboxCase{}), agent.ID, org.ID).Count(&count).Error)
	require.Zero(t, count)
}

func TestInboxQualityPreflightHasNoSideEffectsAndKeepsBlockedReply(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	a := &App{DB: db, Log: testutil.NopLogger()}
	user := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContactWith(t, db, org.ID, testutil.WithPhoneNumber("+12025550193"))
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	a.recordInboxInbound(account, contact, "synthetic-preflight", time.Now().UTC(), nil)
	c, err := (inboxThread{org.ID, account.ID, contact.ID}).latest(db)
	require.NoError(t, err)
	w := models.InboxWorkflow{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, CreatedByID: user.ID, Kind: "macro", Name: "Synthetic preview", Actions: []models.InboxAction{{Type: "priority", Value: "urgent"}, {Type: "note", Value: "Must not be persisted by preview"}, {Type: "send_text", Value: "Authored reply remains visible"}}}
	var before int64
	require.NoError(t, db.Model(&models.InboxCaseActivity{}).Where("case_id=?", c.ID).Count(&before).Error)
	preview, err := a.evaluateInboxWorkflow(db, w, c, *account, *contact, user.ID, time.Now().UTC())
	require.NoError(t, err)
	require.True(t, preview.SendUnavailable)
	require.Equal(t, "service_window_closed", preview.SendUnavailableReason)
	require.NotNil(t, preview.Reply)
	require.Equal(t, "Authored reply remains visible", preview.Reply.Text)
	require.Len(t, preview.ActionOutcomes, 3)
	var after int64
	require.NoError(t, db.Model(&models.InboxCaseActivity{}).Where("case_id=?", c.ID).Count(&after).Error)
	require.Equal(t, before, after)
	var unchanged models.InboxCase
	require.NoError(t, db.First(&unchanged, "id=?", c.ID).Error)
	require.Equal(t, c.Version, unchanged.Version)
	require.Equal(t, models.InboxPriorityNormal, unchanged.Priority)
	var runs int64
	require.NoError(t, db.Model(&models.InboxWorkflowRun{}).Where("organization_id=?", org.ID).Count(&runs).Error)
	require.Zero(t, runs)
}

func TestInboxQualityRevokedContactFieldAccessDoesNotPoisonRules(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	a := &App{DB: db, Log: testutil.NopLogger()}
	role := testutil.CreateTestRoleWithKeys(t, db, org.ID, "Synthetic restricted rule author", []string{"settings.general:write", "chat:write"})
	user := testutil.CreateTestUser(t, db, org.ID, testutil.WithRoleID(&role.ID))
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	require.NoError(t, db.Model(contact).Update("assigned_user_id", user.ID).Error)
	field := models.InboxFieldDefinition{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Scope: "contact", Key: "synthetic_reference", Label: "Reference", Kind: "text"}
	require.NoError(t, db.Create(&field).Error)
	enabled := time.Now().UTC().Add(-time.Minute)
	blocked := models.InboxWorkflow{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, CreatedByID: user.ID, Kind: "rule", Name: "Synthetic formerly permitted field", EnabledAt: &enabled, Position: 1, Trigger: "incoming_message", Conditions: models.InboxQueueFilter{Predicates: []models.InboxPredicate{{Field: "custom_field", DefinitionID: &field.ID, Operator: "eq", Value: json.RawMessage(`"yes"`)}}}, Actions: []models.InboxAction{{Type: "priority", Value: "urgent"}}}
	require.NoError(t, db.Create(&blocked).Error)
	valid := models.InboxWorkflow{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, CreatedByID: user.ID, Kind: "rule", Name: "Synthetic following rule", EnabledAt: &enabled, Position: 2, Trigger: "incoming_message", Actions: []models.InboxAction{{Type: "priority", Value: "high"}}}
	require.NoError(t, db.Create(&valid).Error)
	a.recordInboxInbound(account, contact, "synthetic-revoked-field", time.Now().UTC(), nil)
	var event models.InboxCaseActivity
	require.NoError(t, db.First(&event, "organization_id=? AND event_key=?", org.ID, "synthetic-revoked-field").Error)
	require.NoError(t, a.processInboxRuleEvent(context.Background(), event))
	require.NoError(t, a.processInboxRuleEvent(context.Background(), event))
	var run models.InboxWorkflowRun
	require.NoError(t, db.First(&run, "workflow_id=?", blocked.ID).Error)
	require.Equal(t, inboxRunBlocked, run.State)
	require.Equal(t, "conditions_unavailable", run.Outcome)
	current, err := (inboxThread{org.ID, account.ID, contact.ID}).latest(db)
	require.NoError(t, err)
	require.Equal(t, models.InboxPriorityHigh, current.Priority)
	require.NoError(t, db.First(&event, "id=?", event.ID).Error)
	require.NotNil(t, event.WorkflowProcessedAt)
	var count int64
	require.NoError(t, db.Model(&models.InboxWorkflowRun{}).Where("organization_id=?", org.ID).Count(&count).Error)
	require.EqualValues(t, 2, count)
}

func TestInboxQualityWorkloadReturnsAgentIdentityAndActualCapacity(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	a := &App{DB: db, Log: testutil.NopLogger()}
	user := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	require.NoError(t, db.Model(user).Updates(map[string]any{"full_name": "Synthetic workload agent", "is_available": true}).Error)
	team := models.Team{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Name: "Synthetic workload team"}
	require.NoError(t, db.Create(&team).Error)
	require.NoError(t, db.Create(&models.TeamMember{TeamID: team.ID, UserID: user.ID}).Error)
	require.NoError(t, db.Create(&models.InboxAgentCapacity{OrganizationID: org.ID, AccountID: account.ID, AgentID: user.ID, MaxActive: 7}).Error)
	c := newInboxCase(inboxThread{org.ID, account.ID, contact.ID}, time.Now().UTC(), &user.ID, &team.ID)
	require.NoError(t, db.Create(&c).Error)
	r := testutil.NewGETRequest(t)
	testutil.SetAuthContext(r, org.ID, user.ID)
	testutil.SetQueryParam(r, "team_id", team.ID.String())
	testutil.SetQueryParam(r, "account_id", account.ID.String())
	require.NoError(t, a.GetInboxWorkload(r))
	var response struct {
		Agents []struct {
			AgentID   uuid.UUID `json:"agent_id"`
			Name      string    `json:"name"`
			Available bool      `json:"available"`
			Active    int64     `json:"active"`
			MaxActive int       `json:"max_active"`
			TeamID    uuid.UUID `json:"team_id"`
			AccountID uuid.UUID `json:"account_id"`
		} `json:"agents"`
	}
	testutil.ParseEnvelopeResponse(t, r, &response)
	require.Len(t, response.Agents, 1)
	row := response.Agents[0]
	require.Equal(t, user.ID, row.AgentID)
	require.Equal(t, "Synthetic workload agent", row.Name)
	require.True(t, row.Available)
	require.EqualValues(t, 1, row.Active)
	require.Equal(t, 7, row.MaxActive)
	require.Equal(t, team.ID, row.TeamID)
	require.Equal(t, account.ID, row.AccountID)
}
