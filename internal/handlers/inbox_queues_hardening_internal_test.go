package handlers

import (
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

func TestInboxHardeningTransferStillGrantsOtherAccount(t *testing.T) {
	db := testutil.SetupTestDB(t)
	a := &App{DB: db, Redis: testutil.SetupTestRedis(t), Log: testutil.NopLogger()}
	require.NotNil(t, a.Redis)
	org := testutil.CreateTestOrganization(t, db)
	role := testutil.CreateTestRoleWithKeys(t, db, org.ID, "review-agent", []string{"chat:read", "chat:write"})
	user := testutil.CreateTestUser(t, db, org.ID, testutil.WithRoleID(&role.ID))
	require.NoError(t, db.Where("organization_id = ? AND user_id = ?", org.ID, user.ID).FirstOrCreate(
		&models.UserOrganization{OrganizationID: org.ID, UserID: user.ID, RoleID: &role.ID}).Error)
	first := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	other := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	require.NoError(t, db.Create(&models.AgentTransfer{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, ContactID: contact.ID,
		AgentID: &user.ID, WhatsAppAccount: first.Name, PhoneNumber: contact.PhoneNumber,
		Status: models.TransferStatusActive,
	}).Error)
	require.NoError(t, db.Create(&models.Message{
		BaseModel: models.BaseModel{ID: uuid.New()}, OrganizationID: org.ID, ContactID: contact.ID,
		WhatsAppAccount: other.Name, Direction: models.DirectionIncoming,
		MessageType: models.MessageTypeText, Content: "synthetic other-account confidential message",
	}).Error)
	require.False(t, a.HasPermission(user.ID, models.ResourceContacts, models.ActionRead, org.ID))
	_, only, err := a.findInboxChatContact(org.ID, user.ID, contact.ID, first.Name)
	require.NoError(t, err)
	require.True(t, only)
	_, _, err = a.findInboxChatContact(org.ID, user.ID, contact.ID, other.Name)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	_, _, err = a.findInboxChatContact(org.ID, user.ID, contact.ID, "")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.True(t, a.canReceiveConversation(org.ID, user.ID, contact.ID, first.Name))
	require.False(t, a.canReceiveConversation(org.ID, user.ID, contact.ID, other.Name))
	require.NoError(t, db.Model(&models.AgentTransfer{}).Where("contact_id = ?", contact.ID).Update("status", models.TransferStatusResumed).Error)
	require.False(t, a.canReceiveConversation(org.ID, user.ID, contact.ID, first.Name))
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", contact.ID.String())
	testutil.SetQueryParam(req, "account", other.Name)
	require.NoError(t, a.GetMessages(req))
	require.Equal(t, 404, testutil.GetResponseStatusCode(req))
}

func TestInboxHardeningTransferCannotOverrideCurrentCaseAssignment(t *testing.T) {
	db := testutil.SetupTestDB(t)
	a := &App{DB: db, Redis: testutil.SetupTestRedis(t), Log: testutil.NopLogger()}
	org := testutil.CreateTestOrganization(t, db)
	require.NoError(t, db.Model(org).Update("inbox_workflows_enabled", true).Error)
	role := testutil.CreateTestRoleWithKeys(t, db, org.ID, "case-transfer-agent", []string{"chat:read", "chat:write"})
	agent := testutil.CreateTestUser(t, db, org.ID, testutil.WithRoleID(&role.ID))
	other := testutil.CreateTestUser(t, db, org.ID)
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	contact := testutil.CreateTestContact(t, db, org.ID)
	require.NoError(t, db.Create(&models.AgentTransfer{OrganizationID: org.ID, ContactID: contact.ID,
		AgentID: &agent.ID, WhatsAppAccount: account.Name, Status: models.TransferStatusActive}).Error)
	assigned := agent.ID // Keep the model pointer independent of the actor's identity.
	c := newInboxCase(inboxThread{org.ID, account.ID, contact.ID}, time.Now().UTC(), &assigned, nil)
	require.NoError(t, db.Create(&c).Error)
	require.True(t, a.canReceiveConversation(org.ID, agent.ID, contact.ID, account.Name))
	require.NoError(t, db.Model(&c).Update("assignee_id", other.ID).Error)
	require.False(t, a.HasPermission(agent.ID, models.ResourceContacts, models.ActionRead, org.ID))
	_, _, err := a.findInboxChatContact(org.ID, agent.ID, contact.ID, account.Name)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.False(t, a.canReceiveConversation(org.ID, agent.ID, contact.ID, account.Name))
}

func TestInboxHardeningScopesSessionsAndReadReceiptsByAccount(t *testing.T) {
	db := testutil.SetupTestDB(t)
	a := &App{DB: db, Redis: testutil.SetupTestRedis(t), Log: testutil.NopLogger(), Config: &config.Config{}}
	org := testutil.CreateTestOrganization(t, db)
	role := testutil.CreateTestRoleWithKeys(t, db, org.ID, "receipt-agent", []string{"chat:read", "chat:write"})
	agent := testutil.CreateTestUser(t, db, org.ID, testutil.WithRoleID(&role.ID))
	account := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	other := testutil.CreateTestWhatsAppAccount(t, db, org.ID)
	require.NoError(t, db.Model(account).Update("auto_read_receipt", false).Error)
	contact := testutil.CreateTestContact(t, db, org.ID)
	require.NoError(t, db.Create(&models.AgentTransfer{OrganizationID: org.ID, ContactID: contact.ID,
		AgentID: &agent.ID, WhatsAppAccount: account.Name, Status: models.TransferStatusActive}).Error)
	messages := []models.Message{
		{OrganizationID: org.ID, ContactID: contact.ID, WhatsAppAccount: account.Name, Direction: models.DirectionIncoming, MessageType: models.MessageTypeText, Status: models.MessageStatusDelivered},
		{OrganizationID: org.ID, ContactID: contact.ID, WhatsAppAccount: other.Name, Direction: models.DirectionIncoming, MessageType: models.MessageTypeText, Status: models.MessageStatusDelivered},
	}
	require.NoError(t, db.Create(&messages).Error)
	sessions := []models.ChatbotSession{
		{OrganizationID: org.ID, ContactID: contact.ID, WhatsAppAccount: account.Name, Status: models.SessionStatusActive},
		{OrganizationID: org.ID, ContactID: contact.ID, WhatsAppAccount: other.Name, Status: models.SessionStatusActive},
	}
	sessions[0].CreatedAt = time.Now().UTC().Add(-time.Minute)
	sessions[1].CreatedAt = time.Now().UTC()
	require.NoError(t, db.Create(&sessions).Error)
	read := testutil.NewJSONRequest(t, map[string]any{})
	testutil.SetAuthContext(read, org.ID, agent.ID)
	testutil.SetPathParam(read, "id", contact.ID.String())
	testutil.SetQueryParam(read, "account", account.Name)
	require.NoError(t, a.MarkContactRead(read))
	require.Equal(t, 200, testutil.GetResponseStatusCode(read))
	for i, status := range []models.MessageStatus{models.MessageStatusRead, models.MessageStatusDelivered} {
		var stored models.Message
		require.NoError(t, db.First(&stored, "id = ?", messages[i].ID).Error)
		require.Equal(t, status, stored.Status)
	}
	for _, name := range []string{account.Name, other.Name, ""} {
		request := testutil.NewGETRequest(t)
		testutil.SetAuthContext(request, org.ID, agent.ID)
		testutil.SetPathParam(request, "id", contact.ID.String())
		testutil.SetQueryParam(request, "account", name)
		require.NoError(t, a.GetContactSessionData(request))
		if name != account.Name {
			require.Equal(t, 404, testutil.GetResponseStatusCode(request))
			continue
		}
		var data ContactSessionDataResponse
		testutil.ParseEnvelopeResponse(t, request, &data)
		require.NotNil(t, data.SessionID)
		require.Equal(t, sessions[0].ID, *data.SessionID)
	}
}
