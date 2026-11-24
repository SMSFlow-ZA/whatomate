package config_test

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/stretchr/testify/require"
)

func TestAuditProxyConfigDefaultsToNoTrustIndependentlyOfRateLimiter(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, "[rate_limit]\ntrust_proxy=true\n"))
	require.NoError(t, err)
	proxies, err := cfg.Audit.TrustedProxies()
	require.NoError(t, err)
	require.Empty(t, proxies)
}

func TestAuditProxyConfigLoadsExactPolicyAndEnvironmentOverride(t *testing.T) {
	cfg, err := config.Load(writeConfig(t, "[audit]\ntrusted_proxy_cidrs='127.0.0.1/32, ::ffff:203.0.113.123/120'\n"))
	require.NoError(t, err)
	proxies, err := cfg.Audit.TrustedProxies()
	require.NoError(t, err)
	require.Equal(t, []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("203.0.113.0/24")}, proxies)
	t.Setenv("WHATOMATE_AUDIT__TRUSTED_PROXY_CIDRS", "2001:db8::/64")
	cfg, err = config.Load(writeConfig(t, "[audit]\ntrusted_proxy_cidrs='0.0.0.0/0'\n"))
	require.NoError(t, err)
	proxies, err = cfg.Audit.TrustedProxies()
	require.NoError(t, err)
	require.Equal(t, []netip.Prefix{netip.MustParsePrefix("2001:db8::/64")}, proxies)
}

func TestAuditProxyConfigRejectsUniversalMalformedAndOversizedTrustAtStartup(t *testing.T) {
	for _, raw := range []string{"0.0.0.0/0", "::/0", "::ffff:0.0.0.0/96", "synthetic-private-policy", "127.0.0.1/32,", strings.Repeat("127.0.0.1/32,", 33) + "127.0.0.1/32", strings.Repeat("x", 2049)} {
		t.Run(raw[:min(len(raw), 30)], func(t *testing.T) {
			_, err := config.Load(writeConfig(t, "[audit]\ntrusted_proxy_cidrs='"+raw+"'\n"))
			require.EqualError(t, err, "invalid audit trusted proxy policy")
		})
	}
}
