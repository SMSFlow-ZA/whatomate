package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/plan"
	"github.com/shridarpatil/whatomate/pkg/whatsapp"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func mediaPlanApp(t *testing.T) *App {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return &App{DB: db, Config: &config.Config{Plan: config.PlanConfig{Enabled: true, MaxStorageBytes: 3}, Storage: config.StorageConfig{LocalPath: t.TempDir()}, Calling: config.CallingConfig{AudioDir: t.TempDir()}}, Log: testutil.NopLogger()}
}

func TestPlanStorageContactAndCampaignWriters(t *testing.T) {
	for _, writer := range []string{"contact", "campaign"} {
		t.Run(writer, func(t *testing.T) {
			app := mediaPlanApp(t)
			// Files in the audio root consume upload allowance too.
			require.NoError(t, os.WriteFile(filepath.Join(app.getAudioDir(), "existing.ogg"), []byte("123"), 0644))
			var err error
			if writer == "contact" {
				_, err = app.saveMediaLocally([]byte("x"), "image/png", "test.png")
			} else {
				_, err = app.saveCampaignMedia("campaign", []byte("x"), "image/png")
			}
			require.ErrorIs(t, err, plan.ErrStorageLimit)
			usage, err := plan.StorageUsage([]string{app.getMediaStoragePath(), app.getAudioDir()})
			require.NoError(t, err)
			require.Equal(t, int64(3), usage)
		})
	}
}

func TestPlanStorageReceivedMediaWriter(t *testing.T) {
	app := mediaPlanApp(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/download" {
			_, _ = w.Write([]byte("1234"))
			return
		}
		_, _ = w.Write([]byte(`{"url":"http://` + r.Host + `/download"}`))
	}))
	defer server.Close()
	app.WhatsApp = whatsapp.NewWithBaseURL(testutil.NopLogger(), server.URL)
	_, err := app.DownloadAndSaveMedia(context.Background(), "media", "image/png", &whatsapp.Account{APIVersion: "v1", AccessToken: "test"})
	require.ErrorIs(t, err, plan.ErrStorageLimit)
	usage, err := plan.StorageUsage([]string{app.getMediaStoragePath(), app.getAudioDir()})
	require.NoError(t, err)
	require.Zero(t, usage)
}
