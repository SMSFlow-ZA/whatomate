package config

import "fmt"

// PlanConfig is trusted operator configuration. Ordinary installations have no
// allowances or organization restriction unless Enabled is explicitly set.
type PlanConfig struct {
	Enabled          bool  `koanf:"enabled"`
	MaxUsers         int   `koanf:"max_users"`
	MaxNumbers       int   `koanf:"max_numbers"`
	MaxStorageBytes  int64 `koanf:"max_storage_bytes"`
	MaxOrganizations int   `koanf:"max_organizations"` // zero retains multi-organization operation
}

func ValidatePlan(cfg *Config) error {
	if !cfg.Plan.Enabled {
		return nil
	}
	p := cfg.Plan
	if p.MaxUsers < 1 || p.MaxUsers > 10000 || p.MaxNumbers < 1 || p.MaxNumbers > 1000 || p.MaxStorageBytes < 1 || p.MaxStorageBytes > 10000*int64(1073741824) || p.MaxOrganizations < 0 || p.MaxOrganizations > 10000 {
		return fmt.Errorf("enabled allowances require positive user, number and storage limits; organization limit is optional")
	}
	if cfg.Storage.Type != "local" || cfg.Calling.RecordingEnabled {
		return fmt.Errorf("allowances require local storage and disabled call recording")
	}
	return nil
}
