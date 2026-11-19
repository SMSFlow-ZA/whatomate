package handlers

import (
	"context"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/database"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestInboxHardeningOrphanEventsStarveLiveWork(t *testing.T) {
	db := testutil.SetupTestDB(t)
	a := &App{DB: db, Log: testutil.NopLogger()}
	org := testutil.CreateTestOrganization(t, db)
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	for range 50 {
		c := newInboxCase(inboxThread{org.ID, account.ID, contact.ID}, time.Now().UTC().Add(-time.Hour), nil, nil)
		c.Status = models.InboxCaseResolved
		require.NoError(t, db.Create(&c).Error)
		require.NoError(t, appendInboxActivity(db, c, models.InboxCaseActivity{Kind: "opened"}))
	}
	require.NoError(t, db.Delete(contact).Error)
	live := testutil.CreateTestContact(t, db, org.ID)
	a.recordInboxInbound(account, live, "live-review-event", time.Now().UTC(), nil)
	for range 2 {
		require.NoError(t, a.runInboxWorkflowCycle(context.Background()))
	}
	var event models.InboxCaseActivity
	require.NoError(t, db.First(&event, "event_key = ?", "live-review-event").Error)
	require.NotNil(t, event.WorkflowProcessedAt)
}

func TestInboxHardeningQueuedRuleSendsAfterConditionChanges(t *testing.T) {
	db := testutil.SetupTestDB(t)
	rdb := testutil.SetupTestRedis(t)
	require.NotNil(t, rdb)
	a := &App{DB: db, Redis: rdb, Log: testutil.NopLogger(), Config: &config.Config{
		InboxAutomation: config.InboxAutomationConfig{DispatchEnabled: true},
	}}
	var calls atomic.Int32
	a.WhatsApp = whatsapp.NewWithTimeout(a.Log, time.Second)
	a.WhatsApp.HTTPClient = &http.Client{Transport: inboxFixtureTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader("{\"messages\":[{\"id\":\"wamid.synthetic.review\"}]}"))}, nil
	})}
	org := testutil.CreateTestOrganization(t, db)
	operator := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	require.NoError(t, db.Model(org).Updates(map[string]any{"inbox_workflows_enabled": true, "inbox_automation_sends_enabled": true}).Error)
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContactWith(t, db, org.ID, testutil.WithPhoneNumber("+12025550191"))
	enabled := time.Now().UTC().Add(-time.Hour)
	w := models.InboxWorkflow{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID,
		CreatedByID: operator.ID, Name: "Only open cases", Kind: "rule", Trigger: "case_opened", EnabledAt: &enabled,
		Conditions: models.InboxQueueFilter{Status: models.InboxCaseOpen},
		Actions:    []models.InboxAction{{Type: "send_text", Value: "Synthetic open case acknowledgement"}},
	}
	require.NoError(t, db.Create(&w).Error)
	at := time.Now().UTC().Add(-time.Second)
	require.NoError(t, db.Create(&models.Message{BaseModel: models.BaseModel{ID: uuid.New(), CreatedAt: at},
		OrganizationID: org.ID, ContactID: contact.ID, WhatsAppAccount: account.Name,
		Direction: models.DirectionIncoming, MessageType: models.MessageTypeText}).Error)
	a.recordInboxInbound(account, contact, "review-stale-rule", at, nil)
	var event models.InboxCaseActivity
	require.NoError(t, db.First(&event, "event_key = ?", "review-stale-rule").Error)
	require.NoError(t, a.processInboxRuleEvent(context.Background(), event))
	var run models.InboxWorkflowRun
	require.NoError(t, db.First(&run, "workflow_id = ?", w.ID).Error)
	require.Equal(t, inboxRunPending, run.State)
	require.NoError(t, db.Model(&models.InboxCase{}).Where("id = ?", run.CaseID).
		Updates(map[string]any{"status": models.InboxCaseResolved, "resolved_at": time.Now().UTC()}).Error)
	claimed, ok, err := a.claimInboxSend(context.Background(), run.ID, org.ID, time.Now().UTC())
	require.NoError(t, err)
	require.True(t, ok)
	state, outcome := a.dispatchClaimedInboxSend(context.Background(), claimed)
	require.Equal(t, inboxRunBlocked, state, outcome)
	require.EqualValues(t, 0, calls.Load())
}

func TestInboxHardeningWorkerQueryScansProcessedHistory(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	c := newInboxCase(inboxThread{org.ID, account.ID, contact.ID}, time.Now().UTC(), nil, nil)
	require.NoError(t, db.Create(&c).Error)
	require.NoError(t, db.Exec(`INSERT INTO inbox_case_activities
        (id,organization_id,account_id,case_id,created_at,updated_at,kind,workflow_trigger,workflow_processed_at)
        SELECT gen_random_uuid(),?::uuid,?::uuid,?::uuid,now(),now(),'opened','case_opened',now()
        FROM generate_series(1,30000)`, org.ID, account.ID, c.ID).Error)
	require.NoError(t, appendInboxActivity(db, c, models.InboxCaseActivity{Kind: "opened"}))
	require.NoError(t, database.CreateIndexes(db))
	require.NoError(t, db.Exec("ANALYZE inbox_case_activities").Error)
	rows, err := db.Raw(`EXPLAIN (ANALYZE, FORMAT TEXT)
        SELECT * FROM inbox_case_activities WHERE deleted_at IS NULL
        AND workflow_processed_at IS NULL AND workflow_trigger <> ''
        AND EXISTS (SELECT 1 FROM organizations o WHERE o.id = inbox_case_activities.organization_id
            AND o.inbox_workflows_enabled AND o.deleted_at IS NULL)
        ORDER BY created_at,id LIMIT 50`).Rows()
	require.NoError(t, err)
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		plan.WriteString(line + "\n")
	}
	require.NoError(t, rows.Err())
	require.Contains(t, plan.String(), "idx_inbox_workflow_pending")
}

func TestInboxHardeningEditedRuleBorrowsCreatorsPermissions(t *testing.T) {
	db := testutil.SetupTestDB(t)
	a := &App{DB: db, Redis: testutil.SetupTestRedis(t), Log: testutil.NopLogger()}
	require.NotNil(t, a.Redis)
	org := testutil.CreateTestOrganization(t, db)
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	creator := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	restrictedRole := testutil.CreateTestRoleWithKeys(t, db, org.ID, "review-workflow-editor",
		[]string{"chat:read", "chat:write", "settings.general:write"})
	editor := testutil.CreateTestUser(t, db, org.ID, testutil.WithRoleID(&restrictedRole.ID))
	require.NoError(t, db.Where("organization_id = ? AND user_id = ?", org.ID, editor.ID).FirstOrCreate(
		&models.UserOrganization{OrganizationID: org.ID, UserID: editor.ID, RoleID: &restrictedRole.ID}).Error)
	require.False(t, a.HasPermission(editor.ID, models.ResourceContacts, models.ActionWrite, org.ID))
	target := testutil.CreateTestUser(t, db, org.ID)
	require.NoError(t, db.Where("organization_id = ? AND user_id = ?", org.ID, target.ID).FirstOrCreate(
		&models.UserOrganization{OrganizationID: org.ID, UserID: target.ID}).Error)
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	w := models.InboxWorkflow{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID,
		CreatedByID: creator.ID, Name: "Created by privileged operator", Kind: "rule", Trigger: "case_opened",
		Actions: []models.InboxAction{{Type: "note", Value: "Synthetic old note"}}}
	require.NoError(t, db.Create(&w).Error)
	request := testutil.NewJSONRequest(t, inboxWorkflowRequest{Name: w.Name, Kind: "rule", Trigger: "case_opened", Enabled: true,
		Actions: []models.InboxAction{{Type: "assign", Value: target.ID.String()}}})
	testutil.SetAuthContext(request, org.ID, editor.ID)
	testutil.SetPathParam(request, "id", w.ID.String())
	require.NoError(t, a.UpdateInboxWorkflow(request))
	require.Equal(t, 200, testutil.GetResponseStatusCode(request))
	require.NoError(t, db.Model(contact).Update("assigned_user_id", editor.ID).Error)
	contact.AssignedUserID = &editor.ID
	a.recordInboxInbound(account, contact, "privilege-review", time.Now().UTC(), nil)
	var event models.InboxCaseActivity
	require.NoError(t, db.First(&event, "event_key = ?", "privilege-review").Error)
	require.NoError(t, a.processInboxRuleEvent(context.Background(), event))
	c, err := (inboxThread{org.ID, account.ID, contact.ID}).latest(db)
	require.NoError(t, err)
	require.NotNil(t, c.AssigneeID)
	require.Equal(t, editor.ID, *c.AssigneeID)
	var run models.InboxWorkflowRun
	require.NoError(t, db.First(&run, "workflow_id = ?", w.ID).Error)
	require.Equal(t, editor.ID, run.ActorID)
	require.Equal(t, inboxRunFailed, run.State)
	require.NoError(t, db.First(&w, "id = ?", w.ID).Error)
	require.NotNil(t, w.UpdatedByID)
	require.Equal(t, editor.ID, *w.UpdatedByID)
	require.Equal(t, creator.ID, w.CreatedByID)
}

func TestInboxHardeningLegacyRulesRequireReview(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	actor, now := uuid.New(), time.Now().UTC()
	legacy := models.InboxWorkflow{OrganizationID: org.ID, CreatedByID: actor, Name: "Legacy rule",
		Kind: "rule", EnabledAt: &now, Actions: []models.InboxAction{}}
	reviewed := legacy
	reviewed.Name, reviewed.UpdatedByID = "Reviewed rule", &actor
	require.NoError(t, db.Create(&legacy).Error)
	require.NoError(t, db.Create(&reviewed).Error)
	for range 2 {
		require.NoError(t, database.MigrateInboxWorkflowAuthority(db))
	}
	legacy.EnabledAt = nil // Scan into a fresh nullable value rather than reusing the old pointer.
	require.NoError(t, db.First(&legacy, "id = ?", legacy.ID).Error)
	require.NoError(t, db.First(&reviewed, "id = ?", reviewed.ID).Error)
	require.Nil(t, legacy.EnabledAt)
	require.NotNil(t, reviewed.EnabledAt)
}
