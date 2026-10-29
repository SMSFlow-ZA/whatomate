package config

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAllowancesAreOptInAndOrganizationLimitIsOptional(t *testing.T) {
	require.NoError(t, ValidatePlan(&Config{}))
	cfg := &Config{Plan: PlanConfig{Enabled: true, MaxUsers: 3, MaxNumbers: 1, MaxStorageBytes: 1024}, Storage: StorageConfig{Type: "local"}}
	require.NoError(t, ValidatePlan(cfg))
	require.Zero(t, cfg.Plan.MaxOrganizations)
	cfg.Plan.MaxOrganizations = 1
	require.NoError(t, ValidatePlan(cfg))
	cfg.Plan.MaxOrganizations = -1
	require.Error(t, ValidatePlan(cfg))
	cfg.Plan.MaxOrganizations = 0
	cfg.Plan.MaxUsers = 0
	require.Error(t, ValidatePlan(cfg))
	cfg.Plan.MaxUsers = 3
	cfg.Storage.Type = "s3"
	require.Error(t, ValidatePlan(cfg))
	cfg.Storage.Type = "local"
	cfg.Calling.RecordingEnabled = true
	require.Error(t, ValidatePlan(cfg))
}
