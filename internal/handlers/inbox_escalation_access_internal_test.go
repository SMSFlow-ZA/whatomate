package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/inboxquality"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestInboxEscalationSameContactAcrossAccountsNeverGrantsRecipientAccess(t *testing.T) {
	f := newInboxEscalationFixture(t)
	role := testutil.CreateTestRoleWithKeys(t, f.db, f.org.ID, "notice-assigned-reader", []string{"chat:read"})
	agent := testutil.CreateTestUser(t, f.db, f.org.ID, testutil.WithRoleID(&role.ID))
	f.arm(t, f.admin.ID, agent.ID)
	accountB := testutil.CreateTestWhatsAppAccount(t, f.db, f.org.ID)
	policy := responseInput("", 30).Targets
	policy.OrganizationID, policy.AccountID = f.org.ID, accountB.ID
	require.NoError(t, f.db.Create(&policy).Error)
	cB := newInboxCase(inboxThread{f.org.ID, accountB.ID, f.contact.ID}, f.start, &agent.ID, nil)
	require.NoError(t, f.db.Create(&cB).Error)
	require.NoError(t, f.db.Transaction(func(tx *gorm.DB) error { return inboxquality.Sync(tx, &cB, f.start) }))
	_, err := f.a.saveInboxEscalationSettings(f.org.ID, f.admin.ID, accountB.ID, escalationInput(agent.ID), f.start.Add(-time.Minute))
	require.NoError(t, err)
	f.process(t, "first_reply", inboxSLABreach, f.start.Add(31*time.Minute))
	var obligation models.InboxSLAObligation
	require.NoError(t, f.db.First(&obligation, "organization_id=? AND case_id=? AND kind='first_reply'", f.org.ID, cB.ID).Error)
	require.NoError(t, f.a.processInboxEscalation(context.Background(), inboxEscalationCandidate{obligation.ID, cB.ID, f.org.ID}, inboxSLABreach, f.start.Add(31*time.Minute)))
	f.counts(t, 2, 3)
	var denied models.InboxSLANotice
	require.NoError(t, f.db.First(&denied, "organization_id=? AND recipient_id=? AND outcome=?", f.org.ID, agent.ID, "case_access_denied").Error)
	r := f.get(t, agent.ID)
	require.NoError(t, f.a.ListInboxSLANotices(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(r.RequestCtx.Response.Body()))
	var list struct {
		Notices []inboxSLANoticeItem `json:"notices"`
	}
	testutil.ParseEnvelopeResponse(t, r, &list)
	require.Len(t, list.Notices, 1)
	require.Equal(t, accountB.ID, list.Notices[0].AccountID)
	require.Equal(t, cB.ID, list.Notices[0].CaseID)
	r = f.get(t, agent.ID)
	testutil.SetPathParam(r, "id", denied.ID.String())
	require.NoError(t, f.a.ReadInboxSLANotice(r))
	require.Equal(t, 404, testutil.GetResponseStatusCode(r))
	// A legacy contact assignment cannot override the current case's owner.
	require.NoError(t, f.db.Model(&cB).Update("assignee_id", f.admin.ID).Error)
	require.NoError(t, f.db.Model(f.contact).Update("assigned_user_id", agent.ID).Error)
	r = f.get(t, agent.ID)
	require.NoError(t, f.a.ListInboxSLANotices(r))
	testutil.ParseEnvelopeResponse(t, r, &list)
	require.Empty(t, list.Notices)
}

func TestInboxEscalationFreshRoleAndMembershipChecksIgnoreStaleRedis(t *testing.T) {
	f := newInboxEscalationFixture(t)
	f.a.Redis = testutil.SetupTestRedis(t)
	role := testutil.CreateTestRoleWithKeys(t, f.db, f.org.ID, "notice-config-writer", []string{"chat:read", "contacts:read", "settings.general:read", "settings.general:write", "users:read"})
	writer := testutil.CreateTestUser(t, f.db, f.org.ID, testutil.WithRoleID(&role.ID))
	require.True(t, f.a.HasPermission(writer.ID, models.ResourceSettingsGeneral, models.ActionWrite, f.org.ID))
	var permission models.Permission
	require.NoError(t, f.db.First(&permission, "resource=? AND action=?", models.ResourceSettingsGeneral, models.ActionWrite).Error)
	require.NoError(t, f.db.Where("custom_role_id=? AND permission_id=?", role.ID, permission.ID).Delete(&models.RolePermission{}).Error)
	require.True(t, f.a.HasPermission(writer.ID, models.ResourceSettingsGeneral, models.ActionWrite, f.org.ID), "This fixture intentionally retains the stale cache")
	r := testutil.NewJSONRequest(t, escalationInput(f.admin.ID))
	testutil.SetAuthContext(r, f.org.ID, writer.ID)
	testutil.SetPathParam(r, "account_id", f.account.ID.String())
	require.NoError(t, f.a.SetInboxEscalationSettings(r))
	require.Equal(t, 403, testutil.GetResponseStatusCode(r))
	var count int64
	require.NoError(t, f.db.Model(&models.InboxSLAEscalationSettings{}).Where("organization_id=?", f.org.ID).Count(&count).Error)
	require.Zero(t, count)
	// Receiving also ignores an old cached chat permission.
	permission = models.Permission{}
	require.NoError(t, f.db.First(&permission, "resource=? AND action=?", models.ResourceChat, models.ActionRead).Error)
	require.NoError(t, f.db.Where("custom_role_id=? AND permission_id=?", role.ID, permission.ID).Delete(&models.RolePermission{}).Error)
	r = f.get(t, writer.ID)
	require.NoError(t, f.a.ListInboxSLANotices(r))
	require.Equal(t, 403, testutil.GetResponseStatusCode(r))
	// A member cannot inherit a home-org role in this organisation.
	other := testutil.CreateTestOrganization(t, f.db)
	homeRole := testutil.CreateAdminRole(t, f.db, other.ID)
	member := testutil.CreateTestUser(t, f.db, other.ID, testutil.WithRoleID(&homeRole.ID))
	localRole := testutil.CreateTestRoleWithKeys(t, f.db, f.org.ID, "notice-local-reader", []string{"chat:read"})
	membership := models.UserOrganization{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, UserID: member.ID, RoleID: &localRole.ID}
	require.NoError(t, f.db.Create(&membership).Error)
	people, err := f.a.inboxEscalationPeople(f.db, f.org.ID, []uuid.UUID{member.ID})
	require.NoError(t, err)
	require.Contains(t, people, member.ID)
	require.False(t, people[member.ID].FullContactRead)
	require.NoError(t, f.db.Delete(&membership).Error)
	people, err = f.a.inboxEscalationPeople(f.db, f.org.ID, []uuid.UUID{member.ID})
	require.NoError(t, err)
	require.NotContains(t, people, member.ID)
}

func TestInboxEscalationDirectoryRequiresCurrentUserReadAndContainsOnlyEligibleStaff(t *testing.T) {
	f := newInboxEscalationFixture(t)
	readerRole := testutil.CreateTestRoleWithKeys(t, f.db, f.org.ID, "notice-reader", []string{"chat:read", "contacts:read", "settings.general:read"})
	reader := testutil.CreateTestUser(t, f.db, f.org.ID, testutil.WithRoleID(&readerRole.ID))
	r := f.get(t, reader.ID)
	require.NoError(t, f.a.GetInboxEscalationSettings(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	r = f.get(t, reader.ID)
	require.NoError(t, f.a.ListInboxEscalationRecipients(r))
	require.Equal(t, 403, testutil.GetResponseStatusCode(r))
	foreignOrg := testutil.CreateTestOrganization(t, f.db)
	foreign := testutil.CreateTestUser(t, f.db, foreignOrg.ID, testutil.WithSuperAdmin())
	noChat := testutil.CreateTestUser(t, f.db, f.org.ID)
	inactive := testutil.CreateTestUser(t, f.db, f.org.ID, testutil.WithInactive(), testutil.WithSuperAdmin())
	r = f.get(t, f.admin.ID)
	require.NoError(t, f.a.ListInboxEscalationRecipients(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(r.RequestCtx.Response.Body()))
	var body struct {
		Recipients []struct {
			ID uuid.UUID `json:"id"`
		} `json:"recipients"`
	}
	testutil.ParseEnvelopeResponse(t, r, &body)
	ids := []uuid.UUID{}
	for _, person := range body.Recipients {
		ids = append(ids, person.ID)
	}
	require.Contains(t, ids, f.admin.ID)
	require.Contains(t, ids, reader.ID)
	for _, id := range []uuid.UUID{foreign.ID, noChat.ID, inactive.ID} {
		require.NotContains(t, ids, id)
	}
	// Global administrators may configure, but are not automatically recipients.
	r = f.get(t, foreign.ID)
	require.NoError(t, f.a.GetInboxEscalationSettings(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
}

func TestInboxEscalationLegacyTransferCoverageRequiresCorrelatedWorkAndRecipient(t *testing.T) {
	for _, link := range []string{"matching", "missing_handoff", "other_account"} {
		t.Run(link, func(t *testing.T) {
			f := newInboxEscalationFixture(t)
			second := testutil.CreateTestUser(t, f.db, f.org.ID, testutil.WithSuperAdmin())
			f.arm(t, f.admin.ID, second.ID)
			escalates := f.start.Add(5 * time.Minute)
			transfer := models.AgentTransfer{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, ContactID: f.contact.ID, WhatsAppAccount: f.account.Name, Status: models.TransferStatusActive, TransferredAt: f.start.Add(time.Minute), SLA: models.SLATracking{EscalationAt: &escalates}}
			if link == "other_account" {
				transfer.WhatsAppAccount = testutil.CreateTestWhatsAppAccount(t, f.db, f.org.ID).Name
			}
			require.NoError(t, f.db.Create(&transfer).Error)
			require.NoError(t, f.db.Create(&models.ChatbotSettings{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, SLA: models.SLAConfig{Enabled: true, EscalationMinutes: 5, EscalationNotifyIDs: models.StringArray{f.admin.ID.String()}}}).Error)
			if link != "missing_handoff" {
				f.a.recordInboxHandoff(f.account, f.contact, &transfer)
			}
			f.process(t, "first_reply", inboxSLABreach, f.start.Add(31*time.Minute))
			var receipt models.InboxSLANotice
			require.NoError(t, f.db.First(&receipt, "organization_id=? AND recipient_id=?", f.org.ID, f.admin.ID).Error)
			if link == "matching" {
				require.Equal(t, "legacy_transfer_covered", receipt.Outcome)
			} else {
				require.Equal(t, inboxNoticeAvailable, receipt.Outcome)
			}
			receipt = models.InboxSLANotice{}
			require.NoError(t, f.db.First(&receipt, "organization_id=? AND recipient_id=?", f.org.ID, second.ID).Error)
			require.Equal(t, inboxNoticeAvailable, receipt.Outcome)
			f.process(t, "resolution", inboxSLABreach, f.start.Add(121*time.Minute))
			var resolutionAvailable int64
			require.NoError(t, f.db.Model(&models.InboxSLANotice{}).Joins("JOIN inbox_sla_escalation_events e ON e.id=inbox_sla_notices.event_id").Where("inbox_sla_notices.organization_id=? AND e.kind='resolution' AND inbox_sla_notices.outcome=?", f.org.ID, inboxNoticeAvailable).Count(&resolutionAvailable).Error)
			require.EqualValues(t, 2, resolutionAvailable)
		})
	}
}

func TestInboxEscalationHintRevalidatesReadStateSettingsAndCurrentIdentity(t *testing.T) {
	f := newInboxEscalationFixtureAt(t, time.Now().UTC().Truncate(time.Second).Add(-25*time.Minute))
	settings := f.arm(t)
	f.process(t, "first_reply", inboxSLAAtRisk, time.Now().UTC())
	var notice models.InboxSLANotice
	require.NoError(t, f.db.First(&notice, "organization_id=? AND outcome=?", f.org.ID, inboxNoticeAvailable).Error)
	require.True(t, f.a.canReceiveInboxNotice(f.org.ID, f.admin.ID, notice.ID))
	require.False(t, f.a.canReceiveInboxNotice(f.org.ID, uuid.New(), notice.ID))
	input := escalationInput(f.admin.ID)
	input.Revision, input.Enabled = settings.Revision, escalationBool(false)
	_, err := f.a.saveInboxEscalationSettings(f.org.ID, f.admin.ID, f.account.ID, input, time.Now().UTC())
	require.NoError(t, err)
	require.False(t, f.a.canReceiveInboxNotice(f.org.ID, f.admin.ID, notice.ID))
	// Durable history remains inspectable; it never claims socket delivery.
	r := f.get(t, f.admin.ID)
	require.NoError(t, f.a.ListInboxEscalationHistory(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(r.RequestCtx.Response.Body()))
	var history struct {
		Events []struct {
			AvailableCount int `json:"available_count"`
		} `json:"events"`
	}
	testutil.ParseEnvelopeResponse(t, r, &history)
	require.Len(t, history.Events, 1)
	require.Equal(t, 1, history.Events[0].AvailableCount)
	r = f.get(t, f.admin.ID)
	testutil.SetPathParam(r, "id", notice.ID.String())
	require.NoError(t, f.a.ReadInboxSLANotice(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(r.RequestCtx.Response.Body()))
	var first models.InboxSLANotice
	require.NoError(t, f.db.First(&first, "id=?", notice.ID).Error)
	require.NotNil(t, first.ReadAt)
	r = f.get(t, f.admin.ID)
	testutil.SetPathParam(r, "id", notice.ID.String())
	require.NoError(t, f.a.ReadInboxSLANotice(r))
	var again models.InboxSLANotice
	require.NoError(t, f.db.First(&again, "id=?", notice.ID).Error)
	require.True(t, first.ReadAt.Equal(*again.ReadAt))
	require.NoError(t, f.db.Model(f.admin).Update("is_active", false).Error)
	require.False(t, f.a.canReceiveInboxNotice(f.org.ID, f.admin.ID, notice.ID))
}
