package handlers_test

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/handlers"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/shridarpatil/whatomate/test/testutil"
	"github.com/stretchr/testify/require"
)

func TestProtectedConfiguredBrandingDoesNotRequireLegacyStorage(t *testing.T) {
	a := &handlers.App{Config: &config.Config{}}
	a.Config.Extensions.ProtectedMode = true
	a.Config.Database.SchemaManagedExternally = true
	a.Config.Branding = config.BrandingConfig{DisplayName: "Example ER — demonstration", AccentColor: "#b00020"}
	// The protected runtime deliberately has no legacy branding-table access.
	// No database or filesystem recovery is needed for host-owned text defaults.
	r := testutil.NewGETRequest(t)
	require.NoError(t, a.GetBranding(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	require.Contains(t, string(testutil.GetResponseBody(r)), "Example ER — demonstration")
	require.Contains(t, string(testutil.GetResponseBody(r)), `"accent_color":"#b00020"`)
	require.Contains(t, string(testutil.GetResponseBody(r)), `"custom":false`)
	require.Equal(t, "no-store", string(r.RequestCtx.Response.Header.Peek("Cache-Control")))
}

func TestBrandingStorageErrorsAreNotMaskedOutsideExplicitProtectedDefaults(t *testing.T) {
	for _, tc := range []struct {
		name                string
		protected, external bool
		display             string
	}{
		{"ordinary configured", false, false, "Example"},
		{"protected without external schema", true, false, "Example"},
		{"external without protection", false, true, "Example"},
		{"protected without explicit name", true, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &handlers.App{Config: &config.Config{}}
			a.Config.Extensions.ProtectedMode = tc.protected
			a.Config.Database.SchemaManagedExternally = tc.external
			a.Config.Branding.DisplayName = tc.display
			r := testutil.NewGETRequest(t)
			require.NoError(t, a.GetBranding(r))
			require.Equal(t, 503, testutil.GetResponseStatusCode(r))
		})
	}
}

func TestOrdinaryConfiguredBrandingRetainsStoredOverride(t *testing.T) {
	a := brandingApp(t)
	a.Config.Branding = config.BrandingConfig{DisplayName: "Host default", AccentColor: "#b00020"}
	require.NoError(t, a.DB.Create(&models.InstanceBranding{ID: 1, DisplayName: "Stored customer name", AccentColor: "#112233"}).Error)
	r := testutil.NewGETRequest(t)
	require.NoError(t, a.GetBranding(r))
	require.Equal(t, 200, testutil.GetResponseStatusCode(r))
	require.Contains(t, string(testutil.GetResponseBody(r)), `"display_name":"Stored customer name"`)
	require.Contains(t, string(testutil.GetResponseBody(r)), `"default_display_name":"Host default"`)
	require.Contains(t, string(testutil.GetResponseBody(r)), `"custom":true`)
}
