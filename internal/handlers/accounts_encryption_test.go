package handlers_test

import (
	"testing"

	appcrypto "github.com/shridarpatil/whatomate/internal/crypto"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestApp_UpdateAccount_PreservesEncryptedSecretsOnMetadataEdit(t *testing.T) {
	app := newTestApp(t)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := createAdminUser(t, app, org.ID)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	account.AccessToken, account.AppSecret, account.Pin = "existing-token", "existing-app-secret", "123456"
	require.NoError(t, appcrypto.EncryptFields(app.Config.App.EncryptionKey, &account.AccessToken, &account.AppSecret, &account.Pin))
	require.NoError(t, app.DB.Save(account).Error)

	req := testutil.NewJSONRequest(t, map[string]any{"name": account.Name, "auto_read_receipt": true})
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", account.ID.String())
	require.NoError(t, app.UpdateAccount(req))
	require.Equal(t, fasthttp.StatusOK, testutil.GetResponseStatusCode(req))

	var stored models.WhatsAppAccount
	require.NoError(t, app.DB.First(&stored, "id = ?", account.ID).Error)
	for label, value := range map[string]string{"access_token": stored.AccessToken, "app_secret": stored.AppSecret, "pin": stored.Pin} {
		require.True(t, appcrypto.IsEncrypted(value), "%s must remain encrypted after a metadata-only edit", label)
	}
	stored.DecryptSecrets(app.Config.App.EncryptionKey)
	require.Equal(t, "existing-token", stored.AccessToken)
	require.Equal(t, "existing-app-secret", stored.AppSecret)
	require.Equal(t, "123456", stored.Pin)
}
