package handlers

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shridarpatil/whatomate/internal/inboxlive"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
)

func liveContextListener(t *testing.T) (*pgx.Conn, func(func(inboxlive.Event) bool) inboxlive.Event) {
	t.Helper()
	configuration, err := pgx.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	require.NoError(t, err)
	listener, err := pgx.ConnectConfig(context.Background(), configuration)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, listener.Close(context.Background())) })
	_, err = listener.Exec(context.Background(), "LISTEN "+inboxlive.Channel)
	require.NoError(t, err)
	return listener, func(match func(inboxlive.Event) bool) inboxlive.Event {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		for {
			notice, err := listener.WaitForNotification(ctx)
			require.NoError(t, err)
			require.NotContains(t, notice.Payload, "synthetic-private")
			require.NotContains(t, notice.Payload, "password")
			event, err := inboxlive.Decode([]byte(notice.Payload))
			require.NoError(t, err)
			if match(event) {
				return event
			}
		}
	}
}

func TestInboxLiveContextAvailabilityCoversMembershipsAndStableCapacityResources(t *testing.T) {
	f := newRoutingPolicyFixture(t)
	f.enable(t)
	b := testutil.CreateTestWhatsAppAccount(t, f.db, f.org.ID)
	foreign := testutil.CreateTestOrganization(t, f.db)
	foreignAccount := testutil.CreateTestWhatsAppAccount(t, f.db, foreign.ID)
	require.NoError(t, f.db.Create(&models.UserOrganization{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: foreign.ID, UserID: f.agent.ID}).Error)
	listener, next := liveContextListener(t)
	require.NoError(t, f.db.Exec("UPDATE users SET is_available=NOT is_available WHERE id=?", f.agent.ID).Error)
	want := map[uuid.UUID]uuid.UUID{f.account.ID: f.org.ID, b.ID: f.org.ID, foreignAccount.ID: foreign.ID}
	for range 3 {
		event := next(func(e inboxlive.Event) bool { return e.Kind == "workload" })
		require.NotNil(t, event.Before)
		require.NotNil(t, event.After)
		require.NotNil(t, event.After.AccountID)
		require.Equal(t, want[*event.After.AccountID], event.OrganizationID)
		require.Equal(t, f.agent.ID, *event.After.AssigneeID)
		delete(want, *event.After.AccountID)
	}
	require.Empty(t, want)
	require.NoError(t, f.db.Exec("INSERT INTO inbox_agent_capacities (organization_id,account_id,agent_id,max_active) VALUES (?,?,?,7)", f.org.ID, f.account.ID, f.agent.ID).Error)
	first := next(func(e inboxlive.Event) bool { return e.Kind == "workload" && e.OrganizationID == f.org.ID })
	require.Equal(t, f.account.ID, *first.After.AccountID)
	require.Equal(t, f.agent.ID, *first.After.AssigneeID)
	require.NoError(t, f.db.Exec("UPDATE inbox_agent_capacities SET max_active=8 WHERE organization_id=? AND account_id=? AND agent_id=?", f.org.ID, f.account.ID, f.agent.ID).Error)
	second := next(func(e inboxlive.Event) bool { return e.Kind == "workload" && e.ResourceID == first.ResourceID })
	require.Equal(t, first.Version+1, second.Version)
	require.NoError(t, f.db.Exec("UPDATE inbox_agent_capacities SET max_active=8 WHERE organization_id=? AND account_id=? AND agent_id=?", f.org.ID, f.account.ID, f.agent.ID).Error)
	tx := f.db.Begin()
	require.NoError(t, tx.Error)
	require.NoError(t, tx.Exec("UPDATE users SET is_available=NOT is_available WHERE id=?", f.agent.ID).Error)
	require.NoError(t, tx.Rollback().Error)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := listener.WaitForNotification(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded, "no-op and rolled-back writes publish nothing")
	var pending int64
	require.NoError(t, f.db.Model(&models.InboxLiveChange{}).Count(&pending).Error)
	require.Zero(t, pending)
}

func TestInboxLiveContextContactNotesAndOwnershipNeverPublishContent(t *testing.T) {
	f := newRoutingPolicyFixture(t)
	f.enable(t)
	c := f.waiting(t, f.account.ID, models.InboxPriorityNormal)
	_, next := liveContextListener(t)
	note := models.ConversationNote{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: f.org.ID, ContactID: c.ContactID, CreatedByID: f.admin.ID, Content: "synthetic-private note content"}
	require.NoError(t, f.db.Create(&note).Error)
	match := func(e inboxlive.Event) bool { return e.Kind == "contact" && e.ResourceID == c.ContactID }
	first := next(match)
	require.Equal(t, c.ContactID, *first.After.ContactID)
	require.Nil(t, first.After.AssigneeID)
	require.NoError(t, f.db.Exec("UPDATE contacts SET assigned_user_id=?,profile_name=? WHERE id=?", f.agent.ID, "synthetic-private profile", c.ContactID).Error)
	second := next(match)
	require.Equal(t, first.Version+1, second.Version)
	require.Equal(t, f.agent.ID, *second.After.AssigneeID)
	require.NoError(t, f.db.Exec("UPDATE contacts SET assigned_user_id=NULL WHERE id=?", c.ContactID).Error)
	third := next(match)
	require.Equal(t, f.agent.ID, *third.Before.AssigneeID)
	require.Nil(t, third.After.AssigneeID)
	require.NoError(t, f.db.Delete(&note).Error)
	fourth := next(match)
	require.Equal(t, third.Version+1, fourth.Version)
	viewer, err := f.a.inboxLiveViewer(context.Background(), f.org.ID, f.agent.ID, nil)
	require.NoError(t, err)
	matched, reset, err := inboxLiveEventVisible(f.db, viewer, inboxlive.WatchScope{Kind: "queue", AccountID: &f.account.ID}, &third)
	require.NoError(t, err)
	require.True(t, matched && reset, "removal of the contact-level grant clears old queue rows")
}

func TestInboxLiveContextRolePermissionAndSessionRevocationAreGenericResets(t *testing.T) {
	f := newRoutingPolicyFixture(t)
	f.enable(t)
	var member models.UserOrganization
	require.NoError(t, f.db.First(&member, "organization_id=? AND user_id=?", f.org.ID, f.agent.ID).Error)
	var link models.RolePermission
	require.NoError(t, f.db.First(&link, "custom_role_id=?", *member.RoleID).Error)
	_, next := liveContextListener(t)
	require.NoError(t, f.db.Exec("DELETE FROM role_permissions WHERE custom_role_id=? AND permission_id=?", link.CustomRoleID, link.PermissionID).Error)
	match := func(e inboxlive.Event) bool { return e.Kind == "access" && e.OrganizationID == f.org.ID }
	first := next(match)
	require.Equal(t, f.org.ID, first.ResourceID)
	require.Equal(t, &inboxlive.Scope{}, first.Before)
	require.NoError(t, f.db.Create(&link).Error)
	second := next(match)
	require.Equal(t, first.Version+1, second.Version)
	require.Equal(t, &inboxlive.Scope{}, second.After)
	require.NoError(t, f.db.Exec("UPDATE users SET auth_stamp=? WHERE id=?", "synthetic-private stamp", f.agent.ID).Error)
	third := next(match)
	require.Equal(t, second.Version+1, third.Version)
	require.Equal(t, &inboxlive.Scope{}, third.Before)
	require.Equal(t, &inboxlive.Scope{}, third.After)
	require.NoError(t, f.db.Where("organization_id=? AND user_id=?", f.org.ID, f.agent.ID).Delete(&models.UserOrganization{}).Error)
	fourth := next(match)
	require.Equal(t, third.Version+1, fourth.Version)
}
