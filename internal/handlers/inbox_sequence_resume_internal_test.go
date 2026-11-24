package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/inboxintegration"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func pausedResumeFixture(t *testing.T) (integrationFixture, models.InboxWorkflowRun, map[string]any) {
	t.Helper()
	f := retryFixture(t, 3600)
	run := queueSequenceMacro(t, f)
	require.NoError(t, f.a.DB.Model(&run).Updates(map[string]any{"state": inboxSequenceReview, "outcome": "synthetic_review_required"}).Error)
	confirmation := sequenceReviewRequest(t, f, f.actor.ID, run.ID)
	return f, run, map[string]any{"operation_id": uuid.New(), "confirmation": confirmation, "reason": "Reviewed synthetic remaining actions"}
}

func resumeReceiptRequest(t *testing.T, f integrationFixture, actor, org, run uuid.UUID, input map[string]any) *fastglue.Request {
	t.Helper()
	r := testutil.NewJSONRequest(t, input)
	testutil.SetAuthContext(r, org, actor)
	r.RequestCtx.SetUserValue("run_id", run.String())
	require.NoError(t, f.a.ResumeInboxSequence(r))
	return r
}

func TestInboxSequenceResumeReceiptSurvivesWorkerRestartAndDoesNotRepeatCompletedWork(t *testing.T) {
	f, run, input := pausedResumeFixture(t)
	var calls atomic.Int32
	f.a.InboxIntegrationExecutor = fakeIntegration(func(context.Context, inboxintegration.Endpoint, string, string, []byte, string) (inboxintegration.Response, error) {
		calls.Add(1)
		return inboxintegration.Response{Status: 200, Fields: map[string]json.RawMessage{"reference": json.RawMessage(`"RESUME-RECEIPT"`)}}, nil
	})
	r := resumeReceiptRequest(t, f, f.actor.ID, f.c.OrganizationID, run.ID, input)
	require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(testutil.GetResponseBody(r)))
	require.NotContains(t, string(testutil.GetResponseBody(r)), "synthetic-secret")
	require.NotContains(t, string(testutil.GetResponseBody(r)), "encrypted_sequence_plan")
	var out struct {
		Recovery models.InboxIntegrationRecovery `json:"recovery"`
	}
	testutil.ParseEnvelopeResponse(t, r, &out)
	require.Equal(t, input["operation_id"], out.Recovery.ID)
	require.Equal(t, "resume_sequence", out.Recovery.Kind)
	require.Equal(t, uuid.Nil, out.Recovery.ExecutionID)
	require.Zero(t, calls.Load(), "recording the decision must not call a provider")
	final := driveSequence(t, f, run.ID, 8)
	require.Equal(t, inboxRunApplied, final.State)
	require.EqualValues(t, 1, calls.Load())
	config := *f.a.Config
	f.a = &App{DB: f.a.DB, Config: &config, Log: f.a.Log, InboxIntegrationExecutor: f.a.InboxIntegrationExecutor}
	r = resumeReceiptRequest(t, f, f.actor.ID, f.c.OrganizationID, run.ID, input)
	require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(testutil.GetResponseBody(r)))
	testutil.ParseEnvelopeResponse(t, r, &out)
	require.Equal(t, input["operation_id"], out.Recovery.ID)
	_ = driveSequence(t, f, run.ID, 3)
	require.EqualValues(t, 1, calls.Load())
	var count int64
	require.NoError(t, f.a.DB.Model(&models.InboxIntegrationRecovery{}).Where("run_id=? AND kind='resume_sequence'", run.ID).Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.NoError(t, f.a.DB.Model(&models.InboxCaseActivity{}).Where("case_id=? AND kind='workflow_note'", f.c.ID).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestInboxSequenceResumeReceiptConcurrentDecisionsCommitOnce(t *testing.T) {
	f, run, input := pausedResumeFixture(t)
	statuses := make(chan int, 6)
	var workers sync.WaitGroup
	for i := 0; i < 6; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			r := resumeReceiptRequest(t, f, f.actor.ID, f.c.OrganizationID, run.ID, input)
			statuses <- testutil.GetResponseStatusCode(r)
		}()
	}
	workers.Wait()
	require.Len(t, statuses, 6)
	close(statuses)
	for status := range statuses {
		require.Equal(t, 200, status)
	}
	var count int64
	require.NoError(t, f.a.DB.Model(&models.InboxIntegrationRecovery{}).Where("run_id=?", run.ID).Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.NoError(t, f.a.DB.Model(&models.InboxCaseActivity{}).Where("case_id=? AND kind='workflow_sequence_reviewed'", f.c.ID).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestInboxSequenceResumeReceiptRejectsConflictingReuseAndKeepsLegacyRequests(t *testing.T) {
	f, run, input := pausedResumeFixture(t)
	require.Equal(t, 200, testutil.GetResponseStatusCode(resumeReceiptRequest(t, f, f.actor.ID, f.c.OrganizationID, run.ID, input)))
	changed := map[string]any{"operation_id": input["operation_id"], "confirmation": input["confirmation"], "reason": "Changed decision payload"}
	require.Equal(t, 409, testutil.GetResponseStatusCode(resumeReceiptRequest(t, f, f.actor.ID, f.c.OrganizationID, run.ID, changed)))
	other := testutil.CreateTestUser(t, f.a.DB, f.c.OrganizationID, testutil.WithSuperAdmin())
	require.Equal(t, 409, testutil.GetResponseStatusCode(resumeReceiptRequest(t, f, other.ID, f.c.OrganizationID, run.ID, input)))
	copy := run
	copy.ID = uuid.New()
	copy.OrganizationID = f.c.OrganizationID // Organisation is intentionally absent from the API DTO.
	require.NoError(t, f.a.DB.Create(&copy).Error)
	require.Equal(t, 409, testutil.GetResponseStatusCode(resumeReceiptRequest(t, f, f.actor.ID, f.c.OrganizationID, copy.ID, input)))
	legacy, legacyRun, legacyInput := pausedResumeFixture(t)
	delete(legacyInput, "operation_id")
	r := resumeReceiptRequest(t, legacy, legacy.actor.ID, legacy.c.OrganizationID, legacyRun.ID, legacyInput)
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	require.NotContains(t, string(testutil.GetResponseBody(r)), `"recovery":`)
	require.Equal(t, 409, testutil.GetResponseStatusCode(resumeReceiptRequest(t, legacy, legacy.actor.ID, legacy.c.OrganizationID, legacyRun.ID, legacyInput)))
	var count int64
	require.NoError(t, legacy.a.DB.Model(&models.InboxIntegrationRecovery{}).Where("run_id=?", legacyRun.ID).Count(&count).Error)
	require.Zero(t, count)
}

func TestInboxSequenceResumeReceiptValidatesUUIDStaleReviewsAndSafeReasons(t *testing.T) {
	f, run, input := pausedResumeFixture(t)
	for _, reason := range []string{"line\nbreak", "synthetic-secret", "https://crm.example.invalid/cases"} {
		bad := map[string]any{"operation_id": uuid.New(), "confirmation": input["confirmation"], "reason": reason}
		r := resumeReceiptRequest(t, f, f.actor.ID, f.c.OrganizationID, run.ID, bad)
		require.Equal(t, 400, testutil.GetResponseStatusCode(r))
		require.NotContains(t, string(testutil.GetResponseBody(r)), reason)
	}
	bad := map[string]any{"operation_id": uuid.Nil, "confirmation": input["confirmation"], "reason": "Synthetic review"}
	require.Equal(t, 400, testutil.GetResponseStatusCode(resumeReceiptRequest(t, f, f.actor.ID, f.c.OrganizationID, run.ID, bad)))
	require.NoError(t, f.a.DB.Model(&f.c).Updates(map[string]any{"priority": "high", "version": gorm.Expr("version+1")}).Error)
	require.Equal(t, 409, testutil.GetResponseStatusCode(resumeReceiptRequest(t, f, f.actor.ID, f.c.OrganizationID, run.ID, input)))
	var count int64
	require.NoError(t, f.a.DB.Model(&models.InboxIntegrationRecovery{}).Where("run_id=?", run.ID).Count(&count).Error)
	require.Zero(t, count)
}

func TestInboxSequenceResumeReceiptHistoryFollowsCurrentAgentAndAccountScope(t *testing.T) {
	f, run, input := pausedResumeFixture(t)
	role := testutil.CreateTestRoleWithKeys(t, f.a.DB, f.c.OrganizationID, "Resume agent", []string{"chat:read", "chat:write", "custom_actions:write", "contacts:read", "contacts:write"})
	agent := testutil.CreateTestUser(t, f.a.DB, f.c.OrganizationID, testutil.WithRoleID(&role.ID))
	require.NoError(t, f.a.DB.Model(&f.c).Update("assignee_id", agent.ID).Error)
	require.NoError(t, f.a.DB.Model(&run).Updates(map[string]any{"actor_id": agent.ID, "initiated_by_id": agent.ID}).Error)
	input["confirmation"] = sequenceReviewRequest(t, f, agent.ID, run.ID)
	r := resumeReceiptRequest(t, f, agent.ID, f.c.OrganizationID, run.ID, input)
	require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(testutil.GetResponseBody(r)))
	list := func() *fastglue.Request {
		r := testutil.NewJSONRequest(t, map[string]any{})
		testutil.SetAuthContext(r, f.c.OrganizationID, agent.ID)
		r.RequestCtx.SetUserValue("run_id", run.ID.String())
		require.NoError(t, f.a.ListInboxRunRecoveries(r))
		return r
	}
	r = list()
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	require.Contains(t, string(testutil.GetResponseBody(r)), input["operation_id"].(uuid.UUID).String())
	evidence := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(evidence, f.c.OrganizationID, agent.ID)
	evidence.RequestCtx.SetUserValue("execution_id", uuid.NewString())
	evidence.RequestCtx.SetUserValue("recovery_id", input["operation_id"].(uuid.UUID).String())
	require.NoError(t, f.a.GetInboxIntegrationRecoveryEvidence(evidence))
	require.Equal(t, 403, testutil.GetResponseStatusCode(evidence), "metadata read must not grant evidence decryption")
	require.NoError(t, f.a.DB.Model(&models.Organization{}).Where("id=?", f.c.OrganizationID).Update("inbox_workflows_enabled", false).Error)
	require.Equal(t, 200, testutil.GetResponseStatusCode(list()), "a recorded decision remains inspectable while dispatch is held")
	otherAccount := testutil.CreateTestWhatsAppAccount(t, f.a.DB, f.c.OrganizationID)
	require.NoError(t, f.a.DB.Model(&run).Update("account_id", otherAccount.ID).Error)
	require.Equal(t, 404, testutil.GetResponseStatusCode(list()), "a mixed-account run must not borrow a visible case")
	require.NoError(t, f.a.DB.Model(&run).Update("account_id", f.c.AccountID).Error)
	require.NoError(t, f.a.DB.Model(&f.c).Update("assignee_id", f.actor.ID).Error)
	require.Equal(t, 200, testutil.GetResponseStatusCode(list()), "explicit contact-read access survives reassignment")
	limited := testutil.CreateTestRoleWithKeys(t, f.a.DB, f.c.OrganizationID, "Assignment-only resume reader", []string{"chat:read", "chat:write", "custom_actions:write"})
	require.NoError(t, f.a.DB.Model(agent).Update("role_id", limited.ID).Error)
	require.NoError(t, f.a.DB.Model(&models.UserOrganization{}).Where("user_id=? AND organization_id=?", agent.ID, f.c.OrganizationID).Update("role_id", limited.ID).Error)
	require.Equal(t, 404, testutil.GetResponseStatusCode(list()), "ownership must not bypass current case access")
	foreignOrg := testutil.CreateTestOrganization(t, f.a.DB)
	foreignRead := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(foreignRead, foreignOrg.ID, agent.ID)
	foreignRead.RequestCtx.SetUserValue("run_id", run.ID.String())
	require.NoError(t, f.a.ListInboxRunRecoveries(foreignRead))
	require.Contains(t, []int{403, 404}, testutil.GetResponseStatusCode(foreignRead))
	require.NotContains(t, string(testutil.GetResponseBody(foreignRead)), input["operation_id"].(uuid.UUID).String())
	reader := testutil.CreateTestRoleWithKeys(t, f.a.DB, f.c.OrganizationID, "Read-only resume history", []string{"chat:read", "contacts:read"})
	require.NoError(t, f.a.DB.Model(agent).Update("role_id", reader.ID).Error)
	require.NoError(t, f.a.DB.Model(&models.UserOrganization{}).Where("user_id=? AND organization_id=?", agent.ID, f.c.OrganizationID).Update("role_id", reader.ID).Error)
	require.Equal(t, 200, testutil.GetResponseStatusCode(list()))
	require.Equal(t, 403, testutil.GetResponseStatusCode(resumeReceiptRequest(t, f, agent.ID, f.c.OrganizationID, run.ID, input)), "metadata access does not authorise a write or replay")
}

func TestInboxSequenceResumeReceiptAdoptionRetainsOriginalInitiator(t *testing.T) {
	f, run, input := pausedResumeFixture(t)
	original := f.actor.ID
	supervisor := testutil.CreateTestUser(t, f.a.DB, f.c.OrganizationID, testutil.WithSuperAdmin())
	require.NoError(t, f.a.DB.Model(f.actor).Update("is_active", false).Error)
	input["confirmation"] = sequenceReviewRequest(t, f, supervisor.ID, run.ID)
	r := resumeReceiptRequest(t, f, supervisor.ID, f.c.OrganizationID, run.ID, input)
	require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(testutil.GetResponseBody(r)))
	require.NoError(t, f.a.DB.First(&run, "id=?", run.ID).Error)
	require.Equal(t, supervisor.ID, run.ActorID)
	require.NotNil(t, run.InitiatedByID)
	require.Equal(t, original, *run.InitiatedByID)
	var decision models.InboxIntegrationRecovery
	require.NoError(t, f.a.DB.First(&decision, "id=?", input["operation_id"]).Error)
	require.Equal(t, supervisor.ID, decision.ActorID)
	require.Equal(t, 403, testutil.GetResponseStatusCode(resumeReceiptRequest(t, f, original, f.c.OrganizationID, run.ID, input)))
}

func TestInboxSequenceResumeReceiptRollsBackAndRedactsStorageFailure(t *testing.T) {
	f, run, input := pausedResumeFixture(t)
	constraint := "synthetic_resume_rejection_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, f.a.DB.Exec("ALTER TABLE inbox_integration_recoveries ADD CONSTRAINT "+constraint+" CHECK (organization_id <> '"+f.c.OrganizationID.String()+"'::uuid)").Error)
	t.Cleanup(func() {
		f.a.DB.Exec("ALTER TABLE inbox_integration_recoveries DROP CONSTRAINT IF EXISTS " + constraint)
	})
	var buffer bytes.Buffer
	f.a.DB = f.a.DB.Session(&gorm.Session{Logger: logger.New(log.New(&buffer, "", 0), logger.Config{LogLevel: logger.Info})})
	var probe int
	require.NoError(t, f.a.DB.Raw("SELECT 1").Scan(&probe).Error)
	marker := "SYNTHETIC-RESUME-DECISION-DO-NOT-LOG"
	input["reason"] = marker
	r := resumeReceiptRequest(t, f, f.actor.ID, f.c.OrganizationID, run.ID, input)
	require.Equal(t, 500, testutil.GetResponseStatusCode(r))
	require.Contains(t, buffer.String(), "SELECT")
	require.NotContains(t, buffer.String(), marker)
	require.NotContains(t, string(testutil.GetResponseBody(r)), marker)
	require.NoError(t, f.a.DB.First(&run, "id=?", run.ID).Error)
	require.Equal(t, inboxSequenceReview, run.State)
	var count int64
	require.NoError(t, f.a.DB.Model(&models.InboxIntegrationRecovery{}).Where("run_id=?", run.ID).Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, f.a.DB.Model(&models.InboxCaseActivity{}).Where("case_id=? AND kind='workflow_sequence_reviewed'", f.c.ID).Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, f.a.DB.Exec("ALTER TABLE inbox_integration_recoveries DROP CONSTRAINT "+constraint).Error)
	r = resumeReceiptRequest(t, f, f.actor.ID, f.c.OrganizationID, run.ID, input)
	require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(testutil.GetResponseBody(r)))
}

func TestInboxSequenceResumeReceiptHistoryRejectsMismatchedExecutionLinks(t *testing.T) {
	f, run, input := pausedResumeFixture(t)
	require.Equal(t, 200, testutil.GetResponseStatusCode(resumeReceiptRequest(t, f, f.actor.ID, f.c.OrganizationID, run.ID, input)))
	e := f.enqueue(t)
	validLegacy := models.InboxIntegrationRecovery{OrganizationID: f.c.OrganizationID, ExecutionID: e.ID, ActorID: f.actor.ID, Kind: "retry", FromState: "unknown", Reason: "Legacy metadata", RequestDigest: strings.Repeat("a", 64)}
	require.NoError(t, f.a.DB.Create(&validLegacy).Error)
	// This execution belongs to f.run, not the target sequenced run. Neither
	// an explicit mismatched link nor a null-run legacy link may borrow it.
	bad := validLegacy
	bad.ID, bad.RunID = uuid.New(), &run.ID
	require.NoError(t, f.a.DB.Create(&bad).Error)
	r := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(r, f.c.OrganizationID, f.actor.ID)
	r.RequestCtx.SetUserValue("run_id", run.ID.String())
	require.NoError(t, f.a.ListInboxRunRecoveries(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	require.NotContains(t, string(testutil.GetResponseBody(r)), bad.ID.String())
	require.NotContains(t, string(testutil.GetResponseBody(r)), validLegacy.ID.String())
	require.Contains(t, string(testutil.GetResponseBody(r)), input["operation_id"].(uuid.UUID).String())
	legacyRead := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(legacyRead, f.c.OrganizationID, f.actor.ID)
	legacyRead.RequestCtx.SetUserValue("run_id", f.run.ID.String())
	require.NoError(t, f.a.ListInboxRunRecoveries(legacyRead))
	require.Equal(t, 200, testutil.GetResponseStatusCode(legacyRead))
	require.Contains(t, string(testutil.GetResponseBody(legacyRead)), validLegacy.ID.String(), "valid null-run historical records remain readable")
	require.NotContains(t, string(testutil.GetResponseBody(legacyRead)), bad.ID.String())
}
