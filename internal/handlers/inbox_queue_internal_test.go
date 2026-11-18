package handlers

import (
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
)

func TestInboxQueueFiltersAndBulkBoundaries(t *testing.T) {
	db := testutil.SetupTestDB(t)
	app := &App{DB: db, Log: testutil.NopLogger()}
	org := testutil.CreateTestOrganization(t, db)
	other := testutil.CreateTestOrganization(t, db)
	user := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	require.NoError(t, db.Model(other).Update("inbox_workflows_enabled", true).Error)
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	otherAccount := testutil.CreateTestWhatsAppAccount(t, db, other.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	otherContact := testutil.CreateTestContact(t, db, other.ID)
	now := time.Now().UTC()
	app.recordInboxInbound(account, contact, "fixture", now, nil)
	app.recordInboxInbound(otherAccount, otherContact, "fixture", now, nil)
	thread := inboxThread{org.ID, account.ID, contact.ID}
	c, err := thread.latest(db)
	require.NoError(t, err)
	outside, err := (inboxThread{other.ID, otherAccount.ID, otherContact.ID}).latest(db)
	require.NoError(t, err)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetQueryParam(req, "filters", `{"queue":"awaiting_reply"}`)
	require.NoError(t, app.ListInboxQueue(req))
	var listed struct{ Cases []inboxQueueRow }
	testutil.ParseEnvelopeResponse(t, req, &listed)
	require.Len(t, listed.Cases, 1)
	require.Equal(t, c.ID, listed.Cases[0].ID)

	viewReq := testutil.NewJSONRequest(t, map[string]any{"name": "Follow-ups", "filters": map[string]string{"queue": "follow_ups"}})
	testutil.SetAuthContext(viewReq, org.ID, user.ID)
	require.NoError(t, app.CreateInboxSavedView(viewReq))
	require.Equal(t, 200, testutil.GetResponseStatusCode(viewReq))

	bulk := testutil.NewJSONRequest(t, bulkInboxRequest{Action: "resolve", Items: []bulkInboxItem{
		{ID: c.ID, Version: c.Version}, {ID: outside.ID, Version: outside.Version}, {ID: c.ID, Version: c.Version},
	}})
	testutil.SetAuthContext(bulk, org.ID, user.ID)
	require.NoError(t, app.BulkUpdateInboxCases(bulk))
	var outcome struct{ Results []struct{ Result string } }
	testutil.ParseEnvelopeResponse(t, bulk, &outcome)
	require.Len(t, outcome.Results, 3)
	require.Equal(t, "updated", outcome.Results[0].Result)
	require.Equal(t, "unavailable", outcome.Results[1].Result)
	require.Equal(t, "stale", outcome.Results[2].Result)
	c, err = thread.latest(db)
	require.NoError(t, err)
	require.Equal(t, models.InboxCaseResolved, c.Status)
}
