package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/inboxintegration"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func retryFixture(t *testing.T, retention int) integrationFixture {
	t.Helper()
	f := sequenceFixture(t, func(f integrationFixture) []models.InboxAction {
		return []models.InboxAction{{Type: "integration", Value: f.d.ID.String(), IntegrationInputs: f.mappings}, {Type: "note", Value: "After recovered operation"}}
	})
	endpoints, err := f.a.integrationEndpoints()
	require.NoError(t, err)
	endpoints[0].IdempotencyRetentionSeconds = retention
	raw, err := json.Marshal(endpoints)
	require.NoError(t, err)
	f.a.Config.InboxAutomation.IntegrationEndpoints = string(raw)
	return f
}

func unknownRetry(t *testing.T, f integrationFixture) (models.InboxWorkflowRun, models.InboxIntegrationExecution) {
	t.Helper()
	f.a.InboxIntegrationExecutor = fakeIntegration(func(context.Context, inboxintegration.Endpoint, string, string, []byte, string) (inboxintegration.Response, error) {
		return inboxintegration.Response{}, errors.New("synthetic lost reply")
	})
	run := queueSequenceMacro(t, f)
	paused := driveSequence(t, f, run.ID, 3)
	require.Equal(t, inboxSequenceUnknown, paused.State)
	var execution models.InboxIntegrationExecution
	require.NoError(t, f.a.DB.First(&execution, "run_id=?", run.ID).Error)
	require.Equal(t, "unknown", execution.State)
	return paused, execution
}

func retryPreviewRequest(t *testing.T, f integrationFixture, actor, id uuid.UUID) *fastglue.Request {
	t.Helper()
	r := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(r, f.c.OrganizationID, actor)
	r.RequestCtx.SetUserValue("execution_id", id.String())
	require.NoError(t, f.a.PreviewInboxIntegrationRetry(r))
	return r
}

func retryConfirmation(t *testing.T, f integrationFixture, id uuid.UUID) string {
	t.Helper()
	r := retryPreviewRequest(t, f, f.actor.ID, id)
	require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(testutil.GetResponseBody(r)))
	var preview struct {
		Eligible     bool   `json:"eligible"`
		Confirmation string `json:"confirmation"`
		Reason       string `json:"reason"`
	}
	testutil.ParseEnvelopeResponse(t, r, &preview)
	require.True(t, preview.Eligible, preview.Reason)
	require.NotEmpty(t, preview.Confirmation)
	require.NotContains(t, string(testutil.GetResponseBody(r)), "synthetic-secret")
	require.NotContains(t, string(testutil.GetResponseBody(r)), "/cases")
	return preview.Confirmation
}

func retrySubmit(t *testing.T, f integrationFixture, actor, id, operation uuid.UUID, confirmation, reason string) *fastglue.Request {
	t.Helper()
	r := testutil.NewJSONRequest(t, map[string]any{"operation_id": operation, "confirmation": confirmation, "reason": reason})
	testutil.SetAuthContext(r, f.c.OrganizationID, actor)
	r.RequestCtx.SetUserValue("execution_id", id.String())
	require.NoError(t, f.a.RetryInboxIntegration(r))
	return r
}

func TestInboxIntegrationRecoveryRetriesSameOperationAndKeepsOneProviderObject(t *testing.T) {
	f := retryFixture(t, 3600)
	var calls, objects atomic.Int32
	var mu sync.Mutex
	var firstID string
	var firstBody []byte
	f.a.InboxIntegrationExecutor = fakeIntegration(func(_ context.Context, _ inboxintegration.Endpoint, _, _ string, body []byte, id string) (inboxintegration.Response, error) {
		mu.Lock()
		defer mu.Unlock()
		calls.Add(1)
		if firstID == "" {
			firstID = id
			firstBody = append([]byte{}, body...)
			objects.Add(1)
			return inboxintegration.Response{}, errors.New("provider accepted; response lost")
		}
		if id != firstID || !bytes.Equal(firstBody, body) {
			return inboxintegration.Response{}, errors.New("synthetic idempotency conflict")
		}
		return inboxintegration.Response{Status: 200, Fields: map[string]json.RawMessage{"reference": json.RawMessage(`"CASE-IDEMPOTENT"`)}}, nil
	})
	run := queueSequenceMacro(t, f)
	paused := driveSequence(t, f, run.ID, 3)
	require.Equal(t, inboxSequenceUnknown, paused.State)
	var before models.InboxIntegrationExecution
	require.NoError(t, f.a.DB.First(&before, "run_id=?", run.ID).Error)
	require.NotNil(t, before.RetryUntil)
	confirmation := retryConfirmation(t, f, before.ID)
	op := uuid.New()
	reason := "Verified provider idempotency contract and original operation"
	for i := 0; i < 2; i++ {
		r := retrySubmit(t, f, f.actor.ID, before.ID, op, confirmation, reason)
		require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(testutil.GetResponseBody(r)))
	}
	r := retrySubmit(t, f, f.actor.ID, before.ID, op, confirmation, "Conflicting reason")
	require.Equal(t, 409, testutil.GetResponseStatusCode(r))
	var decisions int64
	require.NoError(t, f.a.DB.Model(&models.InboxIntegrationRecovery{}).Where("execution_id=?", before.ID).Count(&decisions).Error)
	require.EqualValues(t, 1, decisions)
	// Concurrent workers and a replacement App keep the same operation identity.
	f.a = &App{DB: f.a.DB, Config: f.a.Config, Log: f.a.Log, InboxIntegrationExecutor: f.a.InboxIntegrationExecutor}
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); failures <- f.a.runInboxSequences(context.Background()) }()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	final := driveSequence(t, f, run.ID, 5)
	require.Equal(t, inboxRunApplied, final.State)
	require.EqualValues(t, 2, calls.Load())
	require.EqualValues(t, 1, objects.Load())
	var after models.InboxIntegrationExecution
	require.NoError(t, f.a.DB.First(&after, "id=?", before.ID).Error)
	require.Equal(t, "succeeded", after.State)
	require.Equal(t, before.EncryptedRequest, after.EncryptedRequest)
	require.Equal(t, before.RequestDigest, after.RequestDigest)
	require.Equal(t, before.DefinitionDigest, after.DefinitionDigest)
	require.True(t, before.RetryUntil.Equal(*after.RetryUntil))
	require.Equal(t, 2, after.AttemptCount)
	var recovery models.InboxIntegrationRecovery
	require.NoError(t, f.a.DB.First(&recovery, "id=?", op).Error)
	require.Equal(t, "unknown", recovery.FromState)
	require.Equal(t, 1, recovery.AttemptCount)
	require.NotNil(t, recovery.PriorStartedAt)
	require.NotNil(t, recovery.PriorCompletedAt)
	history := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(history, f.c.OrganizationID, f.actor.ID)
	history.RequestCtx.SetUserValue("execution_id", before.ID.String())
	require.NoError(t, f.a.ListInboxIntegrationRecoveries(history))
	require.Equal(t, 200, testutil.GetResponseStatusCode(history))
	var recorded struct {
		Items []models.InboxIntegrationRecovery `json:"recoveries"`
	}
	testutil.ParseEnvelopeResponse(t, history, &recorded)
	require.Len(t, recorded.Items, 1)
	require.Equal(t, op, recorded.Items[0].ID)
	require.NotContains(t, string(testutil.GetResponseBody(history)), "request_digest")
	r = retrySubmit(t, f, f.actor.ID, before.ID, op, confirmation, reason)
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	var replay struct {
		Queued bool   `json:"queued"`
		State  string `json:"state"`
	}
	testutil.ParseEnvelopeResponse(t, r, &replay)
	require.False(t, replay.Queued)
	require.Equal(t, "succeeded", replay.State)
}

func TestInboxIntegrationRecoveryDefaultsToNoRetryWithoutRetention(t *testing.T) {
	f := retryFixture(t, 0)
	_, e := unknownRetry(t, f)
	require.Nil(t, e.RetryUntil)
	r := retryPreviewRequest(t, f, f.actor.ID, e.ID)
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	require.Contains(t, string(testutil.GetResponseBody(r)), "idempotency_window_unavailable")
	r = retrySubmit(t, f, f.actor.ID, e.ID, uuid.New(), strings.Repeat("f", 64), "No guarantee")
	require.Equal(t, 409, testutil.GetResponseStatusCode(r))
	var count int64
	require.NoError(t, f.a.DB.Model(&models.InboxIntegrationRecovery{}).Where("execution_id=?", e.ID).Count(&count).Error)
	require.Zero(t, count)
}

func TestInboxIntegrationRecoveryRejectsStaleUnsafeAndHeldRetries(t *testing.T) {
	for _, kind := range []string{"case", "definition", "expiry", "actor", "host_hold", "organization_hold"} {
		t.Run(kind, func(t *testing.T) {
			f := retryFixture(t, 3600)
			_, e := unknownRetry(t, f)
			confirmation := retryConfirmation(t, f, e.ID)
			switch kind {
			case "case":
				require.NoError(t, f.a.DB.Model(&f.c).Update("priority", "high").Error)
			case "definition":
				require.NoError(t, f.a.DB.Model(&f.d).Update("description", "Definition changed").Error)
			case "expiry":
				require.NoError(t, f.a.DB.Model(&e).Update("retry_until", time.Now().UTC().Add(20*time.Second)).Error)
			case "actor":
				require.NoError(t, f.a.DB.Model(f.actor).Update("is_active", false).Error)
			case "host_hold":
				f.a.Config.InboxAutomation.IntegrationDispatchEnabled = false
			case "organization_hold":
				require.NoError(t, f.a.DB.Model(&models.Organization{}).Where("id=?", f.c.OrganizationID).Update("settings", models.JSONB{"inbox_integrations_enabled": false}).Error)
			}
			r := retrySubmit(t, f, f.actor.ID, e.ID, uuid.New(), confirmation, "Review became stale")
			expected := 409
			if kind == "actor" {
				expected = 401 // A disabled identity must authenticate again; no recovery is applied.
			}
			require.Equal(t, expected, testutil.GetResponseStatusCode(r), string(testutil.GetResponseBody(r)))
			var stored models.InboxIntegrationExecution
			require.NoError(t, f.a.DB.First(&stored, "id=?", e.ID).Error)
			require.Equal(t, "unknown", stored.State)
			require.Equal(t, 1, stored.AttemptCount)
		})
	}
}

func TestInboxIntegrationRecoveryChecksExpiryAgainBeforeDispatch(t *testing.T) {
	f := retryFixture(t, 3600)
	run, e := unknownRetry(t, f)
	confirmation := retryConfirmation(t, f, e.ID)
	r := retrySubmit(t, f, f.actor.ID, e.ID, uuid.New(), confirmation, "Explicit retry inside declared window")
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	require.NoError(t, f.a.DB.Model(&e).Update("retry_until", time.Now().UTC().Add(20*time.Second)).Error)
	var calls atomic.Int32
	f.a.InboxIntegrationExecutor = fakeIntegration(func(context.Context, inboxintegration.Endpoint, string, string, []byte, string) (inboxintegration.Response, error) {
		calls.Add(1)
		return inboxintegration.Response{}, nil
	})
	paused := driveSequence(t, f, run.ID, 3)
	require.Equal(t, inboxSequenceUnknown, paused.State)
	require.Zero(t, calls.Load())
	require.NoError(t, f.a.DB.First(&e, "id=?", e.ID).Error)
	require.Equal(t, "unknown", e.State)
	require.Equal(t, "retry_window_expired", e.Outcome)
	require.Equal(t, 1, e.AttemptCount)
}

func TestInboxIntegrationRecoveryChecksExpiryAfterAWorkerClaim(t *testing.T) {
	f := retryFixture(t, 3600)
	_, e := unknownRetry(t, f)
	confirmation := retryConfirmation(t, f, e.ID)
	r := retrySubmit(t, f, f.actor.ID, e.ID, uuid.New(), confirmation, "Explicit retry before worker pause")
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	claimed, prepared, ok, err := f.a.claimInboxIntegration(context.Background(), e.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 2, claimed.AttemptCount)
	// Model elapsed time after claiming without sleeping or using a public
	// test endpoint. Production obtains this expiry from the immutable record.
	expiry := time.Now().UTC().Add(20 * time.Second)
	claimed.RetryUntil = &expiry
	var calls atomic.Int32
	f.a.InboxIntegrationExecutor = fakeIntegration(func(context.Context, inboxintegration.Endpoint, string, string, []byte, string) (inboxintegration.Response, error) {
		calls.Add(1)
		return inboxintegration.Response{}, nil
	})
	require.NoError(t, f.a.executeClaimedInboxIntegration(context.Background(), claimed, prepared))
	require.Zero(t, calls.Load())
	require.NoError(t, f.a.DB.First(&e, "id=?", e.ID).Error)
	require.Equal(t, "unknown", e.State)
	require.Equal(t, "retry_window_expired", e.Outcome)
}

func TestInboxIntegrationRecoveryHistoryScopeAndSecretRedaction(t *testing.T) {
	f := retryFixture(t, 3600)
	run, e := unknownRetry(t, f)
	confirmation := retryConfirmation(t, f, e.ID)
	for _, reason := range []string{"Credential synthetic-secret", "Copied /cases"} {
		r := retrySubmit(t, f, f.actor.ID, e.ID, uuid.New(), confirmation, reason)
		require.Equal(t, 400, testutil.GetResponseStatusCode(r))
	}
	r := retrySubmit(t, f, f.actor.ID, e.ID, uuid.New(), confirmation, "Safe explicit recovery reason")
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	list := func(actor, caseID uuid.UUID) *fastglue.Request {
		r := testutil.NewJSONRequest(t, map[string]any{})
		testutil.SetAuthContext(r, f.c.OrganizationID, actor)
		if caseID != uuid.Nil {
			r.RequestCtx.QueryArgs().Set("case_id", caseID.String())
		}
		require.NoError(t, f.a.ListInboxIntegrationExecutions(r))
		return r
	}
	r = list(f.actor.ID, f.c.ID)
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	body := string(testutil.GetResponseBody(r))
	for _, secret := range []string{"synthetic-secret", "encrypted_request", "encrypted_result", "definition_digest", "request_digest", "Synthetic customer", "/cases", "reconciliation_reason"} {
		require.NotContains(t, body, secret)
	}
	other := retryFixture(t, 3600)
	_, foreign := unknownRetry(t, other)
	r = list(f.actor.ID, other.c.ID)
	require.Contains(t, string(testutil.GetResponseBody(r)), `"executions":[]`)
	r = testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(r, f.c.OrganizationID, f.actor.ID)
	r.RequestCtx.SetUserValue("execution_id", foreign.ID.String())
	require.NoError(t, f.a.ListInboxIntegrationRecoveries(r))
	require.Equal(t, 404, testutil.GetResponseStatusCode(r))
	role := testutil.CreateTestRoleWithKeys(t, f.a.DB, f.c.OrganizationID, "Recovery history agent", []string{"chat:read", "chat:write", "custom_actions:write"})
	agent := testutil.CreateTestUser(t, f.a.DB, f.c.OrganizationID, testutil.WithRoleID(&role.ID))
	require.NoError(t, f.a.DB.Model(&f.c).Update("assignee_id", agent.ID).Error)
	require.NoError(t, f.a.DB.Model(&run).Update("initiated_by_id", agent.ID).Error)
	otherAccount := testutil.CreateTestWhatsAppAccount(t, f.a.DB, f.c.OrganizationID)
	otherCase := newInboxCase(inboxThread{f.c.OrganizationID, otherAccount.ID, f.c.ContactID}, time.Now().UTC(), &f.actor.ID, nil)
	require.NoError(t, f.a.DB.Create(&otherCase).Error)
	otherRun := newInboxWorkflowRun(uuid.New(), f.w, otherCase, agent.ID)
	require.NoError(t, f.a.DB.Create(&otherRun).Error)
	otherExecution := e
	otherExecution.ID, otherExecution.RunID, otherExecution.CaseID, otherExecution.AccountID, otherExecution.ActorID = uuid.New(), otherRun.ID, otherCase.ID, otherAccount.ID, agent.ID
	require.NoError(t, f.a.DB.Create(&otherExecution).Error)
	r = list(agent.ID, f.c.ID)
	require.Contains(t, string(testutil.GetResponseBody(r)), e.ID.String())
	r = list(agent.ID, uuid.Nil)
	require.NotContains(t, string(testutil.GetResponseBody(r)), otherExecution.ID.String(), "same contact on another account must remain scoped")
	r = retryPreviewRequest(t, f, agent.ID, e.ID)
	require.Equal(t, 403, testutil.GetResponseStatusCode(r))
	require.NoError(t, f.a.DB.Model(&f.c).Update("assignee_id", f.actor.ID).Error)
	r = list(agent.ID, f.c.ID)
	require.Contains(t, string(testutil.GetResponseBody(r)), `"executions":[]`)
}

func TestInboxIntegrationRecoveryHistoryHasBoundedStablePaging(t *testing.T) {
	f := retryFixture(t, 3600)
	_, e := unknownRetry(t, f)
	tied := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	for i := 0; i < 52; i++ {
		run := newInboxWorkflowRun(uuid.New(), f.w, f.c, f.actor.ID)
		require.NoError(t, f.a.DB.Create(&run).Error)
		copy := e
		copy.ID, copy.RunID, copy.CreatedAt = uuid.New(), run.ID, tied
		require.NoError(t, f.a.DB.Create(&copy).Error)
	}
	var ids []uuid.UUID
	cursor := ""
	for page := 0; page < 2; page++ {
		r := testutil.NewJSONRequest(t, map[string]any{})
		testutil.SetAuthContext(r, f.c.OrganizationID, f.actor.ID)
		r.RequestCtx.QueryArgs().Set("case_id", f.c.ID.String())
		if cursor != "" {
			r.RequestCtx.QueryArgs().Set("cursor", cursor)
		}
		require.NoError(t, f.a.ListInboxIntegrationExecutions(r))
		require.Equal(t, 200, testutil.GetResponseStatusCode(r))
		var result struct {
			Items  []inboxIntegrationHistoryItem `json:"executions"`
			More   bool                          `json:"has_more"`
			Cursor string                        `json:"next_cursor"`
		}
		testutil.ParseEnvelopeResponse(t, r, &result)
		if page == 0 {
			require.Len(t, result.Items, 50)
			require.True(t, result.More)
		} else {
			require.Len(t, result.Items, 3)
			require.False(t, result.More)
		}
		for _, item := range result.Items {
			require.NotContains(t, ids, item.ID)
			ids = append(ids, item.ID)
		}
		cursor = result.Cursor
	}
	require.Len(t, ids, 53)
	for _, value := range []string{"###", strings.Repeat("a", 257), "e30"} {
		r := testutil.NewJSONRequest(t, map[string]any{})
		testutil.SetAuthContext(r, f.c.OrganizationID, f.actor.ID)
		r.RequestCtx.QueryArgs().Set("cursor", value)
		require.NoError(t, f.a.ListInboxIntegrationExecutions(r))
		require.Equal(t, 400, testutil.GetResponseStatusCode(r))
	}
}

func TestInboxIntegrationRecoveryDoesNotWaitOnExecutingRow(t *testing.T) {
	f := retryFixture(t, 3600)
	_, e := unknownRetry(t, f)
	confirmation := retryConfirmation(t, f, e.ID)
	locked := f.a.DB.Begin()
	require.NoError(t, locked.Error)
	defer locked.Rollback()
	require.NoError(t, locked.Clauses(clause.Locking{Strength: "UPDATE"}).First(&models.InboxIntegrationExecution{}, "id=?", e.ID).Error)
	started := time.Now()
	r := retrySubmit(t, f, f.actor.ID, e.ID, uuid.New(), confirmation, "Concurrent operator review")
	require.Equal(t, 409, testutil.GetResponseStatusCode(r))
	require.Less(t, time.Since(started), 2*time.Second)
}

func TestInboxIntegrationRecoveryRejectsCredentialShapedNumericOutputs(t *testing.T) {
	f := retryFixture(t, 3600)
	credential, err := crypto.Encrypt("12345", f.a.Config.App.EncryptionKey)
	require.NoError(t, err)
	field := models.InboxFieldDefinition{OrganizationID: f.c.OrganizationID, Scope: "case", Key: "provider_number", Label: "Provider number", Kind: "number"}
	require.NoError(t, f.a.DB.Create(&field).Error)
	f.d.Credential, f.d.Outputs = credential, []models.InboxIntegrationOutput{{Name: "number", DefinitionID: field.ID}}
	require.NoError(t, f.a.DB.Save(&f.d).Error)
	f.a.InboxIntegrationExecutor = fakeIntegration(func(context.Context, inboxintegration.Endpoint, string, string, []byte, string) (inboxintegration.Response, error) {
		return inboxintegration.Response{Status: 200, Fields: map[string]json.RawMessage{"reference": json.RawMessage(`"SAFE-REFERENCE"`), "number": json.RawMessage(`12345`)}}, nil
	})
	run := queueSequenceMacro(t, f)
	paused := driveSequence(t, f, run.ID, 3)
	require.Equal(t, inboxSequenceUnknown, paused.State)
	var e models.InboxIntegrationExecution
	require.NoError(t, f.a.DB.First(&e, "run_id=?", run.ID).Error)
	require.Equal(t, "output_invalid", e.Outcome)
	require.Empty(t, e.EncryptedResult)
	var count int64
	require.NoError(t, f.a.DB.Model(&models.InboxFieldValue{}).Where("definition_id=?", field.ID).Count(&count).Error)
	require.Zero(t, count)
}

func TestInboxIntegrationRecoveryHistoryRepresentativeCostAndCursorIndexes(t *testing.T) {
	f := retryFixture(t, 3600)
	_, e := unknownRetry(t, f)
	// Small tables legitimately favour an in-memory scan. Use enough retained
	// operations to exercise the production cursor index without planner hints.
	runs := make([]models.InboxWorkflowRun, 20000)
	executions := make([]models.InboxIntegrationExecution, 20000)
	for i := range runs {
		runs[i] = newInboxWorkflowRun(uuid.New(), f.w, f.c, f.actor.ID)
		executions[i] = e
		executions[i].ID, executions[i].RunID = uuid.New(), runs[i].ID
		executions[i].CreatedAt = time.Now().UTC().Add(time.Duration(-i) * time.Minute)
	}
	require.NoError(t, f.a.DB.CreateInBatches(&runs, 100).Error)
	require.NoError(t, f.a.DB.CreateInBatches(&executions, 100).Error)
	require.NoError(t, database.CreateInboxWorkflowIndexes(f.a.DB))
	require.True(t, f.a.DB.Migrator().HasIndex(&models.InboxIntegrationExecution{}, "idx_inbox_integration_history"))
	require.True(t, f.a.DB.Migrator().HasIndex(&models.InboxIntegrationRecovery{}, "idx_inbox_integration_recovery_history"))
	for _, table := range []string{"inbox_integration_executions", "inbox_workflow_runs", "inbox_cases"} {
		require.NoError(t, f.a.DB.Exec("ANALYZE "+table).Error)
	}
	sql := f.a.DB.ToSQL(func(tx *gorm.DB) *gorm.DB {
		return f.a.scopedInboxIntegrationHistory(tx, f.c.OrganizationID, f.actor.ID).Select(integrationHistoryColumns).Order("created_at DESC,id DESC").Limit(51).Find(&[]models.InboxIntegrationExecution{})
	})
	rows, err := f.a.DB.Raw("EXPLAIN (ANALYZE, FORMAT TEXT) " + sql).Rows()
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var plan strings.Builder
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		plan.WriteString(line + "\n")
	}
	require.NoError(t, rows.Err())
	t.Log("Scoped 20,000-operation history plan:\n" + plan.String())
	// PostgreSQL legitimately chooses different indexes as tenant cardinality
	// changes. Qualify real first/deep cursor pages rather than forcing a named
	// planner path. This coarse local/race budget is not a customer SLA.
	for _, cursor := range []string{"", integrationHistoryCursor(executions[19000].CreatedAt, executions[19000].ID)} {
		r := testutil.NewJSONRequest(t, map[string]any{})
		testutil.SetAuthContext(r, f.c.OrganizationID, f.actor.ID)
		if cursor != "" {
			r.RequestCtx.QueryArgs().Set("cursor", cursor)
		}
		started := time.Now()
		require.NoError(t, f.a.ListInboxIntegrationExecutions(r))
		elapsed := time.Since(started)
		require.Equal(t, 200, testutil.GetResponseStatusCode(r))
		var page struct {
			Items []inboxIntegrationHistoryItem `json:"executions"`
		}
		testutil.ParseEnvelopeResponse(t, r, &page)
		require.Len(t, page.Items, 50)
		t.Logf("20,000-operation first/deep API page: %s", elapsed)
		require.Less(t, elapsed, 2*time.Second)
	}
}
