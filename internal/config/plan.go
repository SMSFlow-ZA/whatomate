package config

import (
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
)

// PlanConfig is host-controlled. No application endpoint updates these values.
// SupportPassword is used only to seed a missing support account and is never
// included in the plan status response or corresponding-source metadata.
type PlanConfig struct {
	Enabled         bool   `koanf:"enabled"`
	MaxUsers        int    `koanf:"max_users"`
	MaxNumbers      int    `koanf:"max_numbers"`
	MaxStorageBytes int64  `koanf:"max_storage_bytes"`
	SupportEmail    string `koanf:"support_email"`
	SupportPassword string `koanf:"support_password"`
	SourceURL       string `koanf:"source_url"`
}

func ValidatePlan(cfg *Config) error {
	if !cfg.Plan.Enabled {
		return nil
	}
	p := &cfg.Plan
	if p.MaxUsers < 1 || p.MaxUsers > 10000 || p.MaxNumbers < 1 || p.MaxNumbers > 1000 || p.MaxStorageBytes < 1 || p.MaxStorageBytes > 10000*int64(1073741824) {
		return fmt.Errorf("managed plan requires positive user, number and storage limits within supported bounds")
	}
	p.SupportEmail = strings.ToLower(strings.TrimSpace(p.SupportEmail))
	address, err := mail.ParseAddress(p.SupportEmail)
	if err != nil || address.Address != p.SupportEmail {
		return fmt.Errorf("managed plan requires a plain support email address")
	}
	source, err := url.Parse(p.SourceURL)
	if err != nil || source.Scheme != "https" || source.Host != "github.com" || source.User != nil || source.RawQuery != "" || source.Fragment != "" || !regexp.MustCompile(`^/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/commit/[a-f0-9]{40}$`).MatchString(source.Path) {
		return fmt.Errorf("managed plan requires an exact public corresponding-source commit URL")
	}
	if cfg.Storage.Type != "local" || cfg.Calling.RecordingEnabled {
		return fmt.Errorf("managed plans require local media storage and disabled call recording")
	}
	return nil
}
