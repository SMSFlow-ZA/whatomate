// Package aiproviders defines the compiled-in chatbot inference contract.
package aiproviders

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

const APIVersion = 1
const (
	Foundry    = "foundry"
	Compatible = "compatible"
)

var endpointID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
var hostnamePattern = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var reserved = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("168.63.129.16/32"), netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}
var publicV6 = netip.MustParsePrefix("2000::/3")

// Endpoint is deployment configuration, never workspace-supplied input.
// PrivateIP is one approved HTTPS gateway address. LocalOnly is an operator
// assertion about that gateway; Models are its explicit local model allowlist.
type Endpoint struct {
	ID         string   `json:"id"`
	Label      string   `json:"label"`
	Kind       string   `json:"kind"`
	BaseURL    string   `json:"base_url"`
	PrivateIP  string   `json:"private_ip,omitempty"`
	LocalOnly  bool     `json:"local_only,omitempty"`
	Models     []string `json:"models,omitempty"`
	BlockedIPs []string `json:"blocked_ips,omitempty"`
}

type Target struct {
	endpoint  Endpoint
	url       url.URL
	privateIP netip.Addr
	blocked   []netip.Addr
}

func ParseEndpoint(e Endpoint) (Target, error) {
	invalid := errors.New("invalid AI endpoint policy")
	u, err := url.Parse(e.BaseURL)
	if err != nil || !endpointID.MatchString(e.ID) || len(e.BaseURL) > 300 || len(e.Label) > 100 {
		return Target{}, invalid
	}
	host := u.Hostname()
	if u.Scheme != "https" || u.Host != host || u.User != nil || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" || strings.Contains(e.BaseURL, "#") || u.RawPath != "" || !hostnamePattern.MatchString(host) ||
		len(host) > 253 || net.ParseIP(host) != nil || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".metadata.google.internal") || host == "metadata.google.internal" {
		return Target{}, invalid
	}
	path := strings.TrimSuffix(u.Path, "/")
	switch e.Kind {
	case Foundry:
		openAIHost := strings.HasSuffix(host, ".openai.azure.com") && strings.Count(host, ".") == 3
		servicesHost := strings.HasSuffix(host, ".services.ai.azure.com") && strings.Count(host, ".") == 4
		if path != "/openai/v1" || e.PrivateIP != "" || e.LocalOnly || (!openAIHost && !servicesHost) {
			return Target{}, invalid
		}
	case Compatible:
		if path != "/v1" {
			return Target{}, invalid
		}
	default:
		return Target{}, invalid
	}
	t := Target{endpoint: e, url: *u}
	if e.PrivateIP != "" {
		ip, err := netip.ParseAddr(e.PrivateIP)
		if err != nil || ip.Zone() != "" || !ip.Unmap().IsPrivate() {
			return Target{}, invalid
		}
		t.privateIP = ip.Unmap()
	}
	if e.LocalOnly && (!t.privateIP.IsValid() || len(e.Models) == 0) {
		return Target{}, invalid
	}
	for _, model := range e.Models {
		if !ValidModel(model) {
			return Target{}, invalid
		}
	}
	t.endpoint.Models = slices.Clone(e.Models)
	t.endpoint.BlockedIPs = slices.Clone(e.BlockedIPs)
	for _, raw := range e.BlockedIPs {
		ip, err := netip.ParseAddr(raw)
		if err != nil || ip.Zone() != "" {
			return Target{}, invalid
		}
		t.blocked = append(t.blocked, ip.Unmap())
	}
	if t.privateIP.IsValid() && slices.Contains(t.blocked, t.privateIP) {
		return Target{}, invalid
	}
	t.url.Path = path + "/chat/completions"
	return t, nil
}

func (t Target) ID() string      { return t.endpoint.ID }
func (t Target) Label() string   { return t.endpoint.Label }
func (t Target) Kind() string    { return t.endpoint.Kind }
func (t Target) Host() string    { return t.url.Hostname() }
func (t Target) LocalOnly() bool { return t.endpoint.LocalOnly }
func (t Target) AllowsModel(model string) bool {
	return ValidModel(model) && (len(t.endpoint.Models) == 0 || slices.Contains(t.endpoint.Models, model))
}

// CredentialScope prevents an endpoint ID from forwarding an existing key
// to a replacement gateway if an operator edits the approved policy.
func (t Target) CredentialScope() string {
	digest := sha256.Sum256([]byte(t.Kind() + "\n" + t.url.String() + "\n" + t.privateIP.String()))
	return hex.EncodeToString(digest[:])
}

func (t Target) Fingerprint() string {
	data, _ := json.Marshal(t.endpoint)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func ValidModel(model string) bool {
	return model != "" && len(model) <= 150 && strings.IndexFunc(model, func(r rune) bool {
		lowercase := r >= 'a' && r <= 'z'
		uppercase := r >= 'A' && r <= 'Z'
		digit := r >= '0' && r <= '9'
		return !lowercase && !uppercase && !digit && !strings.ContainsRune("-_./:", r)
	}) == -1
}

func (t Target) dialAddress(answers []string) (string, error) {
	if len(answers) == 0 {
		return "", ErrEndpoint
	}
	for _, raw := range answers {
		ip, err := netip.ParseAddr(raw)
		if err != nil || ip.Zone() != "" {
			return "", ErrEndpoint
		}
		ip = ip.Unmap()
		if slices.Contains(t.blocked, ip) || !ip.IsGlobalUnicast() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			return "", ErrEndpoint
		}
		if t.privateIP.IsValid() {
			if ip != t.privateIP {
				return "", ErrEndpoint
			}
			continue
		}
		if ip.IsPrivate() || (ip.Is6() && !publicV6.Contains(ip)) {
			return "", ErrEndpoint
		}
		for _, prefix := range reserved {
			if prefix.Contains(ip) {
				return "", ErrEndpoint
			}
		}
	}
	ip := netip.MustParseAddr(answers[0]).Unmap()
	return net.JoinHostPort(ip.String(), "443"), nil
}

func (t Target) dial(ctx context.Context, network, address string) (net.Conn, error) {
	if address != net.JoinHostPort(t.Host(), "443") {
		return nil, ErrEndpoint
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	answers, err := net.DefaultResolver.LookupHost(ctx, t.Host())
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrEndpoint
	}
	target, err := t.dialAddress(answers)
	if err != nil {
		return nil, err
	}
	return (&net.Dialer{}).DialContext(ctx, network, target)
}
