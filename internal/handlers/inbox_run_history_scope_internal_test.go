package handlers

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"github.com/zerodha/fastglue"
)

func TestInboxWorkflowRunHistoryIdentityAndAgentScope(t *testing.T) {
	f := newIntegrationFixture(t)
	role := testutil.CreateTestRoleWithKeys(t, f.a.DB, f.c.OrganizationID, "Case history reader", []string{"chat:read", "chat:write"})
	agent := testutil.CreateTestUser(t, f.a.DB, f.c.OrganizationID, testutil.WithRoleID(&role.ID))
	require.NoError(t, f.a.DB.Model(&f.c).Update("assignee_id", agent.ID).Error)
	require.NoError(t, f.a.DB.Model(&f.run).Update("actor_id", agent.ID).Error)
	otherAccount := testutil.CreateTestWhatsAppAccount(t, f.a.DB, f.c.OrganizationID)
	otherContact := testutil.CreateTestContact(t, f.a.DB, f.c.OrganizationID)
	hiddenCase := newInboxCase(inboxThread{f.c.OrganizationID, otherAccount.ID, f.c.ContactID}, time.Now().UTC(), &f.actor.ID, nil)
	require.NoError(t, f.a.DB.Create(&hiddenCase).Error)
	badAccount, badContact, hiddenRun := f.run, f.run, f.run
	badAccount.ID, badAccount.AccountID = uuid.New(), otherAccount.ID
	badContact.ID, badContact.ContactID = uuid.New(), otherContact.ID
	hiddenRun.ID, hiddenRun.CaseID, hiddenRun.AccountID = uuid.New(), hiddenCase.ID, otherAccount.ID
	for _, run := range []*models.InboxWorkflowRun{&badAccount, &badContact, &hiddenRun} {
		require.NoError(t, f.a.DB.Create(run).Error)
	}
	adopted := f.run
	adopted.ID, adopted.ActorID, adopted.InitiatedByID = uuid.New(), f.actor.ID, &agent.ID
	require.NoError(t, f.a.DB.Create(&adopted).Error)
	list := func(actor uuid.UUID, caseID uuid.UUID) *fastglue.Request {
		r := testutil.NewJSONRequest(t, map[string]any{})
		testutil.SetAuthContext(r, f.c.OrganizationID, actor)
		if caseID != uuid.Nil {
			r.RequestCtx.QueryArgs().Set("case_id", caseID.String())
		}
		require.NoError(t, f.a.ListInboxWorkflowRuns(r))
		require.Equal(t, 200, testutil.GetResponseStatusCode(r))
		return r
	}
	for _, actor := range []uuid.UUID{agent.ID, f.actor.ID} {
		for _, caseID := range []uuid.UUID{uuid.Nil, f.c.ID} {
			body := string(testutil.GetResponseBody(list(actor, caseID)))
			require.Contains(t, body, f.run.ID.String())
			require.Contains(t, body, adopted.ID.String(), "authorised adoption retains the original initiator's history")
			require.NotContains(t, body, badAccount.ID.String(), "a run must not borrow another account's visible case")
			require.NotContains(t, body, badContact.ID.String(), "a run must not borrow another contact's visible case")
			if actor == agent.ID {
				require.NotContains(t, body, hiddenRun.ID.String(), "a shared contact is not access to another inbox")
			}
		}
	}
	require.NoError(t, f.a.DB.Model(&f.c).Update("assignee_id", f.actor.ID).Error)
	body := string(testutil.GetResponseBody(list(agent.ID, f.c.ID)))
	require.Contains(t, body, `"runs":[]`, "authorship must not bypass current case access")
}
