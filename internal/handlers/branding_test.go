package handlers_test

import (
	"bytes"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"github.com/zerodha/fastglue"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"os"
	"strings"
	"testing"
)

func brandingApp(t *testing.T) *handlers.App {
	a := newTestApp(t)
	require.NoError(t, a.DB.Exec("DELETE FROM instance_brandings").Error)
	a.Config.Storage.LocalPath = t.TempDir()
	a.Config.Calling.AudioDir = t.TempDir()
	return a
}
func brandImage(t *testing.T) []byte {
	im := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	im.Set(0, 0, color.NRGBA{R: 255, A: 255})
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, im))
	return b.Bytes()
}
func brandRequest(t *testing.T, org, user uuid.UUID, fields map[string]string, files map[string][]byte) *fastglue.Request {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	for name, value := range fields {
		require.NoError(t, w.WriteField(name, value))
	}
	for name, data := range files {
		f, e := w.CreateFormFile(name, "test.png")
		require.NoError(t, e)
		_, e = f.Write(data)
		require.NoError(t, e)
	}
	require.NoError(t, w.Close())
	r := testutil.NewRequest(t)
	r.RequestCtx.Request.Header.SetMethod("PUT")
	r.RequestCtx.Request.Header.SetContentType(w.FormDataContentType())
	r.RequestCtx.Request.SetBody(b.Bytes())
	if user != uuid.Nil {
		testutil.SetAuthContext(r, org, user)
	}
	return r
}
func TestBrandingPublicDefaultsAndAdminSaveReset(t *testing.T) {
	a := brandingApp(t)
	org := testutil.CreateTestOrganization(t, a.DB)
	user := createAdminUser(t, a, org.ID)
	r := testutil.NewGETRequest(t)
	require.NoError(t, a.GetBranding(r))
	require.Contains(t, string(testutil.GetResponseBody(r)), "SMSFlow")
	require.NotContains(t, string(testutil.GetResponseBody(r)), "secret")
	r = brandRequest(t, org.ID, user.ID, map[string]string{"display_name": "Acme <Team>", "accent_color": "#b00020"}, map[string][]byte{"logo": brandImage(t), "favicon": brandImage(t)})
	require.NoError(t, a.UpdateBranding(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r), string(testutil.GetResponseBody(r)))
	var row models.InstanceBranding
	require.NoError(t, a.DB.First(&row, 1).Error)
	require.Equal(t, "Acme <Team>", row.DisplayName)
	require.NotEmpty(t, row.LogoHash)
	require.NotEmpty(t, row.FaviconHash)
	r = testutil.NewGETRequest(t)
	r.RequestCtx.SetUserValue("asset", "logo")
	r.RequestCtx.QueryArgs().Set("v", row.LogoHash)
	require.NoError(t, a.BrandingAsset(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	_, e := png.Decode(bytes.NewReader(testutil.GetResponseBody(r)))
	require.NoError(t, e)
	r = testutil.NewRequest(t)
	testutil.SetAuthContext(r, org.ID, user.ID)
	require.NoError(t, a.ResetBranding(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	var n int64
	require.NoError(t, a.DB.Model(&models.InstanceBranding{}).Count(&n).Error)
	require.Zero(t, n)
	r = testutil.NewGETRequest(t)
	require.NoError(t, a.GetBranding(r))
	var result map[string]any
	require.NoError(t, json.Unmarshal(testutil.GetResponseBody(r), &result))
	require.Contains(t, string(testutil.GetResponseBody(r)), "SMSFlow")
}
func TestBrandingAuthorizationAndValidation(t *testing.T) {
	a := brandingApp(t)
	org := testutil.CreateTestOrganization(t, a.DB)
	user := testutil.CreateTestUser(t, a.DB, org.ID)
	r := brandRequest(t, org.ID, uuid.Nil, nil, nil)
	require.NoError(t, a.UpdateBranding(r))
	require.Equal(t, 401, testutil.GetResponseStatusCode(r))
	r = brandRequest(t, org.ID, user.ID, nil, nil)
	require.NoError(t, a.UpdateBranding(r))
	require.Equal(t, 403, testutil.GetResponseStatusCode(r))
	admin := createAdminUser(t, a, org.ID)
	for _, fields := range []map[string]string{{"display_name": "", "accent_color": "#123456"}, {"display_name": strings.Repeat("x", 81), "accent_color": "#123456"}, {"display_name": "Test", "accent_color": "red;url(x)"}} {
		r = brandRequest(t, org.ID, admin.ID, fields, nil)
		require.NoError(t, a.UpdateBranding(r))
		require.Equal(t, 400, testutil.GetResponseStatusCode(r))
	}
	for _, data := range [][]byte{[]byte("<svg><script/></svg>"), make([]byte, 2*1024*1024+1)} {
		r = brandRequest(t, org.ID, admin.ID, map[string]string{"display_name": "Test", "accent_color": "#123456"}, map[string][]byte{"logo": data})
		require.NoError(t, a.UpdateBranding(r))
		require.Equal(t, 400, testutil.GetResponseStatusCode(r))
	}
}
func TestBrandingImageBoundariesAndQuotaRetainPrevious(t *testing.T) {
	a := brandingApp(t)
	org := testutil.CreateTestOrganization(t, a.DB)
	admin := createAdminUser(t, a, org.ID)
	fields := map[string]string{"display_name": "Original", "accent_color": "#123456"}
	r := brandRequest(t, org.ID, admin.ID, fields, map[string][]byte{"logo": brandImage(t), "favicon": brandImage(t)})
	require.NoError(t, a.UpdateBranding(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	var before models.InstanceBranding
	require.NoError(t, a.DB.First(&before, 1).Error)
	large := image.NewNRGBA(image.Rect(0, 0, 2049, 1))
	var imageBytes bytes.Buffer
	require.NoError(t, png.Encode(&imageBytes, large))
	for _, files := range []map[string][]byte{{"logo": imageBytes.Bytes()}, {"favicon": make([]byte, 512*1024+1)}} {
		r = brandRequest(t, org.ID, admin.ID, fields, files)
		require.NoError(t, a.UpdateBranding(r))
		require.Equal(t, 400, testutil.GetResponseStatusCode(r))
	}
	a.Config.Plan.Enabled = true
	a.Config.Plan.MaxStorageBytes = 1
	// Growing the image over an already-full budget cannot publish new metadata.
	big := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for x := 0; x < 16; x++ {
		for y := 0; y < 16; y++ {
			big.Set(x, y, color.NRGBA{R: uint8(x * 13), G: uint8(y * 11), B: uint8(x * y), A: 255})
		}
	}
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, big))
	r = brandRequest(t, org.ID, admin.ID, map[string]string{"display_name": "Changed", "accent_color": "#abcdef"}, map[string][]byte{"logo": b.Bytes()})
	require.NoError(t, a.UpdateBranding(r))
	require.Equal(t, 409, testutil.GetResponseStatusCode(r))
	var after models.InstanceBranding
	require.NoError(t, a.DB.First(&after, 1).Error)
	require.Equal(t, before, after)
	r = brandRequest(t, org.ID, admin.ID, map[string]string{"display_name": "Original", "accent_color": "#123456", "remove_logo": "true", "remove_favicon": "true"}, nil)
	require.NoError(t, a.UpdateBranding(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
}

func TestBrandingFirstManagedSaveWithProductionDatabaseSession(t *testing.T) {
	a := brandingApp(t)
	db, err := gorm.Open(postgres.Open(os.Getenv("TEST_DATABASE_URL")), &gorm.Config{})
	require.NoError(t, err)
	connection, _ := db.DB()
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	a.DB = db
	require.NoError(t, db.Exec("CREATE TABLE whatomate_plan_state(singleton bool,max_users bigint,max_numbers bigint,max_storage_bytes bigint,support_email text); INSERT INTO whatomate_plan_state VALUES (true,3,1,1073741824,'support@example.invalid')").Error)
	t.Cleanup(func() { require.NoError(t, db.Exec("DROP TABLE whatomate_plan_state").Error) })
	a.Config.Plan.Enabled = true
	a.Config.Plan.MaxUsers = 3
	a.Config.Plan.MaxNumbers = 1
	a.Config.Plan.MaxStorageBytes = 1073741824
	org := testutil.CreateTestOrganization(t, db)
	admin := createAdminUser(t, a, org.ID)
	request := brandRequest(t, org.ID, admin.ID, map[string]string{"display_name": "Managed customer", "accent_color": "#8246AF"}, map[string][]byte{"logo": brandImage(t)})
	require.NoError(t, a.UpdateBranding(request))
	require.Equal(t, 200, testutil.GetResponseStatusCode(request), string(testutil.GetResponseBody(request)))
}
