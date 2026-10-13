package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManagedPlanEnvironmentIsTypedAndValidated(t *testing.T) {
	t.Setenv("WHATOMATE_PLAN__ENABLED", "true")
	t.Setenv("WHATOMATE_PLAN__MAX_USERS", "3")
	t.Setenv("WHATOMATE_PLAN__MAX_NUMBERS", "1")
	t.Setenv("WHATOMATE_PLAN__MAX_STORAGE_BYTES", "5368709120")
	t.Setenv("WHATOMATE_PLAN__SUPPORT_EMAIL", "Support@Example.test")
	t.Setenv("WHATOMATE_PLAN__SOURCE_URL", "https://github.com/example/whatomate/commit/"+strings.Repeat("a", 40))
	cfg, err := Load("")
	require.NoError(t, err)
	require.True(t, cfg.Plan.Enabled)
	require.Equal(t, 3, cfg.Plan.MaxUsers)
	require.Equal(t, 1, cfg.Plan.MaxNumbers)
	require.Equal(t, int64(5368709120), cfg.Plan.MaxStorageBytes)
	require.Equal(t, "support@example.test", cfg.Plan.SupportEmail)
}

func TestManagedPlanRejectsMissingOrInvalidAllowances(t *testing.T) {
	for _, change := range []func(*Config){
		func(c *Config) { c.Plan.MaxUsers = 0 }, func(c *Config) { c.Plan.MaxUsers = 10001 },
		func(c *Config) { c.Plan.MaxNumbers = -1 }, func(c *Config) { c.Plan.MaxNumbers = 1001 },
		func(c *Config) { c.Plan.MaxStorageBytes = 0 }, func(c *Config) { c.Plan.MaxStorageBytes = 10737418240001 },
		func(c *Config) { c.Plan.SupportEmail = "invalid" }, func(c *Config) { c.Plan.SourceURL = "" },
		func(c *Config) { c.Plan.SourceURL = "https://github.com/example/whatomate/tree/main" },
		func(c *Config) { c.Storage.Type = "s3" }, func(c *Config) { c.Calling.RecordingEnabled = true },
	} {
		cfg := Config{Plan: PlanConfig{Enabled: true, MaxUsers: 3, MaxNumbers: 1, MaxStorageBytes: 5368709120, SupportEmail: "support@example.test", SourceURL: "https://github.com/example/whatomate/commit/" + strings.Repeat("a", 40)}, Storage: StorageConfig{Type: "local"}}
		change(&cfg)
		require.Error(t, ValidatePlan(&cfg))
	}
}

func TestUnmanagedConfigurationRetainsUpstreamBehavior(t *testing.T) {
	cfg := Config{}
	require.NoError(t, ValidatePlan(&cfg))
}
