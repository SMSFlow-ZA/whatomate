// Package inboxintegration provides bounded HTTP execution for approved inbox
// integrations. It does not expose scripting, arbitrary destinations or retries.
package inboxintegration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

const MaxBody = 64 << 10

// Endpoint is operator-controlled configuration, never an agent supplied URL.
// A configured idempotency header asserts that this provider honours that key.
type Endpoint struct {
	OrganizationID    string `json:"organization_id,omitempty"`
	Key               string `json:"key"`
	Name              string `json:"name"`
	URL               string `json:"url"`
	Method            string `json:"method"`
	IdempotencyHeader string `json:"idempotency_header,omitempty"`
	// A positive retention is an operator assertion of the provider's durable
	// key/payload contract. Zero preserves the legacy no-retry policy.
	IdempotencyRetentionSeconds int `json:"idempotency_retention_seconds,omitempty"`
}

type Failure struct {
	Category  string
	Ambiguous bool
}

func (e Failure) Error() string { return "integration: " + e.Category }

type Response struct {
	Status   int
	Fields   map[string]json.RawMessage
	Duration time.Duration
}

// Executor allows tests to use fake providers; production uses Client.
type Executor interface {
	Execute(context.Context, Endpoint, string, string, []byte, string) (Response, error)
}

func Identifier(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for i, r := range value {
		if r >= 'a' && r <= 'z' || i > 0 && (r >= '0' && r <= '9' || r == '_') {
			continue
		}
		return false
	}
	return true
}

func (p Endpoint) Validate() error {
	if p.OrganizationID != "" {
		id, err := uuid.Parse(p.OrganizationID)
		if err != nil || id == uuid.Nil {
			return Failure{"endpoint_invalid", false}
		}
	}
	u, err := url.Parse(p.URL)
	if err != nil || !Identifier(p.Key) || strings.TrimSpace(p.Name) == "" || len(p.Name) > 100 || len(p.URL) > 2048 {
		return Failure{"endpoint_invalid", false}
	}
	if u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || (u.Port() != "" && u.Port() != "443") || strings.ContainsAny(u.Hostname(), "%\\") {
		return Failure{"endpoint_invalid", false}
	}
	host := strings.ToLower(u.Hostname())
	if ip, err := netip.ParseAddr(host); err == nil {
		if !PublicAddress(ip) {
			return Failure{"destination_blocked", false}
		}
	} else if host == "localhost" || !strings.Contains(host, ".") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".") {
		return Failure{"endpoint_invalid", false}
	}
	if p.Method != "POST" && p.Method != "PUT" && p.Method != "PATCH" {
		return Failure{"method_invalid", false}
	}
	if p.IdempotencyHeader != "" && p.IdempotencyHeader != "Idempotency-Key" && p.IdempotencyHeader != "X-Idempotency-Key" {
		return Failure{"idempotency_invalid", false}
	}
	if p.IdempotencyRetentionSeconds < 0 || p.IdempotencyRetentionSeconds > 30*24*60*60 || (p.IdempotencyRetentionSeconds != 0 && (p.IdempotencyHeader == "" || p.IdempotencyRetentionSeconds < 60)) {
		return Failure{"idempotency_invalid", false}
	}
	return nil
}

func (p Endpoint) AllowsOrganization(org uuid.UUID) bool {
	if p.OrganizationID == "" {
		return true
	}
	id, err := uuid.Parse(p.OrganizationID)
	return err == nil && id != uuid.Nil && id == org
}

// DisplayDestination omits paths as well as credentials: webhook paths can
// themselves contain secret tokens. Full destinations stay operator-side.
func (p Endpoint) DisplayDestination() string {
	u, err := url.Parse(p.URL)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

var blockedPrefixes = func() []netip.Prefix {
	values := []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "168.63.129.16/32", "64:ff9b::/96", "64:ff9b:1::/48", "2001::/32", "2001:db8::/32", "2002::/16"}
	result := make([]netip.Prefix, len(values))
	for i, value := range values {
		result[i] = netip.MustParsePrefix(value)
	}
	return result
}()

func PublicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

// Client never reads proxy settings, follows redirects or replays a request.
type Client struct {
	lookup    func(context.Context, string) ([]net.IPAddr, error)
	dial      func(context.Context, string, string) (net.Conn, error)
	transport func(*http.Transport) http.RoundTripper // package-private fake transport for contract tests
}

func NewClient() *Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	return &Client{lookup: net.DefaultResolver.LookupIPAddr, dial: dialer.DialContext}
}

func (c *Client) validatedDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "443" {
		return nil, Failure{"destination_blocked", false}
	}
	ips, err := c.lookup(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, Failure{"dns_unavailable", false}
	}
	for _, ip := range ips {
		addr, ok := netip.AddrFromSlice(ip.IP)
		if !ok || ip.Zone != "" || !PublicAddress(addr) {
			return nil, Failure{"destination_blocked", false}
		}
	}
	// Dial the validated address, keeping the original URL hostname for normal
	// TLS certificate and SNI verification. DNS is never consulted a second time.
	return c.dial(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
}

func (c *Client) Execute(ctx context.Context, endpoint Endpoint, mode, credential string, body []byte, executionID string) (Response, error) {
	var result Response
	if err := endpoint.Validate(); err != nil {
		return result, err
	}
	if len(body) == 0 || len(body) > MaxBody || !json.Valid(body) || executionID == "" || len(executionID) > 64 || strings.ContainsAny(executionID, "\r\n") {
		return result, Failure{"request_invalid", false}
	}
	if len(credential) > 8192 || strings.ContainsAny(credential, "\r\n") || (mode != "none" && mode != "bearer" && mode != "api_key") || (mode != "none" && credential == "") || (mode == "none" && credential != "") {
		return result, Failure{"credential_invalid", false}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, endpoint.Method, endpoint.URL, bytes.NewReader(body))
	if err != nil {
		return result, Failure{"request_invalid", false}
	}
	request.GetBody = nil // prohibit transparent transport replay
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "Whatomate-InboxIntegration/1")
	request.Header.Set("X-Whatomate-Execution-ID", executionID)
	if endpoint.IdempotencyHeader != "" {
		request.Header.Set(endpoint.IdempotencyHeader, executionID)
	}
	switch mode {
	case "bearer":
		request.Header.Set("Authorization", "Bearer "+credential)
	case "api_key":
		request.Header.Set("X-API-Key", credential)
	}
	transport := &http.Transport{DialContext: c.validatedDial, DisableKeepAlives: true, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 25 * time.Second, MaxResponseHeaderBytes: 16 << 10}
	defer transport.CloseIdleConnections()
	var roundTripper http.RoundTripper = transport
	if c.transport != nil {
		roundTripper = c.transport(transport)
	}
	client := &http.Client{Transport: roundTripper, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	started := time.Now()
	response, err := client.Do(request)
	result.Duration = time.Since(started)
	if err != nil {
		var policyFailure Failure
		if errors.As(err, &policyFailure) && !policyFailure.Ambiguous {
			return result, policyFailure
		}
		return result, Failure{"submission_uncertain", true}
	}
	defer func() { _ = response.Body.Close() }()
	result.Status = response.StatusCode
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// Even a rejection/redirect may follow a write at the provider. No
		// inferred non-submission, body logging or automatic retries.
		return result, Failure{"provider_rejected", true}
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, MaxBody+1))
	if err != nil || len(raw) > MaxBody {
		return result, Failure{"response_invalid", true}
	}
	if len(bytes.TrimSpace(raw)) == 0 && response.StatusCode == http.StatusNoContent {
		result.Fields = map[string]json.RawMessage{}
		return result, nil
	}
	result.Fields, err = decodeObject(raw)
	if err != nil {
		return result, Failure{"response_invalid", true}
	}
	return result, nil
}

func decodeObject(raw []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return nil, errors.New("object required")
	}
	values := map[string]json.RawMessage{}
	for d.More() {
		name, err := d.Token()
		if err != nil {
			return nil, err
		}
		key, ok := name.(string)
		if !ok || len(key) > 128 || len(values) >= 64 {
			return nil, errors.New("response keys invalid")
		}
		if _, duplicate := values[key]; duplicate {
			return nil, errors.New("duplicate response key")
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nil, err
		}
		values[key] = value
	}
	if _, err := d.Token(); err != nil {
		return nil, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return nil, errors.New("trailing data")
	}
	return values, nil
}
