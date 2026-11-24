package config

import (
	"errors"
	"net/netip"
	"strings"

	"github.com/shridarpatil/whatomate/internal/audit"
)

// AuditConfig is operator-owned. No proxy is trusted by default, independently
// of rate_limit.trust_proxy. Workspace APIs cannot change this policy.
type AuditConfig struct {
	TrustedProxyCIDRs string `koanf:"trusted_proxy_cidrs"`
}

func (c AuditConfig) TrustedProxies() ([]netip.Prefix, error) {
	if len(c.TrustedProxyCIDRs) > 2048 {
		return nil, errors.New("invalid audit trusted proxy policy")
	}
	if strings.TrimSpace(c.TrustedProxyCIDRs) == "" {
		return nil, nil
	}
	values := strings.Split(c.TrustedProxyCIDRs, ",")
	for i := range values {
		values[i] = strings.TrimSpace(values[i])
	}
	return audit.ParseTrustedProxies(values)
}
