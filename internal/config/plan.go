package config

import "fmt"

// PlanConfig is trusted operator configuration. Disabled retains unrestricted
// ordinary operation; zero MaxOrganizations preserves multiple organizations.
type PlanConfig struct {
	Enabled          bool `koanf:"enabled"`
	MaxUsers         int  `koanf:"max_users"`
	MaxNumbers       int  `koanf:"max_numbers"`
	MaxOrganizations int  `koanf:"max_organizations"`
}

func ValidatePlan(cfg *Config) error {
	if !cfg.Plan.Enabled {
		return nil
	}
	p := cfg.Plan
	if p.MaxUsers < 1 || p.MaxUsers > 10000 || p.MaxNumbers < 1 || p.MaxNumbers > 1000 || p.MaxOrganizations < 0 || p.MaxOrganizations > 10000 {
		return fmt.Errorf("enabled allowances require positive user and number limits; organization limit is optional")
	}
	return nil
}
