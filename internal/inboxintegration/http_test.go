package inboxintegration

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/stretchr/testify/require"
)

func TestPublicAddresses(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "168.63.129.16", "100.100.100.200", "100.64.0.1", "0.1.2.3", "192.0.2.1", "198.18.1.1", "224.0.0.1", "255.255.255.255", "::1", "::2", "::ffff:127.0.0.1", "fc00::1", "fe80::1", "64:ff9b::a9fe:a9fe", "2002:7f00:1::1", "2001:db8::1"} {
		require.False(t, PublicAddress(netip.MustParseAddr(address)), address)
	}
	for _, address := range []string{"93.184.216.34", "8.8.8.8", "2001:4860:4860::8888"} {
		require.True(t, PublicAddress(netip.MustParseAddr(address)), address)
	}
}

func TestEndpointValidation(t *testing.T) {
	policy := Endpoint{Key: "crm", Name: "Example CRM", URL: "https://crm.example.invalid/api/cases", Method: "POST", IdempotencyHeader: "Idempotency-Key"}
	require.NoError(t, policy.Validate())
	org := uuid.New()
	policy.OrganizationID = org.String()
	require.NoError(t, policy.Validate())
	require.True(t, policy.AllowsOrganization(org))
	require.False(t, policy.AllowsOrganization(uuid.New()))
	policy.OrganizationID = "not-an-organisation"
	require.Error(t, policy.Validate())
	policy.OrganizationID = ""
	for _, value := range []string{"http://crm.example.invalid/api", "https://user:password@crm.example.invalid/api", "https://crm.example.invalid/api?token=secret", "https://crm.example.invalid/api#fragment", "https://crm.example.invalid:8443/api", "https://127.0.0.1/api", "https://metadata.internal/api", "https://localhost/api", "https://crm.example.invalid./api", "https://[::ffff:169.254.169.254]/api"} {
		copy := policy
		copy.URL = value
		require.Error(t, copy.Validate(), value)
	}
	policy.URL = "https://[2001:4860:4860::8888]/api"
	require.NoError(t, policy.Validate())
	policy.IdempotencyHeader = "Authorization"
	require.Error(t, policy.Validate())
	policy.IdempotencyHeader = ""
	policy.Method = "DELETE"
	require.Error(t, policy.Validate())
}

func TestValidatedDialUsesOneCheckedAddressAndRejectsMixedDNS(t *testing.T) {
	c := NewClient()
	lookups, calls := 0, 0
	c.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		lookups++
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	}
	c.dial = func(_ context.Context, network, address string) (net.Conn, error) {
		calls++
		require.Equal(t, "tcp", network)
		require.Equal(t, "8.8.8.8:443", address)
		return nil, errors.New("fixture stops network")
	}
	_, err := c.validatedDial(context.Background(), "tcp", "crm.example.invalid:443")
	require.Error(t, err)
	require.Equal(t, 1, lookups)
	require.Equal(t, 1, calls)
	c.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("169.254.169.254")}}, nil
	}
	_, err = c.validatedDial(context.Background(), "tcp", "crm.example.invalid:443")
	require.Equal(t, "integration: destination_blocked", err.Error())
	require.Equal(t, 1, calls)
	c.lookup = func(context.Context, string) ([]net.IPAddr, error) { return nil, nil }
	_, err = c.validatedDial(context.Background(), "tcp", "crm.example.invalid:443")
	require.Equal(t, "integration: dns_unavailable", err.Error())
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHTTPBoundsAuthenticationAndNoReplay(t *testing.T) {
	policy := Endpoint{Key: "crm", Name: "Example CRM", URL: "https://crm.example.invalid/api/cases", Method: "POST", IdempotencyHeader: "Idempotency-Key"}
	c := NewClient()
	calls := 0
	c.transport = func(transport *http.Transport) http.RoundTripper {
		require.Nil(t, transport.Proxy)
		require.Nil(t, transport.TLSClientConfig)
		require.True(t, transport.DisableKeepAlives)
		return roundTrip(func(request *http.Request) (*http.Response, error) {
			calls++
			require.Nil(t, request.GetBody)
			require.Equal(t, policy.URL, request.URL.String())
			require.Equal(t, "Bearer synthetic-secret", request.Header.Get("Authorization"))
			require.Equal(t, "fixed-operation", request.Header.Get("Idempotency-Key"))
			deadline, ok := request.Context().Deadline()
			require.True(t, ok)
			require.LessOrEqual(t, time.Until(deadline), 30*time.Second)
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"reference":"CASE-1","count":0,"enabled":false}`))}, nil
		})
	}
	result, err := c.Execute(context.Background(), policy, "bearer", "synthetic-secret", []byte(`{"name":"Example"}`), "fixed-operation")
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.JSONEq(t, `0`, string(result.Fields["count"]))
	require.JSONEq(t, `false`, string(result.Fields["enabled"]))
	_, err = c.Execute(context.Background(), policy, "bearer", "leak\r\nInjected: yes", []byte(`{}`), "fixed-operation")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "leak")
	require.Equal(t, 1, calls)
	_, err = c.Execute(context.Background(), policy, "none", "", []byte(strings.Repeat(" ", MaxBody+1)), "fixed-operation")
	require.Error(t, err)
	require.Equal(t, 1, calls)
}

func TestHTTPFailureClassificationAndResponseValidation(t *testing.T) {
	policy := Endpoint{Key: "crm", Name: "Example CRM", URL: "https://crm.example.invalid/api/cases", Method: "POST"}
	for _, scenario := range []struct {
		name, body string
		status     int
		failure    string
	}{
		{"redirect", `{"secret":"do-not-log"}`, 302, "provider_rejected"},
		{"rejection", `{"secret":"do-not-log"}`, 401, "provider_rejected"},
		{"duplicate_keys", `{"reference":"a","reference":"b"}`, 200, "response_invalid"},
		{"array", `[]`, 200, "response_invalid"},
		{"trailing", `{} {}`, 200, "response_invalid"},
		{"oversize", strings.Repeat(" ", MaxBody+1), 200, "response_invalid"},
		{"empty", ``, 200, "response_invalid"},
		{"no_content", ``, 204, ""},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			c := NewClient()
			calls := 0
			c.transport = func(*http.Transport) http.RoundTripper {
				return roundTrip(func(*http.Request) (*http.Response, error) {
					calls++
					return &http.Response{StatusCode: scenario.status, Header: http.Header{"Location": {"https://unapproved.example.invalid/"}}, Body: io.NopCloser(strings.NewReader(scenario.body))}, nil
				})
			}
			_, err := c.Execute(context.Background(), policy, "api_key", "synthetic-secret", []byte(`{}`), "fixed-operation")
			require.Equal(t, 1, calls)
			if scenario.failure == "" {
				require.NoError(t, err)
			} else {
				var failure Failure
				require.ErrorAs(t, err, &failure)
				require.True(t, failure.Ambiguous)
				require.Equal(t, scenario.failure, failure.Category)
				require.NotContains(t, err.Error(), "do-not-log")
			}
		})
	}
	client := NewClient()
	calls := 0
	client.transport = func(*http.Transport) http.RoundTripper {
		return roundTrip(func(*http.Request) (*http.Response, error) {
			calls++
			return nil, errors.New("response lost: synthetic-secret")
		})
	}
	_, err := client.Execute(context.Background(), policy, "none", "", []byte(`{}`), "fixed-operation")
	require.Equal(t, 1, calls)
	require.Equal(t, "integration: submission_uncertain", err.Error())
	client.transport = func(*http.Transport) http.RoundTripper {
		return roundTrip(func(*http.Request) (*http.Response, error) {
			return nil, Failure{"destination_blocked", false}
		})
	}
	_, err = client.Execute(context.Background(), policy, "none", "", []byte(`{}`), "fixed-operation")
	var policyFailure Failure
	require.ErrorAs(t, err, &policyFailure)
	require.False(t, policyFailure.Ambiguous)
	require.Equal(t, "destination_blocked", policyFailure.Category)
}

func TestHTTPUsesValidatedIPAndVerifiesOriginalTLSHostname(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Synthetic integration gateway"}, DNSNames: []string{"crm.example.invalid"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	require.NoError(t, err)
	certificate, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	var calls atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		require.Equal(t, "crm.example.invalid", r.Host)
		require.Equal(t, "Bearer synthetic-secret", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"reference":"SYNTHETIC-1"}`)
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()
	client := NewClient()
	lookups := 0
	client.lookup = func(_ context.Context, host string) ([]net.IPAddr, error) {
		lookups++
		require.Equal(t, "crm.example.invalid", host)
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	}
	client.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		require.Equal(t, "8.8.8.8:443", address)
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	client.transport = func(transport *http.Transport) http.RoundTripper {
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
		return transport
	}
	policy := Endpoint{Key: "crm", Name: "Synthetic gateway", URL: "https://crm.example.invalid/cases", Method: "POST"}
	_, err = client.Execute(context.Background(), policy, "bearer", "synthetic-secret", []byte(`{}`), "fixed-operation")
	require.NoError(t, err)
	require.EqualValues(t, 1, calls.Load())
	require.Equal(t, 1, lookups)
	// Trust in the certificate never permits a different hostname, even when
	// the same approved IP and local test server are used.
	policy.URL = "https://wrong.example.invalid/cases"
	client.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	}
	_, err = client.Execute(context.Background(), policy, "bearer", "synthetic-secret", []byte(`{}`), "second-operation")
	require.Error(t, err)
	require.EqualValues(t, 1, calls.Load())
	require.NotContains(t, err.Error(), "synthetic-secret")
}
