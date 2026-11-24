package handlers

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/inboxquality"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"github.com/zerodha/fastglue"
	"gorm.io/gorm"
)

type inboxEscalationFixture struct {
	responsePolicyFixture
	contact *models.Contact
	c       models.InboxCase
	start   time.Time
}

func newInboxEscalationFixture(t *testing.T) inboxEscalationFixture {
	return newInboxEscalationFixtureAt(t, time.Now().UTC().Truncate(time.Second).Add(-4*time.Hour))
}

func newInboxEscalationFixtureAt(t *testing.T, start time.Time) inboxEscalationFixture {
	t.Helper()
	f := newResponsePolicyFixture(t)
	f.seedDefault(t)
	contact := testutil.CreateTestContact(t, f.db, f.org.ID)
	f.a.recordInboxInbound(f.account, contact, "synthetic-notice-entry:"+uuid.NewString(), start, nil)
	c, err := (inboxThread{f.org.ID, f.account.ID, contact.ID}).latest(f.db)
	require.NoError(t, err)
	t.Cleanup(func() {
		f.db.Model(&models.InboxSLAEscalationSettings{}).Where("organization_id=?", f.org.ID).Update("enabled", false)
		// These synthetic entries are not workflow-worker acceptance fixtures.
		// Stop only this test's organisation so retained entries cannot fill a
		// later test's global, bounded workflow batch.
		f.db.Model(f.org).Update("inbox_workflows_enabled", false)
	})
	return inboxEscalationFixture{f, contact, c, start}
}

func escalationBool(value bool) *bool { return &value }

func escalationInput(ids ...uuid.UUID) inboxEscalationSettingsInput {
	return inboxEscalationSettingsInput{Enabled: escalationBool(true), AtRisk: escalationBool(true), Breach: escalationBool(true), RecipientIDs: ids}
}

func (f inboxEscalationFixture) arm(t *testing.T, ids ...uuid.UUID) models.InboxSLAEscalationSettings {
	t.Helper()
	if len(ids) == 0 {
		ids = []uuid.UUID{f.admin.ID}
	}
	settings, err := f.a.saveInboxEscalationSettings(f.org.ID, f.admin.ID, f.account.ID, escalationInput(ids...), f.start.Add(-time.Minute))
	require.NoError(t, err)
	return settings
}

func (f inboxEscalationFixture) obligation(t *testing.T, kind string) models.InboxSLAObligation {
	t.Helper()
	var row models.InboxSLAObligation
	require.NoError(t, f.db.First(&row, "organization_id=? AND case_id=? AND kind=?", f.org.ID, f.c.ID, kind).Error)
	return row
}

func (f inboxEscalationFixture) process(t *testing.T, kind, threshold string, now time.Time) {
	t.Helper()
	row := f.obligation(t, kind)
	require.NoError(t, f.a.processInboxEscalation(context.Background(), inboxEscalationCandidate{row.ID, row.CaseID, row.OrganizationID}, threshold, now))
}

func (f inboxEscalationFixture) counts(t *testing.T, events, notices int64) {
	t.Helper()
	var count int64
	require.NoError(t, f.db.Model(&models.InboxSLAEscalationEvent{}).Where("organization_id=?", f.org.ID).Count(&count).Error)
	require.Equal(t, events, count)
	require.NoError(t, f.db.Model(&models.InboxSLANotice{}).Where("organization_id=?", f.org.ID).Count(&count).Error)
	require.Equal(t, notices, count)
}

func (f inboxEscalationFixture) get(t *testing.T, actor uuid.UUID) *fastglue.Request {
	t.Helper()
	r := testutil.NewGETRequest(t)
	testutil.SetAuthContext(r, f.org.ID, actor)
	testutil.SetPathParam(r, "account_id", f.account.ID.String())
	return r
}

func TestInboxEscalationDefaultsActivationAndRevisionConflicts(t *testing.T) {
	f := newInboxEscalationFixture(t)
	r := f.get(t, f.admin.ID)
	require.NoError(t, f.a.GetInboxEscalationSettings(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	var result struct {
		Settings models.InboxSLAEscalationSettings `json:"settings"`
	}
	testutil.ParseEnvelopeResponse(t, r, &result)
	require.False(t, result.Settings.Enabled)
	require.Zero(t, result.Settings.Revision)
	require.Empty(t, result.Settings.RecipientIDs)
	f.process(t, "first_reply", inboxSLABreach, f.start.Add(time.Hour))
	f.counts(t, 0, 0)
	settings := f.arm(t)
	require.EqualValues(t, 1, settings.Revision)
	require.Equal(t, f.start.Add(-time.Minute), settings.AtRiskSince.UTC())
	input := escalationInput(f.admin.ID)
	input.Revision = settings.Revision
	input.AtRisk = escalationBool(false)
	settings, err := f.a.saveInboxEscalationSettings(f.org.ID, f.admin.ID, f.account.ID, input, f.start.Add(10*time.Minute))
	require.NoError(t, err)
	require.Nil(t, settings.AtRiskSince)
	require.Equal(t, f.start.Add(-time.Minute), settings.BreachSince.UTC())
	_, err = f.a.saveInboxEscalationSettings(f.org.ID, f.admin.ID, f.account.ID, input, f.start.Add(11*time.Minute))
	require.ErrorIs(t, err, errInboxConflict)
	input.Revision = settings.Revision
	input.AtRisk = escalationBool(true)
	settings, err = f.a.saveInboxEscalationSettings(f.org.ID, f.admin.ID, f.account.ID, input, f.start.Add(12*time.Minute))
	require.NoError(t, err)
	require.Equal(t, f.start.Add(12*time.Minute), settings.AtRiskSince.UTC())
	input.Revision = settings.Revision
	input.Enabled = escalationBool(false)
	settings, err = f.a.saveInboxEscalationSettings(f.org.ID, f.admin.ID, f.account.ID, input, f.start.Add(13*time.Minute))
	require.NoError(t, err)
	require.Nil(t, settings.AtRiskSince)
	require.Nil(t, settings.BreachSince)
	input.Revision = settings.Revision
	input.Enabled = escalationBool(true)
	settings, err = f.a.saveInboxEscalationSettings(f.org.ID, f.admin.ID, f.account.ID, input, f.start.Add(40*time.Minute))
	require.NoError(t, err)
	f.process(t, "first_reply", inboxSLABreach, f.start.Add(time.Hour))
	f.counts(t, 0, 0) // No retroactive crossing after reactivation.
}

func TestInboxEscalationRecipientValidationAndDisableAfterRevocation(t *testing.T) {
	f := newInboxEscalationFixture(t)
	other := testutil.CreateTestOrganization(t, f.db)
	foreign := testutil.CreateTestUser(t, f.db, other.ID, testutil.WithSuperAdmin())
	inactive := testutil.CreateTestUser(t, f.db, f.org.ID, testutil.WithInactive(), testutil.WithSuperAdmin())
	noChat := testutil.CreateTestUser(t, f.db, f.org.ID)
	for _, ids := range [][]uuid.UUID{{}, {uuid.Nil}, {f.admin.ID, f.admin.ID}, {foreign.ID}, {inactive.ID}, {noChat.ID}} {
		_, err := f.a.saveInboxEscalationSettings(f.org.ID, f.admin.ID, f.account.ID, escalationInput(ids...), f.start)
		require.ErrorIs(t, err, errInboxTransition)
	}
	tooMany := make([]uuid.UUID, 21)
	for i := range tooMany {
		tooMany[i] = uuid.New()
	}
	require.ErrorIs(t, validateInboxEscalationInput(escalationInput(tooMany...)), errInboxTransition)
	settings := f.arm(t)
	operator := testutil.CreateTestUser(t, f.db, f.org.ID, testutil.WithSuperAdmin())
	require.NoError(t, f.db.Model(f.admin).Update("is_active", false).Error)
	input := escalationInput(f.admin.ID)
	input.Revision, input.Enabled = settings.Revision, escalationBool(false)
	_, err := f.a.saveInboxEscalationSettings(f.org.ID, operator.ID, f.account.ID, input, f.start.Add(time.Minute))
	require.NoError(t, err)
	f.process(t, "first_reply", inboxSLABreach, f.start.Add(time.Hour))
	f.counts(t, 0, 0)
}

func TestInboxEscalationRiskBreachAndImmutableVersionDeduplicate(t *testing.T) {
	f := newInboxEscalationFixture(t)
	f.arm(t)
	f.process(t, "first_reply", inboxSLAAtRisk, f.start.Add(23*time.Minute))
	f.counts(t, 0, 0)
	f.process(t, "first_reply", inboxSLAAtRisk, f.start.Add(25*time.Minute))
	f.process(t, "first_reply", inboxSLAAtRisk, f.start.Add(26*time.Minute))
	f.counts(t, 1, 1)
	f.process(t, "first_reply", inboxSLABreach, f.start.Add(30*time.Minute))
	f.counts(t, 1, 1) // Exact deadline is not breached.
	f.process(t, "first_reply", inboxSLABreach, f.start.Add(31*time.Minute))
	f.process(t, "first_reply", inboxSLAAtRisk, f.start.Add(31*time.Minute))
	f.counts(t, 2, 2)
	var event models.InboxSLAEscalationEvent
	require.NoError(t, f.db.First(&event, "organization_id=? AND threshold=?", f.org.ID, inboxSLABreach).Error)
	require.Equal(t, *f.c.SLAPolicyID, event.PolicyVersionID)
	// Later account policy changes never select a different version for this case.
	p := responseInput("", 5).Targets
	p.OrganizationID, p.AccountID = f.org.ID, f.account.ID
	require.NoError(t, f.db.Save(&p).Error)
	require.NoError(t, f.db.Transaction(func(tx *gorm.DB) error { return inboxquality.Sync(tx, &f.c, f.start.Add(time.Hour)) }))
	f.process(t, "first_reply", inboxSLABreach, f.start.Add(time.Hour))
	f.counts(t, 2, 2)
	var outgoing int64
	require.NoError(t, f.db.Model(&models.Message{}).Where("organization_id=? AND direction='outgoing'", f.org.ID).Count(&outgoing).Error)
	require.Zero(t, outgoing)
}

func TestInboxEscalationConcurrentWorkersAndRestartCreateOneReceipt(t *testing.T) {
	f := newInboxEscalationFixture(t)
	f.arm(t)
	row := f.obligation(t, "first_reply")
	candidate := inboxEscalationCandidate{row.ID, row.CaseID, row.OrganizationID}
	var wg sync.WaitGroup
	errors := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errors <- f.a.processInboxEscalation(context.Background(), candidate, inboxSLABreach, f.start.Add(31*time.Minute))
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	f.counts(t, 1, 1)
	restarted := &App{DB: f.db, Log: testutil.NopLogger()}
	require.NoError(t, restarted.processInboxEscalation(context.Background(), candidate, inboxSLABreach, f.start.Add(32*time.Minute)))
	f.counts(t, 1, 1)
}

func TestInboxEscalationRecipientEditsDoNotReplayAnObservedCrossing(t *testing.T) {
	f := newInboxEscalationFixture(t)
	settings := f.arm(t)
	f.process(t, "first_reply", inboxSLAAtRisk, f.start.Add(25*time.Minute))
	second := testutil.CreateTestUser(t, f.db, f.org.ID, testutil.WithSuperAdmin())
	input := escalationInput(second.ID)
	input.Revision = settings.Revision
	changed, err := f.a.saveInboxEscalationSettings(f.org.ID, f.admin.ID, f.account.ID, input, f.start.Add(26*time.Minute))
	require.NoError(t, err)
	require.True(t, changed.AtRiskSince.Equal(*settings.AtRiskSince))
	f.process(t, "first_reply", inboxSLAAtRisk, f.start.Add(27*time.Minute))
	f.process(t, "first_reply", inboxSLABreach, f.start.Add(31*time.Minute))
	f.counts(t, 2, 2)
	var recipients []uuid.UUID
	require.NoError(t, f.db.Model(&models.InboxSLANotice{}).Joins("JOIN inbox_sla_escalation_events e ON e.id=inbox_sla_notices.event_id").Where("inbox_sla_notices.organization_id=? AND e.threshold=?", f.org.ID, inboxSLABreach).Pluck("recipient_id", &recipients).Error)
	require.Equal(t, []uuid.UUID{second.ID}, recipients)
	input.Revision, input.Enabled = changed.Revision, escalationBool(false)
	require.NoError(t, f.db.Model(second).Update("is_active", false).Error)
	r := testutil.NewJSONRequest(t, input)
	testutil.SetAuthContext(r, f.org.ID, f.admin.ID)
	testutil.SetPathParam(r, "account_id", f.account.ID.String())
	require.NoError(t, f.a.SetInboxEscalationSettings(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(r.RequestCtx.Response.Body()))
}

func TestInboxEscalationRechecksCompletedPausedResolvedAndDeletedWork(t *testing.T) {
	for _, state := range []string{"completed", "paused", "resolved", "account_deleted", "contact_deleted", "feature_off"} {
		t.Run(state, func(t *testing.T) {
			f := newInboxEscalationFixture(t)
			f.arm(t)
			row := f.obligation(t, "first_reply")
			switch state {
			case "completed":
				require.NoError(t, f.db.Model(&row).Update("completed_at", f.start.Add(20*time.Minute)).Error)
			case "paused":
				require.NoError(t, f.db.Model(&row).Update("paused", true).Error)
			case "resolved":
				require.NoError(t, f.db.Model(&f.c).Updates(map[string]any{"status": models.InboxCaseResolved, "resolved_at": f.start.Add(20 * time.Minute)}).Error)
			case "account_deleted":
				require.NoError(t, f.db.Delete(f.account).Error)
			case "contact_deleted":
				require.NoError(t, f.db.Delete(f.contact).Error)
			case "feature_off":
				require.NoError(t, f.db.Model(f.org).Update("inbox_workflows_enabled", false).Error)
			}
			require.NoError(t, f.a.processInboxEscalation(context.Background(), inboxEscalationCandidate{row.ID, row.CaseID, row.OrganizationID}, inboxSLABreach, f.start.Add(time.Hour)))
			f.counts(t, 0, 0)
		})
	}
}

func TestInboxEscalationAtomicStorageFailureRecoversWithoutPartialEvent(t *testing.T) {
	f := newInboxEscalationFixture(t)
	f.arm(t)
	constraint := "notice_fault_" + fmt.Sprintf("%x", f.org.ID[:8])
	require.NoError(t, f.db.Exec("ALTER TABLE inbox_sla_notices ADD CONSTRAINT "+constraint+" CHECK (organization_id <> '"+f.org.ID.String()+"'::uuid)").Error)
	t.Cleanup(func() { f.db.Exec("ALTER TABLE inbox_sla_notices DROP CONSTRAINT IF EXISTS " + constraint) })
	row := f.obligation(t, "first_reply")
	err := f.a.processInboxEscalation(context.Background(), inboxEscalationCandidate{row.ID, row.CaseID, row.OrganizationID}, inboxSLABreach, f.start.Add(time.Hour))
	require.Error(t, err)
	f.counts(t, 0, 0)
	require.NoError(t, f.db.Exec("ALTER TABLE inbox_sla_notices DROP CONSTRAINT "+constraint).Error)
	f.process(t, "first_reply", inboxSLABreach, f.start.Add(time.Hour))
	f.counts(t, 1, 1)
}
