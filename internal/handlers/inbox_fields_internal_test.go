package handlers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
)

func TestInboxCustomFieldResolutionAndArchivedFilter(t *testing.T) {
	db := testutil.SetupTestDB(t)
	a := &App{DB: db, Log: testutil.NopLogger()}
	org := testutil.CreateTestOrganization(t, db)
	admin := testutil.CreateTestUser(t, db, org.ID, testutil.WithSuperAdmin())
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	now := time.Now().UTC()
	a.recordInboxInbound(account, contact, "in", now, nil)
	thread := inboxThread{org.ID, account.ID, contact.ID}
	current, err := thread.latest(db)
	require.NoError(t, err)
	d := models.InboxFieldDefinition{BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, Scope: "case", Key: "referred", Label: "Referred", Kind: "boolean", RequiredOnResolve: true}
	require.NoError(t, db.Create(&d).Error)
	resolve := inboxCaseUpdate{Status: models.InboxCaseResolved}
	require.ErrorIs(t, applyInboxTransition(db, &current, resolve, nil, now, "updated"), errRequiredInboxFields)

	value := testutil.NewJSONRequest(t, map[string]any{"subject_id": current.ID, "value": false})
	testutil.SetAuthContext(value, org.ID, admin.ID)
	testutil.SetPathParam(value, "id", contact.ID.String())
	testutil.SetPathParam(value, "account_id", account.ID.String())
	testutil.SetPathParam(value, "definition_id", d.ID.String())
	require.NoError(t, a.SetInboxCaseField(value))
	require.Equal(t, 200, testutil.GetResponseStatusCode(value))
	require.NoError(t, applyInboxTransition(db, &current, resolve, nil, now, "updated"))
	require.NoError(t, db.Model(&d).Update("archived_at", now).Error)

	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, admin.ID)
	filters, err := json.Marshal(models.InboxQueueFilter{Fields: []models.InboxFieldMatch{{DefinitionID: d.ID, Value: json.RawMessage("false")}}})
	require.NoError(t, err)
	testutil.SetQueryParam(req, "filters", string(filters))
	require.NoError(t, a.ListInboxQueue(req))
	var got struct{ Cases []inboxQueueRow }
	testutil.ParseEnvelopeResponse(t, req, &got)
	require.Len(t, got.Cases, 1)
	require.Equal(t, current.ID, got.Cases[0].ID)
}
