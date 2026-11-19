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

func TestInboxFieldEditsInvalidateQueuedSendWithinOrganization(t *testing.T) {
	db := testutil.SetupTestDB(t)
	org := testutil.CreateTestOrganization(t, db)
	other := testutil.CreateTestOrganization(t, db)
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	c := newInboxCase(inboxThread{org.ID, account.ID, contact.ID}, time.Now().UTC(), nil, nil)
	require.NoError(t, db.Create(&c).Error)
	before, err := inboxSendCaseDigest(db, c)
	require.NoError(t, err)
	value := models.InboxFieldValue{OrganizationID: other.ID, DefinitionID: uuid.New(), SubjectID: c.ID, Value: json.RawMessage(`"synthetic value"`)}
	require.NoError(t, db.Create(&value).Error)
	unchanged, err := inboxSendCaseDigest(db, c)
	require.NoError(t, err)
	require.Equal(t, before, unchanged)
	value.OrganizationID = org.ID
	require.NoError(t, db.Create(&value).Error)
	after, err := inboxSendCaseDigest(db, c)
	require.NoError(t, err)
	require.NotEqual(t, before, after)
}
