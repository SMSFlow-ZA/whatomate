package config

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAllowancesAreOptInAndOrganizationLimitIsOptional(t *testing.T) {
	require.NoError(t, ValidatePlan(&Config{}))
	cfg := &Config{Plan: PlanConfig{Enabled: true, MaxUsers: 3, MaxNumbers: 1}}
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
	// Entity allowances do not add a storage backend or call-recording restriction.
	cfg.Storage.Type = "s3"
	cfg.Calling.RecordingEnabled = true
	require.NoError(t, ValidatePlan(cfg))
}
