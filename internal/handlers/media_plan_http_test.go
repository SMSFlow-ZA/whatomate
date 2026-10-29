package handlers_test

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

func planMediaRequest(t *testing.T, mimeType string, fields map[string]string) *fastglue.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="file"; filename="media.bin"`)
	header.Set("Content-Type", mimeType)
	part, err := writer.CreatePart(header)
	require.NoError(t, err)
	_, err = part.Write([]byte("content"))
	require.NoError(t, err)
	for key, value := range fields {
		require.NoError(t, writer.WriteField(key, value))
	}
	require.NoError(t, writer.Close())
	req := testutil.NewRequest(t)
	var request fasthttp.Request
	request.Header.SetMethod("POST")
	request.Header.SetContentType(writer.FormDataContentType())
	request.SetBody(body.Bytes())
	req.RequestCtx.Init(&request, nil, nil)
	return req
}

func TestPlanStorageAudioUploadsReturnConflictAndPreserveExisting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test converter requires sh")
	}
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "ffmpeg"), []byte("#!/bin/sh\nfor arg in \"$@\"; do out=\"$arg\"; done\nprintf OggS > \"$out\"\n"), 0755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, kind := range []string{"ivr", "org"} {
		t.Run(kind, func(t *testing.T) {
			app := newTestApp(t)
			app.Config.Plan = config.PlanConfig{Enabled: true, MaxStorageBytes: 3}
			app.Config.Storage.LocalPath = t.TempDir()
			app.Config.Calling.AudioDir = t.TempDir()
			org := testutil.CreateTestOrganization(t, app.DB)
			role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "audio-plan", []string{"ivr_flows:write", "organizations:write"})
			user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
			req := planMediaRequest(t, "audio/wav", nil)
			testutil.SetAuthContext(req, org.ID, user.ID)
			previous := filepath.Join(app.Config.Calling.AudioDir, "org_"+org.ID.String()+"_hold_music.ogg")
			if kind == "org" {
				require.NoError(t, os.WriteFile(previous, []byte("old"), 0644))
				testutil.SetQueryParam(req, "type", "hold_music")
				require.NoError(t, app.UploadOrgAudio(req))
			} else {
				require.NoError(t, app.UploadIVRAudio(req))
			}
			require.Equal(t, fasthttp.StatusConflict, testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req)))
			require.Contains(t, string(testutil.GetResponseBody(req)), "storage")
			entries, err := os.ReadDir(app.Config.Calling.AudioDir)
			require.NoError(t, err)
			if kind == "org" {
				got, err := os.ReadFile(previous)
				require.NoError(t, err)
				require.Equal(t, "old", string(got))
				require.Len(t, entries, 1)
			} else {
				require.Empty(t, entries)
			}
		})
	}
}

func TestPlanStorageContactUploadReturnsConflict(t *testing.T) {
	app := newTestApp(t)
	app.Config.Plan = config.PlanConfig{Enabled: true, MaxStorageBytes: 3}
	app.Config.Storage.LocalPath = t.TempDir()
	app.Config.Calling.AudioDir = t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"media","messages":[{"id":"wamid.test"}]}`))
	}))
	t.Cleanup(server.Close)
	app.WhatsApp = whatsapp.NewWithBaseURL(testutil.NopLogger(), server.URL)
	t.Cleanup(app.WaitForBackgroundTasks)
	org := testutil.CreateTestOrganization(t, app.DB)
	role := testutil.CreateTestRoleWithKeys(t, app.DB, org.ID, "media-plan", []string{"contacts:read"})
	user := testutil.CreateTestUser(t, app.DB, org.ID, testutil.WithRoleID(&role.ID))
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	contact := testutil.CreateTestContact(t, app.DB, org.ID)
	require.NoError(t, app.DB.Model(contact).Update("whats_app_account", account.Name).Error)
	req := planMediaRequest(t, "image/png", map[string]string{"contact_id": contact.ID.String(), "whatsapp_account": account.Name})
	testutil.SetAuthContext(req, org.ID, user.ID)
	require.NoError(t, app.SendMediaMessage(req))
	require.Equal(t, fasthttp.StatusConflict, testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req)))
}

func TestPlanStorageCampaignUploadReturnsConflictBeforeMetaUpload(t *testing.T) {
	app := newTestApp(t)
	app.Config.Plan = config.PlanConfig{Enabled: true, MaxStorageBytes: 3}
	app.Config.Storage.LocalPath = t.TempDir()
	app.Config.Calling.AudioDir = t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"id":"media"}`)) }))
	t.Cleanup(server.Close)
	app.WhatsApp = whatsapp.NewWithBaseURL(testutil.NopLogger(), server.URL)
	org := testutil.CreateTestOrganization(t, app.DB)
	user := testutil.CreateTestUser(t, app.DB, org.ID)
	account := testutil.CreateTestWhatsAppAccount(t, app.DB, org.ID)
	template := testutil.CreateTestTemplate(t, app.DB, org.ID, account.Name)
	require.NoError(t, app.DB.Model(template).Update("header_type", "IMAGE").Error)
	campaign := createTestCampaign(t, app, org.ID, template.ID, user.ID, account.Name, models.CampaignStatusDraft)
	req := planMediaRequest(t, "image/png", nil)
	testutil.SetAuthContext(req, org.ID, user.ID)
	testutil.SetPathParam(req, "id", campaign.ID.String())
	require.NoError(t, app.UploadCampaignMedia(req))
	require.Equal(t, fasthttp.StatusConflict, testutil.GetResponseStatusCode(req), string(testutil.GetResponseBody(req)))
}
