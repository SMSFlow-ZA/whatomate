package handlers

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestInboxCaseInboundAndSnooze(t *testing.T) {
	db := testutil.SetupTestDB(t)
	app := &App{DB: db, Log: testutil.NopLogger()}
	org := testutil.CreateTestOrganization(t, db)
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	otherAccount := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	thread := inboxThread{org.ID, account.ID, contact.ID}
	now := time.Now().UTC().Truncate(time.Millisecond)

	app.recordInboxInbound(account, contact, "ignored-before-enabling", now, nil)
	var count int64
	require.NoError(t, thread.query(db.Model(&models.InboxCase{})).Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)

	var workers sync.WaitGroup
	for range 6 {
		workers.Go(func() { app.recordInboxInbound(account, contact, "first-inbound", now, nil) })
	}
	workers.Wait()
	c, err := thread.latest(db)
	require.NoError(t, err)
	require.Equal(t, int64(1), c.Version)
	require.NoError(t, thread.query(db.Model(&models.InboxCase{})).Count(&count).Error)
	require.EqualValues(t, 1, count)

	deadline := now.Add(time.Minute)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return applyInboxTransition(tx, &c, inboxCaseUpdate{Status: models.InboxCaseSnoozed, SnoozedUntil: &deadline}, nil, now, "updated")
	}))
	app.recordInboxInbound(otherAccount, contact, "first-inbound", now.Add(time.Second), nil)
	afterOtherNumber, err := thread.latest(db)
	require.NoError(t, err)
	require.Equal(t, models.InboxCaseSnoozed, afterOtherNumber.Status)

	for range 4 {
		workers.Go(func() { _ = app.reopenDueInboxCases(context.Background(), deadline) })
	}
	workers.Wait()
	c, err = thread.latest(db)
	require.NoError(t, err)
	require.Equal(t, models.InboxCaseOpen, c.Status)
	require.Nil(t, c.SnoozedUntil)
	require.NoError(t, db.Model(&models.InboxCaseActivity{}).Where("case_id = ? AND kind = ?", c.ID, "snooze_elapsed").Count(&count).Error)
	require.EqualValues(t, 1, count)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return applyInboxTransition(tx, &c, inboxCaseUpdate{Status: models.InboxCaseResolved}, nil, deadline, "updated")
	}))
	app.recordInboxInbound(account, contact, "first-inbound", deadline.Add(time.Second), nil)
	unchanged, err := thread.latest(db)
	require.NoError(t, err)
	require.Equal(t, c.ID, unchanged.ID)
	require.Equal(t, models.InboxCaseResolved, unchanged.Status)

	app.recordInboxInbound(account, contact, "later-inbound", deadline.Add(time.Second), nil)
	later, err := thread.latest(db)
	require.NoError(t, err)
	require.NotEqual(t, c.ID, later.ID)
	require.Equal(t, models.InboxCaseOpen, later.Status)
}

func TestInboxCaseAuthorizationAndStaleUpdate(t *testing.T) {
	db := testutil.SetupTestDB(t)
	app := &App{DB: db, Log: testutil.NopLogger()}
	org := testutil.CreateTestOrganization(t, db)
	otherOrg := testutil.CreateTestOrganization(t, db)
	otherAccount := testutil.CreateTestWhatsAppAccount(t, db, otherOrg.ID)
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	user := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)

	update := func(accountID uuid.UUID, input inboxCaseUpdate) int {
		req := testutil.NewJSONRequest(t, input)
		testutil.SetAuthContext(req, org.ID, user.ID)
		testutil.SetPathParam(req, "account_id", accountID.String())
		testutil.SetPathParam(req, "id", contact.ID.String())
		require.NoError(t, app.UpdateInboxConversation(req))
		return testutil.GetResponseStatusCode(req)
	}
	require.Equal(t, 404, update(otherAccount.ID, inboxCaseUpdate{Status: models.InboxCaseOpen}))
	require.Equal(t, 200, update(account.ID, inboxCaseUpdate{Status: models.InboxCaseOpen}))
	thread := inboxThread{org.ID, account.ID, contact.ID}
	c, err := thread.latest(db)
	require.NoError(t, err)
	require.Equal(t, 200, update(account.ID, inboxCaseUpdate{CaseID: &c.ID, Version: c.Version, Status: models.InboxCaseWaiting}))
	require.Equal(t, 409, update(account.ID, inboxCaseUpdate{CaseID: &c.ID, Version: c.Version, Status: models.InboxCaseResolved}))

	list := testutil.NewGETRequest(t)
	testutil.SetAuthContext(list, org.ID, user.ID)
	testutil.SetQueryParam(list, "contacts", contact.ID.String())
	require.NoError(t, app.ListInboxCaseSummaries(list))
	var data struct{ Cases []models.InboxCase }
	testutil.ParseEnvelopeResponse(t, list, &data)
	require.Len(t, data.Cases, 1)
	require.Equal(t, models.InboxCaseWaiting, data.Cases[0].Status)
}
