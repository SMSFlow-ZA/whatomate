package config_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/stretchr/testify/require"
)

func TestHostBrandingConfiguration(t *testing.T) {
	for _, name := range []string{"", "Example Employee Relations — demonstration", strings.Repeat("é", 80)} {
		cfg, err := config.Load(writeConfig(t, fmt.Sprintf("[branding]\ndisplay_name=%q\naccent_color=\"#b00020\"\n", name)))
		require.NoError(t, err)
		require.Equal(t, name, cfg.Branding.DisplayName)
		require.Equal(t, "#b00020", cfg.Branding.AccentColor)
	}
	for _, value := range []config.BrandingConfig{{DisplayName: " "}, {DisplayName: " padded"}, {DisplayName: "bad\nname"}, {DisplayName: strings.Repeat("é", 81)}, {DisplayName: string([]byte{0xff})}, {AccentColor: "red"}, {AccentColor: "#fff"}, {AccentColor: "#b00020;"}} {
		require.Error(t, value.Validate())
	}
	_, err := config.Load(writeConfig(t, "[branding]\naccent_color=\"red\"\n"))
	require.Error(t, err, "invalid defaults must prevent startup")
	t.Setenv("WHATOMATE_BRANDING__DISPLAY_NAME", "Environment workspace")
	cfg, err := config.Load(writeConfig(t, ""))
	require.NoError(t, err)
	require.Equal(t, "Environment workspace", cfg.Branding.DisplayName)
}
