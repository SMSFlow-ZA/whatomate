package handlers

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type inboxAuditFixture struct {
	db   *gorm.DB
	app  *App
	org  *models.Organization
	user *models.User
}

func newInboxAuditFixture(t *testing.T) inboxAuditFixture {
	t.Helper()
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	user := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	require.NoError(t, db.Model(user).Update("full_name", "Synthetic audit operator").Error)
	return inboxAuditFixture{db: db, org: org, user: user, app: &App{DB: db, Log: testutil.NopLogger()}}
}
func (f inboxAuditFixture) event() audit.SecurityEvent {
	return audit.SecurityEvent{OrganizationID: f.org.ID, ActorID: f.user.ID, ResourceID: f.user.ID, ResourceType: "user", Kind: audit.SignIn, Outcome: audit.Success, Reason: audit.Password, Context: audit.RequestContext{RequestID: uuid.New(), Origin: audit.Origin{Source: "unknown"}}}
}

func TestInboxAuditCaptureConcurrentIndependentCallersPersistOneExactEvent(t *testing.T) {
	f := newInboxAuditFixture(t)
	e := f.event()
	require.True(t, f.db.Migrator().HasIndex(&models.AuditLog{}, "idx_audit_request_event"))
	var wg sync.WaitGroup
	results := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- audit.RecordSecurity(f.db.Session(&gorm.Session{NewDB: true}), e) }()
	}
	wg.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	require.NoError(t, audit.RecordSecurity(f.db.Session(&gorm.Session{NewDB: true}), e))
	var rows []models.AuditLog
	require.NoError(t, f.db.Where("organization_id=?", f.org.ID).Find(&rows).Error)
	require.Len(t, rows, 1)
	row := rows[0]
	require.Equal(t, 1, row.ContextVersion)
	require.Equal(t, "security", row.Category)
	require.Equal(t, "Synthetic audit operator", row.UserName)
	require.True(t, row.ActorVerified)
	require.Empty(t, row.Changes)
	require.Empty(t, row.OriginIP)
	require.Equal(t, "unknown", row.OriginSource)
	require.Equal(t, e.Context.RequestID.String(), row.RequestID)
	changed := e
	changed.Reason = audit.MicrosoftSSO
	require.ErrorIs(t, audit.RecordSecurity(f.db, changed), audit.ErrConflictingEvent)
	require.NoError(t, f.db.Model(f.user).Update("full_name", "Synthetic renamed operator").Error)
	require.NoError(t, audit.RecordSecurity(f.db, e))
	var retained models.AuditLog
	require.NoError(t, f.db.First(&retained, "id=?", row.ID).Error)
	require.Equal(t, "Synthetic audit operator", retained.UserName)
}

func TestInboxAuditCaptureRejectsPayloadCodesAndFalseAuthenticationClaims(t *testing.T) {
	f := newInboxAuditFixture(t)
	for _, mutate := range []func(*audit.SecurityEvent){
		func(e *audit.SecurityEvent) { e.Reason = audit.Reason("synthetic-secret") },
		func(e *audit.SecurityEvent) { e.Kind = audit.EventKind("synthetic-secret") },
		func(e *audit.SecurityEvent) { e.Outcome = audit.Outcome("synthetic-secret") },
		func(e *audit.SecurityEvent) { e.Reason = audit.InvalidCredentials },
		func(e *audit.SecurityEvent) { e.ActorID = uuid.Nil },
		func(e *audit.SecurityEvent) { e.Context.RequestID = uuid.Nil },
		func(e *audit.SecurityEvent) { e.Context.Origin = audit.Origin{IP: "synthetic-secret", Source: "peer"} },
		func(e *audit.SecurityEvent) { e.Permission = "synthetic-secret" },
		func(e *audit.SecurityEvent) { e.Kind = audit.PermissionChanged; e.Reason = audit.RoleCreated },
		func(e *audit.SecurityEvent) {
			e.Kind = audit.PermissionChanged
			e.Reason = audit.UserRoleChanged
			e.ResourceType = "role"
		},
	} {
		e := f.event()
		mutate(&e)
		err := audit.RecordSecurity(f.db, e)
		require.ErrorIs(t, err, audit.ErrInvalidEvent)
		require.NotContains(t, err.Error(), "synthetic-secret")
	}
	var count int64
	require.NoError(t, f.db.Model(&models.AuditLog{}).Where("organization_id=?", f.org.ID).Count(&count).Error)
	require.Zero(t, count)
	e := f.event()
	e.ActorID = uuid.Nil
	e.Outcome = audit.Denied
	e.Reason = audit.InvalidCredentials
	require.NoError(t, audit.RecordSecurity(f.db, e))
	var record models.AuditLog
	require.NoError(t, f.db.First(&record, "organization_id=?", f.org.ID).Error)
	require.False(t, record.ActorVerified)
	require.Equal(t, uuid.Nil, record.UserID)
	require.Equal(t, "Unauthenticated", record.UserName)
}

func TestInboxAuditCaptureValidatesCaseAccountAndActorTenant(t *testing.T) {
	f := newInboxAuditFixture(t)
	account := testutil.CreateTestWhatsAppAccount(t, f.db, f.org.ID)
	other := testutil.CreateTestWhatsAppAccount(t, f.db, f.org.ID)
	contact := testutil.CreateTestContact(t, f.db, f.org.ID)
	c := newInboxCase(inboxThread{f.org.ID, account.ID, contact.ID}, time.Now().UTC(), nil, nil)
	require.NoError(t, f.db.Create(&c).Error)
	e := f.event()
	e.Kind = audit.CaseChanged
	e.Reason = audit.PriorityChanged
	e.ResourceType = "inbox_case"
	e.ResourceID = c.ID
	e.Context.AccountID = &other.ID
	e.Context.CaseID = &c.ID
	require.ErrorIs(t, audit.RecordSecurity(f.db, e), audit.ErrInvalidEvent)
	e.Context.AccountID = &account.ID
	require.NoError(t, audit.RecordSecurity(f.db, e))
	foreignOrg := testutil.CreateTestOrganization(t, f.db)
	foreign := testutil.CreateTestUser(t, f.db, foreignOrg.ID)
	e = f.event()
	e.ActorID = foreign.ID
	e.ResourceID = foreign.ID
	require.Error(t, audit.RecordSecurity(f.db, e))
	e = f.event()
	e.Context.AccountID = &account.ID
	require.ErrorIs(t, audit.RecordSecurity(f.db, e), audit.ErrInvalidEvent)
	var count int64
	require.NoError(t, f.db.Model(&models.AuditLog{}).Where("organization_id=?", f.org.ID).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestInboxAuditCaptureRetainsTargetAfterDeletionAndSupportsSuperAdminOrgContext(t *testing.T) {
	f := newInboxAuditFixture(t)
	target := testutil.CreateTestUser(t, f.db, f.org.ID)
	require.NoError(t, f.db.Delete(target).Error)
	e := f.event()
	e.Kind = audit.SessionsRevoked
	e.Reason = audit.UserDeleted
	e.ResourceID = target.ID
	require.NoError(t, audit.RecordSecurity(f.db, e))
	otherOrg := testutil.CreateTestOrganization(t, f.db)
	e = f.event()
	e.OrganizationID = otherOrg.ID
	require.NoError(t, audit.RecordSecurity(f.db, e))
	var count int64
	require.NoError(t, f.db.Model(&models.AuditLog{}).Where("organization_id=?", otherOrg.ID).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestInboxAuditCaptureIsAtomicWithCallerTransaction(t *testing.T) {
	f := newInboxAuditFixture(t)
	e := f.event()
	rollback := errors.New("synthetic rollback")
	err := f.db.Transaction(func(tx *gorm.DB) error {
		if err := audit.RecordSecurity(tx, e); err != nil {
			return err
		}
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	var count int64
	require.NoError(t, f.db.Model(&models.AuditLog{}).Where("organization_id=?", f.org.ID).Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, audit.RecordSecurity(f.db, e))
}

func TestInboxAuditCaptureDeadlineCancelsBlockedStorageWithoutPartialEvent(t *testing.T) {
	f := newInboxAuditFixture(t)
	lock := f.db.Begin()
	require.NoError(t, lock.Error)
	t.Cleanup(func() { _ = lock.Rollback().Error })
	// A second connection can validate the actor, but cannot insert while this
	// owned transaction holds the table lock. Capture must not wait indefinitely.
	require.NoError(t, lock.Exec("LOCK TABLE audit_logs IN SHARE MODE").Error)
	started := time.Now()
	err := audit.RecordSecurity(f.db.Session(&gorm.Session{NewDB: true}), f.event())
	require.Error(t, err)
	require.Equal(t, "timeout", audit.ErrorCode(err))
	require.Less(t, time.Since(started), 4*time.Second)
	require.NoError(t, lock.Rollback().Error)
	var count int64
	require.NoError(t, f.db.Model(&models.AuditLog{}).Where("organization_id=?", f.org.ID).Count(&count).Error)
	require.Zero(t, count)
}

func TestInboxAuditLegacyReadRedactsWithoutRewritingStoredHistory(t *testing.T) {
	f := newInboxAuditFixture(t)
	account := testutil.CreateTestWhatsAppAccount(t, f.db, f.org.ID)
	row := models.AuditLog{OrganizationID: f.org.ID, ResourceType: "account", ResourceID: account.ID, UserID: f.user.ID, UserName: "Synthetic operator", Action: models.AuditActionUpdated, Changes: models.JSONBArray{map[string]any{"field": "access_token", "old_value": "synthetic-old-secret", "new_value": "synthetic-new-secret"}, map[string]any{"field": "name", "old_value": "Old label", "new_value": "New label"}}}
	require.NoError(t, f.db.Create(&row).Error)
	for _, detail := range []bool{false, true} {
		r := testutil.NewGETRequest(t)
		testutil.SetAuthContext(r, f.org.ID, f.user.ID)
		if detail {
			testutil.SetPathParam(r, "id", row.ID.String())
			require.NoError(t, f.app.GetAuditLog(r))
		} else {
			require.NoError(t, f.app.ListAuditLogs(r))
		}
		require.Equal(t, 200, testutil.GetResponseStatusCode(r))
		body := string(r.RequestCtx.Response.Body())
		require.NotContains(t, body, "-secret")
		require.Contains(t, body, "[redacted]")
		require.Contains(t, body, "New label")
	}
	var retained models.AuditLog
	require.NoError(t, f.db.First(&retained, "id=?", row.ID).Error)
	raw, err := json.Marshal(retained.Changes)
	require.NoError(t, err)
	require.Contains(t, string(raw), "synthetic-old-secret")
	require.Zero(t, retained.ContextVersion)
	require.Empty(t, retained.RequestID)
	require.False(t, retained.ActorVerified)
}

func TestInboxAuditNewConfigurationWritesRedactComputedAndExtraChanges(t *testing.T) {
	f := newInboxAuditFixture(t)
	audit.LogAudit(f.db, f.org.ID, f.user.ID, "Synthetic operator", "account", uuid.New(), models.AuditActionCreated, nil, map[string]any{"api_key": "synthetic-computed-secret", "name": "Visible configuration"}, map[string]any{"field": "client_secret", "new_value": "synthetic-extra-secret"})
	require.Eventually(t, func() bool {
		var rows []models.AuditLog
		if f.db.Where("organization_id=?", f.org.ID).Find(&rows).Error != nil || len(rows) != 1 {
			return false
		}
		encoded, err := json.Marshal(rows[0].Changes)
		return err == nil && strings.Contains(string(encoded), "[redacted]")
	}, 2*time.Second, 10*time.Millisecond)
	var row models.AuditLog
	require.NoError(t, f.db.First(&row, "organization_id=?", f.org.ID).Error)
	encoded, err := json.Marshal(row.Changes)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "-secret")
	require.Contains(t, string(encoded), "Visible configuration")
}
