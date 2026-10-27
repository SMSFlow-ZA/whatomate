package handlers

import (
	"testing"

	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/require"
)

func TestHostBrandingDefaultsAndCustomPrecedence(t *testing.T) {
	a := &App{Config: &config.Config{}}
	require.Equal(t, "Whatomate", a.brandingDisplay(models.InstanceBranding{}).DisplayName)
	a.Config.Plan.Enabled = true
	require.Equal(t, "SMSFlow", a.brandingDisplay(models.InstanceBranding{}).DisplayName)
	a.Config.Branding = config.BrandingConfig{DisplayName: "Example ER — demonstration", AccentColor: "#b00020"}
	d := a.brandingDisplay(models.InstanceBranding{})
	require.Equal(t, "Example ER — demonstration", d.DisplayName)
	require.Equal(t, "#b00020", d.AccentColor)
	require.False(t, d.Custom, "host defaults are not a stored customer override")
	require.Equal(t, "/branding/smsflow-logo.png", d.LogoURL)
	d = a.brandingDisplay(models.InstanceBranding{ID: 1, DisplayName: "Customer override", AccentColor: "#112233"})
	require.True(t, d.Custom)
	require.Equal(t, "Customer override", d.DisplayName)
	require.Equal(t, "#112233", d.AccentColor)
	require.Equal(t, "Example ER — demonstration", d.DefaultDisplayName)
}
