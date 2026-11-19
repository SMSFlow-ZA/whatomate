package handlers

import (
	"testing"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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

