package handlers

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"testing"

	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/internal/plan"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func TestManagedCampaignFailedPublicationPreservesPreviewAndMetadata(t *testing.T) {
	for _, failure := range []string{"provider", "database"} {
		t.Run(failure, func(t *testing.T) {
			app, org, owner, _ := managedEntityApp(t)
			app.Config.Storage.LocalPath = t.TempDir()
			app.Config.Calling.AudioDir = t.TempDir()
			require.NoError(t, app.DB.AutoMigrate(&models.Template{}, &models.BulkMessageCampaign{}))
			account := models.WhatsAppAccount{OrganizationID: org.ID, Name: "account", PhoneID: "phone", BusinessID: "business", AccessToken: "token"}
			require.NoError(t, app.DB.Create(&account).Error)
			template := models.Template{OrganizationID: org.ID, Name: "template", WhatsAppAccount: account.Name, HeaderType: "IMAGE"}
			require.NoError(t, app.DB.Create(&template).Error)
			campaign := models.BulkMessageCampaign{OrganizationID: org.ID, Name: "campaign", WhatsAppAccount: account.Name, TemplateID: template.ID, CreatedBy: owner.ID, Status: models.CampaignStatusDraft, HeaderMediaID: "old-provider-id", HeaderMediaFilename: "old.png", HeaderMediaMimeType: "image/png"}
			require.NoError(t, app.DB.Create(&campaign).Error)
			relative := filepath.Join("campaigns", campaign.ID.String()+".png")
			path := filepath.Join(app.getMediaStoragePath(), relative)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
			require.NoError(t, os.WriteFile(path, []byte("old"), 0644))
			require.NoError(t, app.DB.Model(&campaign).Update("header_media_local_path", relative).Error)
			if failure == "database" {
				require.NoError(t, app.DB.Exec(`CREATE FUNCTION reject_media_publication() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test publication failure'; END $$; CREATE TRIGGER reject_media_publication BEFORE UPDATE ON bulk_message_campaigns FOR EACH ROW EXECUTE FUNCTION reject_media_publication()`).Error)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if failure == "provider" {
					w.WriteHeader(400)
					_, _ = w.Write([]byte(`{"error":{"message":"rejected","code":10}}`))
					return
				}
				_, _ = w.Write([]byte(`{"id":"new-provider-id"}`))
			}))
			defer server.Close()
			app.WhatsApp = whatsapp.NewWithBaseURL(app.Log, server.URL)
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			part, err := writer.CreatePart(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="file"; filename="new.png"`}, "Content-Type": {"image/png"}})
			require.NoError(t, err)
			_, err = part.Write([]byte("new"))
			require.NoError(t, err)
			require.NoError(t, writer.Close())
			req := testutil.NewRequest(t)
			req.RequestCtx.Init(&fasthttp.Request{}, nil, nil)
			req.RequestCtx.Request.Header.SetMethod("POST")
			req.RequestCtx.Request.Header.SetContentType(writer.FormDataContentType())
			req.RequestCtx.Request.SetBody(body.Bytes())
			testutil.SetAuthContext(req, org.ID, owner.ID)
			testutil.SetPathParam(req, "id", campaign.ID.String())
			require.NoError(t, app.UploadCampaignMedia(req))
			require.Equal(t, 500, testutil.GetResponseStatusCode(req))
			got, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, "old", string(got))
			require.NoError(t, app.DB.First(&campaign, campaign.ID).Error)
			require.Equal(t, "old-provider-id", campaign.HeaderMediaID)
			require.Equal(t, "old.png", campaign.HeaderMediaFilename)
		})
	}
}

func TestManagedStatusReportsInstalledCapsFromStaleProcess(t *testing.T) {
	app, org, owner, _ := managedEntityApp(t)
	app.Config.Storage.LocalPath = t.TempDir()
	app.Config.Calling.AudioDir = t.TempDir()
	current := app.Config.Plan
	current.MaxUsers = 2
	current.MaxNumbers = 3
	current.MaxStorageBytes = 4
	require.NoError(t, plan.Install(app.DB, current))
	req := testutil.NewGETRequest(t)
	testutil.SetAuthContext(req, org.ID, owner.ID)
	require.NoError(t, app.GetPlan(req))
	require.Equal(t, 200, testutil.GetResponseStatusCode(req))
	var response struct {
		Data struct {
			Limits struct {
				Users   int   `json:"max_users"`
				Numbers int   `json:"max_numbers"`
				Storage int64 `json:"max_storage_bytes"`
			}
			Usage struct {
				Users int `json:"users"`
			}
		}
	}
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(req), &response))
	require.Equal(t, 2, response.Data.Limits.Users)
	require.Equal(t, 3, response.Data.Limits.Numbers)
	require.EqualValues(t, 4, response.Data.Limits.Storage)
	require.Equal(t, 1, response.Data.Usage.Users)
}
