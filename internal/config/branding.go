package config

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// BrandingConfig supplies operator-owned display defaults without granting
// access to generic settings APIs in a protected organization.
type BrandingConfig struct {
	DisplayName string `koanf:"display_name"`
	AccentColor string `koanf:"accent_color"`
}

func (c BrandingConfig) Validate() error {
	if c.DisplayName != "" && (!utf8.ValidString(c.DisplayName) || strings.TrimSpace(c.DisplayName) != c.DisplayName || utf8.RuneCountInString(c.DisplayName) > 80 || strings.ContainsFunc(c.DisplayName, unicode.IsControl)) {
		return fmt.Errorf("branding.display_name must contain 1–80 characters without surrounding whitespace or control characters")
	}
	if c.AccentColor != "" && !regexp.MustCompile(`^#[a-fA-F0-9]{6}$`).MatchString(c.AccentColor) {
		return fmt.Errorf("branding.accent_color must be a six-digit hex colour")
	}
	return nil
}
