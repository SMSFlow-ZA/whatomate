package audit_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shridarpatil/whatomate/internal/audit"
	"github.com/shridarpatil/whatomate/internal/models"
	"github.com/stretchr/testify/require"
)

func TestAuditPrivacyOriginUsesPeerAndStopsAtFirstUntrustedHop(t *testing.T) {
	trusted, err := audit.ParseTrustedProxies([]string{"127.0.0.1/32", "10.0.0.0/24", "2001:db8:1::/64"})
	require.NoError(t, err)
	for _, test := range []struct{ peer, forwarded, ip, source string }{
		{"203.0.113.1", "198.51.100.99", "203.0.113.1", "peer"},
		{"127.0.0.1", "198.51.100.99,203.0.113.50", "203.0.113.50", "forwarded"},
		{"10.0.0.1", "198.51.100.50,10.0.0.2", "198.51.100.50", "forwarded"},
		{"::ffff:10.0.0.1", "::ffff:198.51.100.50", "198.51.100.50", "forwarded"},
		{"2001:db8:1::1", "2001:db8:2::3", "2001:db8:2::3", "forwarded"},
		{"127.0.0.1", "invalid,198.51.100.50", "127.0.0.1", "peer"},
		{"127.0.0.1", "0.0.0.0", "127.0.0.1", "peer"},
		{"127.0.0.1", "ff02::1", "127.0.0.1", "peer"},
		{"127.0.0.1", "fe80::1%eth0", "127.0.0.1", "peer"},
		{"127.0.0.1", strings.Repeat("198.51.100.1,", 21), "127.0.0.1", "peer"},
		{"127.0.0.1", strings.Repeat("x", 1025), "127.0.0.1", "peer"},
		{"0.0.0.0", "198.51.100.50", "", "unknown"},
	} {
		t.Run(test.peer+"/"+test.source+"/"+test.ip, func(t *testing.T) {
			got := audit.ResolveOrigin(netip.MustParseAddr(test.peer), test.forwarded, trusted)
			require.Equal(t, audit.Origin{IP: test.ip, Source: test.source}, got)
		})
	}
	require.Equal(t, audit.Origin{IP: "127.0.0.1", Source: "peer"}, audit.ResolveOrigin(netip.MustParseAddr("127.0.0.1"), "198.51.100.50", nil))
}

func TestAuditPrivacyProxyPolicyRejectsMalformedOrUniversalTrust(t *testing.T) {
	for _, values := range [][]string{{"not-a-network"}, {"0.0.0.0/0"}, {"::/0"}, {"::ffff:0.0.0.0/96"}, make([]string, 33)} {
		_, err := audit.ParseTrustedProxies(values)
		require.EqualError(t, err, "invalid audit trusted proxy policy")
	}
	got, err := audit.ParseTrustedProxies([]string{"::ffff:203.0.113.123/120"})
	require.NoError(t, err)
	require.Equal(t, []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")}, got)
}

func TestAuditPrivacyRedactionCopiesAndProtectsNestedAndExtraDiffs(t *testing.T) {
	input := models.JSONBArray{
		map[string]any{"field": "Password_Hash", "old_value": "synthetic-old-secret", "new_value": "synthetic-new-secret", "request_body": "synthetic-extra-secret"},
		map[string]any{"field": "configuration", "new_value": map[string]any{"name": "Synthetic label", "Authorization": "synthetic-auth-secret", "nested": []any{map[string]any{"api-key": "synthetic-api-secret", "enabled": false, "count": float64(0)}}}},
		map[string]any{"field": "name", "old_value": nil, "new_value": "New label"},
	}
	before, err := json.Marshal(input)
	require.NoError(t, err)
	got := audit.SanitizeChanges(input)
	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "-secret")
	require.Contains(t, string(encoded), "[redacted]")
	require.Contains(t, string(encoded), "Synthetic label")
	require.Contains(t, string(encoded), `"enabled":false`)
	require.Contains(t, string(encoded), `"count":0`)
	after, err := json.Marshal(input)
	require.NoError(t, err)
	require.JSONEq(t, string(before), string(after))
	require.Len(t, got, 3)
}

type forbiddenAuditMarshal struct{}

func (forbiddenAuditMarshal) MarshalJSON() ([]byte, error) {
	panic("redaction must not invoke a custom marshaler")
}

func TestAuditPrivacyRedactionBoundsDepthBytesAndUnsupportedValues(t *testing.T) {
	var nested any = strings.Repeat("synthetic", 100000)
	for i := 0; i < 20; i++ {
		nested = map[string]any{"next": nested}
	}
	input := models.JSONBArray{map[string]any{"field": "deep", "new_value": nested}, map[string]any{"field": "custom", "new_value": forbiddenAuditMarshal{}}, map[string]any{"field": "name", "new_value": strings.Repeat("界", 100000)}}
	for i := 0; i < 200; i++ {
		input = append(input, map[string]any{"field": "other", "new_value": strings.Repeat("x", 1000)})
	}
	got := audit.SanitizeChanges(input)
	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	require.Less(t, len(encoded), 20000)
	require.LessOrEqual(t, len(got), 65)
	require.Contains(t, string(encoded), "[truncated]")
	require.Contains(t, string(encoded), "[unavailable]")
}

func TestAuditPrivacyErrorCodesNeverReturnErrorPayload(t *testing.T) {
	for _, test := range []struct {
		err  error
		code string
	}{
		{audit.ErrInvalidEvent, "invalid_event"}, {context.DeadlineExceeded, "timeout"}, {context.Canceled, "cancelled"},
		{&pgconn.PgError{Code: "23514", Message: "synthetic-secret"}, "database_23514"},
		{&pgconn.PgError{Code: "synthetic-secret"}, "database_error"}, {errors.New("synthetic-secret"), "storage_unavailable"},
	} {
		require.Equal(t, test.code, audit.ErrorCode(test.err))
		require.NotContains(t, audit.ErrorCode(test.err), "secret")
	}
}
